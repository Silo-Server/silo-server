import { pendingDurableSessions, retainedStartSession } from "./durable-session-mutations";
import {
  readOwnerLossRecovery,
  PlaybackOwnerLostError,
  type OwnerLossRecovery,
} from "./owner-loss-recovery";
import { readProgressTimeline, type ProgressTimeline } from "./bound-client-timeline";
import type { components } from "@/api/v2/schema";
import type { PlaybackMutationContext, PlayerConfig } from "./context/PlayerConfigContext";
import { playerRequestHeaders, PlayerFetchError } from "./player-fetch";
import { playerV2Origin } from "./player-v2";
import type { DecisionResponseV3, StartRequestV3 } from "./protocol-v3";
import {
  offerPendingPlaybackStops,
  registerDurableSessionMutations,
  stopSequencedSession,
} from "./session-mutations";

export type InitialPlaybackCapabilities = components["schemas"]["PlaybackCapabilities"];
export async function initialPlaybackCapabilities(
  config: PlayerConfig,
): Promise<InitialPlaybackCapabilities> {
  if (!config.capturePlaybackMutationContext)
    throw new Error("Playback account identity unavailable");
  const authority = config.capturePlaybackMutationContext();
  if (!authority?.isCurrent()) throw new Error("Playback account identity unavailable");
  const response = await fetch(`${playerV2Origin(config)}/api/v2/playback/capabilities`, {
    headers: playerRequestHeaders(config, undefined, false),
    signal: AbortSignal.timeout(5000),
  });
  if (!authority.isCurrent()) throw new Error("Playback identity changed");
  if (response.status === 404) throw new Error("API v2 playback is unavailable on this server");
  if (!response.ok)
    throw new PlayerFetchError(response.status, "Playback capabilities unavailable");
  const cap = (await response.json()) as InitialPlaybackCapabilities;
  if (
    (cap.state !== "not_configured" && !cap.installation_id) ||
    !Array.isArray(cap.features) ||
    !Array.isArray(cap.protocol_versions)
  )
    throw new Error("Invalid playback capabilities");
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
export async function startInitialPlayback(
  config: PlayerConfig,
  body: StartRequestV3,
  expectedInstallationId?: string,
  expectedTimeline?: Readonly<ProgressTimeline>,
): Promise<DecisionResponseV3> {
  if (body.progress_persistence === "client_bound") {
    if (!expectedTimeline)
      throw new Error("Bound playback requires its retained timeline selection");
    expectedTimeline = readProgressTimeline(
      expectedTimeline,
      expectedTimeline.media_item_id,
      String(body.file_id),
    );
    if (expectedTimeline.timeline_id !== body.timeline_id)
      throw new Error("Playback timeline does not match its retained selection");
  }
  const cap = await initialPlaybackCapabilities(config);
  if (cap.state === "not_configured") {
    const pending = pendingStartInstallation(config);
    if (pending) {
      offerPendingInitialStart(config, {
        installation_id: pending,
        revision: "",
        state: "not_configured",
        allowed: false,
        features: [],
        protocol_versions: [],
        deliveries: [],
      });
      throw new Error(
        "An earlier playback start is still unconfirmed; API v2 playback is not configured.",
      );
    }
    throw new Error("API v2 playback is not configured on this server");
  }
  if (
    !cap.installation_id ||
    cap.state !== "available" ||
    !cap.allowed ||
    !cap.protocol_versions.includes(3) ||
    !cap.features.includes("sequenced_progress_v1")
  )
    throw new Error("Playback is not admitted for this account");
  if (expectedInstallationId && cap.installation_id !== expectedInstallationId)
    throw new Error("Playback installation changed; start a new explicit request");
  if (
    body.progress_persistence === "client_bound" &&
    (!cap.features.includes("bound_client_timeline") || !body.timeline_id)
  )
    throw new Error("Bound audiobook playback is unavailable");
  if (!navigator.locks?.request)
    throw new Error("Durable playback requires browser storage locking");
  const authority = config.capturePlaybackMutationContext?.();
  if (!authority?.isCurrent()) throw new Error("Playback identity unavailable");
  const key = startKey(authority, cap.installation_id);
  const payload = JSON.stringify({
    ...body,
    file_id: String(body.file_id),
    installation_id: cap.installation_id,
  } satisfies components["schemas"]["PlaybackStartBody"]);
  return await navigator.locks.request(key, { signal: AbortSignal.timeout(90000) }, async () => {
    if (!authority.isCurrent()) throw new Error("Playback identity changed");
    let pending = localStorage.getItem(key);
    if (pending && pending !== payload) {
      await resolveRetainedStart(config, authority, cap.installation_id!, key, pending);
      pending = localStorage.getItem(key);
    }
    if (
      !pending &&
      body.progress_persistence === "client_bound" &&
      pendingDurableSessions(config, cap.installation_id!).some((session) => session.timeline)
    ) {
      offerPendingPlaybackStops(config, cap.installation_id!, true);
      throw new Error("A previous audiobook part still needs its terminal stop receipt");
    }
    if (expectedTimeline) {
      const expected = JSON.stringify(expectedTimeline);
      const prior = localStorage.getItem(timelineKey(key));
      if (pending && prior !== expected)
        throw new Error("Pending playback timeline must remain unchanged");
      localStorage.setItem(timelineKey(key), expected);
      if (localStorage.getItem(timelineKey(key)) !== expected)
        throw new Error("Playback timeline retry storage unavailable");
    }
    localStorage.setItem(key, payload);
    if (localStorage.getItem(key) !== payload)
      throw new Error("Playback retry storage unavailable");
    try {
      const response = await dispatchInitialStartWithRetry(
        config,
        authority,
        cap.installation_id!,
        key,
        payload,
      );
      config.onPlaybackStartResolved?.();
      return response;
    } catch (error) {
      if (authority.isCurrent()) reportStartRecoveryFailure(config, cap, authority);
      throw error;
    }
  });
}

function pendingStartInstallation(config: PlayerConfig): string | undefined {
  const context = config.capturePlaybackMutationContext?.();
  if (!context?.isCurrent()) return;
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (!key?.startsWith("silo-playback-start-v1:")) continue;
    const identity = JSON.parse(
      decodeURIComponent(key.slice("silo-playback-start-v1:".length)),
    ) as { installationId: string; accountId: string; profileId: string; origin: string };
    if (
      identity.accountId === context.accountId &&
      identity.profileId === context.profileId &&
      identity.origin === context.origin
    )
      return identity.installationId;
  }
}

