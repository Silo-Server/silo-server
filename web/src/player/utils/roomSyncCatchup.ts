/**
 * How a watch-together client applies a room transport command's position.
 *
 * Small drift between local playback and the room position is absorbed with a
 * playbackRate nudge instead of a seek. On copy-remux deliveries a seek
 * outside the already-downloaded window cannot run inside the current stream:
 * it forces a seek-reanchor replan, which rebuilds the whole stream. Rate
 * convergence keeps the room synced without rebuilding anything. Local files
 * and seek-anywhere routes are unaffected: every position the element can
 * reach without a rebuild stays a seek, exactly as before.
 */

/** Drift within this band is corrected by rate instead of rebuilding the stream. */
export const roomCatchupBandSeconds = 2;
/** Local playback already matching the room within this bound needs no correction. */
export const roomCatchupDeadbandSeconds = 0.35;
/** Bounds for the convergence nudge. */
export const roomCatchupMinRate = 0.9;
export const roomCatchupMaxRate = 1.25;
/**
 * A 1s deficit plays at ~1.125x and converges in ~8s; the band edge (2s)
 * reaches the cap. Gentle on purpose: this runs during normal playback.
 */
const roomCatchupDivisorSeconds = 8;

export type RoomCatchupDecision =
  | { kind: "seek" }
  | { kind: "rate"; rate: number }
  | { kind: "none" };

/**
 * The room position a rate catch-up is converging toward. The room keeps
 * advancing at 1x from the command's execution, so convergence has to track
 * that moving position: against a static one, a member behind the room ends
 * the nudge early, and a member slowed ahead of it never ends it at all.
 */
export interface RoomCatchupTarget {
  positionSeconds: number;
  /** Local wall-clock time (ms) at which `positionSeconds` starts advancing. */
  executeAtMs: number;
}

/** The room's expected position at `nowMs`, advancing at 1x from execution. */
export function roomCatchupExpectedPosition(target: RoomCatchupTarget, nowMs: number): number {
  return Math.max(0, target.positionSeconds + Math.max(0, (nowMs - target.executeAtMs) / 1000));
}

/** Whether local playback has reached the advancing room position. */
export function roomCatchupConverged(
  target: RoomCatchupTarget,
  localPositionSeconds: number,
  nowMs: number,
): boolean {
  return (
    Math.abs(roomCatchupExpectedPosition(target, nowMs) - localPositionSeconds) <=
    roomCatchupDeadbandSeconds
  );
}

export interface RoomCatchupInput {
  action: "play" | "pause" | "seek";
  /** The command's position in media time. */
  targetPositionSeconds: number;
  /** The local playback position in media time. */
  localPositionSeconds: number;
  /**
   * Whether the element can take the target without a stream rebuild: the
   * plan seeks anywhere, or the target lies inside the seekable ranges.
   */
  targetLocallySeekable: boolean;
}

export function decideRoomCatchup(input: RoomCatchupInput): RoomCatchupDecision {
  if (input.action === "seek") return { kind: "seek" };
  const delta = input.targetPositionSeconds - input.localPositionSeconds;
  const drift = Math.abs(delta);
  if (drift <= roomCatchupDeadbandSeconds) return { kind: "none" };
  // Everything the element can reach without a rebuild stays a seek, exactly
  // as before the rate path existed.
  if (input.targetLocallySeekable) return { kind: "seek" };
  if (input.action === "pause") {
    // Pausing is cheap and the room's next play command realigns; never
    // rebuild a stream to park a paused member at the anchor.
    return { kind: "none" };
  }
  if (drift > roomCatchupBandSeconds) return { kind: "seek" };
  const rate = 1 + delta / roomCatchupDivisorSeconds;
  return {
    kind: "rate",
    rate: Math.min(roomCatchupMaxRate, Math.max(roomCatchupMinRate, rate)),
  };
}

/** Whether `nativeSeconds` falls inside one of `ranges`, such as the element's seekable or buffered ranges. */
export function isNativePositionInRanges(ranges: TimeRanges, nativeSeconds: number): boolean {
  for (let i = 0; i < ranges.length; i++) {
    if (nativeSeconds >= ranges.start(i) && nativeSeconds <= ranges.end(i)) {
      return true;
    }
  }
  return false;
}

/**
 * Correction-driven media reloads. A room correction whose target is not
 * already buffered has to load new media: a range request on a seekable
 * stream, or a whole stream rebuild outside the seekable window. Either way
 * the viewer lands late by the load time. Without a budget a viewer on a slow
 * connection or storage chases the advancing room forever, reloading on every
 * correction. Only one reload runs at a time, later ones back off, and each
 * aims ahead by the load time the previous reload took.
 */
export const roomReloadMinIntervalMs = 10_000;
export const roomReloadMaxIntervalMs = 60_000;
/** An unlanded reload stops blocking the next one after this long. */
export const roomReloadStaleMs = 30_000;
/** Upper bound on the load-time lead added to a reload's target. */
export const roomReloadMaxLeadSeconds = 10;

