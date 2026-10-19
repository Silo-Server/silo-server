/**
 * How a watch-together client follows the room position. This is the shared
 * cross-client algorithm; Apple and Android use the same constants.
 *
 * Every transport command carries the room position at its `execute_at`. The
 * latest applied command for the current selection is the room reference:
 * while it plays, the room advances at 1x from `execute_at`. Drift is
 * `room position - local position`, positive when this viewer is behind.
 *
 * Small drift is absorbed with a gentle playbackRate nudge (1-4 %) instead of
 * a seek, so playback never jumps for drift a viewer would not notice. Only a
 * playing viewer more than HARD_SEEK off the room seeks. A viewer that is not
 * playing also seeks a smaller drift when the element reaches the target in
 * place: on copy-remux deliveries a target outside the seekable window forces
 * a stream rebuild, which is not worth it for a second or two.
 */

/** No correction starts below this drift. */
export const roomSyncDeadbandSeconds = 0.4;
/** An active rate nudge ends within this drift. */
export const roomSyncSettleSeconds = 0.1;
/** Only above this drift does a playing viewer seek. */
export const roomSyncHardSeekSeconds = 5;
/** A viewer that is not playing seeks above this drift when it needs no rebuild. */
export const roomSyncPausedSeekSeconds = 1;
/** Bounds of the rate nudge, as a fraction of 1x. */
export const roomSyncRateMinDelta = 0.01;
export const roomSyncRateMaxDelta = 0.04;
/** Drift (seconds) that maps to a 1x + 100 % nudge before clamping. */
export const roomSyncRateGainSeconds = 25;
/**
 * Nudge rates are quantized to 0.005 (1/200) so tiny drift changes do not
 * churn the element rate. Dividing by the integer keeps rates exact decimals.
 */
const roomSyncRateStepsPerUnit = 200;
/** The drift loop leaves playback alone for this long after a command finished. */
export const roomSyncLoopSettleMs = 1_000;
/** Drift loop cadence. */
export const roomSyncLoopIntervalMs = 500;

/**
 * The room position a command describes: `positionSeconds` at the server's
 * `executeAtServerMs`, advancing at 1x afterwards while the room plays.
 */
export interface RoomPositionReference {
  positionSeconds: number;
  /** The command's `execute_at` on the server clock (ms). */
  executeAtServerMs: number;
  playing: boolean;
}

/**
 * The room's position at local wall-clock `nowMs`, where server time is local
 * time plus `serverTimeOffsetMs`.
 */
export function roomPositionAt(
  reference: RoomPositionReference,
  nowMs: number,
  serverTimeOffsetMs: number,
): number {
  if (!reference.playing) return Math.max(0, reference.positionSeconds);
  const elapsedSeconds = Math.max(
    0,
    (nowMs + serverTimeOffsetMs - reference.executeAtServerMs) / 1000,
  );
  return Math.max(0, reference.positionSeconds + elapsedSeconds);
}

/**
 * The nudge rate for `driftSeconds`: 1 + sign(d) * clamp(|d| / 25, 1 %, 4 %),
 * quantized to 0.005. Zero drift needs no nudge.
 */
export function roomSyncNudgeRate(driftSeconds: number): number {
  if (driftSeconds === 0 || !Number.isFinite(driftSeconds)) return 1;
  const delta = Math.min(
    roomSyncRateMaxDelta,
    Math.max(roomSyncRateMinDelta, Math.abs(driftSeconds) / roomSyncRateGainSeconds),
  );
  // Quantize the magnitude, as the Apple and Android clients do, so a speed-up
  // and a slow-down for the same drift are mirror images.
  const steps = Math.round(delta * roomSyncRateStepsPerUnit);
  return (roomSyncRateStepsPerUnit + Math.sign(driftSeconds) * steps) / roomSyncRateStepsPerUnit;
}

/**
 * One step of the continuous drift loop. A nudge starts above the deadband and
 * runs until the drift is within SETTLE; the gap between the two keeps the
 * rate from flapping at the deadband edge. Returns the rate to play at, 1 when
 * no nudge applies. The loop never seeks: drift above HARD_SEEK keeps the
 * strongest nudge until a server correction arrives.
 */
