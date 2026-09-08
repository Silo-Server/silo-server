import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  initialPlaybackCapabilities,
  startInitialPlayback as dispatchStart,
  offerPendingInitialStart,
} from "./initial-v2";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { buildStartRequestV3 } from "./playback-session-wire-v3";
import {
  fixtureClientCapabilitiesV3,
  fixtureClientPlaybackContextV3,
  fixturePlanV3,
} from "./protocol-v3.fixtures";
const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile",
  getDeviceId: () => "device",
  capturePlaybackMutationContext: () => ({
    accountId: "account",
    profileId: "profile",
    origin: "http://localhost:3000",
    isCurrent: () => true,
  }),
};
const body = buildStartRequestV3({
  fileId: 42,
  profileId: "profile",
  playbackAttemptId: "attempt",
  qualityPreference: "auto",
  position: 0,
  forceStartPosition: false,
  metered: false,
  clientCapabilities: fixtureClientCapabilitiesV3(),
  clientPlaybackContext: fixtureClientPlaybackContextV3(),
});
const cap = {
  installation_id: "installation",
  revision: "1",
  state: "available",
  allowed: true,
  protocol_versions: [3],
  features: ["sequenced_progress_v1"],
  deliveries: ["direct"],
};
const startAborted = {
  protocol_version: 3,
  server_features: ["sequenced_progress_v1"],
  outcome: "adaptation_unavailable",
  terminal: {
    reason: "playback_start_aborted",
    message: "Playback could not start.",
    retryable: false,
  },
};
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
beforeEach(() => vi.useFakeTimers());
// Advance bounded retry backoff without waiting on wall-clock time. Attach both
// outcomes before advancing so rejected requests never become unhandled promises.
async function settle<T>(promise: Promise<T>): Promise<T> {
  const outcome = promise.then(
    (value) => ({ value }),
    (error: unknown) => ({ error }),
  );
  await vi.runAllTimersAsync();
  const result = await outcome;
  if ("error" in result) throw result.error;
  return result.value;
}
function startInitialPlayback(...args: Parameters<typeof dispatchStart>) {
  return settle(dispatchStart(...args));
}
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.clear();
});
it("refuses an unconfigured server without dispatching a start", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        reply({ state: "not_configured", allowed: false, protocol_versions: [], features: [] }),
      ),
  );
  await expect(startInitialPlayback(config, body)).rejects.toThrow("not configured");
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("does not fall back on admission refusal or transient capability failure", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValue(reply({ ...cap, state: "not_admitted", allowed: false }));
  vi.stubGlobal("fetch", fetcher);
  await expect(startInitialPlayback(config, body)).rejects.toThrow("not admitted");
  expect(fetcher).toHaveBeenCalledTimes(1);
  fetcher.mockResolvedValue(reply({}, 503));
  await expect(initialPlaybackCapabilities(config)).rejects.toThrow("unavailable");
});
it("uses v2 installation binding and converts string media IDs for the player", async () => {
  vi.stubGlobal("navigator", {
    locks: {
      request: (_key: string, _options: unknown, action: () => Promise<unknown>) => action(),
    },
  });
  const plan = fixturePlanV3();
  const wire = {
    ...plan,
    requested_media_file_id: "42",
    effective_media_file_id: "42",
    source: { ...plan.source, media_file_id: "42" },
  };
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(reply(cap))
    .mockResolvedValueOnce(
      reply({
        protocol_version: 3,
        server_features: ["sequenced_progress_v1"],
        session_id: "v2-start",
        outcome: "play",
        playback_plan: wire,
      }),
    );
  vi.stubGlobal("fetch", fetcher);
  const result = await startInitialPlayback(config, body);
  expect(fetcher.mock.calls[0]![0]).toBe("/api/v2/playback/capabilities");
  expect(fetcher.mock.calls[1]![0]).toBe("http://localhost:3000/api/v2/playback/start");
  expect(JSON.parse(fetcher.mock.calls[1]![1].body)).toMatchObject({
    installation_id: "installation",
    file_id: "42",
  });
  expect(result!.playback_plan!.source.media_file_id).toBe(42);
  expect(localStorage.length).toBe(1);
});
it("refuses configured start before effects when browser locking is unavailable", async () => {
  vi.stubGlobal("navigator", {});
  const fetcher = vi.fn().mockResolvedValue(reply(cap));
  vi.stubGlobal("fetch", fetcher);
  await expect(startInitialPlayback(config, body)).rejects.toThrow("storage locking");
  expect(fetcher).toHaveBeenCalledTimes(1);
});
it("automatically resolves an uncertain start after reload and never replaces its bytes", async () => {
  const fetcher = boundTransport(async () => {
    throw new TypeError("lost successful reply");
  });
  const onPlaybackStartError = vi.fn();
  const scoped = { ...config, onPlaybackStartError };
  await expect(startInitialPlayback(scoped, body)).rejects.toThrow("lost successful reply");
  const starts = () => fetcher.mock.calls.filter(([url]) => url.endsWith("/start"));
  const original = starts()[0]![1]?.body;
  expect(localStorage.getItem(localStorage.key(0)!)).toBe(original);
  expect(onPlaybackStartError).toHaveBeenCalledTimes(1);
  await expect(
    startInitialPlayback(config, { ...body, playback_attempt_id: "different-attempt" }),
  ).rejects.toThrow("lost successful reply");
  expect(starts().length).toBeGreaterThan(2);
  expect(starts().every((call) => call[1]?.body === original)).toBe(true);
  vi.resetModules();
  const reloaded = await import("./initial-v2");
  fetcher.mockImplementation(async (url: string) =>
    url.endsWith("capabilities") ? reply(cap) : reply(startAborted, 201),
  );
  const resolved = vi.fn();
  await settle(
    reloaded.offerPendingInitialStart({ ...config, onPlaybackStartResolved: resolved }, cap),
  );
  expect(starts().slice(-1)[0]![1]?.body).toBe(original);
  expect(localStorage.length).toBe(0);
  expect(resolved).toHaveBeenCalledTimes(1);
});
it("quarantines an old start recovery action after identity changes", async () => {
  let profile = "profile";
  let retry: (() => void) | undefined;
  const scoped = {
    ...config,
    getProfileId: () => profile,
    capturePlaybackMutationContext: () => {
      const capturedProfile = profile;
      return {
        ...config.capturePlaybackMutationContext!()!,
        profileId: capturedProfile,
        isCurrent: () => profile === capturedProfile,
      };
    },
    onPlaybackStartError: (_error: Error, action: () => void) => {
      retry = action;
    },
  };
  vi.stubGlobal("navigator", {
    locks: {
      request: (_key: string, _options: unknown, action: () => Promise<unknown>) => action(),
    },
  });
  const fetcher = vi.fn().mockImplementation(async (url: string) => {
    if (url.endsWith("capabilities")) return reply(cap);
    throw new TypeError("lost");
  });
  vi.stubGlobal("fetch", fetcher);
  await expect(startInitialPlayback(scoped, body)).rejects.toThrow("lost");
  expect(retry).toBeTypeOf("function");
  const oldKey = Object.keys(localStorage)[0]!;
  profile = "other-profile";
  const newKey = oldKey.replace(
    encodeURIComponent('"profile"'),
    encodeURIComponent('"other-profile"'),
  );
  localStorage.setItem(
    newKey,
    localStorage.getItem(oldKey)!.replace('"profile"', '"other-profile"'),
  );
  fetcher.mockClear();
  retry!();
  await Promise.resolve();
  await Promise.resolve();
  expect(fetcher).not.toHaveBeenCalled();
  expect(localStorage.length).toBe(2);
});
it("does not fall back to legacy while an earlier start remains uncertain", async () => {
  vi.stubGlobal("navigator", {
    locks: {
      request: (_key: string, _options: unknown, action: () => Promise<unknown>) => action(),
    },
  });
  const fetcher = vi.fn().mockImplementation(async (url: string) => {
    if (url.endsWith("capabilities")) return reply(cap);
    throw new TypeError("lost");
  });
  vi.stubGlobal("fetch", fetcher);
  await expect(startInitialPlayback(config, body)).rejects.toThrow("lost");
  fetcher.mockImplementation(async () =>
    reply({ state: "not_configured", allowed: false, protocol_versions: [], features: [] }),
  );
  await expect(startInitialPlayback(config, body)).rejects.toThrow(
    "API v2 playback is not configured",
  );
  expect(localStorage.length).toBe(1);
});
it("retains the same attempt after a lost response followed by validation_failed", async () => {
  let reject = true;
  let starts = 0;
  const fetcher = boundTransport(async () => {
    starts++;
    if (starts === 1) throw new TypeError("lost successful reply");
    return reject ? reply({ status: 422 }, 422) : reply(startAborted, 201);
  });
  await expect(startInitialPlayback(config, body)).rejects.toThrow("Failed to start playback");
  const stored = localStorage.getItem(localStorage.key(0)!);
  await expect(
    startInitialPlayback(config, { ...body, playback_attempt_id: "different-attempt" }),
  ).rejects.toThrow("Failed to start playback");
  expect(localStorage.getItem(localStorage.key(0)!)).toBe(stored);
  expect(starts).toBe(3);
  reject = false;
  await startInitialPlayback(config, body);
  expect(localStorage.length).toBe(0);
  const requests = fetcher.mock.calls.filter(([url]) => url.endsWith("/start"));
  expect(requests).toHaveLength(4);
  for (const request of requests) expect(request[1]?.body).toBe(stored);
});
it.each([
  {
    status: 422,
    problem: { type: "https://siloserver.org/docs/api/v2/problems/validation_failed", status: 422 },
  },
  {
    status: 422,
    problem: { type: "https://siloserver.org/docs/api/v2/problems/other", status: 422 },
  },
  {
    status: 503,
    problem: { type: "https://siloserver.org/docs/api/v2/problems/validation_failed", status: 503 },
  },
  { status: 422, problem: {} },
])(
  "retains the start journal for uncertain rejection $status $problem.type",
  async ({ status, problem }) => {
    vi.stubGlobal("navigator", {
      locks: {
        request: (_key: string, _options: unknown, action: () => Promise<unknown>) => action(),
      },
    });
    const fetcher = vi
      .fn()
      .mockImplementation(async (url: string) =>
        url.endsWith("capabilities") ? reply(cap) : reply(problem, status),
      );
    vi.stubGlobal("fetch", fetcher);
    await expect(startInitialPlayback(config, body)).rejects.toThrow("Failed to start playback");
    expect(localStorage.length).toBe(1);
    await expect(
      startInitialPlayback(config, { ...body, playback_attempt_id: "new-attempt" }),
    ).rejects.toThrow();
    expect(
      new Set(fetcher.mock.calls.filter((c) => c[0].endsWith("/start")).map((c) => c[1].body)).size,
    ).toBe(1);
  },
);