// Separate metadata preserves the exact wire body and the legacy journal namespace.
function timelineKey(key: string): string {
  return key.replace("silo-playback-start-v1:", "silo-playback-start-timeline-v1:");
}
function startKey(context: PlaybackMutationContext, installationId: string): string {
  return (
    "silo-playback-start-v1:" +
    encodeURIComponent(
      JSON.stringify({
        installationId,
        accountId: context.accountId,
        profileId: context.profileId,
        origin: context.origin,
      }),
    )
  );
}
function readSavedInitialStart(
  payload: string,
  authority: PlaybackMutationContext,
  installationId: string,
) {
  const saved = JSON.parse(payload) as {
    installation_id?: string;
    playback_attempt_id?: string;
    profile_id?: string;
    progress_persistence?: string;
    timeline_id?: string;
    file_id?: string;
  };
  if (
    payload.length > 65536 ||
    saved.installation_id !== installationId ||
    saved.profile_id !== authority.profileId ||
    !saved.playback_attempt_id
  )
    throw new Error("Invalid saved playback start identity");
  return { ...saved, playback_attempt_id: saved.playback_attempt_id };
}
async function dispatchInitialStart(
  config: PlayerConfig,
  authority: PlaybackMutationContext,
  installationId: string,
  key: string,
  payload: string,
  timeout: number,
  retainUntilStopped: boolean,
): Promise<DecisionResponseV3> {
  if (!authority.isCurrent()) throw new Error("Playback identity changed");
  const saved = readSavedInitialStart(payload, authority, installationId);
  let expectedTimeline;
  if (saved.progress_persistence === "client_bound") {
    const value = JSON.parse(
      localStorage.getItem(timelineKey(key)) ?? "null",
    ) as ProgressTimeline | null;
    expectedTimeline = readProgressTimeline(value, value?.media_item_id ?? "", saved.file_id ?? "");
    if (expectedTimeline.timeline_id !== saved.timeline_id)
      throw new Error("Saved playback timeline changed");
  }
  const response = await fetch(`${authority.origin}/api/v2/playback/start`, {
    method: "POST",
    headers: playerRequestHeaders(config, undefined, true),
    body: payload,
    signal: AbortSignal.timeout(timeout),
  });
  if (!authority.isCurrent()) throw new Error("Playback identity changed while starting");
  if (!response.ok) {
    // A rejection of this dispatch cannot prove that an earlier dispatch
    // with a lost reply never allocated a session. Retain the exact attempt
    // until a successful replay supplies its authoritative decision.
    throw new PlayerFetchError(response.status, "Failed to start playback");
  }
  const wire = (await response.json()) as components["schemas"]["PlaybackDecision"];
  if (!authority.isCurrent()) throw new Error("Playback identity changed while starting");
  if (localStorage.getItem(key) !== payload)
    throw new Error("The pending playback start changed while resolving its decision");
  const recoveryKey =
    key.replace("silo-playback-start-v1:", "silo-playback-start-recovery-v1:") +
    ":" +
    encodeURIComponent(saved.playback_attempt_id);
  const priorRaw = localStorage.getItem(recoveryKey);
  const prior = priorRaw
    ? (JSON.parse(priorRaw) as { originalStart: string; recovery: OwnerLossRecovery })
    : undefined;
  if (prior && (prior.originalStart !== payload || !prior.recovery))
    throw new Error("Playback recovery conflicts with the original START body");
  const recovery = readOwnerLossRecovery(wire, response.status, "start", {
    attemptId: saved.playback_attempt_id,
    timeline: expectedTimeline,
    prior: prior?.recovery,
  });
  if (recovery) {
    const recorded = JSON.stringify({
      originalStart: payload,
      recovery,
      timeline: expectedTimeline,
    });
    localStorage.setItem(recoveryKey, recorded);
    if (localStorage.getItem(recoveryKey) !== recorded)
      throw new Error("Playback recovery receipt could not be saved");
    if (recovery.state === "draining")
      throw new PlaybackStartDrainingError("Playback is still stopping on the server");
    // Persist terminal abandonment before releasing the old START. Its original bytes
    // and the server's accepted Last remain in the per-attempt recovery record.
    localStorage.removeItem(key);
    localStorage.removeItem(timelineKey(key));
    return wire as unknown as DecisionResponseV3;
  }
  if (prior) throw new Error("Playback recovery returned no matching recovery receipt");
  // A bound refusal is the retained decision for this exact dispatched attempt.
  // HTTP conflicts and publication failures were rejected above; they prove nothing.
  if (saved.progress_persistence === "client_bound" && (wire.terminal || !wire.playback_plan)) {
    if (
      response.status !== 201 ||
      wire.protocol_version !== 3 ||
      !Array.isArray(wire.server_features) ||
      !wire.server_features.every((feature) => typeof feature === "string") ||
      wire.outcome !== "adaptation_unavailable" ||
      !wire.terminal ||
      typeof wire.terminal.reason !== "string" ||
      !wire.terminal.reason ||
      typeof wire.terminal.message !== "string" ||
      typeof wire.terminal.retryable !== "boolean" ||
      Object.prototype.hasOwnProperty.call(wire, "session_id") ||
      Object.prototype.hasOwnProperty.call(wire, "playback_plan") ||
      (wire.terminal.reason === "client_timeline_changed" && wire.terminal.retryable !== false)
    )
      throw new Error("Bound playback start returned an unconfirmed terminal decision");
  }
  const plan = wire.playback_plan;
  const converted = plan
    ? {
        ...plan,
        requested_media_file_id: numericFileID(plan.requested_media_file_id),
        effective_media_file_id: numericFileID(plan.effective_media_file_id),
        source: { ...plan.source, media_file_id: numericFileID(plan.source.media_file_id) },
      }
    : undefined;
  let timeline;
  if (saved.progress_persistence === "client_bound" && wire.session_id) {
    const mapping = (wire as unknown as DecisionResponseV3).progress_timeline;
    timeline = readProgressTimeline(mapping, mapping?.media_item_id ?? "", saved.file_id ?? "");
    if (
      JSON.stringify(timeline) !== JSON.stringify(expectedTimeline) ||
      (plan &&
        (plan.effective_media_file_id !== saved.file_id ||
          plan.requested_media_file_id !== saved.file_id))
    )
      throw new Error("Playback timeline changed while starting");
  }
  if (wire.session_id)
    await registerDurableSessionMutations(
      config,
      wire.session_id,
      installationId,
      timeline,
      authority,
      saved.playback_attempt_id,
    );
  else if (!wire.terminal)
    throw new Error("Playback start returned no durable session or terminal decision");
  if (!retainUntilStopped || !wire.session_id) {
    localStorage.removeItem(key);
    localStorage.removeItem(timelineKey(key));
  }
  return { ...wire, playback_plan: converted } as DecisionResponseV3;
}