export function nextRoomNudgeRate(driftSeconds: number, nudging: boolean): number {
  const drift = Math.abs(driftSeconds);
  if (nudging ? drift <= roomSyncSettleSeconds : drift <= roomSyncDeadbandSeconds) return 1;
  return roomSyncNudgeRate(driftSeconds);
}

/**
 * A correction seek within this of a room seek still loading is the same
 * correction; issuing it again would reload or rebuild the stream twice.
 */
export const roomSeekDuplicateSeconds = 1;

/** A seek room sync issued: where to, when (monotonic ms), and for which command. */
export interface RoomIssuedSeek {
  targetSeconds: number;
  issuedAtMs: number;
  commandId: string | null;
}

/**
 * Whether a seek to `targetSeconds` would repeat the room seek `issued` that
 * the element has not reached yet (`pendingSeconds` is the position it is
 * heading to, null once the seek landed or failed). A seek still unfinished
 * after `roomReloadStaleMs` has failed and is no reason to hold back.
 */
export function roomSeekAlreadyPending(
  issued: RoomIssuedSeek | null,
  pendingSeconds: number | null,
  targetSeconds: number,
  nowMs: number,
): boolean {
  return (
    issued !== null &&
    pendingSeconds !== null &&
    nowMs - issued.issuedAtMs < roomReloadStaleMs &&
    Math.abs(pendingSeconds - issued.targetSeconds) <= roomSeekDuplicateSeconds &&
    Math.abs(targetSeconds - issued.targetSeconds) <= roomSeekDuplicateSeconds
  );
}

/**
 * The drift loop of a player that cannot nudge its rate (see `rateCapable`)
 * seeks to the room instead, only when the drift exceeds PAUSED_SEEK and the
 * element reaches the room position without a rebuild.
 */
export function roomLoopSeekDue(driftSeconds: number, targetLocallySeekable: boolean): boolean {
  return targetLocallySeekable && Math.abs(driftSeconds) > roomSyncPausedSeekSeconds;
}

/** Upper bound on how far ahead of the room a correcting seek aims. */
export const roomSeekLeadMaxSeconds = 3;
/** Longest wait between correcting seeks that keep missing. */
export const roomSeekBackoffMaxMs = 16_000;

/**
 * Seek-based drift correction for a player without rate nudges. A seek takes
 * time, and a player whose clock stands still while it seeks lands that long
 * behind a room that kept moving. Aimed at the room's current position, each
 * seek would land short and the next would follow forever. So each seek's
 * result is measured: the drift left after it lands is added to a lead the
 * next seek aims ahead by, and a seek that did not bring the drift under
 * PAUSED_SEEK doubles the wait before the next. Convergence resets the wait;
 * the lead, a property of this player, is kept.
 */
export interface RoomSeekCorrector {
  leadSeconds: number;
  intervalMs: number;
  /** Monotonic time of the last correcting seek, or of it still running. */
  lastSeekAtMs: number;
  /** A seek ran and its landing has not been measured yet. */
  awaitingResult: boolean;
}

export function createRoomSeekCorrector(): RoomSeekCorrector {
  return {
    leadSeconds: 0,
    intervalMs: roomSyncLoopSettleMs,
    lastSeekAtMs: Number.NEGATIVE_INFINITY,
    awaitingResult: false,
  };
}

/** Where a correcting seek aims, given the room position now. */
export function roomCorrectorSeekTarget(
  corrector: RoomSeekCorrector,
  roomPositionSeconds: number,
): number {
  return roomPositionSeconds + corrector.leadSeconds;
}

/** A correcting seek was issued (or is still running) at `nowMs`. */
export function noteRoomCorrectorSeek(corrector: RoomSeekCorrector, nowMs: number): void {
  corrector.lastSeekAtMs = nowMs;
  corrector.awaitingResult = true;
}

/**
 * Takes the drift measured once playback runs again after a correcting seek.
 * Positive drift is a seek that landed short of the room.
 */
export function observeRoomCorrector(corrector: RoomSeekCorrector, driftSeconds: number): void {
  if (!corrector.awaitingResult) return;
  corrector.awaitingResult = false;
  corrector.leadSeconds = Math.min(
    roomSeekLeadMaxSeconds,
    Math.max(0, corrector.leadSeconds + driftSeconds),
  );
  corrector.intervalMs =
    Math.abs(driftSeconds) > roomSyncPausedSeekSeconds
      ? Math.min(roomSeekBackoffMaxMs, corrector.intervalMs * 2)
      : roomSyncLoopSettleMs;
}

