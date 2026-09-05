import { describe, expect, it } from "vitest";
import type { AdminLiveSessionsResponse, AdminSession, AdminSessionTelemetry } from "@/api/types";
import { describeLiveSessionsSource, describeSessionDelivery } from "@/lib/sessionTelemetry";

function sessionWith(telemetry?: Partial<AdminSessionTelemetry>): AdminSession {
  return {
    session_id: "s1",
    telemetry: telemetry
      ? ({
          evidence: "both",
          viewer_bytes: 0,
          open_observations: 1,
          ...telemetry,
        } as AdminSessionTelemetry)
      : undefined,
  } as AdminSession;
}

function responseWith(overrides: Partial<AdminLiveSessionsResponse>): AdminLiveSessionsResponse {
  return {
    telemetry_enabled: true,
    view_available: true,
    view_complete: true,
    view_stale: false,
    view_age_ms: 0,
    no_delivery_count: 0,
    no_delivery_shown: false,
    unclaimed_idle_count: 0,
    unclaimed_idle_shown: false,
    sessions: [],
    ...overrides,
  };
}

describe("describeSessionDelivery", () => {
  it("returns null only when the server sent no telemetry block", () => {
    expect(describeSessionDelivery(sessionWith(undefined))).toBeNull();
  });

  it("formats bytes and rate", () => {
    const delivery = describeSessionDelivery(
      sessionWith({ viewer_bytes: 2 * 1024 ** 3, delivery_rate_kbps: 12400 }),
    );
    expect(delivery?.bytes).toBe("2.0 GB");
    expect(delivery?.rate).toBe("12.4 Mbps");
  });

  // A rate needs two consecutive view builds. Until then it is unknown, and
  // rendering unknown as 0 would read as a stalled stream on a healthy session.
  it("reports an unmeasured rate as null rather than zero", () => {
    const delivery = describeSessionDelivery(sessionWith({ viewer_bytes: 1024 }));
    expect(delivery?.rate).toBeNull();
    expect(delivery?.bytes).toBe("1.0 KB");
  });

  it("surfaces a session reported as playing that delivered nothing", () => {
    const delivery = describeSessionDelivery(
      sessionWith({ evidence: "reported", viewer_bytes: 0, no_delivery: true }),
    );
    expect(delivery?.noDelivery).toBe(true);
    expect(delivery?.unclaimed).toBe(false);
  });

  // A paused client stops pulling bytes, so the server does not flag it and
  // neither does the UI.
  it("does not mark a paused session as undelivered", () => {
    const delivery = describeSessionDelivery(
      sessionWith({ evidence: "reported", viewer_bytes: 0 }),
    );
    expect(delivery?.noDelivery).toBe(false);
  });

  it("marks delivery no session manager claims", () => {
    const delivery = describeSessionDelivery(
      sessionWith({ evidence: "measured", viewer_bytes: 8192, unclaimed_idle: true }),
    );
    expect(delivery?.unclaimed).toBe(true);
    expect(delivery?.noDelivery).toBe(false);
  });

  // The regression this pins: `evidence === "measured"` is true from the first byte
  // of every normal stream and stays true for the whole retention window after it
  // ends. Only the server knows whether the viewer edge is still delivering, and it
  // already says so, so a row it has not called unclaimed_idle must not be badged.
  it("does not badge measured delivery the server has not called unclaimed", () => {
    const delivery = describeSessionDelivery(
      sessionWith({ evidence: "measured", viewer_bytes: 8192 }),
    );
    expect(delivery?.unclaimed).toBe(false);
  });

  it("badges measured delivery the server called unclaimed while the view is readable", () => {
    const session = {
      ...sessionWith({ evidence: "measured", viewer_bytes: 8192, unclaimed_idle: true }),
      started_at: "2026-08-27T11:59:00Z",
    };

    expect(describeSessionDelivery(session, { viewBlind: false })?.unclaimed).toBe(true);
  });

  it("suppresses unclaimed framing when the merged view is merely stale", () => {
    // A stale view is blindness about NOW, not disagreement: the cache keeps serving its
    // last good view after a failed refresh, and that view stays COMPLETE while it ages.
    // The server suppresses on `!viewComplete || viewStale` for exactly this reason, so a
    // client that only mirrored completeness would badge every healthy session in the
    // window where the server had already stopped drawing conclusions.
    const session = {
      ...sessionWith({ evidence: "measured", viewer_bytes: 8192, unclaimed_idle: true }),
      started_at: "2026-08-27T11:59:00Z",
    };

    expect(describeSessionDelivery(session, { viewBlind: true })?.unclaimed).toBe(false);
  });

  // A retired measurement is an ended session; no missing publisher can revive
  // it, so the server keeps hiding it under blindness and the badge follows.
  it("keeps badging a pruned unclaimed row while the merged view is blind", () => {
    const session = sessionWith({
      evidence: "measured",
      viewer_bytes: 8192,
      unclaimed_idle: true,
      measurement_pruned: true,
    });

    expect(describeSessionDelivery(session, { viewBlind: true })?.unclaimed).toBe(true);
  });

  it("suppresses unclaimed framing when the merged view is incomplete", () => {
    const session = {
      ...sessionWith({ evidence: "measured", viewer_bytes: 8192, unclaimed_idle: true }),
      started_at: "2026-08-27T11:59:00Z",
    };
    const delivery = describeSessionDelivery(session, { viewBlind: true });

    expect(delivery?.unclaimed).toBe(false);
    expect(session.telemetry?.evidence).toBe("measured");
    expect(delivery?.noDelivery).toBe(false);
  });

  // The server applies its own grace before it ever sets unclaimed_idle, so
  // started_at is no longer an input here — including the shapes that used to make
  // the client guess: a missing, unparseable or future start.
  it.each([
    ["a missing start", undefined],
    ["an unparseable start", "not-a-date"],
    ["a future start", "2026-08-27T12:00:05Z"],
  ])("ignores %s when the server has classified the row", (_label, startedAt) => {
    // Cast on purpose: `started_at` is a required string on the wire, and two of
    // these cases are rows a correct server would never send. Constructing them
    // anyway is the point — the helper must not fall over on either.
    const session = {
      ...sessionWith({ evidence: "measured", viewer_bytes: 8192, unclaimed_idle: true }),
      started_at: startedAt,
    } as AdminSession;

    expect(describeSessionDelivery(session, { viewBlind: false })?.unclaimed).toBe(true);
  });

  it("surfaces the degraded, multi-IP and identity-conflict signals", () => {
    const delivery = describeSessionDelivery(
      sessionWith({
        viewer_bytes: 10,
        bytes_degraded: true,
        identity_conflict: true,
        viewer_ips: ["198.51.100.1", "203.0.113.7"],
      }),
    );
    expect(delivery?.degraded).toBe(true);
    expect(delivery?.identityConflict).toBe(true);
    expect(delivery?.viewerIpCount).toBe(2);
  });
});

