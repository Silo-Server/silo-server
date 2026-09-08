/**
 * Starting playback on the native `/api/v2` contract.
 *
 * The flow is deliberately small: read capabilities (memoized per origin for
 * a minute), check the server offers protocol v3, POST the start with the
 * capabilities' `installation_id`, and retry the identical body while the
 * failure looks transient. Start is idempotent server-side on
 * `playback_attempt_id` plus the body digest, so a retry after a lost reply
 * returns the stored decision instead of a second session. A 4xx is final:
 * nothing is retained and the caller decides what to do.
 */

import type { components } from "@/api/v2/schema";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { playerRequestHeaders, PlayerFetchError } from "./player-fetch";
import { playerV2Origin } from "./player-v2";
import type { DecisionResponseV3, StartRequestV3 } from "./protocol-v3";
import { registerSessionMutations } from "./session-mutations";

export type PlaybackCapabilitiesV2 = components["schemas"]["PlaybackCapabilities"];

const CAPABILITIES_TTL_MS = 60_000;
const START_BUDGET_MS = 30_000;
const START_TIMEOUT_MS = 15_000;
const CAPABILITIES_TIMEOUT_MS = 5_000;

const capabilitiesCache = new Map<string, { at: number; cap: PlaybackCapabilitiesV2 }>();

/** Drops memoized capabilities; tests and a changed viewer call this. */
export function resetPlaybackCapabilitiesV2() {
  capabilitiesCache.clear();
}

/** Reads `GET /api/v2/playback/capabilities`, memoized per API origin. */
export async function playbackCapabilitiesV2(
  config: PlayerConfig,
  options: { force?: boolean } = {},
): Promise<PlaybackCapabilitiesV2> {
  const origin = playerV2Origin(config);
  const cached = capabilitiesCache.get(origin);
  if (!options.force && cached && Date.now() - cached.at < CAPABILITIES_TTL_MS) return cached.cap;
  const response = await fetch(`${origin}/api/v2/playback/capabilities`, {
    headers: playerRequestHeaders(config, { Accept: "application/json" }, false),
    signal: AbortSignal.timeout(CAPABILITIES_TIMEOUT_MS),
  });
  if (response.status === 404) throw new Error("API v2 playback is unavailable on this server");
  if (!response.ok)
    throw new PlayerFetchError(response.status, "Playback capabilities unavailable");
  const cap = (await response.json()) as PlaybackCapabilitiesV2;
  if (!Array.isArray(cap.features) || !Array.isArray(cap.protocol_versions))
    throw new Error("Invalid playback capabilities");
  capabilitiesCache.set(origin, { at: Date.now(), cap });
  return cap;
}

function numericFileID(value: unknown): number {
  if (typeof value !== "string" || !/^[1-9]\d*$/.test(value))
    throw new Error("Invalid playback media file ID");
  const n = Number(value);
  if (!Number.isSafeInteger(n))
    throw new Error("Playback media file ID exceeds this player's supported range");
  return n;
}

/** Converts a v2 decision (string ids) into the player's v3 shape (numeric ids). */
export function decisionFromWireV2(
  wire: components["schemas"]["PlaybackDecision"],
): DecisionResponseV3 {
  const plan = wire.playback_plan;
  const converted = plan
    ? {
        ...plan,
        requested_media_file_id: numericFileID(plan.requested_media_file_id),
        effective_media_file_id: numericFileID(plan.effective_media_file_id),
        source: { ...plan.source, media_file_id: numericFileID(plan.source.media_file_id) },
      }
    : undefined;
  return { ...wire, playback_plan: converted } as DecisionResponseV3;
}

function isTransient(error: unknown): boolean {
  if (error instanceof PlayerFetchError) return error.status >= 500;
  // Network failure, timeout, abort.
  return error instanceof TypeError || error instanceof DOMException;
}

const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/**
 * Starts playback through `POST /api/v2/playback/start` and registers the
 * session for sequenced progress and stop. Throws PlayerFetchError for a
 * refused start (4xx, including `installation_changed`) and a plain Error
 * when capabilities rule out v2 playback.
 */
export async function startPlaybackV2(
  config: PlayerConfig,
  body: StartRequestV3,
  options: { signal?: AbortSignal } = {},
): Promise<DecisionResponseV3> {
  const cap = await playbackCapabilitiesV2(config);
  if (cap.state === "not_configured")
    throw new Error("API v2 playback is not configured on this server");
  if (cap.state !== "available" || !cap.allowed || !cap.installation_id)
    throw new Error("Playback is not available for this account");
  if (!cap.protocol_versions.includes(3))
    throw new Error("This server does not offer playback protocol v3");
  const installationId = cap.installation_id;
  const payload = JSON.stringify({
    ...body,
    file_id: String(body.file_id),
    installation_id: installationId,
  } satisfies components["schemas"]["PlaybackStartBody"]);
  const url = `${playerV2Origin(config)}/api/v2/playback/start`;
  const deadline = Date.now() + START_BUDGET_MS;
  for (let attempt = 0; ; attempt++) {
    options.signal?.throwIfAborted();
    try {
      const controller = new AbortController();
      const timer = setTimeout(
        () => controller.abort(new DOMException("Playback start timed out", "TimeoutError")),
        Math.max(1, Math.min(START_TIMEOUT_MS, deadline - Date.now())),
      );
      const onAbort = () => controller.abort(options.signal?.reason);
      options.signal?.addEventListener("abort", onAbort, { once: true });
      let response: Response;
      try {
        response = await fetch(url, {
          method: "POST",
          headers: playerRequestHeaders(config, { Accept: "application/json" }, true),
          body: payload,
          signal: controller.signal,
        });
      } finally {
        clearTimeout(timer);
        options.signal?.removeEventListener("abort", onAbort);
      }
      const text = await response.text().catch(() => "");
      if (!response.ok) {
        let message = "Playback start was refused";
        let code: string | undefined;
        try {
          const problem = JSON.parse(text) as { detail?: string; title?: string; type?: string };
          message = problem.detail?.trim() || problem.title?.trim() || message;
          code = problem.type?.split("?")[0]?.split("/").pop()?.replace(/#.*$/, "") || undefined;
        } catch {
          // A non-problem body keeps the generic message.
        }
        if (code === "installation_changed") resetPlaybackCapabilitiesV2();
        throw new PlayerFetchError(response.status, message, code, text);
      }
      const decision = decisionFromWireV2(
        JSON.parse(text) as components["schemas"]["PlaybackDecision"],
      );
      const sessionId = decision.playback_plan?.session_id ?? decision.session_id;
      if (decision.playback_plan && sessionId) {
        registerSessionMutations(sessionId, installationId);
      }
      return decision;
    } catch (error) {
      if (options.signal?.aborted) throw error;
      if (!isTransient(error) || Date.now() >= deadline) throw error;
      await pause(Math.min(5_000, 500 * 2 ** attempt));
    }
  }
}
