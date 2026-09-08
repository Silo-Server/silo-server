import type { components } from "@/api/v2/schema";
import type { PlayerConfig, PlaybackMutationContext } from "./context/PlayerConfigContext";
import { playerRequestHeaders, PlayerFetchError } from "./player-fetch";
import { randomUUID } from "@/lib/uuid";
import {
  PlaybackOwnerLostError,
  readOwnerLossRecovery,
  type OwnerLossRecovery,
} from "./owner-loss-recovery";

import {
  readProgressTimeline,
  readBoundAcceptedProgress,
  type BoundAcceptedProgress,
  type ProgressTimeline,
} from "./bound-client-timeline";

const PREFIX = "silo-playback-mutation-v1:";
type Sample = { position: number; is_paused: boolean };
type Identity = {
  installationId: string;
  accountId: string;
  profileId: string;
  origin: string;
  sessionId: string;
};
type RecordV1 = {
  version: 1;
  identity: Identity;
  sequence: number;
  pendingProgress?: string;
  stopBody?: string;
  stopped: boolean;
  timeline?: Readonly<ProgressTimeline>;
  accepted?: BoundAcceptedProgress;
  attemptId?: string;
  recovery?: OwnerLossRecovery;
};
export type DurableSession = {
  key: string;
  identity: Identity;
  context: PlaybackMutationContext;
  timeline?: Readonly<ProgressTimeline>;
  onAccepted?: (accepted: BoundAcceptedProgress) => void;
  attemptId?: string;
};

function assertCurrent(config: PlayerConfig, binding: DurableSession) {
  const current = config.capturePlaybackMutationContext?.();
  if (
    !binding.context.isCurrent() ||
    !current ||
    current.accountId !== binding.identity.accountId ||
    current.profileId !== binding.identity.profileId ||
    current.origin !== binding.identity.origin ||
    !current.isCurrent()
  )
    throw new Error(
      "Playback retry belongs to another account, profile, or server. Return to its original identity to retry.",
    );
}
// A v2 administrator terminate command follows durable server revocation.
// Keep that proof separate from the client stop receipt and its exact journal.
const terminated = new Set<string>();
const terminationKey = (binding: DurableSession) =>
  "silo-playback-termination-v1:" + encodeURIComponent(JSON.stringify(binding.identity));