describe("describeLiveSessionsSource", () => {
  it("labels a complete view as measured", () => {
    const source = describeLiveSessionsSource(responseWith({}));
    expect(source.label).toBe("measured");
    expect(source.trustworthy).toBe(true);
  });

  // Telemetry off is the one case where the list is unchecked client reports,
  // and it must not be presented as measured.
  it("labels a telemetry-off server as reported", () => {
    const source = describeLiveSessionsSource(responseWith({ telemetry_enabled: false }));
    expect(source.label).toBe("reported");
    expect(source.trustworthy).toBe(false);
    expect(source.canRevealHidden).toBe(false);
  });

  it("distinguishes a view that has not been built yet", () => {
    const source = describeLiveSessionsSource(responseWith({ view_available: false }));
    expect(source.label).toBe("starting up");
    expect(source.trustworthy).toBe(false);
  });

  // An incomplete view is missing sessions by construction, so the count cannot
  // be read at face value and the reasons have to travel with it.
  it("labels an incomplete view as partial and carries the reasons", () => {
    const source = describeLiveSessionsSource(
      responseWith({ view_complete: false, incomplete_reasons: ["missing_publisher"] }),
    );
    expect(source.label).toBe("partial");
    expect(source.trustworthy).toBe(false);
    expect(source.detail).toContain("missing_publisher");
  });

  it("offers the reveal control only when something is hidden", () => {
    expect(describeLiveSessionsSource(responseWith({ no_delivery_count: 0 })).canRevealHidden).toBe(
      false,
    );
    expect(describeLiveSessionsSource(responseWith({ no_delivery_count: 2 })).canRevealHidden).toBe(
      true,
    );
  });

  // The server withholds two classes behind one include_idle switch. Counting only
  // no_delivery made an all-unclaimed_idle fleet unreachable: no count, no control.
  it("offers the reveal control for withheld unclaimed-idle rows too", () => {
    const source = describeLiveSessionsSource(
      responseWith({ no_delivery_count: 0, unclaimed_idle_count: 3 }),
    );

    expect(source.canRevealHidden).toBe(true);
    expect(source.hiddenCount).toBe(3);
  });

  // Once revealed, the control has to stay available to toggle back.
  it("keeps the control available while hidden rows are shown", () => {
    const source = describeLiveSessionsSource(
      responseWith({ no_delivery_count: 0, no_delivery_shown: true }),
    );
    expect(source.canRevealHidden).toBe(true);
  });

  // A complete-but-stale view is not the same claim as a fresh measurement: the
  // refresh failed, so both the totals and the session list may be obsolete.
  it("does not call a stale view trustworthy", () => {
    const source = describeLiveSessionsSource(responseWith({ view_stale: true }));
    expect(source.trustworthy).toBe(false);
    expect(source.label).toBe("measured (stale)");
    expect(source.detail).toContain("could not be refreshed");
  });

  it("is safe before the first response arrives", () => {
    const source = describeLiveSessionsSource(undefined);
    expect(source.label).toBe("");
    expect(source.canRevealHidden).toBe(false);
  });
});