export interface RoomReloadBudget {
  /**
   * Identifies the current reload. A completion from an earlier reload must
   * not act on a later one, even one aimed at the same position.
   */
  generation: number;
  /** Media position the in-flight reload aims at, or null when none runs. */
  targetSeconds: number | null;
  /** Whether this reload's own seek or reanchor has been taken. */
  loadStarted: boolean;
  startedAtMs: number;
  /** Earliest time the next correction may reload media. */
  nextAllowedAtMs: number;
  /** Reloads since this viewer last converged on the room. */
  attempts: number;
  /** Load time the last reload took, added to the next target. */
  leadSeconds: number;
}

export function createRoomReloadBudget(): RoomReloadBudget {
  return {
    generation: 0,
    targetSeconds: null,
    loadStarted: false,
    startedAtMs: 0,
    nextAllowedAtMs: 0,
    attempts: 0,
    leadSeconds: 0,
  };
}

function roomReloadBackoffMs(attempts: number): number {
  return Math.min(
    roomReloadMaxIntervalMs,
    roomReloadMinIntervalMs * 2 ** Math.max(0, attempts - 1),
  );
}

export function roomReloadAllowed(budget: RoomReloadBudget, nowMs: number): boolean {
  if (budget.targetSeconds !== null) {
    return nowMs - budget.startedAtMs >= roomReloadStaleMs;
  }
  return nowMs >= budget.nextAllowedAtMs;
}

/**
 * Records a reload toward `roomPositionSeconds` and returns where to aim it.
 * The load-time lead never aims past the end of the media, which the server
 * refuses as a seek target.
 */
export function beginRoomReload(
  budget: RoomReloadBudget,
  roomPositionSeconds: number,
  nowMs: number,
  durationSeconds?: number,
): number {
  budget.generation += 1;
  let targetSeconds = roomPositionSeconds + budget.leadSeconds;
  if (durationSeconds !== undefined && durationSeconds > 0) {
    targetSeconds = Math.min(targetSeconds, Math.max(roomPositionSeconds, durationSeconds));
  }
  budget.targetSeconds = targetSeconds;
  budget.loadStarted = false;
  budget.startedAtMs = nowMs;
  budget.attempts += 1;
  budget.nextAllowedAtMs = nowMs + roomReloadStaleMs;
  return targetSeconds;
}

/** The in-flight reload's seek was taken, or its reanchor adopted. */
export function noteRoomReloadLoading(budget: RoomReloadBudget): void {
  if (budget.targetSeconds !== null) budget.loadStarted = true;
}

/**
 * Whether media at `localPositionSeconds` is the in-flight reload playing: the
 * reload's own seek was taken, and playback sits at its target.
 * The stream being replaced keeps playing until then and can be on either
 * side of the target, so position alone cannot settle the reload.
 */
export function roomReloadLanded(budget: RoomReloadBudget, localPositionSeconds: number): boolean {
  if (budget.targetSeconds === null || !budget.loadStarted) return false;
  const offset = localPositionSeconds - budget.targetSeconds;
  return offset >= -roomCatchupDeadbandSeconds && offset <= roomCatchupBandSeconds;
}

/** The reload is playing: remember its load time and space the next one. */
export function landRoomReload(budget: RoomReloadBudget, nowMs: number): void {
  if (budget.targetSeconds === null) return;
  budget.generation += 1;
  budget.leadSeconds = Math.min(
    roomReloadMaxLeadSeconds,
    Math.max(0, (nowMs - budget.startedAtMs) / 1000),
  );
  budget.targetSeconds = null;
  budget.nextAllowedAtMs = nowMs + roomReloadBackoffMs(budget.attempts);
}

/** The reload was refused or superseded; space the next one. */
export function abandonRoomReload(budget: RoomReloadBudget, nowMs: number): void {
  budget.generation += 1;
  budget.targetSeconds = null;
  budget.nextAllowedAtMs = nowMs + roomReloadBackoffMs(budget.attempts);
}

/** The viewer reached the room; the next drift starts a fresh backoff. */
export function settleRoomReloads(budget: RoomReloadBudget): void {
  budget.attempts = 0;
  budget.nextAllowedAtMs = 0;
}

interface RoomMemberStatus {
  user_id: number;
  profile_id: string;
  display_name: string;
  is_self: boolean;
  is_ready?: boolean;
  is_syncing?: boolean;
}

interface RoomStatusSnapshot {
  phase: string;
  playback_state: string;
  selection_revision: number;
  members?: RoomMemberStatus[];
}

/**
 * Other viewers the room stopped waiting for: they were still syncing when the
 * room waited, and it is playing now without them being ready.
 */
export function roomMembersLeftBehind(
  previous: RoomStatusSnapshot | null,
  next: RoomStatusSnapshot | null,
): string[] {
  if (
    !previous ||
    !next ||
    previous.playback_state !== "waiting" ||
    next.phase !== "playing" ||
    next.playback_state !== "playing" ||
    next.selection_revision !== previous.selection_revision
  ) {
    return [];
  }
  return (next.members ?? [])
    .filter(
      (member) =>
        !member.is_self &&
        !member.is_ready &&
        previous.members?.some(
          (before) =>
            before.is_syncing &&
            before.user_id === member.user_id &&
            before.profile_id === member.profile_id,
        ),
    )
    .map((member) => member.display_name);
}
