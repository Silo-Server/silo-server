import type { components } from "@/api/v2/schema";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { playerFetchResponse, PlayerFetchError } from "./player-fetch";
import { playerV2Origin } from "./player-v2";
import type { DecisionResponseV3, ReplanRequestV3 } from "./protocol-v3";
import { sessionInstallation } from "./session-mutations";
import { decisionFromWireV2 } from "./start-v2";

type ReplanBody = components["schemas"]["PlaybackReplanBody"];

// A replan that starts a new transport returns only after FFmpeg has produced
// its first safe manifest, which the server allows up to 30 s for (software
// tone-mapped 4K HDR routinely needs 10-15 s). Aborting earlier cancels the
// request context and kills the transport mid-startup, so the client budget
// must exceed the server's, matching the 45 s given to HLS loads.
const REPLAN_TIMEOUT_MS = 45_000;

/**
 * Replans a v2 session: the ordinary v3 replan body plus the installation the
 * session was started with. Failure recovery, seek re-anchor and track,
 * quality or output changes all go through here. Refusals surface as
 * PlayerFetchError so the hook's existing handling applies.
 */
export async function replanV2(
  config: PlayerConfig,
  sessionId: string,
  body: ReplanRequestV3,
): Promise<DecisionResponseV3> {
  const installationId = sessionInstallation(sessionId);
  if (!installationId) throw new Error("Playback session was not started on API v2");
  const payload: ReplanBody = { ...body, installation_id: installationId } as ReplanBody;
  const response = await playerFetchResponse(
    config,
    `${playerV2Origin(config)}/api/v2/playback/${encodeURIComponent(sessionId)}/replan`,
    {
      method: "POST",
      headers: { Accept: "application/json" },
      body: JSON.stringify(payload),
      signal: AbortSignal.timeout(REPLAN_TIMEOUT_MS),
    },
  );
  const text = await response.text().catch(() => "");
  if (!response.ok) {
    let message = "Playback replan was refused";
    let code: string | undefined;
    try {
      const problem = JSON.parse(text) as { detail?: string; type?: string };
      if (problem.detail) message = problem.detail;
      code = problem.type?.split("?")[0]?.split("/").pop()?.replace(/#.*$/, "") || undefined;
    } catch {
      // A non-problem body keeps the generic message.
    }
    throw new PlayerFetchError(response.status, message, code, text);
  }
  return decisionFromWireV2(JSON.parse(text) as components["schemas"]["PlaybackDecision"]);
}