it("requires v2 capabilities and captured identity before start", async () => {
  const fetcher = vi.fn().mockResolvedValue(reply({}, 404));
  vi.stubGlobal("fetch", fetcher);
  await expect(startInitialPlayback(config, body)).rejects.toThrow(
    "API v2 playback is unavailable",
  );
  expect(fetcher).toHaveBeenCalledTimes(1);
  fetcher.mockClear();
  await expect(
    startInitialPlayback({ ...config, capturePlaybackMutationContext: undefined }, body),
  ).rejects.toThrow("identity unavailable");
  expect(fetcher).not.toHaveBeenCalled();
});

const timeline = {
  timeline_id: "a".repeat(64),
  media_item_id: "book",
  file_id: "42",
  part_offset_seconds: 600,
  part_duration_seconds: 300,
  duration_seconds: 900,
};
const boundBody = {
  ...body,
  progress_persistence: "client_bound" as const,
  timeline_id: timeline.timeline_id,
  start_position: 30,
  client_features: [...body.client_features, "bound_client_timeline"],
};
const timelineTerminal = {
  protocol_version: 3,
  server_features: ["bound_client_timeline"],
  outcome: "adaptation_unavailable",
  terminal: {
    reason: "client_timeline_changed",
    message: "The audiobook changed. Start a new playback request.",
    retryable: false,
  },
};
function boundTransport(response: () => Promise<Response>) {
  vi.stubGlobal("navigator", {
    locks: {
      request: (_key: string, _options: unknown, action: () => Promise<unknown>) => action(),
    },
  });
  const fetcher = vi.fn(async (url: string, _init?: RequestInit) =>
    url.endsWith("capabilities")
      ? reply({ ...cap, features: [...cap.features, "bound_client_timeline"] })
      : response(),
  );
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
it("retires only the exact retained201 timeline terminal, then permits a new explicit attempt", async () => {
  let lost = true;
  const fetcher = boundTransport(async () => {
    if (lost) throw new TypeError("lost publication response");
    return reply(timelineTerminal, 201);
  });
  await expect(startInitialPlayback(config, boundBody, "installation", timeline)).rejects.toThrow(
    "lost publication",
  );
  const key = Object.keys(localStorage).find((key) => key.startsWith("silo-playback-start-v1:"))!;
  const original = localStorage.getItem(key);
  lost = false;
  vi.resetModules();
  const reloaded = await import("./initial-v2");
  expect(
    await reloaded.startInitialPlayback(config, boundBody, "installation", timeline),
  ).toMatchObject(timelineTerminal);
  const starts = () => fetcher.mock.calls.filter(([url]) => url.endsWith("/start"));
  const retainedCount = starts().length;
  expect(retainedCount).toBeGreaterThan(2);
  expect(starts()[1]![1]?.body).toBe(original);
  expect(localStorage.length).toBe(0);
  const fresh = { ...timeline, timeline_id: "b".repeat(64) };
  await reloaded.startInitialPlayback(
    config,
    { ...boundBody, playback_attempt_id: "new-explicit-intent", timeline_id: fresh.timeline_id },
    "installation",
    fresh,
  );
  expect(starts()).toHaveLength(retainedCount + 1);
  expect(JSON.parse(String(starts().slice(-1)[0]![1]?.body))).toMatchObject({
    playback_attempt_id: "new-explicit-intent",
    timeline_id: fresh.timeline_id,
  });
  expect(
    fetcher.mock.calls.every(([url]) => url.endsWith("/start") || url.endsWith("capabilities")),
  ).toBe(true);
});
it.each([
  [409, { code: "timeline_changed" }],
  [409, { detail: "conflict" }],
  [503, { detail: "publication unknown" }],
  [200, timelineTerminal],
  [201, { ...timelineTerminal, outcome: "playable" }],
  [201, { ...timelineTerminal, terminal: { ...timelineTerminal.terminal, retryable: true } }],
  [
    201,
    {
      ...timelineTerminal,
      terminal: { reason_code: "client_timeline_changed", retryable: false, message: "changed" },
    },
  ],
  [201, { ...timelineTerminal, session_id: "unexpected" }],
  [201, { ...timelineTerminal, playback_plan: null }],
])(
  "keeps nondefinitive or malformed bound refusal bytes unchanged: %j",
  async (status, response) => {
    const fetcher = boundTransport(async () => reply(response, status as number));
    await expect(
      startInitialPlayback(config, boundBody, "installation", timeline),
    ).rejects.toThrow();
    const key = Object.keys(localStorage).find((key) => key.startsWith("silo-playback-start-v1:"))!;
    const original = localStorage.getItem(key);
    expect(original).toBe(fetcher.mock.calls.find(([url]) => url.endsWith("/start"))![1]?.body);
    await expect(
      startInitialPlayback(
        config,
        { ...boundBody, playback_attempt_id: "replacement" },
        "installation",
        timeline,
      ),
    ).rejects.toThrow();
    expect(localStorage.getItem(key)).toBe(original);
    expect(
      new Set(
        fetcher.mock.calls.filter(([url]) => url.endsWith("/start")).map((call) => call[1]?.body),
      ).size,
    ).toBe(1);
    expect(
      Object.keys(localStorage).some((key) => key.startsWith("silo-playback-mutation-v1:")),
    ).toBe(false);
  },
);
it("does not retire a late201 refusal after the original authority changes", async () => {
  let current = true;
  const captured = {
    ...config,
    capturePlaybackMutationContext: () => ({
      ...config.capturePlaybackMutationContext!()!,
      isCurrent: () => current,
    }),
  };
  boundTransport(async () => {
    current = false;
    return reply(timelineTerminal, 201);
  });
  await expect(startInitialPlayback(captured, boundBody, "installation", timeline)).rejects.toThrow(
    "identity changed",
  );
  expect(Object.keys(localStorage).some((key) => key.startsWith("silo-playback-start-v1:"))).toBe(
    true,
  );
});

const ownerRecovery = {
  recovery_id: "recovery",
  playback_attempt_id: "attempt",
  session_id: "lost-session",
  state: "draining",
  reason: "owner_lost",
};
const ownerTerminal = {
  protocol_version: 3,
  server_features: ["sequenced_progress_v1"],
  outcome: "adaptation_unavailable",
  terminal: {
    reason: "playback_owner_lost",
    message: "Playback ended after its server owner was lost.",
    retryable: false,
  },
  recovery: { ...ownerRecovery, state: "aborted" },
};
it("automatically retries a lost START through draining and records terminal abandonment", async () => {
  let phase = 0;
  const fetcher = boundTransport(async () => {
    if (phase++ === 0) throw new TypeError("lost response");
    return phase === 2
      ? reply({ outcome: "draining", recovery: ownerRecovery }, 202)
      : reply(ownerTerminal, 201);
  });
  const onPlaybackStartError = vi.fn();
  expect(await startInitialPlayback({ ...config, onPlaybackStartError }, body)).toEqual(
    ownerTerminal,
  );
  const starts = fetcher.mock.calls.filter(([url]) => url.endsWith("/start"));
  const original = starts[0]![1]?.body;
  expect(starts).toHaveLength(3);
  expect(starts.every((call) => call[1]?.body === original)).toBe(true);
  expect(onPlaybackStartError).not.toHaveBeenCalled();
  expect(Object.keys(localStorage).some((key) => key.startsWith("silo-playback-start-v1:"))).toBe(
    false,
  );
  const recoveryKey = Object.keys(localStorage).find((key) =>
    key.startsWith("silo-playback-start-recovery-v1:"),
  )!;
  expect(JSON.parse(localStorage.getItem(recoveryKey)!)).toMatchObject({
    originalStart: original,
    recovery: ownerTerminal.recovery,
  });
  expect(
    Object.keys(localStorage).some((key) => key.startsWith("silo-playback-mutation-v1:")),
  ).toBe(false);
});
it.each([
  [200, ownerTerminal],
  [
    202,
    {
      outcome: "draining",
      recovery: { ...ownerRecovery, accepted: { sequence: 1, position: 4, is_paused: false } },
    },
  ],
  [
    201,
    { ...ownerTerminal, recovery: { ...ownerTerminal.recovery, playback_attempt_id: "foreign" } },
  ],
  [201, { ...ownerTerminal, playback_plan: {} }],
  [201, { ...ownerTerminal, terminal: { ...ownerTerminal.terminal, retryable: true } }],
  [
    201,
    {
      ...ownerTerminal,
      recovery: {
        ...ownerTerminal.recovery,
        accepted: { sequence: 0, position: 4, is_paused: false },
      },
    },
  ],
])(
  "keeps the original START pending for invalid owner-loss receipt %j",
  async (status, receipt) => {
    boundTransport(async () => reply(receipt, status as number));
    await expect(startInitialPlayback(config, body)).rejects.toThrow();
    expect(Object.keys(localStorage).some((key) => key.startsWith("silo-playback-start-v1:"))).toBe(
      true,
    );
  },
);
it("refuses a changed recovery identity after observing the original pending START", async () => {
  let receipt: unknown = { outcome: "draining", recovery: ownerRecovery };
  let status = 202;
  boundTransport(async () => reply(receipt, status));
  await expect(startInitialPlayback(config, body)).rejects.toThrow("still stopping");
  const snapshot = { ...localStorage };
  receipt = {
    ...ownerTerminal,
    recovery: { ...ownerTerminal.recovery, session_id: "foreign-session" },
  };
  status = 201;
  await expect(startInitialPlayback(config, body)).rejects.toThrow("identity changed");
  expect({ ...localStorage }).toEqual(snapshot);
});

it("retains the exact START when terminal abandonment storage fails", async () => {
  const fetcher = boundTransport(async () => reply(ownerTerminal, 201));
  const originalSet = Storage.prototype.setItem;
  const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (
    this: Storage,
    key,
    value,
  ) {
    if (key.startsWith("silo-playback-start-recovery-v1:")) throw new Error("storage full");
    return originalSet.call(this, key, value);
  });
  try {
    await expect(startInitialPlayback(config, body)).rejects.toThrow("storage full");
    const start = fetcher.mock.calls.find(([url]) => url.endsWith("/start"))!;
    const key = Object.keys(localStorage).find((key) => key.startsWith("silo-playback-start-v1:"))!;
    expect(localStorage.getItem(key)).toBe(start[1]?.body);
  } finally {
    spy.mockRestore();
  }
});