export function hasDurableTermination(binding: DurableSession): boolean {
  return terminated.has(binding.key) || localStorage.getItem(terminationKey(binding)) !== null;
}
export function recordDurableTermination(
  config: PlayerConfig,
  binding: DurableSession,
  commandId: string,
): void {
  assertCurrent(config, binding);
  if (!commandId) throw new Error("Playback termination command identity is missing");
  terminated.add(binding.key);
  try {
    localStorage.setItem(terminationKey(binding), commandId);
  } catch (error) {
    // Storage failure must not keep this page dispatching after revocation.
    console.error("Playback termination could not be saved", error);
  }
}
function read(binding: DurableSession): RecordV1 {
  const raw = localStorage.getItem(binding.key);
  if (!raw || raw.length > 65536) throw new Error("Playback retry state is missing or invalid");
  const record = JSON.parse(raw) as RecordV1;
  if (
    record.version !== 1 ||
    JSON.stringify(record.identity) !== JSON.stringify(binding.identity) ||
    !Number.isSafeInteger(record.sequence) ||
    record.sequence < 0 ||
    typeof record.stopped !== "boolean"
  )
    throw new Error("Playback retry state is invalid");
  if (JSON.stringify(record.timeline) !== JSON.stringify(binding.timeline))
    throw new Error("Playback timeline conflicts with its saved retry state");
  if (
    record.attemptId !== binding.attemptId ||
    (record.attemptId !== undefined && (typeof record.attemptId !== "string" || !record.attemptId))
  )
    throw new Error("Playback attempt conflicts with its saved retry state");
  for (const body of [record.pendingProgress, record.stopBody])
    if (body !== undefined) {
      if (typeof body !== "string") throw new Error("Playback retry payload is invalid");
      const payload = JSON.parse(body) as {
        installation_id?: string;
        sequence?: number;
        stop_id?: string;
        timeline_id?: string;
      };
      if (
        payload.installation_id !== binding.identity.installationId ||
        (binding.timeline && payload.timeline_id !== binding.timeline.timeline_id) ||
        (payload.sequence !== undefined &&
          (!Number.isSafeInteger(payload.sequence) ||
            payload.sequence < 1 ||
            payload.sequence > record.sequence))
      )
        throw new Error("Playback retry payload identity is invalid");
    }
  if (record.stopBody && typeof JSON.parse(record.stopBody).stop_id !== "string")
    throw new Error("Playback stop identity is missing");
  return record;
}
function save(binding: DurableSession, record: RecordV1) {
  const value = JSON.stringify(record);
  localStorage.setItem(binding.key, value);
  if (localStorage.getItem(binding.key) !== value)
    throw new Error("Unable to durably save playback retry state");
}
function notifyAccepted(binding: DurableSession, accepted: BoundAcceptedProgress | undefined) {
  if (accepted && binding.context.isCurrent()) {
    try {
      binding.onAccepted?.(accepted);
    } catch {
      /* Cache refresh cannot undo a durable receipt. */
    }
  }
}
function validateSample(binding: DurableSession, sample: Sample | undefined) {
  if (
    binding.timeline &&
    sample &&
    (!Number.isFinite(sample.position) ||
      sample.position < 0 ||
      sample.position > binding.timeline.part_duration_seconds ||
      typeof sample.is_paused !== "boolean")
  )
    throw new Error("Audiobook sample is outside its bound part");
}
async function locked<T>(binding: DurableSession, action: () => Promise<T>): Promise<T> {
  if (!navigator.locks?.request)
    return Promise.reject(new Error("Durable playback requires browser storage locking"));
  return await navigator.locks.request(binding.key, { signal: AbortSignal.timeout(30000) }, action);
}
export async function openDurableSession(
  config: PlayerConfig,
  sessionId: string,
  installationId: string,
  timeline?: Readonly<ProgressTimeline>,
  capturedContext?: PlaybackMutationContext,
  attemptId?: string,
): Promise<DurableSession> {
  const context = capturedContext ?? config.capturePlaybackMutationContext?.();
  if (!context || !context.isCurrent() || !installationId)
    throw new Error("Playback installation and account identity are required");
  const identity: Identity = {
    installationId,
    accountId: context.accountId,
    profileId: context.profileId,
    origin: context.origin,
    sessionId,
  };
  if (timeline) timeline = readProgressTimeline(timeline, timeline.media_item_id, timeline.file_id);
  const binding: DurableSession = {
    identity,
    context,
    ...(timeline ? { timeline } : {}),
    key: PREFIX + encodeURIComponent(JSON.stringify(identity)),
    ...(attemptId ? { attemptId } : {}),
  };
  await locked(binding, async () => {
    assertCurrent(config, binding);
    if (localStorage.getItem(binding.key) === null)
      save(binding, {
        version: 1,
        identity,
        sequence: 0,
        stopped: false,
        ...(timeline ? { timeline } : {}),
        ...(attemptId ? { attemptId } : {}),
      });
    else {
      const record = JSON.parse(localStorage.getItem(binding.key)!) as RecordV1;
      if (record.attemptId && attemptId && record.attemptId !== attemptId)
        throw new Error("Playback attempt conflicts with its saved retry state");
      // The exact retained START response may supply missing metadata. Never change the key
      // or any queued request, and never infer an attempt from a recovery response.
      binding.attemptId = record.attemptId;
      read(binding);
      if (!record.attemptId && attemptId) {
        binding.attemptId = attemptId;
        record.attemptId = attemptId;
        save(binding, record);
      }
    }
    read(binding);
  });
  return binding;
}
async function request(
  config: PlayerConfig,
  binding: DurableSession,
  body: string,
  stop: boolean,
  keepalive: boolean,
  deadline: number,
) {
  assertCurrent(config, binding);
  const response = await fetch(
    `${binding.identity.origin}/api/v2/playback/${encodeURIComponent(binding.identity.sessionId)}${stop ? "" : "/progress"}`,
    {
      method: stop ? "DELETE" : "POST",
      body,
      keepalive,
      headers: playerRequestHeaders(config, undefined, true),
      signal: AbortSignal.timeout(Math.max(1, Math.min(5000, deadline - Date.now()))),
    },
  );
  if (!response.ok)
    throw new PlayerFetchError(response.status, `Playback update failed (${response.status})`);
  return response;
}
const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
async function progressRequest(
  config: PlayerConfig,
  binding: DurableSession,
  body: string,
  keepalive: boolean,
) {
  for (let attempt = 0; ; attempt++) {
    if (hasDurableTermination(binding)) return;
    try {
      const response = await request(config, binding, body, false, keepalive, Date.now() + 5000);
      if (response.status !== 200) throw new Error("Unexpected playback progress status");
      if (binding.timeline) {
        const receipt = await response.json();
        assertCurrent(config, binding);
        if (hasDurableTermination(binding)) return;
        return readBoundAcceptedProgress(receipt.accepted, binding.timeline);
      }
      return;
    } catch (error) {
      if (hasDurableTermination(binding)) return;
      if (
        attempt >= 2 ||
        !(
          error instanceof TypeError ||
          error instanceof DOMException ||
          (error instanceof PlayerFetchError && error.status >= 500)
        )
      )
        throw error;
      await pause(250);
    }
  }
}
export function durableProgress(
  config: PlayerConfig,
  binding: DurableSession,
  sample: Sample,
  keepalive: boolean,
): Promise<BoundAcceptedProgress | void> {
  return locked(binding, async () => {
    assertCurrent(config, binding);
    const record = read(binding);
    if (record.stopBody || record.stopped || hasDurableTermination(binding)) return;
    if (record.pendingProgress) {
      const accepted = await progressRequest(config, binding, record.pendingProgress, keepalive);
      if (accepted) record.accepted = accepted;
      if (hasDurableTermination(binding)) return;
      delete record.pendingProgress;
      save(binding, record);
    }
    validateSample(binding, sample);
    if (!Number.isSafeInteger(record.sequence + 1)) throw new Error("Playback sequence exhausted");
    record.pendingProgress = JSON.stringify({
      ...sample,
      sequence: ++record.sequence,
      installation_id: binding.identity.installationId,
      ...(binding.timeline ? { timeline_id: binding.timeline.timeline_id } : {}),
    } satisfies components["schemas"]["PlaybackProgressBody"]);
    save(binding, record);
    const accepted = await progressRequest(config, binding, record.pendingProgress, keepalive);
    if (accepted) record.accepted = accepted;
    if (hasDurableTermination(binding)) return;
    delete record.pendingProgress;
    save(binding, record);
    notifyAccepted(binding, record.accepted);
    return record.accepted;
  });
}
export function durableStop(
  config: PlayerConfig,
  binding: DurableSession,
  sample: Sample | undefined,
  keepalive: boolean,
): Promise<BoundAcceptedProgress | void> {
  return locked(binding, async () => {
    assertCurrent(config, binding);
    const record = read(binding);
    if (hasDurableTermination(binding)) return;
    if (record.stopped) {
      if (record.recovery?.state === "aborted") throw new PlaybackOwnerLostError();
      return record.accepted;
    }
    if (!record.stopBody) {
      const final =
        sample ??
        (record.pendingProgress ? (JSON.parse(record.pendingProgress) as Sample) : undefined);
      validateSample(binding, final);
      if (final && !Number.isSafeInteger(record.sequence + 1))
        throw new Error("Playback sequence exhausted");
      record.stopBody = JSON.stringify({
        installation_id: binding.identity.installationId,
        stop_id: randomUUID(),
        ...(binding.timeline ? { timeline_id: binding.timeline.timeline_id } : {}),
        ...(final
          ? { position: final.position, is_paused: final.is_paused, sequence: ++record.sequence }
          : {}),
      } satisfies components["schemas"]["PlaybackStopBody"]);
      save(binding, record);
    }
    const body = record.stopBody;
    const deadline = Date.now() + 30000;
    while (Date.now() < deadline) {
      if (hasDurableTermination(binding)) return;
      try {
        const response = await request(config, binding, body, true, keepalive, deadline);
        if (hasDurableTermination(binding)) return;
        const receipt = (await response.json()) as components["schemas"]["PlaybackMutation"];
        assertCurrent(config, binding);
        if (hasDurableTermination(binding)) return;
        const recovery = readOwnerLossRecovery(receipt, response.status, "stop", {
          attemptId: binding.attemptId,
          sessionId: binding.identity.sessionId,
          timeline: binding.timeline,
          prior: record.recovery,
        });
        if (recovery) {
          record.recovery = recovery;
          if (recovery.state === "aborted") {
            record.stopped = true;
            if (binding.timeline && recovery.accepted)
              record.accepted = readBoundAcceptedProgress(recovery.accepted, binding.timeline);
            else delete record.accepted;
            // Retain the exact pending progress and STOP as abandoned requests, not applied ones.
            save(binding, record);
            notifyAccepted(binding, record.accepted);
            delete binding.onAccepted;
            throw new PlaybackOwnerLostError();
          }
          save(binding, record);
          await pause(500);
          continue;
        }
        if (record.recovery)
          throw new Error("Playback recovery returned no matching recovery receipt");
        if (receipt.stop_id !== JSON.parse(body).stop_id)
          throw new Error("Playback returned a missing or different stop receipt");
        if (
          response.status === 200 &&
          (receipt.outcome === "stopped" || receipt.outcome === "replayed")
        ) {
          assertCurrent(config, binding);
          if (binding.timeline && receipt.accepted)
            record.accepted = readBoundAcceptedProgress(receipt.accepted, binding.timeline);
          else if (binding.timeline && JSON.parse(body).sequence !== undefined)
            throw new Error("Bound playback stop returned no accepted progress");
          record.stopped = true;
          delete record.pendingProgress;
          save(binding, record);
          notifyAccepted(binding, record.accepted);
          delete binding.onAccepted;
          return record.accepted;
        }
        if (response.status !== 202 || receipt.outcome !== "draining")
          throw new Error("Playback stop not confirmed");
      } catch (error) {
        if (hasDurableTermination(binding)) return;
        if (
          !(
            error instanceof TypeError ||
            error instanceof DOMException ||
            (error instanceof PlayerFetchError && error.status >= 500)
          )
        )
          throw error;
      }
      await pause(500);
    }
    throw new Error("Playback stop is still pending. Please retry.");
  });
}