class PlaybackStartDrainingError extends Error {}

// Retrying this operation never changes the attempt or launches a replacement.
// A successful replay or retained terminal decision is the only journal release.
async function dispatchInitialStartWithRetry(
  config: PlayerConfig,
  authority: PlaybackMutationContext,
  installationId: string,
  key: string,
  payload: string,
  retainUntilStopped = false,
): Promise<DecisionResponseV3> {
  const deadline = Date.now() + 60000;
  let delay = 250;
  for (;;) {
    try {
      return await dispatchInitialStart(
        config,
        authority,
        installationId,
        key,
        payload,
        Math.min(45000, deadline - Date.now()),
        retainUntilStopped,
      );
    } catch (error) {
      const transient =
        error instanceof TypeError ||
        (error instanceof DOMException &&
          (error.name === "TimeoutError" || error.name === "AbortError")) ||
        (error instanceof PlayerFetchError && error.status >= 500) ||
        error instanceof PlaybackStartDrainingError;
      if (!transient || !authority.isCurrent() || Date.now() + delay >= deadline) throw error;
      await new Promise((resolve) => setTimeout(resolve, delay));
      if (!authority.isCurrent()) throw new Error("Playback identity changed");
      delay = Math.min(delay * 2, 2000);
    }
  }
}

async function resolveRetainedStart(
  config: PlayerConfig,
  authority: PlaybackMutationContext,
  installationId: string,
  key: string,
  payload: string,
): Promise<void> {
  const saved = readSavedInitialStart(payload, authority, installationId);
  const retained = retainedStartSession(config, installationId, saved.playback_attempt_id!);
  let sessionId: string | undefined;
  if (retained) {
    // Once START registered the session, STOP may have committed despite a lost
    // reply. Resume that captured STOP directly; a stopped session need not have
    // a replayable START decision anymore.
    sessionId = retained.identity.sessionId;
    await registerDurableSessionMutations(
      config,
      sessionId,
      installationId,
      retained.timeline,
      authority,
      saved.playback_attempt_id,
    );
  } else {
    const response = await dispatchInitialStartWithRetry(
      config,
      authority,
      installationId,
      key,
      payload,
      true,
    );
    sessionId = response.session_id;
  }
  // This response belongs to an earlier, undisplayed start. Close that exact
  // session through its normal durable STOP before returning to the new Play.
  if (sessionId) {
    try {
      await stopSequencedSession(config, sessionId);
    } catch (error) {
      // This exception follows a durably validated terminal receipt. No old
      // executor remains to stop, so the user's new Play may continue.
      if (!(error instanceof PlaybackOwnerLostError)) throw error;
    }
    if (!authority.isCurrent()) throw new Error("Playback identity changed");
    if (localStorage.getItem(key) !== payload)
      throw new Error("The pending playback start changed while stopping its session");
    localStorage.removeItem(key);
    localStorage.removeItem(timelineKey(key));
  }
  config.onPlaybackStartResolved?.();
}