it("refuses a changed completed recovery after a crash before START journal cleanup", async () => {
  let receipt = ownerTerminal;
  boundTransport(async () => reply(receipt, 201));
  const originalRemove = Storage.prototype.removeItem;
  const spy = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(function (
    this: Storage,
    key,
  ) {
    if (key.startsWith("silo-playback-start-v1:")) throw new Error("cleanup interrupted");
    return originalRemove.call(this, key);
  });
  try {
    await expect(startInitialPlayback(config, body)).rejects.toThrow("cleanup interrupted");
  } finally {
    spy.mockRestore();
  }
  const snapshot = { ...localStorage };
  receipt = { ...ownerTerminal, recovery: { ...ownerTerminal.recovery, recovery_id: "changed" } };
  await expect(startInitialPlayback(config, body)).rejects.toThrow("identity changed");
  expect({ ...localStorage }).toEqual(snapshot);
  receipt = ownerTerminal;
  await expect(startInitialPlayback(config, body)).resolves.toEqual(ownerTerminal);
});

it.each([false, true])(
  "keeps exact START and timeline journals when owner-loss proof is missing (bound=%s)",
  async (bound) => {
    const { recovery: _recovery, ...incomplete } = ownerTerminal;
    const fetcher = boundTransport(async () => reply(incomplete, 201));
    const request = bound ? boundBody : body;
    const mapping = bound ? timeline : undefined;
    await expect(startInitialPlayback(config, request, "installation", mapping)).rejects.toThrow(
      "no recovery receipt",
    );
    const key = Object.keys(localStorage).find((key) => key.startsWith("silo-playback-start-v1:"))!;
    const original = fetcher.mock.calls.find(([url]) => url.endsWith("/start"))![1]?.body;
    expect(localStorage.getItem(key)).toBe(original);
    const timelineKey = key.replace("silo-playback-start-v1:", "silo-playback-start-timeline-v1:");
    expect(localStorage.getItem(timelineKey)).toBe(bound ? JSON.stringify(timeline) : null);
    expect(
      Object.keys(localStorage).some((key) => key.startsWith("silo-playback-start-recovery-v1:")),
    ).toBe(false);
    const saved = { ...localStorage };
    await expect(
      startInitialPlayback(
        config,
        { ...request, playback_attempt_id: "fresh" },
        "installation",
        mapping,
      ),
    ).rejects.toThrow();
    expect({ ...localStorage }).toEqual(saved);
    expect(
      new Set(
        fetcher.mock.calls.filter(([url]) => url.endsWith("/start")).map((call) => call[1]?.body),
      ).size,
    ).toBe(1);
  },
);

