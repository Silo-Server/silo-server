import type { AdminLiveSessionsResponse, AdminSession } from "@/api/types";
import { formatFileSize, formatMbpsFromKbps } from "@/lib/mediaFormat";

/**
 * What to render for one session's measured delivery.
 *
 * `null` only when the server has telemetry switched off, in which case no row
 * carries a telemetry block at all. Every session in the merged view has one,
 * because every process publishes into that view — including the session manager
 * that knows about sessions nothing has delivered a byte for.
 */
export interface SessionDeliveryDisplay {
  /** Bytes delivered to the viewer, e.g. "1.8 GB". */
  bytes: string;
  /**
   * Delivery rate, e.g. "12.4 Mbps", or null when it has not been measured yet.
   * A session is only rated once it has been seen in two consecutive view
   * builds, and "not yet measured" must never render as 0 — that reads as a
   * stalled stream on a session that is streaming fine.
   */
  rate: string | null;
  /**
   * True when a client reports this as playing but nothing was measured leaving
   * the server. Paused sessions are never marked: a paused client legitimately
   * stops pulling bytes.
   */
  noDelivery: boolean;
  /**
   * Bytes went out, no session manager claims this session, and the viewer edge
   * has gone quiet. Taken from the server's `unclaimed_idle`, gated only on the
   * view being readable at all: the server is the side that knows whether bytes
   * are still moving, and re-deriving this from `evidence` alone painted a red
   * badge over every normal stream for the whole window after it ended.
   */
  unclaimed: boolean;
  /** The byte total is a known floor, not a measurement. */
  degraded: boolean;
  /** Publishers disagreed about who is watching. Worth showing, not resolving. */
  identityConflict: boolean;
  /** More than one address pulled bytes for this session. */
  viewerIpCount: number;
}

export interface SessionDeliveryContext {
  /**
   * True when the merged view cannot be reasoned about: it is incomplete, or it is stale.
   * Mirrors the server's own `!viewComplete || viewStale` suppression in
   * internal/api/handlers/admin_live_sessions.go, and must keep mirroring it — an
   * incomplete view is blindness rather than disagreement (the publisher holding this
   * session's bytes may be exactly the one that is missing), and a stale view is blindness
   * about NOW: the cache serves its last good view after a failed refresh, and that view
   * stays complete while it ages.
   *
   * One flag rather than two because the server draws no distinction between them either.
   * Omitted means "nothing is known to be wrong" — the pure-helper call sites that pass no
   * context keep their existing behavior.
   */
  viewBlind?: boolean;
}

export function describeSessionDelivery(
  session: AdminSession,
  context: SessionDeliveryContext = {},
): SessionDeliveryDisplay | null {
  const telemetry = session.telemetry;
  if (!telemetry) return null;
  const blind = context.viewBlind === true;
  return {
    bytes: formatFileSize(telemetry.viewer_bytes, { fallback: "0 B" }),
    rate:
      typeof telemetry.delivery_rate_kbps === "number"
        ? formatMbpsFromKbps(telemetry.delivery_rate_kbps, "0.0 Mbps")
        : null,
    noDelivery: telemetry.no_delivery === true,
    unclaimed: telemetry.unclaimed_idle === true && !blind,
    degraded: telemetry.bytes_degraded === true,
    identityConflict: telemetry.identity_conflict === true,
    viewerIpCount: telemetry.viewer_ips?.length ?? 0,
  };
}

/**
 * How the live-session list should describe itself.
 *
 * There is no "is this telemetry-backed or legacy?" question: the merged view is
 * the only source, and every process publishes into it. What is left is how much
 * of the fleet the view can currently see, and how many sessions were held back.
 */
export interface LiveSessionsSourceDisplay {
  /** Short badge text, or "" when there is nothing worth saying. */
  label: string;
  /** The full explanation, for a tooltip. Empty when there is nothing to explain. */
  detail: string;
  /** The view is complete and fresh, so the list can be read at face value. */
  trustworthy: boolean;
  /** Offer the reveal control only when there is something hidden to reveal. */
  canRevealHidden: boolean;
  hiddenCount: number;
}

export function describeLiveSessionsSource(
  response: AdminLiveSessionsResponse | undefined,
): LiveSessionsSourceDisplay {
  if (!response) {
    return { label: "", detail: "", trustworthy: false, canRevealHidden: false, hiddenCount: 0 };
  }
  // Both withheld classes, because one `include_idle` switch reveals both. Counting
  // only no_delivery made unclaimed_idle rows unreachable: with nothing claimed-but-
  // undelivered and three ended-but-still-measured sessions, the toggle never rendered
  // and there was no way to see them at all.
  const hiddenCount = (response.no_delivery_count ?? 0) + (response.unclaimed_idle_count ?? 0);
  // The reveal control stays available once the rows are shown, so the toggle
  // can be turned back off.
  // Coerced rather than left to `||`: an older or partial payload can omit either
  // *_shown field, and `undefined` leaking out of a boolean flows straight into a
  // JSX conditional that then renders nothing with no error.
  const canRevealHidden =
    hiddenCount > 0 ||
    Boolean(response.no_delivery_shown) ||
    Boolean(response.unclaimed_idle_shown);

  if (!response.telemetry_enabled) {
    return {
      label: "reported",
      detail:
        "Stream telemetry is off, so this is what clients report about themselves. " +
        "Nothing here has been checked against bytes actually delivered.",
      trustworthy: false,
      // Nothing was filtered, so there is nothing held back to reveal.
      canRevealHidden: false,
      hiddenCount: 0,
    };
  }
  if (!response.view_available) {
    return {
      label: "starting up",
      detail:
        "The merged telemetry view has not been built yet, so this is the last " +
        "known session list rather than a measured one.",
      trustworthy: false,
      canRevealHidden: false,
      hiddenCount: 0,
    };
  }
  if (!response.view_complete) {
    return {
      label: "partial",
      detail: [
        "Some publishers are missing, so sessions they serve may be absent or " + "under-counted.",
        ...(response.incomplete_reasons ?? []),
      ].join(" "),
      trustworthy: false,
      canRevealHidden,
      hiddenCount,
    };
  }
  if (response.view_stale) {
    // Complete, but the last refresh failed or is still in flight. Totals and
    // membership may both be obsolete, so this is not the same claim as a fresh
    // measurement even though the underlying view was complete when it was built.
    return {
      label: "measured (stale)",
      detail:
        "Measured delivery, but the merged view could not be refreshed, so totals " +
        "and the session list may both be out of date.",
      trustworthy: false,
      canRevealHidden,
      hiddenCount,
    };
  }
  return {
    label: "measured",
    detail: "Measured delivery, corroborated across every publisher.",
    trustworthy: true,
    canRevealHidden,
    hiddenCount,
  };
}