// Restore only exact current ownership. Other identities remain untouched.
function storedDurableSessions(config: PlayerConfig, installationId: string): DurableSession[] {
  const context = config.capturePlaybackMutationContext?.();
  if (!context || !context.isCurrent()) return [];
  const pending: DurableSession[] = [];
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (!key?.startsWith(PREFIX)) continue;
    const identity = JSON.parse(decodeURIComponent(key.slice(PREFIX.length))) as Identity;
    if (
      identity.installationId !== installationId ||
      identity.accountId !== context.accountId ||
      identity.profileId !== context.profileId ||
      identity.origin !== context.origin
    )
      continue;
    const stored = JSON.parse(localStorage.getItem(key) ?? "null") as RecordV1;
    const timeline = stored?.timeline;
    const binding = {
      key,
      identity,
      context,
      ...(stored?.attemptId ? { attemptId: stored.attemptId } : {}),
      ...(timeline
        ? { timeline: readProgressTimeline(timeline, timeline.media_item_id, timeline.file_id) }
        : {}),
    };
    read(binding); // Validate even completed records before exposing their binding.
    pending.push(binding);
  }
  return pending;
}

export function pendingDurableSessions(
  config: PlayerConfig,
  installationId: string,
): DurableSession[] {
  return storedDurableSessions(config, installationId).filter(
    (binding) => !read(binding).stopped && !hasDurableTermination(binding),
  );
}

// A START retained through cleanup may outlive its STOP receipt. Recover the
// original mapping even when that STOP has already completed locally.
export function retainedStartSession(
  config: PlayerConfig,
  installationId: string,
  attemptId: string,
): DurableSession | undefined {
  return storedDurableSessions(config, installationId).find(
    (binding) => binding.attemptId === attemptId,
  );
}