it.each(["success", "stop_failure", "owner_loss", "cleanup_crash"])(
  "finishes the exact undisplayed session before a new Play: %s",
  async (mode) => {
    const fetcher = boundTransport(async () => {
      throw new TypeError("lost response");
    });
    await expect(startInitialPlayback(config, body)).rejects.toThrow("lost response");
    const retained = fetcher.mock.calls.find(([url]) => url.endsWith("/start"))![1]?.body;
    fetcher.mockClear();
    const plan = fixturePlanV3();
    let stopFails = mode === "stop_failure";
    let stopAttempted = false;
    fetcher.mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.endsWith("capabilities")) return reply(cap);
      if (options?.method === "DELETE") {
        expect(url).toContain("undisplayed-session");
        stopAttempted = true;
        if (stopFails) return reply({}, 503);
        if (mode === "owner_loss")
          return reply({
            outcome: "aborted",
            recovery: { ...ownerTerminal.recovery, session_id: "undisplayed-session" },
          });
        return reply({ outcome: "stopped", stop_id: JSON.parse(String(options.body)).stop_id });
      }
      const request = JSON.parse(String(options?.body));
      if (request.playback_attempt_id !== "attempt") return reply(startAborted, 201);
      if (stopAttempted) return reply({}, 503); // STOP may already have committed on the server.
      return reply(
        {
          protocol_version: 3,
          server_features: ["sequenced_progress_v1"],
          outcome: "play",
          session_id: "undisplayed-session",
          playback_plan: {
            ...plan,
            requested_media_file_id: "42",
            effective_media_file_id: "42",
            source: { ...plan.source, media_file_id: "42" },
          },
        },
        201,
      );
    });
    if (stopFails) {
      for (let tries = 0; tries < 2; tries++) {
        await expect(
          startInitialPlayback(config, { ...body, playback_attempt_id: "new-intent" }),
        ).rejects.toThrow("stop is still pending");
        const key = Object.keys(localStorage).find((key) =>
          key.startsWith("silo-playback-start-v1:"),
        )!;
        expect(localStorage.getItem(key)).toBe(retained);
        const starts = fetcher.mock.calls.filter(([url]) => url.endsWith("/start"));
        expect(starts).toHaveLength(1);
        expect(starts.every((call) => call[1]?.body === retained)).toBe(true);
      }
      const stops = fetcher.mock.calls.filter((call) => call[1]?.method === "DELETE");
      expect(new Set(stops.map((call) => call[1]?.body)).size).toBe(1);
      stopFails = false;
      // Reload resumes both original operations, including the saved STOP ID.
      vi.resetModules();
      const reloaded = await import("./initial-v2");
      await settle(reloaded.offerPendingInitialStart(config, cap));
      expect(
        fetcher.mock.calls.filter((call) => call[1]?.method === "DELETE").slice(-1)[0]![1]?.body,
      ).toBe(stops[0]![1]?.body);
      fetcher.mockClear();
    }
    if (mode === "cleanup_crash") {
      const remove = Storage.prototype.removeItem;
      const spy = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(function (
        this: Storage,
        key,
      ) {
        if (key.startsWith("silo-playback-start-v1:")) throw new Error("cleanup interrupted");
        return remove.call(this, key);
      });
      try {
        await expect(
          startInitialPlayback(config, { ...body, playback_attempt_id: "new-intent" }),
        ).rejects.toThrow("cleanup interrupted");
      } finally {
        spy.mockRestore();
      }
    }
    await startInitialPlayback(config, { ...body, playback_attempt_id: "new-intent" });
    const effects = fetcher.mock.calls.filter(([url]) => !url.endsWith("capabilities"));
    if (mode === "stop_failure") {
      expect(effects).toHaveLength(1);
      expect(JSON.parse(String(effects[0]![1]?.body))).toMatchObject({
        playback_attempt_id: "new-intent",
      });
      return;
    }
    expect(effects.map((call) => call[1]?.method)).toEqual(["POST", "DELETE", "POST"]);
    expect(effects[0]![1]?.body).toBe(retained);
    expect(JSON.parse(String(effects[1]![1]?.body))).toMatchObject({
      installation_id: "installation",
      stop_id: expect.any(String),
    });
    expect(JSON.parse(String(effects[2]![1]?.body))).toMatchObject({
      playback_attempt_id: "new-intent",
    });
    expect(Object.keys(localStorage).some((key) => key.startsWith("silo-playback-start-v1:"))).toBe(
      false,
    );
  },
);

