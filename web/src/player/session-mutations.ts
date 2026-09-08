/**
 * Sequenced progress and stop for a session started on `/api/v2`.
 *
 * The server orders an attempt's samples by `sequence` with a compare-and-set
 * on the attempt row, so this module allocates one sequence per sample, sends
 * samples in order, and retries a lost reply with the identical body. A stop
 * mints one `stop_id`, waits for in-flight progress, and keeps its exact body
 * across retries; the server answers `stopped` for the first stop and
 * `replayed` for every later one, and both mean the session is over. There is
 * no 202, no draining state and nothing to poll.
 *
 * A session this module does not know (a bridge session started on v1) keeps
 * the v1 progress/stop bodies.
 */

import type { PlayerConfig } from "./context/PlayerConfigContext";
import { playerFetch, playerRequestHeaders, PlayerFetchError } from "./player-fetch";
import { playerV2Origin } from "./player-v2";
import { randomUUID } from "@/lib/uuid";

export type ProgressSample = { position: number; is_paused: boolean };

type Mutations = {
  installationId: string;
  readSample?: () => ProgressSample | null;
  latestSample?: ProgressSample;
  sequence: number;
  tail: Promise<unknown>;
  stopBody?: string;
  stopping?: Promise<void>;
  stopped?: boolean;
};
const sessions = new Map<string, Mutations>();

/** Registers a v2 session so its progress and stop are sequenced. */
export function registerSessionMutations(sessionId: string, installationId: string) {
  if (!sessions.has(sessionId)) {
    sessions.set(sessionId, { installationId, sequence: 0, tail: Promise.resolve() });
  }
}

/** Forgets every registered session; tests call this between cases. */
export function resetSessionMutations() {
  sessions.clear();
}

export function hasSequencedProgress(sessionId: string) {
  return sessions.has(sessionId);
}

/** The installation a v2 session was started with, if any. */
export function sessionInstallation(sessionId: string): string | undefined {
  return sessions.get(sessionId)?.installationId;
}

export function observeSessionProgress(sessionId: string, reader: () => ProgressSample | null) {
  const state = sessions.get(sessionId);
  if (!state) return () => {};
  state.readSample = reader;
  return () => {
    if (state.readSample === reader) state.readSample = undefined;
  };
}

export function captureSessionProgress(sessionId: string, sample: ProgressSample | null) {
  const state = sessions.get(sessionId);
  if (state && sample && !state.stopBody) state.latestSample = sample;
}

const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

async function waitForProgress(tail: Promise<unknown>) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      tail.catch(() => {}),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, 30_000);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function mutationFetch(
  config: PlayerConfig,
  path: string,
  method: string,
  body: string,
  keepalive: boolean,
  timeout: number,
) {
  const response = await fetch(`${playerV2Origin(config)}/api/v2${path}`, {
    method,
    body,
    keepalive,
    headers: playerRequestHeaders(config, { Accept: "application/json" }, true),
    signal: AbortSignal.timeout(timeout),
  });
  if (!response.ok) {
    let message = `Playback update failed (${response.status})`;
    let code: string | undefined;
    const text = await response.text().catch(() => "");
    try {
      const problem = JSON.parse(text) as { detail?: string; type?: string };
      if (problem.detail) message = problem.detail;
      code = problem.type?.split("?")[0]?.split("/").pop()?.replace(/#.*$/, "") || undefined;
    } catch {
      // A non-problem body keeps the generic message.
    }
    throw new PlayerFetchError(response.status, message, code, text);
  }
  return response;
}

function isTransient(error: unknown) {
  if (error instanceof PlayerFetchError) return error.status >= 500;
  return error instanceof TypeError || error instanceof DOMException;
}

/**
 * Sends one progress sample. Sequenced sessions serialize samples and retry
 * a transient failure with the same body; a v1 session posts the v1 body.
 */
export function sendSessionProgress(
  config: PlayerConfig,
  sessionId: string,
  sample: ProgressSample,
  keepalive = false,
): Promise<void> {
  const state = sessions.get(sessionId);
  if (!state)
    return playerFetch<void>(config, `/playback/${sessionId}/progress`, {
      method: "POST",
      body: JSON.stringify(sample),
      keepalive,
    });
  if (state.stopBody) return Promise.resolve();
  if (!Number.isSafeInteger(state.sequence + 1))
    return Promise.reject(new Error("Playback progress sequence exhausted"));
  state.latestSample = sample;
  const body = JSON.stringify({
    ...sample,
    sequence: ++state.sequence,
    installation_id: state.installationId,
  });
  const pending = state.tail
    .catch(() => {})
    .then(async () => {
      for (let attempt = 0; ; attempt++) {
        try {
          await mutationFetch(
            config,
            `/playback/${sessionId}/progress`,
            "POST",
            body,
            keepalive,
            5000,
          );
          return;
        } catch (error) {
          if (attempt >= 2 || !isTransient(error)) throw error;
          await pause(250);
        }
      }
    });
  state.tail = pending;
  return pending;
}

/**
 * Stops a session. A sequenced session mints one stop identity, attaches the
 * latest sample, waits for prior progress and retries the exact body until a
 * `stopped` or `replayed` receipt arrives or the 30s budget runs out. A v1
 * session sends the bodiless v1 DELETE.
 */
export function stopSequencedSession(
  config: PlayerConfig,
  sessionId: string,
  keepalive = false,
): Promise<void> {
  const state = sessions.get(sessionId);
  if (!state)
    return playerFetch<void>(config, `/playback/${sessionId}`, { method: "DELETE", keepalive });
  if (state.stopped) return Promise.resolve();
  if (state.stopping) return state.stopping;
  if (!state.stopBody) {
    const sample = state.readSample?.() ?? state.latestSample;
    if (sample && !Number.isSafeInteger(state.sequence + 1))
      return Promise.reject(new Error("Playback progress sequence exhausted"));
    state.stopBody = JSON.stringify({
      installation_id: state.installationId,
      stop_id: randomUUID(),
      ...(sample ? { ...sample, sequence: ++state.sequence } : {}),
    });
  }
  const body = state.stopBody;
  const stopId = (JSON.parse(body) as { stop_id: string }).stop_id;
  const deadline = Date.now() + 30_000;
  const stopping = waitForProgress(state.tail).then(async () => {
    for (;;) {
      try {
        const response = await mutationFetch(
          config,
          `/playback/${sessionId}`,
          "DELETE",
          body,
          keepalive,
          Math.max(1, Math.min(5000, deadline - Date.now())),
        );
        const receipt = (await response.json()) as { outcome?: string; stop_id?: string };
        if (
          receipt.outcome === "stopped" &&
          receipt.stop_id !== undefined &&
          receipt.stop_id !== stopId
        )
          throw new Error("Playback stop returned another receipt");
        if (receipt.outcome === "stopped" || receipt.outcome === "replayed") {
          state.stopped = true;
          return;
        }
        throw new Error(`Unexpected playback stop outcome (${receipt.outcome ?? "none"})`);
      } catch (error) {
        if (error instanceof PlayerFetchError && error.status === 404) {
          // The attempt is gone server-side: expired or never durable. Over.
          state.stopped = true;
          return;
        }
        if (!isTransient(error) || Date.now() >= deadline) throw error;
      }
      await pause(500);
    }
  });
  state.stopping = stopping;
  void stopping
    .finally(() => {
      state.stopping = undefined;
    })
    .catch(() => {});
  return stopping;
}
