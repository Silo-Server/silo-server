import type { components } from "@/api/v2/schema";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { playerRequestHeaders } from "./player-fetch";
import { playerV2Origin } from "./player-v2";
import type { RouteEventV3 } from "./protocol-v3";
import { sessionInstallation } from "./session-mutations";
import { randomUUID } from "@/lib/uuid";

type RouteEventBody = components["schemas"]["PlaybackRouteEventBody"];

/**
 * Reports a route event for a v2 attempt. Diagnostics never control
 * playback: the event carries a fresh id, is sent once, and any failure is
 * swallowed. A lost 202 is not retried by this helper; a caller that does
 * retry must reuse the same event_id.
 */
export async function reportRouteEventV2(
  config: PlayerConfig,
  installationId: string,
  event: RouteEventV3,
): Promise<void> {
  const payload: RouteEventBody = {
    ...event,
    installation_id: installationId,
    event_id: randomUUID(),
  } as RouteEventBody;
  try {
    await fetch(`${playerV2Origin(config)}/api/v2/playback/route-events`, {
      method: "POST",
      headers: playerRequestHeaders(config, { Accept: "application/json" }, true),
      body: JSON.stringify(payload),
      signal: AbortSignal.timeout(5000),
    });
  } catch {
    // Dropped, never retried.
  }
}

/** Reports a route event for a registered v2 session; a v1 session is skipped. */
export function reportSessionRouteEventV2(
  config: PlayerConfig,
  sessionId: string,
  event: RouteEventV3,
): Promise<void> {
  const installationId = sessionInstallation(sessionId);
  if (!installationId) return Promise.resolve();
  return reportRouteEventV2(config, installationId, event);
}