/** Whether the drift loop should seek now. */
export function roomCorrectorSeekDue(
  corrector: RoomSeekCorrector,
  driftSeconds: number,
  targetLocallySeekable: boolean,
  nowMs: number,
): boolean {
  return (
    roomLoopSeekDue(driftSeconds, targetLocallySeekable) &&
    nowMs - corrector.lastSeekAtMs >= corrector.intervalMs
  );
}

export type RoomCommandDecision =
  | { kind: "seek" }
  | { kind: "rate"; rate: number }
  | { kind: "none" };

export interface RoomCommandInput {
  action: "play" | "pause" | "seek";
  /** Room position at the moment the command is applied, in media time. */
  targetPositionSeconds: number;
  /** The local playback position in media time. */
  localPositionSeconds: number;
  /** Whether the element was playing when the command executed. */
  localPlaying: boolean;
  /**
   * Whether room sync may nudge this player's playback rate. The browser
   * decode engine corrects drift by seeking instead.
   */
  rateCapable: boolean;
  /**
   * Whether the element can take the target without a stream rebuild: the
   * plan seeks anywhere, or the target lies inside the seekable ranges.
   */
  targetLocallySeekable: boolean;
}

/**
 * How a command corrects the local position when it executes. Play and pause
 * state, and the 1x reset for pause and seek, are the caller's: a `none` or
 * `seek` decision for a play command still plays at 1x afterwards, and the
 * drift loop nudges any residual drift.
 */
export function decideRoomCommand(input: RoomCommandInput): RoomCommandDecision {
  // A seek is a room barrier: everyone lands on the same frame.
  if (input.action === "seek") return { kind: "seek" };
  const driftSeconds = input.targetPositionSeconds - input.localPositionSeconds;
  const drift = Math.abs(driftSeconds);
  if (input.action === "play" && input.localPlaying && !input.rateCapable) {
    // No nudge is available: a seek in place beats audible drift, and only
    // drift beyond HARD_SEEK is worth loading media for.
    if (drift > roomSyncPausedSeekSeconds && input.targetLocallySeekable) return { kind: "seek" };
    if (drift > roomSyncHardSeekSeconds) return { kind: "seek" };
    return { kind: "none" };
  }
  if (input.action === "play" && input.localPlaying) {
    if (drift <= roomSyncDeadbandSeconds) return { kind: "none" };
    // Even a buffered target is not sought: a seek stalls playback visibly
    // for drift a nudge absorbs within seconds.
    if (drift <= roomSyncHardSeekSeconds) {
      return { kind: "rate", rate: roomSyncNudgeRate(driftSeconds) };
    }
    return { kind: "seek" };
  }
  // A pause, or a play for a viewer that is not playing yet.
  if (
    drift > roomSyncHardSeekSeconds ||
    (drift > roomSyncPausedSeekSeconds && input.targetLocallySeekable)
  ) {
    return { kind: "seek" };
  }
  return { kind: "none" };
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
/**
 * Playback this far past a reload's target is still that reload playing: it
 * started a little early and has run since. Further ahead is the old stream.
 */
const roomReloadLandedAheadSeconds = 2;

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

/**
 * Whether a reload is loading and still young enough to wait for. A reload
 * older than `roomReloadStaleMs` has failed to land; it must not hold up the
 * drift loop or the next correction forever.
 */
export function roomReloadInFlight(budget: RoomReloadBudget, nowMs: number): boolean {
  return budget.targetSeconds !== null && nowMs - budget.startedAtMs < roomReloadStaleMs;
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
  return offset >= -roomSyncDeadbandSeconds && offset <= roomReloadLandedAheadSeconds;
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

/**
 * A reload that never landed within `roomReloadStaleMs` is given up. Unlike a
 * refused or superseded reload this earns no backoff: the staleness wait was
 * the backoff, and `roomReloadAllowed` already admits a replacement by now.
 * Attempts are kept, so a replacement that lands still backs off further.
 */
export function expireRoomReload(budget: RoomReloadBudget, nowMs: number): void {
  if (budget.targetSeconds === null) return;
  budget.generation += 1;
  budget.targetSeconds = null;
  budget.nextAllowedAtMs = nowMs;
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