it("shares one automatic recovery task between video and audiobook hosts", async () => {
  const fetcher = boundTransport(async () => {
    throw new TypeError("lost response");
  });
  await expect(startInitialPlayback(config, body)).rejects.toThrow("lost response");
  fetcher.mockClear();
  fetcher.mockImplementation(async (url: string) =>
    url.endsWith("capabilities") ? reply(cap) : reply(startAborted, 201),
  );
  const resolved = vi.fn();
  const failed = vi.fn();
  const scoped = { ...config, onPlaybackStartResolved: resolved, onPlaybackStartError: failed };
  const video = offerPendingInitialStart(scoped, cap);
  const audiobook = offerPendingInitialStart(scoped, cap);
  expect(video).toBe(audiobook);
  await settle(video);
  expect(fetcher.mock.calls.filter(([url]) => url.endsWith("/start"))).toHaveLength(1);
  expect(resolved).toHaveBeenCalledTimes(1);
  expect(failed).not.toHaveBeenCalled();
});

it("bounds the whole START recovery, including the final network request", async () => {
  const originalTimeout = vi.spyOn(AbortSignal, "timeout").mockImplementation((ms) => {
    const controller = new AbortController();
    setTimeout(() => controller.abort(new DOMException("timed out", "TimeoutError")), ms);
    return controller.signal;
  });
  let requests = 0;
  const startAt = Date.now();
  boundTransport(async () => {
    throw new Error("unused");
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url.endsWith("capabilities")) return reply(cap);
      if (++requests === 1) {
        await new Promise((resolve) => setTimeout(resolve, 44000));
        return reply({}, 503);
      }
      return new Promise<Response>((_resolve, reject) =>
        options!.signal!.addEventListener("abort", () => reject(options!.signal!.reason), {
          once: true,
        }),
      );
    }),
  );
  let finishedAt = 0;
  const result = dispatchStart(config, body).catch((error) => {
    finishedAt = Date.now();
    return error;
  });
  try {
    await vi.advanceTimersByTimeAsync(60000);
    expect(await result).toBeInstanceOf(DOMException);
    expect(finishedAt - startAt).toBe(60000);
    expect(requests).toBe(2);
    expect(Object.keys(localStorage).some((key) => key.startsWith("silo-playback-start-v1:"))).toBe(
      true,
    );
  } finally {
    originalTimeout.mockRestore();
  }
});