const recoveringStarts = new Map<string, Promise<void>>();

// The video and audiobook hosts share the same durable START namespace. One
// recovery task per key avoids duplicate requests and notifications in this tab;
// the existing Web Lock serializes recovery against starts in other tabs.
export function offerPendingInitialStart(
  config: PlayerConfig,
  cap: InitialPlaybackCapabilities,
): Promise<void> {
  const authority = config.capturePlaybackMutationContext?.();
  if (!authority?.isCurrent() || !cap.installation_id) return Promise.resolve();
  const installationId = cap.installation_id;
  const key = startKey(authority, installationId);
  if (!localStorage.getItem(key)) return Promise.resolve();
  const running = recoveringStarts.get(key);
  if (running) return running;
  const recovery = (async () => {
    try {
      const current = await initialPlaybackCapabilities(config);
      if (!authority.isCurrent()) return;
      if (
        !current.allowed ||
        current.state !== "available" ||
        current.installation_id !== installationId
      )
        throw new Error("Playback is currently unavailable on this server");
      if (!navigator.locks?.request) throw new Error("Playback storage locking is unavailable");
      await navigator.locks.request(key, { signal: AbortSignal.timeout(90000) }, async () => {
        if (!authority.isCurrent()) return;
        const payload = localStorage.getItem(key);
        if (payload) await resolveRetainedStart(config, authority, installationId, key, payload);
      });
    } catch {
      if (authority.isCurrent()) reportStartRecoveryFailure(config, cap, authority);
    } finally {
      recoveringStarts.delete(key);
    }
  })();
  recoveringStarts.set(key, recovery);
  return recovery;
}

function reportStartRecoveryFailure(
  config: PlayerConfig,
  cap: InitialPlaybackCapabilities,
  authority: PlaybackMutationContext,
): void {
  config.onPlaybackStartError?.(
    new Error("Unable to finish starting playback. Check your connection and try again."),
    () => {
      if (authority.isCurrent()) void offerPendingInitialStart(config, cap);
    },
  );
}
