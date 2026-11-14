import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { inventoryPublicationTargets, publishPlaybackInventory } from "../playbackInventoryMirror";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { startPlaybackV2 } from "../start-v2";
import { hasSequencedProgress, stopSequencedSession } from "../session-mutations";
import {
  CONNECTION_LOST_ERROR,
  describePlanTerminal,
  describePlaybackTransportError,
  isAdaptationUnavailableReason,
  isDeadPlaybackSessionError,
  PLAYBACK_SESSION_GONE_ERROR,
  type PlaybackPolicyErrorDescription,
} from "../playback-errors";
import {
  isPlaybackSessionEndedError,
  isTransientPlayerRequestError,
  PlayerFetchError,
  playerFetchResponse,
} from "../player-fetch";
import { useCodecDetection } from "./useCodecDetection";
import { useBrowserEngineCapabilities } from "./useBrowserEngine";
import type { EngineCapabilities } from "../engine/capabilities";
import type { NativeOriginalSupport } from "../engine/routing";
import {
  buildClientCapabilitiesV3,
  buildClientPlaybackContextV3,
  detectBandwidthEstimateKbpsV3,
} from "../client-context-v3";
import { buildRouteEventV3 } from "../route-events-v3";
import { reportSessionRouteEventV2 } from "../route-events-v2";
import { replanV2 } from "../lifecycle-v2";
import { takePlaybackIntent } from "../first-frame";
import { playerV2Origin } from "../player-v2";
import { buildPlayerStreamUrl } from "../stream-url";
import { randomUUID } from "@/lib/uuid";
import { matchSubtitleTrackAcrossVersions } from "../utils/subtitleSort";
import {
  resolveAppliedAudioIndex,
  resolveAudioPickIdentity,
  resolvePlanAudioIdentity,
} from "../utils/audioTrackMatch";
import { buildPublishedSubtitleTracks, subtitleLanguageBase } from "../utils/subtitleInventory";
import { isBitmapCodec } from "../utils/subtitleCodecs";
import {
  isInventoryProvisional,
  isPlanSubtitleInventoryProvisional,
} from "../utils/inventoryProvenance";
import { isNeutralVirtualVersion } from "../utils/neutralVirtualVersion";
import { normalizeSubtitleMode } from "../utils/subtitleMode";
import {
  FEATURE_OUTPUT_CHANGE_V3,
  MAX_ATTEMPT_COUNT_V3,
  MAX_ATTEMPTED_PLAN_KEYS_V3,
  QUALITY_ORIGINAL_V3,
  SUBTITLE_REASON_INVENTORY_UNAVAILABLE_V3,
  SUBTITLE_REASON_SELECTION_UNAVAILABLE_V3,
  type DecisionResponseV3,
  type FailureV3,
  type AudioTrackV3,
  type PlanV3,
  type RouteEventNameV3,
  type StartRequestV3,
  type SubtitleInventoryItemV3,
  type TrackIdentityV3,
} from "../protocol-v3";
import {
  buildReplanRequestV3,
  buildStartRequestV3,
  routeEventPlanIdentityV3,
  VIDEO_CLIENT_FEATURES_V3,
  type ReplanOptions,
} from "../playback-session-wire-v3";
import {
  PLAN_INVALIDATED_DEFAULT_AUDIO_RECONCILIATION,
  type PlaybackInventoryUpdatedPayload,
} from "../realtime-protocol";
import type {
  PlayerAudioTrack,
  PlayerFileVersion,
  PlayerPlaybackVariant,
  PlayerSubtitleInfo,
  ResumeHints,
  SubtitleMode,
} from "../types";

/** Terminal reasons that mean the selected subtitle, not the release, blocked
 *  the plan. Only these may drop the subtitle and retry the same start. */
const BITMAP_SUBTITLE_REFUSAL_REASONS = new Set([
  "subtitle_burn_in_source_unsupported",
  "subtitle_codec_unsupported",
  "subtitle_conversion_unsupported",
  "subtitle_track_invalid",
  "subtitle_track_unavailable",
  "subtitle_unavailable_in_version",
  "subtitle_artifact_unavailable",
  // HDR conversion refusal can be raised only because the selected bitmap
  // subtitle forced a burn-in; dropping that subtitle is the intended retry.
  "hdr_transcode_unsupported",
]);

function isBitmapSubtitleRefusal(decision: DecisionResponseV3): boolean {
  const reason = decision.terminal?.reason?.trim() ?? "";
  return reason !== "" && BITMAP_SUBTITLE_REFUSAL_REASONS.has(reason);
}

/**
 * The retryable terminal a seek reanchor returns when the session's frozen
 * source can no longer be reopened from evidence that belongs to it.
 *
 * The session is not dead and the file is not unplayable: replaying the frozen
 * recipe would pair the running plan with foreign bytes, so the server fails
 * the reanchor closed. A fresh start at the requested position resolves the
 * source again from scratch, which is why the terminal is retryable.
 */
const SEEK_REANCHOR_EVIDENCE_UNAVAILABLE_REASON = "seek_reanchor_evidence_unavailable";

interface PlaybackSessionState {
  /**
   * The server's plan, verbatim. It is the single source of truth for the
   * stream URL, the timeline, the selected tracks, the subtitle inventory and
   * the quality menu — the client derives none of those itself any more.
   */
  plan: PlanV3 | null;
  /**
   * The live inventory revision folded into the menus, from the adopted plan
   * or a later `inventory_updated` push. A poll of the plan's `inventory_url`
   * echoes it as an ETag; a 304 means the deferred probe has not landed yet.
   */
  inventoryRevision: string | null;
  /**
   * The plan's inventory URL for a deferred enumeration, echoed from
   * `playback_plan.inventory_url`. Present exactly when the plan defers.
   */
  inventoryUrl: string | null;
  /**
   * True while the deferred enumeration is still outstanding: the menus show
   * their loading state. Cleared by a verified or failed terminal outcome
   * (push or poll); never cleared by a 304 or a failed fetch.
   */
  inventoryPending: boolean;
  /**
   * True when probe discovery terminally failed or timed out: menus show an
   * error state instead of spinning forever. Cleared if a verified revision
   * arrives later.
   */
  inventoryFailed: boolean;
  /**
   * Bumped every time a new plan is adopted. Consumers key stream-reload
   * effects on it rather than on object identity.
   */
  planRevision: number;
  transportRevision: number;
  /**
   * Bumped when a new playback session starts at the beginning. Consumers that
   * remember transport-local state can discard it: the same URL and source
   * generation can be reused for a replay from zero, where a remembered
   * playhead is stale.
   */
  replayRevision: number;
  streamUrl: string | null;
  sessionId: string | null;
  playbackAttemptId: string | null;
  mediaFileId: number | null;
  /**
   * The file path of the concrete candidate the server resolved a neutral
   * `virtual://…` requested row to, when the effective source is virtual. Null
   * for an ordinary file or an older plan that does not publish it. This is
   * menu data only: adopting it must never reload the stream, so it does not
   * touch `planRevision`/`transportRevision`.
   */
  effectiveVirtualUri: string | null;
  initialPosition: number;
  audioTrackIndex: number;
  durationSeconds: number | null;
  subtitleUrls: PlayerSubtitleInfo[];
  /**
   * The plan's authoritative audio inventory for the effective source, when
   * the server publishes one. Consumers render the audio menu from this in
   * preference to item metadata, which can be stale after a version fallback.
   */
  planAudioTracks: PlayerAudioTrack[];
  /**
   * True while the plan's audio inventory is still declared metadata (or a
   * deferred/failed probe) rather than probe evidence. The audio menu reads it
   * to say the list may not match the bytes yet. Cleared when the inventory
   * poll folds in probed catalog tracks or a verified replan lands.
   */
  audioInventoryProvisional: boolean;
  /**
   * True while the plan's subtitle inventory is still declared metadata rather
   * than probe evidence. Cleared when a verified replan publishes the real
   * inventory; the poll's subtitle fill is a replan, so it resolves here too.
   */
  subtitleInventoryProvisional: boolean;
  qualityPreference: string;
  shouldAutoPlay: boolean;
  loading: boolean;
  replacing: boolean;
  replanning: boolean;
  /**
   * True only while the in-flight replan is a quality/output change. The
   * quality menu's "…" label keys on this rather than on `replanning`, which
   * is also set for track changes, seek reanchors and failure recovery.
   */
  replanningQuality: boolean;
  /**
   * The file the viewer most recently asked to switch to, while the switch is
   * still in flight. Lets the version menu light the clicked entry as
   * "Requested" optimistically, before the replacement plan lands.
   */
  pendingSwitchFileId: number | null;
  /**
   * Whether automatic version fallback is armed for this session. On by
   * default for an auto start; the viewer can turn it off by picking an
   * explicit version and back on through the version menu's Auto entry. It
   * only authorizes a dead/unplayable source to move to another version, never
   * a healthy mid-play switch.
   */
  autoFallback: boolean;
  errorTitle: string | null;
  error: string | null;
  /**
   * The v3 terminal reason behind `errorTitle`/`error`, when the error came
   * from a server decision rather than a transport failure.
   */
  errorReason: string | null;
  /** The server's retry verdict for the current error, false when unnamed. */
  errorRetryable: boolean;
  /** True while a viewer-initiated retry of a refused start is in flight. */
  retrying: boolean;
  initialSubtitleErrorTitle: string | null;
  initialSubtitleError: string | null;
  /**
   * A track pick that could not be reconciled to the current plan and was not
   * silently discarded. `id` makes repeated identical messages observable to
   * the toast layer; consumers dismiss it themselves.
   */
  trackSelectionNotice: { id: number; title: string; message: string } | null;
  /**
   * Whether a stream that already played is connected to the server.
   * `reconnecting` while the player retries after a mid-stream drop, `lost`
   * once it gave up; `connectionError` then says why.
   */
  connectionStatus: PlaybackConnectionStatus;
  connectionErrorTitle: string | null;
  connectionError: string | null;
}

/**
 * `ended` means the server ended the session for good (an administrator
 * stopped it); the player exits rather than reconnecting. `gone` means a
 * session-scoped answer proved the session no longer exists (a reaped or
 * expired session); the player stops using it and offers a fresh start at the
 * same position instead.
 */
export type PlaybackConnectionStatus = "connected" | "reconnecting" | "lost" | "ended" | "gone";

// Mid-stream reconnect backoff: 1s, 2s, 4s, 8s, then every 15s, for about two
// and a quarter minutes in total before the player gives up and asks the
// viewer. A cycle that starts within RECONNECT_STABLE_MS of the previous one
// recovering continues that cycle's budget, so a server that answers the API
// but drops every stream cannot keep the player retrying forever.
export const RECONNECT_BASE_DELAY_MS = 1_000;
export const RECONNECT_MAX_DELAY_MS = 15_000;
export const RECONNECT_MAX_ATTEMPTS = 12;
const RECONNECT_STABLE_MS = 30_000;

/** Delay before reconnect attempt `attempt` (0-based). */
export function reconnectDelayMs(attempt: number): number {
  return Math.min(RECONNECT_MAX_DELAY_MS, RECONNECT_BASE_DELAY_MS * 2 ** Math.max(0, attempt));
}

/**
 * Whether a failed reconnect request is worth repeating later: the server was
 * unreachable or overloaded, or it asked for a retry. Anything else is its
 * considered answer.
 *
 * `installation_changed` differs by step. A replan always carries the old
 * session's installation, so repeating it cannot succeed; it falls through to
 * a fresh start instead. A refused start has already dropped the cached
 * capabilities, so the next start reads the new installation and can succeed.
 */
function isRetryableReconnectError(error: unknown, step: "replan" | "start"): boolean {
  if (isTransientPlayerRequestError(error)) return true;
  return (
    error instanceof PlayerFetchError &&
    (error.status === 408 ||
      error.status === 429 ||
      error.code === "replan_in_progress" ||
      (step === "start" && error.code === "installation_changed"))
  );
}

/**
 * The tracks a reconnect's fresh start asks for: the ones the lost plan was
 * playing. A burned-in subtitle is flagged so a refusal falls back to playing
 * without it, as the initial start does for bitmap tracks.
 */
function reconnectTrackSelection(plan: PlanV3): {
  audioIndex?: number;
  subtitleIndex?: number;
  subtitleBurnIn: boolean;
} {
  const subtitleIndex = plan.selected_tracks.subtitle?.index;
  const subtitleBurnIn =
    subtitleIndex !== undefined &&
    (plan.subtitle.mode === "burn_in" ||
      plan.subtitle.inventory.some(
        (item) => item.combined_index === subtitleIndex && item.delivery === "burn_in_only",
      ));
  return { audioIndex: plan.selected_tracks.audio?.index, subtitleIndex, subtitleBurnIn };
}

type LoadSessionOutcome =
  | { kind: "adopted" }
  | { kind: "refused"; failure: PlaybackSessionErrorState }
  | { kind: "failed"; error: unknown }
  | { kind: "superseded" };

interface PlaybackSessionErrorState {
  title: string;
  message: string;
  reason?: string;
  retryable?: boolean;
}

/** The live effective identity of a source: its file and, when virtual, its candidate. */
interface SourceIdentity {
  fileId: number | null;
  uri: string | null;
}

/**
 * A realtime push held back while a start, switch, or replan owns the session.
 *
 * Arrival order is preserved so a flush never replays a newer source commit
 * before an older inventory revision. Each entry carries the adoption it
 * arrived under — the plan, the live outgoing identity, the generation, and the
 * session — so the flush decides against the adoption that actually won rather
 * than the rendered state, which still describes the outgoing source at a
 * settle path.
 */
interface DeferredPushBase {
  /** Local, monotonically increasing arrival order. */
  seq: number;
  /**
   * The source-identity transition clock at arrival. A later external transition
   * (a poll fold, or an adopted plan) raises the clock above this; a value still
   * at or below it is the state the entry already saw at capture and is already
   * represented by its `outgoing` baseline.
   */
  arrivalTransitionClock: number;
  /**
   * Whether the wire shape named any source at all. An identity-less push
   * cannot be tied to an adoption, so it may not cross a session change the
   * way an identity-carrying one is allowed to.
   */
  hasIdentity: boolean;
  identity: SourceIdentity;
  /** The plan in force at arrival, or null before any plan. */
  plan: PlanV3 | null;
  /** The live source identity in force at arrival (rotations included). */
  outgoing: SourceIdentity;
  /**
   * True when `outgoing` was itself produced by an earlier deferred source
   * commit rather than by the settled plan or an external poll. Such a baseline
   * is just the transport's previous move captured from the same queue, so it is
   * not authority a newer source commit must answer to.
   */
  outgoingFromDeferredSource: boolean;
  /** The adoption generation (`loadSequence`) that owned the session. */
  generation: number;
  /** The session id at arrival. */
  sessionId: string | null;
}

type DeferredPush =
  | (DeferredPushBase & {
      kind: "source";
      source: {
        effectiveMediaFileId?: number | null;
        effectiveVirtualUri?: string | null;
        inventoryStatus?: string | null;
      };
      audioTracks: PlayerAudioTrack[];
    })
  | (DeferredPushBase & { kind: "inventory"; payload: PlaybackInventoryUpdatedPayload });

type DeferredPushInput =
  | {
      kind: "source";
      identity: SourceIdentity;
      source: {
        effectiveMediaFileId?: number | null;
        effectiveVirtualUri?: string | null;
        inventoryStatus?: string | null;
      };
      audioTracks: PlayerAudioTrack[];
    }
  | {
      kind: "inventory";
      identity: SourceIdentity;
      payload: PlaybackInventoryUpdatedPayload;
    };

/** Whether an identity names a concrete source at all. */
function identityNamesSource(identity: SourceIdentity): boolean {
  return identity.uri != null || identity.fileId != null;
}

/**
 * Whether the settled plan explicitly names the push's exact identity.
 *
 * This is ownership evidence: when it holds, the winning plan itself vouches
 * for the source, so the push is retained even if it also names the candidate
 * the session was leaving — a replacement can retain the same effective
 * candidate, and the winner's own identity takes precedence.
 */
function identityMatchesPlan(identity: SourceIdentity, plan: PlanV3): boolean {
  if (identity.uri != null) {
    if (plan.effective_virtual_uri == null) return false;
    if (identity.uri !== plan.effective_virtual_uri) return false;
    // The concrete candidate is the release identity. A catalog row id can
    // move under it (duplicate rows, the owner row materializing, a serve-layer
    // association); that is not a different source for the menu, so the plan's
    // row id must not veto the payload. The fold adopts the payload's row.
    return true;
  }
  // A file-only push cannot be tied to a candidate when the plan names one.
  // When the plan names none, the file is the only key: an unknown candidate
  // means the same source. See the companion baseline rule in
  // `deferredIdentityIsAdmissible`.
  return (
    plan.effective_virtual_uri == null &&
    identity.fileId != null &&
    identity.fileId === plan.effective_media_file_id
  );
}

/**
 * Whether the settled plan is a different adoption than the one the push
 * arrived under.
 *
 * A plan that was replaced is the winning adoption and its file identity is
 * authoritative; a plan that is byte-for-byte the arrival plan is the same
 * (possibly refused) adoption, so it knows no more about the live source than
 * the arrival plan already did.
 */
function planWasReplaced(arrivalPlan: PlanV3 | null, plan: PlanV3): boolean {
  if (arrivalPlan == null) return true;
  return (
    arrivalPlan.plan_id !== plan.plan_id || arrivalPlan.plan_attempt_key !== plan.plan_attempt_key
  );
}

/**
 * Whether an identity names the live outgoing source.
 *
 * A URI is the discriminator whenever both sides carry one: a different URI is
 * a different candidate even on the same file. When either side is URI-less,
 * the file is the only key, and a shared file is treated as the outgoing source
 * because two candidates can share a file and the plan cannot tell them apart.
 */
function identityNamesSameSource(a: SourceIdentity, b: SourceIdentity): boolean {
  if (a.uri != null || b.uri != null) return a.uri === b.uri;
  return a.fileId != null && a.fileId === b.fileId;
}

/**
 * Whether two identities name one concrete candidate even when their catalog
 * row ids differ.
 *
 * The provider-neutral candidate URI (including the `?result=` pick) is the
 * release identity. One release can have several catalog rows: an owner row
 * that materializes later, duplicate rows imported for the same path, or the
 * row a serve-layer association moves the session onto. A push that keeps the
 * candidate and only moves the row is the same source for the menus, so the
 * stale-delivery guards must compare the URI first and adopt the payload's row
 * instead of dropping the revision.
 */
function identityNamesSameCandidate(a: SourceIdentity, b: SourceIdentity): boolean {
  return a.uri != null && b.uri != null && a.uri === b.uri;
}

/**
 * Whether a held-back push may be folded under the settled plan.
 *
 * The plan is the adoption that won; the rendered state is not consulted.
 * Whether it was replaced at all is decided first, because a refused plan is
 * still the pre-rotation plan and its URI would otherwise veto a legitimate
 * rotation. Against a replaced plan that names a candidate URI, that URI is the
 * only candidate it vouches for, and an exact match is retained even when the
 * live outgoing source names a different candidate (a replacement can retain
 * the effective candidate) — so a replaced URI-ful winner is decided by exact
 * identity *before* any ambiguity guard, or an outgoing candidate the session
 * was leaving would veto the plan the server just selected.
 *
 * When the replaced plan is URI-less (the v2 wire omits the candidate) file
 * equality is still the only key the wire offers, so a candidate-bearing push
 * naming the plan's own file is admitted — the payload routinely carries the
 * candidate the plan omits, and refusing it would leave the track menus stale
 * for the whole session. A URI-only push (no file id) cannot be tied to the
 * settled file by itself; it is admitted only when the arrival-time live source
 * was already on the winner's file, which corroborates that the candidate
 * belongs to the adoption the flush kept. Otherwise the push may be inventory
 * for the file the session was leaving, and folding it would inherit an
 * unrelated replacement file.
 *
 * A same-file source the plan cannot resolve stays refused whenever a concrete
 * candidate is already current on that file: a file-only push under any
 * concrete candidate, and a concrete push whose URI disagrees with one. That is
 * checked against two baselines — `outgoing`, the live source captured when the
 * push arrived, and `applied`, the identity the menus carry when the flush runs
 * — because a poll bypasses the adoption barrier and can fold a concrete
 * candidate after the push was queued, including when the plan is later refused
 * rather than replaced.
 *
 * A URI-bearing source commit is the transport's own move, so arrival order
 * decides among the queue's own commits: when such a commit supersedes a
 * previous deferred source commit, `outgoingSuperseded` drops that queue-produced
 * `outgoing` baseline. It does not license the commit to overwrite external
 * state: a candidate a poll applied after the entry was queued is newer than the
 * commit and stays authoritative. `appliedPollNewer` carries that candidate — the
 * menus may have moved on since — and is checked as an extra baseline. A
 * candidate the menus carried before the entry was queued is either the arrival
 * state already captured by `outgoing`, or a sibling the arrival collision
 * already rejects, so it is not re-checked.
 *
 * When the plan was refused rather than replaced, the live source is the
 * authority and `allowRotation` decides whether a cross-file source commit may
 * move it: a push that still names the live source is kept — the source never
 * moved — while a source commit naming another file is a rotation the transport
 * already made and is kept too.
 */
function deferredIdentityIsAdmissible(
  identity: SourceIdentity,
  plan: PlanV3,
  arrivalPlan: PlanV3 | null,
  outgoing: SourceIdentity,
  applied: SourceIdentity,
  allowRotation: boolean,
  /**
   * The concrete candidate a poll applied *after* this entry was queued, or null
   * when none did. A URI-bearing source commit is judged by arrival order against
   * its own queue, but this state is newer than the commit and still vetoes it.
   */
  appliedPollNewer: SourceIdentity | null,
  outgoingSuperseded = false,
  verifiedInventoryPush = false,
): boolean {
  if (!identityNamesSource(identity)) return false;

  // Whether the settled plan is a different adoption or the byte-for-byte
  // arrival plan. A refused plan still names the pre-rotation source, so its
  // URI must not veto a rotation the transport already committed to.
  const replaced = planWasReplaced(arrivalPlan, plan);

  // The winner was replaced and names a concrete candidate: its URI is the only
  // candidate it vouches for, and an exact match wins outright. This must be
  // decided before the verified-inventory shortcut and every collision guard
  // below, which would otherwise let the outgoing candidate the session was
  // leaving admit a superseded inventory under the winning plan. A URI-less
  // replacement (the v2 wire) leaves a URI-only push as the only identity the
  // client has; the applied-source vetoes below still judge it.
  if (replaced && plan.effective_virtual_uri != null) {
    return identityMatchesPlan(identity, plan);
  }

  // A verified inventory revision is the server's own inventory for the live
  // session. When it names the exact candidate the live source carries — at
  // arrival or once the flush runs — the payload is not another source even
  // when its catalog row differs from the plan's, so it folds in place. This is
  // the same admission the direct path makes; without it, a verified push held
  // behind a reconciliation replan would be refused at the flush and the menus
  // a cold, empty plan never had would stay empty.
  //
  // It still yields to source state newer than the queued entry: once a poll or
  // an applied replacement has moved the live source to another candidate, an
  // inventory naming the outgoing candidate is stale even though that URI was
  // live at arrival.
  if (verifiedInventoryPush && identity.uri != null) {
    if (applied.uri != null && applied.uri !== outgoing.uri && identity.uri !== applied.uri) {
      return false;
    }
    if (appliedPollNewer?.uri != null && identity.uri !== appliedPollNewer.uri) {
      return false;
    }
    if (identity.uri === outgoing.uri || identity.uri === applied.uri) {
      return true;
    }
  }

  // A concrete candidate already current on the push's file is authority the
  // push cannot outweigh: a file-only push carries no candidate, and a push
  // naming a different candidate is a sibling collision. A cross-file baseline
  // is a different source and does not apply.
  //
  // A URI-bearing source commit is the transport's own move, so arrival order
  // decides among the queue's own commits: `outgoingSuperseded` drops the
  // queue-produced `outgoing` baseline of an earlier deferred source commit. The
  // live `outgoing` baseline is still consulted — it is the source the transport
  // was on at arrival, and a differing concrete candidate on the same file is a
  // real collision. What arrival order does *not* license is overwriting a
  // candidate a poll folded *after* the entry was queued: that `appliedPollNewer`
  // state is newer than the commit, so it is added as a veto even for a commit.
  // An older applied candidate predates the entry and is either the arrival state
  // already captured by `outgoing` or a sibling the arrival collision already
  // rejects, so it is not re-checked.
  const judgedByArrival = allowRotation && identity.uri != null;
  const baselines: SourceIdentity[] = [outgoing];
  if (!judgedByArrival) baselines.push(applied);
  else if (appliedPollNewer != null) baselines.push(appliedPollNewer);
  for (const baseline of baselines) {
    if (outgoingSuperseded && baseline === outgoing) continue;
    if (baseline.uri == null) continue;
    // An unknown file on either side is treated as the same file: neither can
    // prove a different source, so a differing concrete URI is a collision.
    // Companion to the file-only branch in `identityMatchesPlan`: both read an
    // absent key as the same source, never as proof of a different one.
    const sameFile =
      baseline.fileId == null || identity.fileId == null || baseline.fileId === identity.fileId;
    if (!sameFile) continue;
    if (identity.uri == null || identity.uri !== baseline.uri) return false;
  }

  // The plan was refused, not replaced. `allowRotation` applies to this branch
  // only; the replaced-plan branches decide from the winner.
  if (!replaced) {
    // The live source is authoritative: a push that names it exactly is a
    // same-source update, and a source commit may also prove the transport
    // rotated to another file. The refused plan's own URI describes the source
    // before that rotation, so it is not applied as authority here.
    if (identityNamesSameSource(identity, outgoing)) return true;
    return (
      allowRotation &&
      identity.fileId != null &&
      (outgoing.fileId == null || identity.fileId !== outgoing.fileId)
    );
  }

  // The winner is URI-less. The baseline loop above already refused a same-file
  // collision, so a push naming the plan's own file is admitted; a push naming
  // another file is a rotation the URI-less plan cannot vouch for.
  if (identity.fileId != null) {
    return identity.fileId === plan.effective_media_file_id;
  }
  // A URI-only push offers no file key. Admit it only when the arrival-time live
  // source was on the winner's file, which ties the candidate to the settled
  // adoption; otherwise it may belong to the file being replaced and must wait
  // for a current-source refresh instead of inheriting the replacement's id.
  return outgoing.fileId != null && outgoing.fileId === plan.effective_media_file_id;
}

/** A start body plus the optional force-relink flag the retry path adds. */
type StartRequestWithForceRelinkV3 = StartRequestV3 & { force_relink?: boolean };

export interface UsePlaybackSessionResult extends PlaybackSessionState {
  /**
   * The browser decode engine's capabilities, when browser decoding is on and
   * this browser can run it. The player routes `original_http` plans with it.
   */
  browserEngine: EngineCapabilities | null;
  /** What the media element itself plays from an untouched original. */
  nativeOriginalSupport: NativeOriginalSupport;
  /** Starts a fresh session against another file (edition/version switch). */
  switchVersion: (fileId: number, currentPosition: number) => void;
  /**
   * Arms automatic version fallback (the version menu's Auto entry). When the
   * current source is already terminal, recovers immediately with an auto
   * re-resolve; otherwise it only arms Auto for a later dead source. A healthy
   * mid-play source is never switched.
   */
  selectAutoVersion: () => void;
  /**
   * Re-issues the start request for the file whose start was refused with a
   * retryable terminal, forcing a fresh release relist. No-op while a retry is
   * already in flight or once a plan is playing.
   */
  retryStart: () => void;
  /**
   * `track_change` replan selecting another audio track by combined index. A
   * pick the plan's inventory cannot name is ignored rather than sent.
   */
  switchAudioTrack: (index: number, currentPosition: number) => void;
  /**
   * `track_change` replan selecting (or clearing) the server-side subtitle
   * track. Only needed for tracks the server has to render — burn-in and
   * conversion. Sidecar tracks are fetched from the plan's inventory and drawn
   * by the client without involving the server. A stale ordinal the plan's
   * inventory does not carry is reconciled by identity/language or ignored.
   *
   * `displayedTrack` is the row the viewer clicked when the menu lists
   * watch-detail fallback rows the session inventory does not carry yet (an
   * empty inventory while the probe is deferred). Without it a click during an
   * inventory refresh would be captured with no identity at all.
   */
  changeSubtitleTrack: (
    combinedIndex: number | null,
    currentPosition: number,
    displayedTrack?: PlayerSubtitleInfo | null,
  ) => void;
  /** `quality_change` replan for a label taken from `plan.available_qualities`. */
  changeQuality: (label: string, currentPosition: number) => void;
  /** `failure_recovery` replan after the client could not play the plan. */
  recoverFromFailure: (failure: FailureV3, currentPosition: number) => void;
  /**
   * Replans off a plan the *server* invalidated over the realtime
   * `plan_invalidated` command: `failure_recovery` for a route failure, or a
   * non-failure `track_change` for the default-audio reconciliation reason.
   * Resolves to whether a replacement plan is now playing; the caller reports
   * that back as the command's result.
   *
   * A stream that already played lost its connection to the server. Retries
   * with backoff until the server answers, then resumes at `positionSeconds`,
   * playing again only when `resume` is set.
   */
  recoverConnection: (positionSeconds: number, resume: boolean) => void;
  /** Starts a fresh reconnect cycle after the previous one gave up. */
  retryConnection: () => void;
  /**
   * Runs the reconnect's next attempt immediately when a session-scoped call
   * confirmed the session it is rebuilding is gone. No-op unless a reconnect
   * is active with its backoff timer armed.
   */
  expediteReconnect: () => void;
  /**
   * Enters the lost state for a session the server no longer has when the
   * reconnect cannot run (no played stream to reposition).
   */
  reportSessionLost: () => void;
  /**
   * Enters the session-gone state: a session-scoped call (a control-ticket
   * mint, a stream/subtitle fetch, or a replan) proved the playback session no
   * longer exists. The player stops using the dead session instead of
   * retrying it, and offers `restartPlayback`.
   */
  markSessionGone: () => void;
  /**
   * Starts a fresh playback session at the current position after the old one
   * was answered gone. This is a new start (the server re-plans from scratch),
   * never a replan against the dead session.
   */
  restartPlayback: () => void;
  /**
   * `failure_recovery` replan for a plan the *server* invalidated over the
   * realtime `plan_invalidated` command. Resolves to whether a replacement plan
   * is now playing; the caller reports that back as the command's result.
   */
  invalidatePlan: (planId: string, reason: string, currentPosition: number) => Promise<boolean>;
  /**
   * `seek_reanchor` replan when the target lies outside the seekable window.
   * Resolves with whether a plan at the new position was adopted.
   */
  reanchorSeek: (positionSeconds: number) => Promise<boolean>;
  /**
   * Re-reads the subtitle inventory by replanning with the selection unchanged.
   * Resolves to whether a fresh plan carrying the inventory was adopted, so a
   * caller polling for late subtitles can tell a real fill-in from a transient
   * replan failure.
   */
  refreshSubtitles: (currentPosition: number) => Promise<boolean>;
  /** Folds a realtime-delivered inventory entry in without a server round trip. */
  applySubtitleTrack: (track: SubtitleInventoryItemV3) => void;
  /**
   * Folds a richer probed audio inventory read from the live catalog into the
   * menu without touching the plan or the transport.
   *
   * A session that started before probe repair carries the synthesized
   * inventory the server had then. Polling the catalog later can reveal the
   * file's real tracks; this adopts them for rendering only. It never bumps
   * `planRevision`/`transportRevision`, so the stream is not reloaded, and the
   * plan stays the source of truth: for the file the plan already names, a
   * request no richer than the current inventory is ignored. When `fileId`
   * names a different file — a poll that resolved the effective virtual
   * candidate while the plan still names the collapsed row — the inventory is
   * for another target and replaces the menu wholesale, and `effectiveVirtualUri`
   * re-keys the live identity so the version menu follows that candidate too.
   */
  applyAudioInventory: (
    tracks: PlayerAudioTrack[],
    fileId?: number | null,
    effectiveVirtualUri?: string | null,
  ) => void;
  /**
   * Adopts the effective version a transport just committed to, and that
   * release's declared audio inventory, from the realtime `source_committed`
   * event.
   *
   * A serve-layer rotation moves the live source without a plan rebuild, so
   * nothing else updates `mediaFileId`/`effectiveVirtualUri`; this is what lets
   * the version and audio menus follow the streamed release. A changed identity
   * replaces the audio inventory wholesale — including with an empty list — so
   * the previous release's tracks are never shown under the new one. The plan
   * and its revisions are untouched, so the stream does not reload; the
   * existing inventory poll upgrades the declared list to probe evidence.
   */
  applyCommittedSource: (
    source: {
      effectiveMediaFileId?: number | null;
      effectiveVirtualUri?: string | null;
      inventoryStatus?: string | null;
    },
    audioTracks: PlayerAudioTrack[],
  ) => void;
  /**
   * The live deferred-inventory state for the adopted plan: the revision the
   * menus were built from, the poll URL the plan promised, and whether the
   * enumeration is still owed. The poll below reads it; pushes clear it.
   */
  inventoryRevision: string | null;
  inventoryUrl: string | null;
  inventoryPending: boolean;
  inventoryFailed: boolean;
  /**
   * Marks deferred discovery exhausted after polling attempts or deadline
   * time out, stopping the spinner and rendering an actionable failure state.
   * Only a still-declared menu is marked; a confident inventory instead clears
   * the pending flag, since no round is scheduled after exhaustion. A call that
   * names another `sessionId`/`mediaFileId` than the live session is a stale
   * timer and is ignored.
   */
  markInventoryExhausted: (expected?: {
    sessionId?: string | null;
    mediaFileId?: number | null;
  }) => void;
  /**
   * Clears a terminal discovery-failure marker when a fresh poll round takes
   * over, so the menus show "Discovering…" while a fallback is retrying rather
   * than a terminal notice beside an active retry.
   */
  markInventoryRetrying: () => void;
  /**
   * Reads the session's live inventory from the plan's `inventory_url` with the
   * folded revision as ETag. A 200 folds the verified (or failed) list through
   * the same reducer the push path uses; a 304 leaves the loading state alone.
   * Resolves true when the menus are no longer provisional.
   */
  pollInventory: () => Promise<boolean>;
  /**
   * Adopts a live inventory revision from the realtime `inventory_updated`
   * event: the committed identity, its audio list, and the complete subtitle
   * inventory in one fold.
   *
   * It obeys the same replacement-start guard as `applyCommittedSource` (a
   * pending switch owns the menus), is status-aware so only verified data
   * clears the provisional markers, and never bumps the plan or transport
   * revisions, so the stream keeps playing.
   */
  applyInventoryUpdate: (payload: PlaybackInventoryUpdatedPayload) => boolean;
  /** Keeps transport state current for output-capability replans. */
  updatePlaybackState: (positionSeconds: number, playing: boolean) => void;
  /**
   * Called when a transport shows its first frame. Reports `first_frame` once
   * per playback attempt, with `first_frame_ms` measured from the viewer's
   * request when one timed it; later transports of the attempt are ignored.
   */
  reportFirstFrame: () => void;
  /** Reports a playback route event as a diagnostic. Never affects playback. */
  reportEvent: (
    event: RouteEventNameV3,
    extra?: {
      failureClassification?: string;
      fallbackReason?: string;
      diagnostics?: Record<string, string | number | boolean | undefined | null>;
    },
  ) => void;
}

function subtitleSourceOf(source: string): PlayerSubtitleInfo["source"] {
  switch (source) {
    case "external":
    case "embedded":
    case "downloaded":
      return source;
    default:
      return undefined;
  }
}

/**
 * Maps the plan's subtitle inventory onto the player's track shape.
 *
 * `index` is the server's combined ordinal, copied verbatim: it is the identity
 * the client echoes back on a track change, and the key every subtitle consumer
 * in the player looks tracks up by. Entries the server publishes as
 * `burn_in_only` have no URL and are kept anyway — they are selectable, and
 * selecting one is what asks the server to burn them in.
 */
function mapSubtitleInventory(
  inventory: SubtitleInventoryItemV3[],
  mediaFileId: number,
  config: PlayerConfig,
): PlayerSubtitleInfo[] {
  const token = config.getAccessToken();
  return inventory.map((item) => ({
    index: item.combined_index,
    media_file_id: mediaFileId,
    track_id: item.track_id,
    burn_in_only: item.delivery === "burn_in_only",
    language: item.language ?? "",
    codec: item.codec,
    label: item.label ?? item.language ?? `Track ${item.combined_index + 1}`,
    source: subtitleSourceOf(item.source),
    forced: item.forced,
    hearing_impaired: item.hearing_impaired,
    default: item.default,
    sync_key: item.sync_key,
    url: item.url ? buildPlayerStreamUrl(config.apiBaseUrl, item.url, token) : "",
    font_bundle_url: item.font_bundle_url
      ? buildPlayerStreamUrl(config.apiBaseUrl, item.font_bundle_url, token)
      : undefined,
  }));
}

/**
 * Projects a plan onto the session state consumers render from.
 *
 * `durationSeconds` comes from `plan.source.duration_seconds` and nowhere else:
 * the spec forbids substituting the playback engine's reported duration, which
 * on an HLS copy remux is only the length produced so far.
 */
function resolveCarriedSubtitleIndexAcrossVersions(
  outgoingPlan: PlanV3 | null,
  targetFileId: number,
  versions: PlayerFileVersion[],
): number | null {
  if (!outgoingPlan) {
    return null;
  }
  const outgoingIndex = outgoingPlan.selected_tracks.subtitle?.index;
  if (outgoingIndex == null || outgoingIndex < 0) {
    return null;
  }
  const outgoingTrack = outgoingPlan.subtitle.inventory.find(
    (item) => item.combined_index === outgoingIndex,
  );
  if (!outgoingTrack) {
    return null;
  }
  const targetVersion = versions.find((v) => v.file_id === targetFileId);
  if (!targetVersion?.subtitle_tracks || targetVersion.subtitle_tracks.length === 0) {
    return null;
  }
  // Shared ordinal derivation so the carried index names the same track the
  // server publishes in the target version's inventory; see subtitleInventory.ts.
  const candidates = buildPublishedSubtitleTracks(targetVersion.subtitle_tracks);
  const match = matchSubtitleTrackAcrossVersions(candidates, outgoingTrack);
  return match?.index ?? null;
}

/**
 * Resolves the audio identity to send for a menu pick, or null when the pick
 * does not name a track in the current plan.
 *
 * The menu can be one inventory behind the plan: `applyAudioInventory` may
 * replace it with a probed catalog list, a rotation may re-key the source, and
 * a replacement plan may not carry the same tracks at all. The plan's stable
 * `track_id` is reconciled first (the audio resolver also falls back to
 * signature and language), and an ordinal that names no plan track is dropped
 * instead of sent: the server has no such selection to resolve, and answering
 * it only earns an `adaptation_unavailable` terminal.
 *
 * A plan that publishes no audio inventory is left alone, and an inventory
 * with no `track_id`s at all (a legacy synthesized list) keeps its ordinal
 * while it is a valid plan position: the server resolves it against the durable
 * request and the effective file, and that is the only selection channel those
 * plans have.
 */
function resolveAudioTrackChange(
  planTracks: readonly AudioTrackV3[],
  displayed: readonly PlayerAudioTrack[],
  index: number,
): TrackIdentityV3 | null {
  if (!Number.isInteger(index) || index < 0) return null;
  if (planTracks.length === 0) return { id: "", index };
  if (index >= displayed.length) return null;

  const identity = resolvePlanAudioIdentity(planTracks, displayed, index);
  if (identity.id !== "" && planTracks.some((track) => track.track_id === identity.id)) {
    // Keep the menu ordinal as the fallback exactly as the audio resolver
    // always has: the id is the authority the server resolves.
    return identity;
  }

  // The pick reconciled to nothing. A plan with stable identities has no
  // counterpart for it, so sending an ordinal would select an unrelated
  // language: report it instead of dropping it silently.
  const hasStableIdentities = planTracks.some((track) => (track.track_id ?? "").trim() !== "");
  if (hasStableIdentities) return null;

  // A legacy synthesized inventory publishes no identities, so the ordinal is
  // the only selection channel. Keep it while it is a valid plan position.
  const planIndex = identity.index ?? index;
  return planIndex < planTracks.length ? { id: "", index: planIndex } : null;
}

/**
 * Describes the plan inventory a queued pick was captured against. A subtitle
 * `track_id` is generated from the file id and published ordinal, so an equal
 * id only names the same asset while the inventory it was read from is still
 * the current one.
 */
function subtitleInventoryIdentity(inventory: readonly SubtitleInventoryItemV3[]): string {
  return JSON.stringify(
    inventory.map((item) => [
      item.combined_index,
      item.track_id,
      item.source,
      item.codec ?? null,
      item.language ?? null,
      item.label ?? null,
      item.forced,
      item.hearing_impaired,
      item.sync_key ?? null,
    ]),
  );
}

/**
 * A descriptor that names the same subtitle asset after the inventory re-keys.
 * Language, flags, codec, source, and label are the evidence the menus show the
 * viewer; unlike the ordinal-derived `track_id` they survive a slot move.
 */
function subtitleDescriptorKey(track: {
  language?: string | null;
  codec?: string | null;
  label?: string | null;
  source?: string | null;
  forced?: boolean | null;
  hearing_impaired?: boolean | null;
}): string {
  const source = (track.source ?? "").trim().toLowerCase();
  const knownSource =
    source === "external" || source === "embedded" || source === "downloaded" ? source : "";
  return [
    subtitleLanguageBase(track.language),
    track.forced ? "forced" : "full",
    track.hearing_impaired ? "hi" : "plain",
    (track.codec ?? "").trim().toLowerCase(),
    knownSource,
    // The player maps a missing label from the language, so compare the same
    // fallback instead of an empty label against that synthesized one.
    (track.label ?? track.language ?? "").trim().toLowerCase(),
  ].join("|");
}

/** The captured sidecar row survives a re-key through its stable sync key. */
function matchSubtitleByAsset(
  inventory: readonly SubtitleInventoryItemV3[],
  picked: PlayerSubtitleInfo,
): SubtitleInventoryItemV3 | null {
  const syncKey = (picked.sync_key ?? "").trim();
  if (!syncKey) return null;
  const matches = inventory.filter((item) => (item.sync_key ?? "").trim() === syncKey);
  return matches.length === 1 ? (matches[0] ?? null) : null;
}

function matchSubtitleByDescriptor(
  inventory: readonly SubtitleInventoryItemV3[],
  picked: PlayerSubtitleInfo,
): SubtitleInventoryItemV3 | null {
  const key = subtitleDescriptorKey(picked);
  const matches = inventory.filter((item) => subtitleDescriptorKey(item) === key);
  return matches.length === 1 ? (matches[0] ?? null) : null;
}

/**
 * Whether a plan row carrying the clicked row's `track_id` can stand in for it.
 *
 * The id embeds the plan's ordinal, so it only names the clicked asset while
 * the plan publishes the order the menu was read from. Adoption can preserve
 * verified rows under a provisional plan that publishes another order, which
 * re-mints every id: the equal id then names a different language. Language,
 * flags, codec, source, and label are the evidence the menus show, so a row
 * that disagrees on any of them is a different asset. A label the player
 * synthesized (from the language, or a slot default) must not veto an
 * otherwise identical row.
 */
function subtitleIdNamesPickedAsset(
  planRow: SubtitleInventoryItemV3,
  picked: PlayerSubtitleInfo,
): boolean {
  if (subtitleLanguageBase(planRow.language) !== subtitleLanguageBase(picked.language))
    return false;
  if ((planRow.codec ?? "").trim().toLowerCase() !== (picked.codec ?? "").trim().toLowerCase()) {
    return false;
  }
  if (Boolean(planRow.forced) !== Boolean(picked.forced)) return false;
  if (Boolean(planRow.hearing_impaired) !== Boolean(picked.hearing_impaired)) return false;
  const planSource = (planRow.source ?? "").trim().toLowerCase();
  const pickedSource = (picked.source ?? "").trim().toLowerCase();
  if (planSource && pickedSource && planSource !== pickedSource) return false;
  const planLabel = (planRow.label ?? "").trim().toLowerCase();
  const pickedLabel = (picked.label ?? "").trim().toLowerCase();
  if (planLabel && pickedLabel && planLabel !== pickedLabel) return false;
  return true;
}

/**
 * Resolves the subtitle identity to send for a menu pick, or null when the pick
 * cannot be reconciled to the current plan's inventory.
 *
 * The menu slot the viewer clicked is what identifies the pick, so the
 * displayed track's stable `track_id` is reconciled first, then its
 * language/signature (the same matcher version switches use). The ordinal is
 * deliberately not the primary key: after a replacement or a source rotation
 * the menu can still show the outgoing inventory, and the plan can publish a
 * *different* track at that same ordinal. Trusting the ordinal there would
 * switch to a language the viewer never asked for.
 *
 * `trustPickedTrackId` is false for a queued pick whose captured inventory has
 * since been replaced. The server derives subtitle ids from the file id and
 * ordinal, so an equal id in the new inventory can name another language. The
 * captured row is then matched by its stable sync key or an unambiguous
 * descriptor instead; when neither names exactly one track the pick is
 * dropped rather than replayed against the old slot. An immediate pick trusts
 * its id only while the plan row that carries it still agrees with the clicked
 * row's descriptor: adoption preserves verified rows under a plan that can
 * publish another order, and the re-minted ids would otherwise name the track
 * that inherited the slot.
 *
 * A pick with a stable identity and no counterpart in the plan is dropped
 * rather than sent. The plan's exact ordinal is used only when there is no
 * stable picked identity to reconcile (the menu publishes no `track_id`s, or
 * names no slot) or when the plan publishes no inventory at all.
 *
 * A plan with no published inventory (deferred or legacy) keeps the ordinal:
 * there is no list to validate against, and the server remaps or degrades the
 * provisional pick. Confirmed server-side: `applySelectedTracksToStartV3`
 * copies the bare positive ordinal onto the start request, then
 * `ResolveSubtitlePolicyV3` resolves it against the effective file's own
 * external, embedded, and downloaded inventory. On a live session, an ordinal
 * that inventory does not carry (the deferred probe has not landed) degrades
 * to subtitles-off, never track 0 and never a 400. Audio behaves the same way through
 * `resolveV3AudioIndex`: a bare `{id: "", index}` is resolved against the
 * effective file's tracks, and an out-of-range index degrades to its default
 * track. See docs/playback-api.md#deferred-track-inventory.
 */
function resolveSubtitleTrackChange(
  plan: PlanV3,
  displayed: readonly PlayerSubtitleInfo[],
  combinedIndex: number,
  pickedTrack?: PlayerSubtitleInfo | null,
  trustPickedTrackId = true,
): TrackIdentityV3 | null {
  if (!Number.isInteger(combinedIndex) || combinedIndex < 0) return null;

  const inventory = plan.subtitle.inventory;
  const picked =
    pickedTrack !== undefined
      ? pickedTrack
      : displayed.find((item) => item.index === combinedIndex);
  if (inventory.length === 0) {
    // The plan publishes no inventory, so its ordinal space is unknown. The
    // menu rows a pick came from can belong to the catalog (this file before a
    // probe, or another release), and echoing their ordinal addresses a slot
    // this file may not have. While the server says the inventory is still
    // unestablished, hold the pick until a verified list lands instead of
    // sending a bare ordinal; a plan with no such signal keeps the historical
    // bare ordinal for menus that have no identity to reconcile against.
    if (picked && planSubtitleInventoryUnavailable(plan)) return null;
    return { id: "", index: combinedIndex };
  }

  if (picked) {
    // An equal `track_id` is only evidence while the plan publishes the same
    // order the menu was read from. When the row it names has another
    // language/flags, the click came from an inventory the plan reordered
    // (adoption preserves verified rows under a provisional plan), so the id
    // must not be replayed against the slot that inherited it.
    let idNamesAnotherAsset = false;
    if (trustPickedTrackId && picked.track_id) {
      const byId = inventory.find((item) => item.track_id === picked.track_id);
      if (byId) {
        if (subtitleIdNamesPickedAsset(byId, picked)) {
          return { id: byId.track_id, index: byId.combined_index };
        }
        idNamesAnotherAsset = true;
      }
    }
    if (!trustPickedTrackId || idNamesAnotherAsset) {
      const byAsset = matchSubtitleByAsset(inventory, picked);
      if (byAsset) return { id: byAsset.track_id, index: byAsset.combined_index };
      const byDescriptor = matchSubtitleByDescriptor(inventory, picked);
      if (byDescriptor) {
        return { id: byDescriptor.track_id, index: byDescriptor.combined_index };
      }
      // The inventory moved and the clicked row has no unambiguous
      // counterpart. Its old slot now names whichever track moved into it.
      return null;
    }
    const candidates = inventory.map((item) => ({ ...item, index: item.combined_index }));
    const byLanguage = matchSubtitleTrackAcrossVersions(candidates, picked);
    if (byLanguage) return { id: byLanguage.track_id, index: byLanguage.combined_index };
    // A displayed slot with a stable identity the plan cannot name has no safe
    // counterpart: its ordinal belongs to the stale menu, and in the current
    // plan it may well point at another language.
    if (picked.track_id) return null;
  }

  // No stable picked identity to reconcile from: the ordinal is the only
  // channel the menu offers, so keep it while the plan publishes that slot.
  // That reasoning only holds for the inventory the pick was captured against;
  // once it was replaced, the same slot names whichever track moved into it.
  if (!trustPickedTrackId) return null;
  const exact = inventory.find((item) => item.combined_index === combinedIndex);
  return exact ? { id: exact.track_id, index: exact.combined_index } : null;
}

/**
 * A viewer's track pick captured when it was made, not when it can finally be
 * sent. A seek reanchor or version switch can adopt a different plan before the
 * replan lane is free, and the plan-bound identity resolved at click time is
 * then stale. Carrying the row the viewer actually clicked lets the pick be
 * re-resolved against the plan that won instead of being dropped.
 */
type PendingTrackPick =
  | {
      kind: "audio";
      index: number;
      positionSeconds: number;
      track: PlayerAudioTrack | undefined;
      refreshAttempted: boolean;
    }
  | {
      kind: "subtitle";
      index: number | null;
      positionSeconds: number;
      track: PlayerSubtitleInfo | undefined;
      /** The inventory the picked row was read from, captured with the pick. */
      inventoryIdentity: string;
      refreshAttempted: boolean;
    };

/**
 * How many times one subtitle pick may be sent against one inventory before the
 * client stops trying. The server's `selection_unavailable` already ends it
 * after the first; the bound also covers a server that answers `mode: "off"`
 * without the reason, so a replay (the pending-pick flush, a retry timer) cannot
 * loop on the same pick.
 */
const MAX_SUBTITLE_PICK_REJECTIONS_V3 = 1;

/**
 * Key for the subtitle-pick rejection ledger: the effective file, the inventory
 * revision the pick was placed against, and the stable track identity. A new
 * click on another track, or a verified inventory that advances the revision,
 * is a different key and gets its own attempt.
 */
function subtitlePickRejectionKey(plan: PlanV3, identity: TrackIdentityV3): string | null {
  const id = identity.id?.trim();
  const index = identity.index;
  const asset = id && id.length > 0 ? id : index != null && index >= 0 ? `index:${index}` : "";
  if (asset === "") return null;
  const inventoryRevision = plan.inventory_revision ?? "";
  return `${plan.effective_media_file_id}|${inventoryRevision}|${asset}`;
}

/** Whether the plan answers the client's pick with the stable rejection reason. */
function planRejectsSubtitleSelection(plan: PlanV3): boolean {
  return plan.claims?.subtitles?.reason === SUBTITLE_REASON_SELECTION_UNAVAILABLE_V3;
}

/**
 * Whether the plan says its (empty) subtitle inventory was never established.
 * The menu must stay in its discovery state and a pick has no ordinal space to
 * address yet.
 */
function planSubtitleInventoryUnavailable(plan: PlanV3): boolean {
  return (
    plan.claims?.subtitles?.reason === SUBTITLE_REASON_INVENTORY_UNAVAILABLE_V3 ||
    plan.tracks_pending === true
  );
}

function trackPickOptions(plan: PlanV3, pick: PendingTrackPick): ReplanOptions | null {
  if (pick.kind === "audio") {
    const audio = resolveAudioPickIdentity(plan.audio_tracks ?? [], pick.track, pick.index);
    if (!audio) return null;
    return {
      operation: "track_change",
      positionSeconds: pick.positionSeconds,
      audio,
    };
  }
  if (pick.index === null) {
    return { operation: "track_change", positionSeconds: pick.positionSeconds, subtitle: null };
  }
  const subtitle = resolveSubtitleTrackChange(
    plan,
    [],
    pick.index,
    pick.track,
    pick.inventoryIdentity === subtitleInventoryIdentity(plan.subtitle.inventory),
  );
  if (!subtitle) return null;
  return {
    operation: "track_change",
    positionSeconds: pick.positionSeconds,
    subtitle,
  };
}

// Temporary client-side heuristic for A/V transport equivalence.
// TODO: replace with a server-authoritative transport_generation on PlanV3
// that bumps only when the A/V bytes change, so sidecar-only replans never
// guess. Until then, compare the fields that determine the produced bytes.
function isSameAVTransport(prev: PlanV3 | null, next: PlanV3): boolean {
  if (!prev) return false;
  if (prev.delivery !== next.delivery) return false;
  if (prev.stream.url !== next.stream.url) return false;
  if (prev.stream.protocol !== next.stream.protocol) return false;
  if (prev.stream.container !== next.stream.container) return false;
  if (prev.stream.mime_type !== next.stream.mime_type) return false;
  if (prev.effective_media_file_id !== next.effective_media_file_id) return false;
  // A start naming a different requested file is a version switch even when the
  // server resolves both rows to the same effective file and reuses the same
  // transport URL: collapsed `virtual://…` rows share one concrete candidate, so
  // `effective_media_file_id` and `stream.url` can be identical across the two
  // plans. Reusing the mounted element there leaves the old session's picture on
  // screen, so the requested identity is part of the transport identity. Replans
  // carry no file id and keep the durable requested row, so this never fires on
  // an ordinary track/quality replan.
  if (prev.requested_media_file_id !== next.requested_media_file_id) return false;
  if (prev.selected_tracks.audio?.index !== next.selected_tracks.audio?.index) return false;
  if (prev.timeline.stream_origin_seconds !== next.timeline.stream_origin_seconds) return false;
  if (prev.timeline.can_seek_anywhere !== next.timeline.can_seek_anywhere) return false;
  // A text-sidecar selection (or a `track_change` that re-mints the same
  // sidecar URLs) does not change the produced A/V bytes, so it must not bump
  // the transport revision. Only a burn-in route changes the picture, and only
  // it makes the selected subtitle track part of the transport identity. The
  // server reuses the active transport for a sidecar-only replan, so the URL
  // compared above stays identical; if it did not, that URL change is a real
  // transport change and must reload the element.
  if (prev.subtitle.mode === "burn_in" || next.subtitle.mode === "burn_in") {
    if (prev.subtitle.mode !== next.subtitle.mode) return false;
    if (prev.subtitle.track_id !== next.subtitle.track_id) return false;
  }
  return true;
}

function planToSessionState(
  plan: PlanV3,
  sessionId: string | null,
  playbackAttemptId: string,
  planRevision: number,
  transportRevision: number,
  replayRevision: number,
  qualityPreference: string,
  shouldAutoPlay: boolean,
  autoFallback: boolean,
  config: PlayerConfig,
): PlaybackSessionState {
  const planAudioTracks = plan.audio_tracks ?? [];
  return {
    plan,
    planRevision,
    transportRevision,
    replayRevision,
    streamUrl: buildPlayerStreamUrl(config.apiBaseUrl, plan.stream.url, config.getAccessToken()),
    sessionId,
    playbackAttemptId,
    mediaFileId: plan.effective_media_file_id,
    effectiveVirtualUri: plan.effective_virtual_uri ?? null,
    inventoryRevision: plan.inventory_revision ?? null,
    inventoryUrl: plan.inventory_url ?? null,
    // tracks_pending is the deferred signal: the enumeration is owed and the
    // menus stay loading until a terminal outcome (push or poll) lands.
    inventoryPending: plan.tracks_pending === true,
    inventoryFailed: plan.inventory_status === "failed" || plan.inventory_provenance === "failed",
    initialPosition: plan.timeline.player_start_seconds,
    // The marker is the applied row, not the raw plan ordinal: the same
    // resolver the inventory folds use (the plan is both sides here, so it
    // short-circuits to the committed ordinal). A plan with no committed
    // selection reports no marker instead of defaulting onto row 0.
    audioTrackIndex: resolveAppliedAudioIndex(
      planAudioTracks,
      planAudioTracks,
      plan.selected_tracks.audio,
    ),
    durationSeconds: plan.source.duration_seconds ?? null,
    planAudioTracks,
    // tracks_pending is the explicit deferred signal and wins over the
    // stamp/provenance fallback: the server sets it exactly when the plan's
    // inventory is the provisional recipe and a follow-up is promised.
    audioInventoryProvisional: isInventoryProvisional(
      plan.inventory_status,
      plan.inventory_provenance,
      plan.tracks_pending,
    ),
    subtitleInventoryProvisional: isPlanSubtitleInventoryProvisional(plan),
    subtitleUrls: mapSubtitleInventory(
      plan.subtitle.inventory,
      plan.effective_media_file_id,
      config,
    ),
    qualityPreference,
    shouldAutoPlay,
    loading: false,
    replacing: false,
    replanning: false,
    autoFallback,
    replanningQuality: false,
    pendingSwitchFileId: null,
    errorTitle: null,
    error: null,
    errorReason: null,
    errorRetryable: false,
    retrying: false,
    initialSubtitleErrorTitle: null,
    initialSubtitleError: null,
    // A track notice describes a pick that could not be reconciled against the
    // plan it was made for. Adopting a plan settles that pick (accepted, or
    // surfaced once after the bounded refresh), so the notice is one adoption
    // old at most and never outlives the request that produced it.
    trackSelectionNotice: null,
    // A plan was just handed out, so the server is reachable.
    connectionStatus: "connected",
    connectionErrorTitle: null,
    connectionError: null,
  };
}

/**
 * Turns a v3 decision into an error state.
 *
 * A conforming decision has exactly two shapes: a plan or a terminal. The
 * fallback below is defensive handling for a malformed or future response; it
 * is not a third protocol outcome.
 */
function describeDecisionWithoutPlan(decision: DecisionResponseV3): PlaybackSessionErrorState {
  if (decision.terminal) {
    return describePlanTerminal(decision.terminal);
  }
  // A server may publish the outcome without a terminal. An outcome that names
  // no playable adaptation is still a player-facing refusal, and retrying it as
  // a fresh start is meaningful (the provider may re-list or re-resolve the
  // source), so it must not fall through to the generic server-busy copy.
  if (isAdaptationUnavailableReason(decision.outcome)) {
    return {
      ...describePlanTerminal({
        reason: decision.outcome,
        message: "",
        retryable: true,
      }),
      reason: decision.outcome,
      retryable: true,
    };
  }
  return {
    title: "Playback unavailable",
    message: "This server is not accepting playback requests right now.",
  };
}

function describePlaybackSessionError(
  error: unknown,
  fallbackMessage: string,
  phase: "start" | "update" = "start",
): PlaybackSessionErrorState {
  const transportError = describePlaybackTransportError(error, phase);
  if (transportError) {
    return transportError;
  }

  if (error instanceof Error && error.message.trim().length > 0) {
    return {
      title: "Playback unavailable",
      message: error.message,
    };
  }

  return {
    title: "Playback unavailable",
    message: fallbackMessage,
  };
}

// Stable empty reference: consumers key effects on the variants array, so a
// fresh `[]` default on every call would restart them.
const EMPTY_PLAYBACK_VARIANTS: PlayerPlaybackVariant[] = [];

/**
 * Owns the v3 playback session: the start decision, the plan it produced, and
 * every replan that supersedes it.
 *
 * 1. On mount: probe the browser → POST `/playback/start` with a v3 request →
 *    adopt the returned plan.
 * 2. Quality, track and recovery changes go to `/playback/{id}/replan`; each
 *    answers with a whole new plan, never a patch.
 * 3. On unmount: DELETE `/playback/{session_id}` with a keepalive fallback.
 */
export function usePlaybackSession(
  requestKey: string,
  versions: PlayerFileVersion[],
  playbackVariants: PlayerPlaybackVariant[] = EMPTY_PLAYBACK_VARIANTS,
  fileId?: number,
  initialPosition = 0,
  forceInitialPosition = false,
  qualityPreference?: string | null,
  maxBitrateKbps?: number | null,
  resumeHints?: ResumeHints,
  explicitAudioTrackIndex?: number | null,
  initialSubtitleTrackIndexByFileId?: Record<number, number>,
  initialBitmapSubtitleTrackIndexByFileId?: Record<number, number>,
  /** True when the initial `fileId` was explicitly chosen by the viewer (not
   * auto-selected); the server must not silently substitute another version. */
  explicitFileSelection = false,
  /** When true, the server should force a re-link/re-query of the virtual file
   *  on this start attempt. Only set when the viewer explicitly picks an
   *  unavailable version. */
  forceRelink = false,
  allowAlternateVersions = true,
  /** The profile's subtitle mode. `auto`/`always` make a version that owns a
   *  subtitle inventory preferable to one that does not when the start has no
   *  explicit file. */
  subtitleMode?: SubtitleMode | null,
): UsePlaybackSessionResult {
  const config = usePlayerConfig();
  const probe = useCodecDetection();
  const browserEngine = useBrowserEngineCapabilities();
  const capabilitiesSettled = probe.settled && browserEngine.settled;
  const clientCapabilities = useMemo(
    () => buildClientCapabilitiesV3(probe, browserEngine.capabilities),
    [probe, browserEngine.capabilities],
  );
  const clientPlaybackContext = useMemo(
    () => buildClientPlaybackContextV3(probe, browserEngine.capabilities),
    [probe, browserEngine.capabilities],
  );
  const nativeOriginalSupport = useMemo<NativeOriginalSupport>(
    () => ({
      containers: probe.containers,
      videoCodecs: probe.codecsVideo,
      audioCodecs: probe.codecsAudio,
    }),
    [probe.codecsAudio, probe.codecsVideo, probe.containers],
  );
  const capabilityRequestKey = useMemo(
    () => JSON.stringify([clientCapabilities, clientPlaybackContext]),
    [clientCapabilities, clientPlaybackContext],
  );

  const [state, setState] = useState<PlaybackSessionState>({
    plan: null,
    planRevision: 0,
    transportRevision: 0,
    replayRevision: 0,
    streamUrl: null,
    sessionId: null,
    playbackAttemptId: null,
    mediaFileId: null,
    effectiveVirtualUri: null,
    inventoryRevision: null,
    inventoryUrl: null,
    inventoryPending: false,
    inventoryFailed: false,
    initialPosition: 0,
    audioTrackIndex: 0,
    durationSeconds: null,
    subtitleUrls: [],
    planAudioTracks: [],
    audioInventoryProvisional: false,
    subtitleInventoryProvisional: false,
    qualityPreference: qualityPreference?.trim() || QUALITY_ORIGINAL_V3,
    shouldAutoPlay: true,
    loading: true,
    replacing: false,
    replanning: false,
    autoFallback: allowAlternateVersions && !explicitFileSelection,
    replanningQuality: false,
    pendingSwitchFileId: null,
    errorTitle: null,
    error: null,
    errorReason: null,
    errorRetryable: false,
    retrying: false,
    initialSubtitleErrorTitle: null,
    initialSubtitleError: null,
    trackSelectionNotice: null,
    connectionStatus: "connected",
    connectionErrorTitle: null,
    connectionError: null,
  });

  const sessionIdRef = useRef<string | null>(null);
  const planRef = useRef<PlanV3 | null>(null);
  const planRevisionRef = useRef(0);
  const transportRevisionRef = useRef(0);
  const replayRevisionRef = useRef(0);
  const serverFeaturesRef = useRef<string[]>([]);
  const stateRef = useRef(state);
  // Set as soon as the server says an administrator ended the session, before
  // the state carrying it renders, so a caller acting on an older render
  // cannot start a reconnect against it.
  const sessionEndedRef = useRef(false);
  // Set when a session-scoped answer proves the session no longer exists, for
  // the same reason: a stale render must not replan or reconnect against it.
  // Cleared by a fresh start (`restartPlayback` or a newly adopted plan).
  const sessionGoneRef = useRef(false);
  // The live effective source identity, folded through the same commits and
  // rotations as `state` but readable synchronously inside callbacks. Deferring
  // a push still moves where the stream actually is; later decisions (a queued
  // replan's staleness check, the next rotation's outgoing baseline) must see
  // that move, which the rendered state at a settle path does not.
  const liveSourceIdentityRef = useRef<SourceIdentity>({
    fileId: state.mediaFileId,
    uri: state.effectiveVirtualUri,
  });
  // The identity the most recent deferred source commit moved the live mirror
  // to, when nothing external has transitioned it since. A later deferred source
  // commit must not treat its own predecessor from the same queue as an
  // authority to answer to, while a poll fold is external and stays
  // authoritative; `transitionSourceIdentity` clears this on every such fold.
  const lastDeferredSourceIdentityRef = useRef<SourceIdentity | null>(null);
  // A monotonic clock raised on every *external* source-identity transition (a
  // poll fold or an adopted plan), never by the deferred queue's own replay. A
  // deferred push records the clock at arrival; a later poll fold raises it, so
  // the flush can tell a candidate folded after the entry was queued from one
  // the menus already carried at capture. See `captureDeferredPush`.
  const identityTransitionClockRef = useRef(0);
  // The live candidate a poll folded on the last external transition, with the
  // clock reading of that transition. Between an entry's arrival and its flush a
  // poll can move the menus to a candidate, and the winning plan's own adoption
  // can then overwrite the menu mirror before the flush runs. Remembering the
  // poll's candidate lets the flush veto a stale commit that would otherwise
  // overwrite it; the clock says whether the fold was newer than the entry.
  const lastPollAppliedRef = useRef<{ clock: number; identity: SourceIdentity } | null>(null);
  // The identity the menus render, mirrored outside the state updater. A poll
  // or rotation can fold before React has processed an earlier push in the same
  // tick, so the next transition is computed against this synchronous mirror;
  // the rendered state would still name the previous source.
  const menuIdentityRef = useRef<SourceIdentity>({
    fileId: state.mediaFileId,
    uri: state.effectiveVirtualUri,
  });
  // The offered candidate the menus are currently displaying for a row whose
  // pinned release vanished from the provider listing, or null. It is a
  // display-only overlay: the plan identity, session id and transport revision
  // never move for it, so it must be read synchronously (a menu selection can
  // land in the same tick as the fold) and cleared whenever the transport's own
  // source identity actually changes.
  const offeredVirtualUriRef = useRef<string | null>(null);
  const activeRequestKeyRef = useRef<string | null>(null);
  const activeCapabilityRequestKeyRef = useRef<string | null>(null);
  const playbackPositionRef = useRef(initialPosition);
  const startIntentRef = useRef({
    position: initialPosition,
    forceStartPosition: forceInitialPosition,
    // When the viewer asked for this request, for its first_frame_ms.
    intentAt: null as number | null,
  });
  const hasAdoptedPlanRef = useRef(false);
  const awaitingInitialPlayerPositionRef = useRef(false);
  const playbackPlayingRef = useRef(true);
  const playbackStartedRef = useRef(false);
  // Whether the viewer means playback to run, as of the current plan. Until
  // that plan's transport shows a frame, the reported transport state is the
  // teardown of the old one or a startup that never got going, so the intent
  // is the one the plan was adopted with.
  const planAutoPlayRef = useRef(true);
  const planTransportShownRef = useRef(false);
  const switchingRef = useRef(false);
  // Mirrors state.autoFallback so callbacks can read the armed state without
  // depending on the rendered value.
  const autoFallbackRef = useRef(allowAlternateVersions && !explicitFileSelection);
  // The last live inventory revision folded into the menus. The server stamps
  // every `inventory_updated` push with its inventory_revision and documents a
  // duplicate or stale revision as a no-op, so this gates repeat deliveries
  // without comparing plan identity (which a rotation can move).
  const inventoryRevisionRef = useRef<string | null>(null);
  const inventoryPendingRef = useRef(false);
  const pollInventoryInflightRef = useRef<Promise<boolean> | null>(null);
  const teardownAbortRef = useRef<AbortController>(new AbortController());
  // Identity-carrying pushes dropped while a start/switch/replan owns the
  // session. Applying them immediately would mutate the menus under the pending
  // replacement, but discarding them loses the only carrier of the incoming
  // source's virtual identity: the v2 wire omits `effective_virtual_uri`, so the
  // replacement plan may not name the candidate the push does. They are held in
  // one arrival-ordered queue and replayed once that operation settles and the
  // settled plan can vouch for the source. The order matters: a source commit
  // and an inventory revision for the same source must fold in the order the
  // server sent them, so a newer source is never overwritten by an older
  // inventory and vice versa.
  const deferredPushesRef = useRef<DeferredPush[]>([]);
  const deferredPushSeqRef = useRef(0);
  const deferredFlushTimerRef = useRef<number | null>(null);
  // Late-bound sink: the flusher is defined later in the render, after the
  // fold callbacks it closes over, so the interval reads it through this ref.
  const flushDeferredPushesRef = useRef<() => void>(() => {});
  // A bounded retry cadence so a push dropped while a start/switch/replan owns
  // the session is still folded in when that operation settles without a signal
  // this hook observes. The settle paths call the flusher directly; this is the
  // backstop for the fast pending/declared start.
  useEffect(() => {
    const controller = new AbortController();
    teardownAbortRef.current = controller;
    deferredFlushTimerRef.current = window.setInterval(() => {
      if (deferredPushesRef.current.length > 0) {
        flushDeferredPushesRef.current();
      }
    }, 2_000);
    return () => {
      controller.abort();
      if (deferredFlushTimerRef.current !== null) {
        window.clearInterval(deferredFlushTimerRef.current);
        deferredFlushTimerRef.current = null;
      }
    };
  }, []);
  // Latest-wins coalescing for version switches: while a switch is in flight,
  // a second click records the newest target here instead of being dropped, and
  // the completion handler starts the switch to it immediately.
  const pendingSwitchFileIdRef = useRef<number | null>(null);
  // The live position the viewer was at when they made that second click. The
  // chained switch starts only after the first one settles, so reusing the
  // first click's closure position would seek the replacement back to where the
  // viewer was dozens of seconds earlier.
  const pendingSwitchPositionRef = useRef<number | null>(null);
  const loadSequenceRef = useRef(0);
  // The start that produced the current terminal, so a retry can re-issue it
  // against the same file with the viewer's original position and selection.
  const retryTargetRef = useRef<{
    fileId: number;
    position: number;
    forceStartPosition: boolean;
    fileSelection: "auto" | "explicit";
    carriedAudioTrackId: string | null;
    offeredVirtualURI: string | null;
  } | null>(null);
  const retryingRef = useRef(false);
  // The dead session whose refused replan has already triggered one fresh
  // start. A reaped session gets exactly one rebuild; the replacement start is
  // a `start`, not a replan, so it cannot recursively trigger another. The key
  // is the session id: recovering the replacement is a different id and is
  // allowed, while a replan still refused for the session already rebuilt
  // surfaces the error instead of starting again.
  const recoveredDeadSessionRef = useRef<string | null>(null);
  // The seek-reanchor refusal that forced a fresh start, tracked as a chain:
  // the session whose frozen source failed and the replacement its rebuild
  // adopted. A second `seek_reanchor_evidence_unavailable` naming either of
  // them surfaces the retryable error instead of starting yet another session,
  // so one original attempt can never restart in a loop. `replacementSessionId`
  // stays null until the rebuild's start actually adopts a plan, and a rebuild
  // that is refused or fails outright leaves it null — the error is already on
  // screen there.
  const seekRebuildChainRef = useRef<{
    originSessionId: string;
    replacementSessionId: string | null;
  } | null>(null);

  // v3 identity. `playback_attempt_id` spans one whole attempt chain (a start
  // and every replan that follows it); `plan_attempt_id` identifies the single
  // plan currently on screen. Both are minted by the client — the server mints
  // `plan_id` and `plan_attempt_key`, which the client only ever echoes.
  const playbackAttemptIdRef = useRef<string | null>(null);
  const planAttemptIdRef = useRef<string>(randomUUID());
  const attemptedPlanKeysRef = useRef<string[]>([]);
  const attemptCountRef = useRef(1);
  const replanInFlightRef = useRef(false);
  // The attempt whose first frame is still to be reported. `intentAt` is the
  // `performance.now()` of the viewer's request that started it, or null when
  // nothing timed it; the event is still sent, just without a duration.
  const firstFrameRef = useRef<{
    attemptId: string;
    intentAt: number | null;
    reported: boolean;
  } | null>(null);
  // Adoptions in flight, counted per load sequence: a start or a replan whose
  // decision has not been applied yet. The server commits a replacement plan —
  // and starts the copy-safety scan behind it — before the client can read the
  // response, so a `plan_invalidated` command can name a plan this client is
  // still adopting. Waiters registered here are woken once their own sequence
  // has nothing in flight, which lets an invalidation decide against the plan
  // that actually won.
  //
  // The key is what makes the wait bounded. A superseded request — a version
  // switch abandoned mid-flight, a start whose `fetch` never settles — is not a
  // candidate to own the session any more, so waiting for it decides nothing
  // and is worse than not waiting: the server's invalidation deadline is 8s,
  // and a session that misses it is stopped outright. Only the sequence that
  // currently owns the session can still change what the invalidation should
  // decide against, so only it is waited on.
  const adoptionsInFlightRef = useRef(new Map<number, number>());
  const adoptionWaitersRef = useRef<Array<{ loadSequence: number; resolve: () => void }>>([]);
  const pendingReplanRef = useRef<{
    options: ReplanOptions;
    loadSequence: number;
    retireSessionOnRefusal: boolean;
    resolve: (adopted: boolean) => void;
    planId: string;
    // The live effective file the op was built against. A serve-layer rotation
    // moves it without changing `plan_id`, so the plan-id check alone would
    // replay a plan-bound op against another release's track ordinals.
    mediaFileId: number | null;
  } | null>(null);
  // Track picks held while a switch or another replan owns the lane. The pick
  // carries the row the viewer clicked, so it can be re-resolved against the
  // plan that eventually wins. Latest pick per kind wins; an audio and a
  // subtitle pick are independent intents and are replayed in click order.
  const pendingTrackPicksRef = useRef<PendingTrackPick[]>([]);
  // Late-bound flusher, like `flushDeferredPushesRef`: the pick dispatcher is
  // defined after the replan callback it needs.
  const flushPendingTrackPicksRef = useRef<() => void>(() => {});
  const issueReplanRef = useRef<
    (
      options: ReplanOptions,
      retireSessionOnRefusal?: boolean,
      reconnect?: boolean,
    ) => Promise<boolean>
  >(async () => false);
  const trackSelectionNoticeIdRef = useRef(0);
  // Per-(effective file, inventory revision, subtitle identity) rejection
  // ledger. A server answer of selection_unavailable is terminal for that pick:
  // the same request against the same inventory cannot succeed, so a replay —
  // the pending-pick flush, a timer — must not issue it again.
  const subtitlePickRejectionsRef = useRef(new Map<string, number>());
  // Late-bound surface for a rejected subtitle pick, bound next to
  // `surfaceTrackPickFailure` below (declared after `replan`).
  const surfaceSubtitlePickRejectionRef = useRef<() => void>(() => {});
  // Latest-wins target for a reanchor dropped while a version switch owned the
  // session. The switch completion replays it against the settled plan so a
  // cold-start seek issued mid-switch is not silently lost — but only if the
  // switch succeeded, the same playback generation still owns it, and no
  // chained switch is about to replace the plan again.
  const pendingSeekRef = useRef<{
    positionSeconds: number;
    playbackAttemptId: string | null;
  } | null>(null);
  // Late-bound reanchor: declared ahead of `reanchorSeek` so the switch
  // completion above can call it without depending on render order. Bound in
  // an effect — render-time ref writes are unsafe under concurrent React.
  const reanchorSeekRef = useRef<(positionSeconds: number) => Promise<boolean>>(async () => false);
  const qualityRef = useRef(qualityPreference?.trim() || QUALITY_ORIGINAL_V3);
  // The running mid-stream reconnect, if any. `generation` invalidates the
  // timer and any request of a cycle that has ended, so nothing it started can
  // act after the viewer left, a new start took over, or a plan was adopted.
  const reconnectRef = useRef({
    active: false,
    generation: 0,
    attempts: 0,
    positionSeconds: 0,
    resume: true,
    timer: null as ReturnType<typeof setTimeout> | null,
    recoveredAt: Number.NEGATIVE_INFINITY,
  });
  const runReconnectAttemptRef = useRef<(generation: number) => Promise<void>>(async () => {});
  const beginReconnectRef = useRef<
    (positionSeconds: number, resume: boolean, freshBudget: boolean) => void
  >(() => {});

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  /**
   * The single transition point for the authoritative source identity.
   *
   * The rendered state is not a usable baseline for the next fold: React may
   * batch two pushes into one render, so a rotation followed by a stale push in
   * the same tick would compare against the pre-rotation identity. Every fold
   * moves `menuIdentityRef` and the live mirror here, synchronously and outside
   * any state updater, and the updater only mirrors the value it wrote.
   */
  const transitionSourceIdentity = useCallback(
    (fileId: number | null, uri: string | null): boolean => {
      const previous = menuIdentityRef.current;
      const changed = previous.fileId !== fileId || previous.uri !== uri;
      menuIdentityRef.current = { fileId, uri };
      // A fold of the menu identity is also a commit of the live source, so the
      // outgoing baseline for the next rotation follows it. A deferred push
      // moves the live mirror separately in `captureDeferredPush`. Any external
      // transition supersedes the last deferred source commit, so it is no
      // longer a baseline a later queued commit may ignore.
      liveSourceIdentityRef.current = { fileId, uri };
      lastDeferredSourceIdentityRef.current = null;
      identityTransitionClockRef.current += 1;
      // A source rotation moves where the stream actually is; verified evidence
      // bound to the previous file/URI must not suppress inventory updates for
      // the new one. A row-only move (same concrete candidate URI, a new
      // catalog row for it) is not a rotation: the verified menu still
      // describes the source on screen, so keep the anchor and let the fold
      // adopt the new row without re-marking the menus provisional.
      if (changed) {
        const rowOnlySameCandidate = previous.uri != null && uri != null && previous.uri === uri;
        if (!rowOnlySameCandidate) {
          lastVerifiedIdentityRef.current = null;
        }
      }
      // A real identity move retires any display-only offered release: the
      // transport is now somewhere else, so the old offer is stale.
      if (changed) offeredVirtualUriRef.current = null;
      return changed;
    },
    [],
  );

  /**
   * Records a candidate a catalog poll folded, with the transition clock of the
   * fold. `deferredIdentityIsAdmissible` consults it so a source commit queued
   * before the poll cannot overwrite the poll's newer candidate, even after the
   * winning plan's own adoption has since overwritten the menu mirror.
   */
  const recordPollApplied = useCallback((identity: SourceIdentity) => {
    lastPollAppliedRef.current = { clock: identityTransitionClockRef.current, identity };
  }, []);

  /**
   * The revision bookkeeping scope for the current session generation.
   *
   * A revision is the server's opaque digest of the whole inventory *and* the
   * effective source it describes, so within one session redelivering a revision
   * that was already folded is the same payload and folding it twice is wrong.
   * Only genuinely applied revisions are recorded; a revision the flush refused
   * is deliberately left unrecorded, so a redelivery that the plan (or a later
   * rotation) vouches for still folds. The scope is keyed by the load generation
   * and resets only when that moves; an identity rotation must not clear it, or a
   * revision folded before the rotation would fold again once the source moved.
   */
  const revisionScopeRef = useRef<{ generation: number; revisions: Set<string> }>({
    generation: -1,
    revisions: new Set(),
  });
  const syncRevisionScope = useCallback(() => {
    const scope = revisionScopeRef.current;
    if (scope.generation !== loadSequenceRef.current) {
      scope.generation = loadSequenceRef.current;
      scope.revisions.clear();
      // A new load generation also abandons any remembered mid-switch seek: it
      // belongs to the session this generation replaces. A display-only offered
      // release belongs to that same superseded transport too.
      pendingSeekRef.current = null;
      offeredVirtualUriRef.current = null;
    }
    return scope;
  }, []);

  /**
   * Ends the running reconnect cycle, if any. `recovered` records when a cycle
   * got a plan back, so a stream that drops again right away continues that
   * cycle's backoff and budget rather than starting over.
   */
  const endReconnect = useCallback((recovered: boolean) => {
    const reconnect = reconnectRef.current;
    if (!reconnect.active) return;
    reconnect.active = false;
    reconnect.generation += 1;
    if (reconnect.timer !== null) {
      clearTimeout(reconnect.timer);
      reconnect.timer = null;
    }
    if (recovered) reconnect.recoveredAt = Date.now();
  }, []);

  const beginAdoption = useCallback((loadSequence: number) => {
    const inFlight = adoptionsInFlightRef.current;
    inFlight.set(loadSequence, (inFlight.get(loadSequence) ?? 0) + 1);
  }, []);

  /**
   * Counts one in-flight adoption out of its load sequence.
   *
   * A sequence's waiters are woken only when nothing is left in flight for it:
   * a queued replan is dispatched from its predecessor's `finally` before the
   * predecessor is counted out, so the count tracks the whole chain rather than
   * one request.
   */
  const endAdoption = useCallback((loadSequence: number) => {
    const inFlight = adoptionsInFlightRef.current;
    const remaining = (inFlight.get(loadSequence) ?? 0) - 1;
    if (remaining > 0) {
      inFlight.set(loadSequence, remaining);
      return;
    }
    inFlight.delete(loadSequence);
    const waiters = adoptionWaitersRef.current;
    if (waiters.length === 0) return;
    const settled = waiters.filter((waiter) => !inFlight.has(waiter.loadSequence));
    if (settled.length === 0) return;
    adoptionWaitersRef.current = waiters.filter((waiter) => inFlight.has(waiter.loadSequence));
    for (const waiter of settled) waiter.resolve();
  }, []);

  /**
   * Resolves once the sequence that currently owns the session has no start or
   * replan in flight, or null when it has none — callers act synchronously in
   * the common case rather than deferring a turn.
   *
   * A request from a superseded sequence is deliberately not waited for. It can
   * no longer install a plan (every path re-checks the sequence before adopting
   * one), so it has nothing left to say about what an invalidation should
   * decide against, and a hung one would otherwise hold the wait open past the
   * server's deadline and cost the live session its stream.
   */
  const awaitAdoptionSettled = useCallback((): Promise<void> | null => {
    const loadSequence = loadSequenceRef.current;
    if (!adoptionsInFlightRef.current.has(loadSequence)) return null;
    return new Promise<void>((resolve) => {
      adoptionWaitersRef.current.push({ loadSequence, resolve });
    });
  }, []);

  const reportEvent = useCallback(
    (
      event: RouteEventNameV3,
      extra?: {
        failureClassification?: string;
        fallbackReason?: string;
        diagnostics?: Record<string, string | number | boolean | undefined | null>;
      },
    ) => {
      const attemptId = playbackAttemptIdRef.current;
      if (!attemptId) return;
      const plan = planRef.current;
      const input = {
        event,
        playbackAttemptId: attemptId,
        ...routeEventPlanIdentityV3(plan, sessionIdRef.current, planAttemptIdRef.current),
        ...extra,
      };
      const sessionId = sessionIdRef.current;
      if (sessionId && hasSequencedProgress(sessionId)) {
        void reportSessionRouteEventV2(config, sessionId, buildRouteEventV3(input));
      }
      // A terminal start never produced a session; there is nothing to report it against.
    },
    [config],
  );

  /**
   * Adopts a decision as the live session, or surfaces its terminal.
   * Returns whether a plan was adopted.
   */
  const adoptDecision = useCallback(
    (
      decision: DecisionResponseV3,
      initialSubtitleFailure?: PlaybackSessionErrorState | null,
      forceTransportBump?: boolean,
    ): boolean => {
      serverFeaturesRef.current = decision.server_features;
      const plan = decision.playback_plan;
      if (!plan) {
        const failure = describeDecisionWithoutPlan(decision);
        if (decision.terminal) {
          reportEvent("terminal", { failureClassification: decision.terminal.reason });
        }
        // The plan already on screen is left alone. A refused replan surfaces
        // its reason without taking away the stream that is still playing; a
        // refused start had no stream to take away in the first place.
        setState((current) => ({
          ...current,
          loading: false,
          replacing: false,
          replanning: false,
          replanningQuality: false,
          pendingSwitchFileId: null,
          errorTitle: failure.title,
          error: failure.message,
          errorReason: failure.reason ?? null,
          errorRetryable: failure.retryable ?? false,
        }));
        return false;
      }

      const prevPlan = planRef.current;
      // Any adopted plan proves the server is reachable and replaces the
      // transport, so it also ends a reconnect in progress. The new transport
      // plays only if the viewer was playing when the connection dropped; the
      // element has been paused since, so its reported state says nothing.
      if (reconnectRef.current.active) playbackPlayingRef.current = reconnectRef.current.resume;
      planAutoPlayRef.current = playbackPlayingRef.current;
      planTransportShownRef.current = false;
      const attemptId = playbackAttemptIdRef.current;
      if (prevPlan !== null && planWasReplaced(prevPlan, plan) && attemptId) {
        firstFrameRef.current = {
          attemptId,
          reported: false,
          intentAt: performance.now(),
        };
      }
      endReconnect(true);
      const sessionId = plan.session_id ?? decision.session_id ?? sessionIdRef.current;
      // The verified-inventory anchor in force *before* this adoption. A real
      // (probed/verified) list already folded for this exact source must not be
      // regressed to a declared/pending plan's list by a later replan — the
      // replan may have been built before the probe persisted. Adoption is in
      // place: the stream is untouched, and only the menus hold the real list.
      const verifiedAnchor = lastVerifiedIdentityRef.current;
      const planIsProvisional = isInventoryProvisional(
        plan.inventory_status,
        plan.inventory_provenance,
      );
      const keepVerifiedInventory =
        planIsProvisional &&
        verifiedAnchor !== null &&
        verifiedAnchor.fileId === plan.effective_media_file_id &&
        (verifiedAnchor.uri ?? null) === (plan.effective_virtual_uri ?? null);
      planAttemptIdRef.current = randomUUID();
      planRef.current = plan;
      sessionEndedRef.current = false;
      // A plan for this session proves the server still holds it, so a gone
      // verdict from an earlier replica miss no longer applies.
      sessionGoneRef.current = false;
      sessionIdRef.current = sessionId ?? null;
      // The adopted plan is the new live source; a rotation deferred before it
      // landed is superseded by what the plan itself names.
      transitionSourceIdentity(plan.effective_media_file_id, plan.effective_virtual_uri ?? null);
      // A plan adopted with verified inventory seeds the verified-identity
      // anchor that same-source declared pushes must not downgrade. A plan
      // that still carries tracks_pending is provisional by definition, even
      // if its stamp or provenance reads confident.
      if (
        plan.tracks_pending !== true &&
        isInventoryProvisional(plan.inventory_status, plan.inventory_provenance) === false
      ) {
        lastVerifiedIdentityRef.current = {
          fileId: plan.effective_media_file_id,
          uri: plan.effective_virtual_uri ?? null,
        };
      } else if (keepVerifiedInventory) {
        // Re-seed the anchor `transitionSourceIdentity` just cleared, so the
        // preserved verified list also keeps suppressing later same-source
        // declared pushes.
        lastVerifiedIdentityRef.current = verifiedAnchor;
      }
      if (plan.inventory_revision != null) {
        inventoryRevisionRef.current = plan.inventory_revision;
        syncRevisionScope().revisions.add(plan.inventory_revision);
      }
      // A live plan means the dead session that forced a rebuild is behind us;
      // a replacement that landed on a new id clears the one-recovery guard so
      // that session can recover once in its turn.
      if (
        recoveredDeadSessionRef.current !== null &&
        recoveredDeadSessionRef.current !== sessionId
      ) {
        recoveredDeadSessionRef.current = null;
      }
      planRevisionRef.current += 1;
      // A failure recovery always prepares a fresh server generation, even when
      // the replacement plan reuses the same session-scoped URL and transport
      // shape; the A/V-byte heuristic cannot see that regeneration. Force a
      // transport revision bump so the player reloads and re-fetches the stream.
      if (forceTransportBump || !isSameAVTransport(prevPlan, plan)) {
        transportRevisionRef.current += 1;
      }
      hasAdoptedPlanRef.current = true;
      if (
        Number.isFinite(plan.timeline.source_start_seconds) &&
        plan.timeline.source_start_seconds >= 0
      ) {
        // Replan positions use the source timeline. Seed it immediately so an
        // output refresh before the first timeupdate preserves server-resolved
        // resume state instead of falling back to the caller's default zero.
        playbackPositionRef.current = plan.timeline.source_start_seconds;
        awaitingInitialPlayerPositionRef.current = plan.timeline.source_start_seconds > 0;
      }

      setState((current) => {
        const next = planToSessionState(
          plan,
          sessionId ?? null,
          playbackAttemptIdRef.current ?? "",
          planRevisionRef.current,
          transportRevisionRef.current,
          replayRevisionRef.current,
          qualityRef.current,
          playbackPlayingRef.current,
          current.autoFallback,
          config,
        );
        const verifiedAnchor = lastVerifiedIdentityRef.current;
        const sameVerifiedSource =
          verifiedAnchor !== null &&
          verifiedAnchor.fileId === plan.effective_media_file_id &&
          (verifiedAnchor.uri ?? null) === (plan.effective_virtual_uri ?? null);
        const isIncomingPlanProvisional =
          plan.tracks_pending === true ||
          isInventoryProvisional(plan.inventory_status, plan.inventory_provenance);

        if (sameVerifiedSource && isIncomingPlanProvisional) {
          // This source was already verified in this session. A replan that
          // echoes an earlier declared/pending plan must not downgrade the
          // menus or restore a pending state, and verified-empty inventories
          // are preserved unconditionally against stale declared lists.
          next.audioInventoryProvisional = false;
          next.subtitleInventoryProvisional = false;
          next.inventoryPending = false;
          next.inventoryFailed = false;
          next.planAudioTracks = current.planAudioTracks;
          next.subtitleUrls = current.subtitleUrls;
          // The preserved list is the applied menu, not the replan's own
          // (possibly reordered) ordinals: keep the marker on the row that
          // names the committed track instead of the plan position.
          next.audioTrackIndex = resolveAppliedAudioIndex(
            plan.audio_tracks ?? [],
            current.planAudioTracks,
            plan.selected_tracks.audio,
          );
          if (current.inventoryRevision != null) {
            next.inventoryRevision = current.inventoryRevision;
          }
        } else if (plan.tracks_pending !== true) {
          const confident =
            plan.inventory_provenance === "verified" ||
            (plan.inventory_provenance == null &&
              plan.inventory_status != null &&
              plan.inventory_status !== "declared");
          if (confident && (current.inventoryPending || current.inventoryUrl != null)) {
            next.inventoryPending = false;
          } else if (!confident && current.inventoryPending) {
            // Keep waiting: preserve the owed URL across a replan that omits
            // it, so the poll below still knows where to read.
            next.inventoryPending = true;
            if (next.inventoryUrl == null) next.inventoryUrl = current.inventoryUrl;
          }
        }
        inventoryPendingRef.current = next.inventoryPending;
        return {
          ...next,
          initialSubtitleErrorTitle:
            initialSubtitleFailure === undefined
              ? current.initialSubtitleErrorTitle
              : (initialSubtitleFailure?.title ?? null),
          initialSubtitleError:
            initialSubtitleFailure === undefined
              ? current.initialSubtitleError
              : (initialSubtitleFailure?.message ?? null),
        };
      });
      reportEvent("plan_selected");
      return true;
    },
    [config, endReconnect, reportEvent, syncRevisionScope, transitionSourceIdentity],
  );

  const requestStart = useCallback(
    async (
      targetFileId: number,
      position: number,
      forceStartPosition: boolean,
      playbackAttemptId: string,
      subtitleTrackIndex: number | undefined,
      subtitleTrackID: string | undefined,
      carriedAudioTrackID: string | null,
      fileSelection: "auto" | "explicit",
      offeredVirtualURI: string | undefined,
      forceRelink?: boolean,
      audioTrackIndex: number | null | undefined = explicitAudioTrackIndex,
    ): Promise<DecisionResponseV3> => {
      const body: StartRequestWithForceRelinkV3 = buildStartRequestV3({
        extraClientFeatures: VIDEO_CLIENT_FEATURES_V3,
        fileId: targetFileId,
        profileId: config.getProfileId() ?? "",
        playbackAttemptId,
        qualityPreference: qualityRef.current,
        allowAlternateVersions,
        position,
        forceStartPosition,
        // A carried audio track (version switch) names the file-bound identity
        // explicitly; the mount-time ordinal is meaningless for the new file
        // and must not be sent alongside it. An explicit per-call ordinal
        // overrides the mount-time default when no carried identity is given.
        explicitAudioTrackIndex:
          carriedAudioTrackID != null ? null : (audioTrackIndex ?? explicitAudioTrackIndex),
        carriedAudioTrackID,
        fileSelection,
        offeredVirtualURI,
        forceRelink,
        subtitleTrackIndex,
        subtitleTrackID,
        bandwidthEstimateKbps: detectBandwidthEstimateKbpsV3(),
        bandwidthCapKbps: maxBitrateKbps,
        clientCapabilities,
        clientPlaybackContext,
      });
      // force_relink asks the server to re-query the provider and drop a stale
      // virtual-source pin; without it a retry replays the cached candidate.
      if (forceRelink) {
        body.force_relink = true;
      }

      return await startPlaybackV2(config, body);
      // carriedAudioTrackID is a per-call argument, not render-scope state, so
      // it is intentionally absent from the deps array.
    },
    [
      allowAlternateVersions,
      clientCapabilities,
      clientPlaybackContext,
      config,
      explicitAudioTrackIndex,
      maxBitrateKbps,
    ],
  );

  const stopSession = useCallback(
    async (sessionId: string) => {
      await stopSequencedSession(config, sessionId);
    },
    [config],
  );

  const retireActiveSession = useCallback(
    (expectedSessionId: string) => {
      if (sessionIdRef.current !== expectedSessionId) return;
      loadSequenceRef.current += 1;
      void stopSession(expectedSessionId).catch(() => {
        // Best effort — stale session will time out server-side.
      });
      planRef.current = null;
      sessionIdRef.current = null;
      planAttemptIdRef.current = randomUUID();
      deferredPushesRef.current = [];
      transitionSourceIdentity(null, null);
      pendingSeekRef.current = null;
      // A held pick belongs to the session being retired. Replaying it against
      // whatever plan replaces this one would dispatch a track change the
      // viewer never made for that session.
      pendingTrackPicksRef.current = [];
      lastVerifiedIdentityRef.current = null;
      inventoryPendingRef.current = false;
      setState((current) => {
        if (current.sessionId !== expectedSessionId) return current;
        return {
          ...current,
          plan: null,
          streamUrl: null,
          sessionId: null,
          mediaFileId: null,
          effectiveVirtualUri: null,
          inventoryRevision: null,
          inventoryUrl: null,
          inventoryPending: false,
          inventoryFailed: false,
          initialPosition: 0,
          audioTrackIndex: 0,
          durationSeconds: null,
          subtitleUrls: [],
          planAudioTracks: [],
          audioInventoryProvisional: false,
          subtitleInventoryProvisional: false,
          loading: false,
          replacing: false,
          replanning: false,
          replanningQuality: false,
          pendingSwitchFileId: null,
        };
      });
    },
    [stopSession, transitionSourceIdentity],
  );

  /**
   * Enters the session-gone state after a session-scoped answer proved the
   * playback session no longer exists (a control-ticket mint, a stream or
   * subtitle fetch, or a replan). No reconnect and no replan can repair a
   * session the server does not hold, so the player stops using it and offers
   * `restartPlayback` instead.
   */
  const markSessionGone = useCallback(() => {
    if (sessionGoneRef.current) return;
    sessionGoneRef.current = true;
    endReconnect(false);
    setState((current) => ({
      ...current,
      loading: false,
      replacing: false,
      replanning: false,
      replanningQuality: false,
      pendingSwitchFileId: null,
      connectionStatus: "gone",
      connectionErrorTitle: PLAYBACK_SESSION_GONE_ERROR.title,
      connectionError: PLAYBACK_SESSION_GONE_ERROR.message,
    }));
  }, [endReconnect]);

  /**
   * Picks which file to *request*. The server owns adaptation — this only
   * expresses which edition the viewer is resuming, which is knowledge the
   * server does not have.
   *
   * Resume hints are a request for the release the viewer last played, not for
   * whatever candidate the server ranks now. A neutral `virtual://series/…` row
   * names no release at all: it is the collapsed episode row the server
   * re-resolves, so its effective candidate can lack the tracks the viewer
   * expects. Such a hint is ignored, and an explicit `preferredFileId` stays
   * authoritative.
   */
  const selectFileId = useCallback(
    (preferredFileId?: number) => {
      // The viewer named this row, even a neutral virtual one: the server
      // resolves it and the choice is not ours to second-guess.
      if (preferredFileId) return preferredFileId;

      const mode = normalizeSubtitleMode(subtitleMode);
      const lastFileId = resumeHints?.lastFileId;
      const lastVersion =
        lastFileId != null ? versions.find((version) => version.file_id === lastFileId) : undefined;
      const resumedVersion =
        lastVersion && !isNeutralVirtualVersion(lastVersion) ? lastVersion : undefined;
      const variantFileId = selectDefaultVariantFile(playbackVariants, versions, resumeHints);
      const variantFile =
        variantFileId != null
          ? versions.find((version) => version.file_id === variantFileId)
          : undefined;

      if (mode === "off") {
        if (resumedVersion) return resumedVersion.file_id;
        return variantFileId ?? versions[0]?.file_id ?? null;
      }

      // Subtitles are on. A concrete candidate that publishes a subtitle
      // inventory is a better request than one that does not, because the
      // candidate the server resolves then actually owns the tracks the viewer
      // expects. The concrete resume target is the first choice: it keeps the
      // release that was playing.
      if (resumedVersion && hasSubtitleTracks(resumedVersion)) return resumedVersion.file_id;

      // Anchor the search on the resumed edition/part, else on the variant's
      // default edition/part. A different cut or another part of a multi-part
      // presentation is not a subtitle fallback; it is a different thing to
      // play, so the scope never widens past the anchor's presentation.
      const anchor =
        resumedVersion ??
        (variantFile && !isNeutralVirtualVersion(variantFile) ? variantFile : undefined);
      if (anchor) {
        const subtitleSibling = versions.find(
          (version) =>
            version.file_id !== anchor.file_id &&
            !isNeutralVirtualVersion(version) &&
            isSamePresentation(version, anchor) &&
            hasSubtitleTracks(version),
        );
        return subtitleSibling?.file_id ?? anchor.file_id;
      }

      // No concrete anchor to preserve. Take any concrete candidate that
      // publishes subtitles, staying inside the resumed edition when the hints
      // name one, then fall back to the default variant.
      const editionScope = resumeHints?.lastEditionKey;
      const subtitleCandidate = versions.find(
        (version) =>
          !isNeutralVirtualVersion(version) &&
          hasSubtitleTracks(version) &&
          (editionScope == null || version.edition_key === editionScope),
      );
      if (subtitleCandidate) return subtitleCandidate.file_id;
      return variantFileId ?? versions[0]?.file_id ?? null;
    },
    [playbackVariants, resumeHints, subtitleMode, versions],
  );

  const loadSession = useCallback(
    async ({
      preferredFileId,
      position,
      forceStartPosition,
      allowPreserveExistingSessionOnError,
      replacementErrorMessage,
      initialErrorMessage,
      carriedAudioTrackId,
      carriedSubtitleTrackIndex,
      fileSelection,
      offeredVirtualURI,
      forceRelink,
      intentAt,
      reconnect = false,
      tracks,
    }: {
      preferredFileId?: number;
      position: number;
      forceStartPosition: boolean;
      allowPreserveExistingSessionOnError: boolean;
      replacementErrorMessage: string;
      initialErrorMessage: string;
      // Audio track identity carried from the current plan into a replacement
      // start (a version switch). The server remaps it by family to the new
      // file instead of dropping the viewer's selection.
      carriedAudioTrackId?: string | null;
      // Subtitle track selection carried across a version switch. Null means
      // subtitles were explicitly off on the outgoing plan and must stay off;
      // undefined leaves the start to initialSubtitleTrackIndexByFileId.
      carriedSubtitleTrackIndex?: number | null;
      /** How the requested file was chosen; `explicit` forbids silent
       * server-side version substitution. */
      fileSelection?: "auto" | "explicit";
      /** Concrete offered candidate this start binds to. */
      offeredVirtualURI?: string;
      /** When true, the server should force a re-link/re-query of the virtual
       * file on this start attempt. Set when the viewer explicitly picks an
       * unavailable version, or by the retry path after a no-streams terminal. */
      forceRelink?: boolean;
      /** When the viewer asked for this start, for its first_frame_ms. */
      intentAt: number | null;
      /** The start is a reconnect attempt, which must not cancel itself. */
      reconnect?: boolean;
      /**
       * Track selection to start with instead of the initial request's, so a
       * restarted session keeps what the viewer picked during playback. An
       * absent `subtitleIndex` is an explicit "subtitles off": a start that
       * names no subtitle track plays without one. `subtitleBurnIn` marks a
       * track the server has to burn in, which gets the same retry without
       * subtitles as a bitmap track at the initial start when it is refused.
       */
      tracks?: {
        audioIndex?: number;
        subtitleIndex?: number;
        subtitleTrackId?: string;
        subtitleBurnIn?: boolean;
      };
    }): Promise<LoadSessionOutcome> => {
      // Any other start supersedes a running reconnect: it owns the session now.
      if (!reconnect) endReconnect(false);
      const previousState = stateRef.current;
      const previousSessionId = sessionIdRef.current;
      // The session id comes from the ref, not the (possibly stale) state
      // snapshot: a recovery that retires a dead session before starting its
      // replacement nulls the ref in the same tick, and the replacement must be
      // treated as a fresh start rather than a replacement of the dead plan.
      const hasExistingSession = !!previousSessionId && !!previousState.streamUrl;
      // A new session at the start is a replay, not a continuation. The URL
      // and source generation can be identical to the previous session, so
      // transport-local consumers need this separate epoch to discard state
      // that belongs to the old playhead.
      if (
        position <= 0 &&
        (hasExistingSession || hasAdoptedPlanRef.current || forceStartPosition)
      ) {
        replayRevisionRef.current += 1;
      }
      const loadSequence = ++loadSequenceRef.current;
      const previousAttempt = {
        playbackAttemptId: playbackAttemptIdRef.current,
        planAttemptId: planAttemptIdRef.current,
        attemptedPlanKeys: [...attemptedPlanKeysRef.current],
        attemptCount: attemptCountRef.current,
      };
      const restorePreviousAttempt = () => {
        playbackAttemptIdRef.current = previousAttempt.playbackAttemptId;
        planAttemptIdRef.current = previousAttempt.planAttemptId;
        attemptedPlanKeysRef.current = previousAttempt.attemptedPlanKeys;
        attemptCountRef.current = previousAttempt.attemptCount;
      };

      setState((current) => ({
        ...current,
        loading: !hasExistingSession,
        replacing: hasExistingSession,
        replanning: false,
        replanningQuality: false,
        errorTitle: hasExistingSession ? current.errorTitle : null,
        error: hasExistingSession ? current.error : null,
        errorReason: hasExistingSession ? current.errorReason : null,
        errorRetryable: hasExistingSession ? current.errorRetryable : false,
        initialSubtitleErrorTitle: hasExistingSession ? current.initialSubtitleErrorTitle : null,
        initialSubtitleError: hasExistingSession ? current.initialSubtitleError : null,
        ...(reconnect
          ? {}
          : {
              connectionStatus: "connected" as const,
              connectionErrorTitle: null,
              connectionError: null,
            }),
      }));

      // A start begins a new attempt chain: fresh attempt id, empty loop guard.
      let playbackAttemptId = randomUUID();
      playbackAttemptIdRef.current = playbackAttemptId;
      attemptedPlanKeysRef.current = [];
      attemptCountRef.current = 1;

      const retirePreviousSession = (nextError?: PlaybackPolicyErrorDescription) => {
        if (previousSessionId) {
          void stopSession(previousSessionId).catch(() => {
            // Best effort — stale session will time out server-side.
          });
        }
        planRef.current = null;
        sessionIdRef.current = null;
        planAttemptIdRef.current = randomUUID();
        deferredPushesRef.current = [];
        transitionSourceIdentity(null, null);
        inventoryPendingRef.current = false;
        setState((current) => ({
          ...current,
          plan: null,
          streamUrl: null,
          sessionId: null,
          playbackAttemptId,
          mediaFileId: null,
          effectiveVirtualUri: null,
          inventoryRevision: null,
          inventoryUrl: null,
          inventoryPending: false,
          inventoryFailed: false,
          initialPosition: 0,
          audioTrackIndex: 0,
          durationSeconds: null,
          subtitleUrls: [],
          planAudioTracks: [],
          audioInventoryProvisional: false,
          subtitleInventoryProvisional: false,
          loading: false,
          replacing: false,
          replanning: false,
          replanningQuality: false,
          pendingSwitchFileId: null,
          errorTitle: nextError?.title ?? current.errorTitle,
          error: nextError?.message ?? current.error,
          errorReason: nextError ? (nextError.reason ?? null) : current.errorReason,
          errorRetryable: nextError?.retryable ?? current.errorRetryable,
        }));
      };

      beginAdoption(loadSequence);
      try {
        const selectedFileId = selectFileId(preferredFileId);
        if (!selectedFileId) {
          throw new Error("No playable version found");
        }
        // Remember what to re-issue if this start ends in a retryable terminal.
        retryTargetRef.current = {
          fileId: selectedFileId,
          position,
          forceStartPosition,
          fileSelection: fileSelection ?? "auto",
          carriedAudioTrackId: carriedAudioTrackId ?? null,
          offeredVirtualURI: offeredVirtualURI ?? null,
        };

        const targetSubtitleIndex =
          carriedSubtitleTrackIndex !== undefined
            ? (carriedSubtitleTrackIndex ?? undefined)
            : initialSubtitleTrackIndexByFileId?.[selectedFileId];

        const startAudioTrackIndex = tracks ? tracks.audioIndex : explicitAudioTrackIndex;
        const decision = await requestStart(
          selectedFileId,
          position,
          forceStartPosition,
          playbackAttemptId,
          tracks ? tracks.subtitleIndex : targetSubtitleIndex,
          tracks?.subtitleTrackId,
          carriedAudioTrackId ?? null,
          fileSelection ?? "auto",
          offeredVirtualURI,
          forceRelink,
          startAudioTrackIndex,
        );

        if (loadSequence !== loadSequenceRef.current) {
          const staleSessionId = decision.playback_plan?.session_id ?? decision.session_id;
          if (staleSessionId) {
            await stopSession(staleSessionId).catch(() => {
              // Best effort cleanup for stale session starts.
            });
          }
          return { kind: "superseded" };
        }

        let decisionToAdopt = decision;
        let initialSubtitleFailure: PlaybackSessionErrorState | null = null;
        const targetVersion = versions.find((v) => v.file_id === selectedFileId);
        // Shared ordinal derivation; `targetSubtitleIndex` is the server's
        // published index, so look it up on the same mapping. See
        // subtitleInventory.ts.
        const targetTracks = buildPublishedSubtitleTracks(targetVersion?.subtitle_tracks);
        const requestedTrack =
          targetSubtitleIndex !== undefined
            ? targetTracks.find((track) => track.index === targetSubtitleIndex)
            : undefined;
        const requestedIsBitmap = requestedTrack?.codec
          ? isBitmapCodec(requestedTrack.codec)
          : false;
        // Upstream's per-file bitmap fallback covers tracks the carried
        // selection does not name.
        const bitmapSubtitleTrackIndex = tracks
          ? tracks.subtitleBurnIn
            ? tracks.subtitleIndex
            : undefined
          : initialBitmapSubtitleTrackIndexByFileId?.[selectedFileId];
        const hasBitmapSubtitle =
          requestedIsBitmap ||
          bitmapSubtitleTrackIndex !== undefined ||
          (carriedSubtitleTrackIndex === undefined &&
            initialBitmapSubtitleTrackIndexByFileId?.[selectedFileId] !== undefined);
        if (!decision.playback_plan && hasBitmapSubtitle && isBitmapSubtitleRefusal(decision)) {
          initialSubtitleFailure = describeDecisionWithoutPlan(decision);
          if (decision.session_id) {
            void stopSession(decision.session_id).catch(() => {
              // Best effort cleanup for the refused subtitle-bearing start.
            });
          }

          // A fresh attempt id avoids colliding with start idempotency: the
          // retry intentionally changes the request by dropping the bitmap
          // subtitle that made the first plan impossible.
          const fallbackPlaybackAttemptId = randomUUID();
          playbackAttemptId = fallbackPlaybackAttemptId;
          playbackAttemptIdRef.current = fallbackPlaybackAttemptId;
          decisionToAdopt = await requestStart(
            selectedFileId,
            position,
            forceStartPosition,
            fallbackPlaybackAttemptId,
            undefined,
            undefined,
            carriedAudioTrackId ?? null,
            fileSelection ?? "auto",
            offeredVirtualURI,
            forceRelink,
            startAudioTrackIndex,
          );
          if (!decisionToAdopt.playback_plan) {
            initialSubtitleFailure = null;
          }
        }

        if (loadSequence !== loadSequenceRef.current) {
          const staleSessionId =
            decisionToAdopt.playback_plan?.session_id ??
            decisionToAdopt.session_id ??
            decision.playback_plan?.session_id ??
            decision.session_id;
          if (staleSessionId) {
            await stopSession(staleSessionId).catch(() => {
              // Best effort cleanup for a superseded start/replan chain.
            });
          }
          return { kind: "superseded" };
        }

        const adopted = adoptDecision(decisionToAdopt, initialSubtitleFailure);
        if (adopted) {
          // A start opens a new attempt. Its first frame is still to come:
          // whatever the player shows until the new transport loads belongs to
          // the attempt it replaced.
          firstFrameRef.current = { attemptId: playbackAttemptId, intentAt, reported: false };
        }
        if (!adopted && hasExistingSession && allowPreserveExistingSessionOnError) {
          restorePreviousAttempt();
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
            replanning: false,
            replanningQuality: false,
            pendingSwitchFileId: null,
            errorTitle: previousState.errorTitle,
            error: previousState.error,
            errorReason: previousState.errorReason,
            errorRetryable: previousState.errorRetryable,
          }));
          return { kind: "refused", failure: describeDecisionWithoutPlan(decisionToAdopt) };
        }
        if (!adopted) {
          retirePreviousSession();
          return { kind: "refused", failure: describeDecisionWithoutPlan(decisionToAdopt) };
        }
        if (adopted && previousSessionId && previousSessionId !== sessionIdRef.current) {
          void stopSession(previousSessionId).catch(() => {
            // Best effort — stale session will time out server-side.
          });
        }
        return { kind: "adopted" };
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) {
          return { kind: "superseded" };
        }

        if (hasExistingSession && allowPreserveExistingSessionOnError) {
          console.error(replacementErrorMessage, err);
          restorePreviousAttempt();
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
            replanning: false,
            replanningQuality: false,
            pendingSwitchFileId: null,
          }));
          return { kind: "failed", error: err };
        }

        const nextError = describePlaybackSessionError(err, initialErrorMessage);
        retirePreviousSession(nextError);
        return { kind: "failed", error: err };
      } finally {
        endAdoption(loadSequence);
        // A push deferred while this start/replan owned the session has waited
        // long enough: fold it in now that the sequence has no adoption left in
        // flight, so the incoming identity follows the plan that won.
        flushDeferredPushesRef.current();
      }
    },
    [
      adoptDecision,
      beginAdoption,
      endAdoption,
      endReconnect,
      explicitAudioTrackIndex,
      initialBitmapSubtitleTrackIndexByFileId,
      initialSubtitleTrackIndexByFileId,
      requestStart,
      selectFileId,
      stopSession,
      transitionSourceIdentity,
      versions,
    ],
  );
  const loadSessionRef = useRef(loadSession);
  loadSessionRef.current = loadSession;

  useEffect(() => {
    if (!capabilitiesSettled) return;
    if (activeRequestKeyRef.current === requestKey) {
      return;
    }
    activeRequestKeyRef.current = requestKey;
    activeCapabilityRequestKeyRef.current = capabilityRequestKey;
    qualityRef.current = qualityPreference?.trim() || QUALITY_ORIGINAL_V3;
    playbackPositionRef.current = initialPosition;
    const intentAt = takePlaybackIntent(requestKey);
    startIntentRef.current = {
      position: initialPosition,
      forceStartPosition: forceInitialPosition,
      intentAt,
    };
    hasAdoptedPlanRef.current = false;
    awaitingInitialPlayerPositionRef.current = false;
    playbackPlayingRef.current = true;
    playbackStartedRef.current = false;
    inventoryRevisionRef.current = null;
    revisionScopeRef.current.revisions.clear();
    deferredPushesRef.current = [];
    transitionSourceIdentity(null, null);
    // A remembered mid-switch seek belongs to the request this effect resets.
    pendingSeekRef.current = null;
    // So does a track pick held for that request: replaying it against this
    // request's plan would switch a track the viewer did not pick here.
    pendingTrackPicksRef.current = [];
    // The notice belongs to the previous request's plan. Leaving it set makes
    // the next mount of this hook replay a stale track toast.
    setState((current) =>
      current.trackSelectionNotice === null ? current : { ...current, trackSelectionNotice: null },
    );
    lastVerifiedIdentityRef.current = null;
    // A new request re-derives Auto intent from its own props: without this,
    // the previous request's armed/disarmed state leaks into the new session.
    // Inline (not setAutoFallback: that callback is declared below this
    // effect and block-scoped use would trip TS2448).
    autoFallbackRef.current = allowAlternateVersions && !explicitFileSelection;
    setState((current) =>
      current.autoFallback === autoFallbackRef.current
        ? current
        : { ...current, autoFallback: autoFallbackRef.current },
    );

    void loadSession({
      preferredFileId: fileId,
      position: initialPosition,
      forceStartPosition: forceInitialPosition,
      allowPreserveExistingSessionOnError: false,
      replacementErrorMessage: "Failed to replace playback request",
      initialErrorMessage: "Failed to start playback",
      fileSelection: explicitFileSelection ? "explicit" : "auto",
      forceRelink,
      intentAt,
    });
  }, [
    allowAlternateVersions,
    capabilityRequestKey,
    capabilitiesSettled,
    explicitFileSelection,
    fileId,
    forceInitialPosition,
    forceRelink,
    initialPosition,
    loadSession,
    qualityPreference,
    requestKey,
    transitionSourceIdentity,
  ]);

  // Clean up session on unmount.
  useEffect(() => {
    return () => {
      // A start can finish after unmount, before it has published a session ID.
      // Let that reply take the stale-start path and stop its own session
      // instead of adopting it into an abandoned player.
      loadSequenceRef.current += 1;
      pendingSeekRef.current = null;
      // A held pick dies with the player: nothing will fold its plan.
      pendingTrackPicksRef.current = [];
      // A viewer who leaves while the player reconnects stops the retries.
      endReconnect(false);
      const sid = sessionIdRef.current;
      if (!sid) return;
      // sendBeacon doesn't support DELETE, so the stop uses fetch keepalive.
      void stopSequencedSession(config, sid, true).catch(() => {
        // Best effort: the session expires server-side.
      });
    };
  }, [config, endReconnect]);

  /**
   * Issues one replan and adopts whatever plan comes back.
   *
   * Replans are serialized server-side behind a lease, so a second concurrent
   * request would earn a `409 replan_in_progress`; the in-flight guard here
   * means the client never asks for one.
   *
   * A `reconnect` replan is a reconnect attempt: a refusal is not adopted (the
   * caller falls back to a fresh start) and a failed request is rethrown for
   * the caller to classify instead of being surfaced as an error.
   */
  const replan = useCallback(
    async function issueReplan(
      options: ReplanOptions,
      retireSessionOnRefusal = false,
      reconnect = false,
    ): Promise<boolean> {
      const plan = planRef.current;
      // Stamp the load sequence with the plan, at the same read. A version
      // switch bumps the sequence before it adopts its replacement plan, so a
      // replan that read the sequence later would pair the outgoing plan with
      // the incoming generation and its stale discard could never fire.
      const loadSequence = loadSequenceRef.current;
      const sessionId = sessionIdRef.current;
      const playbackAttemptId = playbackAttemptIdRef.current;
      if (!plan || !sessionId || !playbackAttemptId) return false;
      // A session answered gone cannot be replanned; every retry earns the same
      // answer. The restart action starts a new session instead.
      if (sessionGoneRef.current) return false;
      // A switch is replacing the whole session and about to retire the plan
      // below. A replan built from that outgoing plan would be stamped with the
      // switch's sequence once the switch lands, so it could clobber the
      // replacement instead of being discarded. Refuse until the switch
      // settles; the switch completion path owns what happens next.
      if (switchingRef.current) {
        // A cold-start seek is a viewer intent, not a stale op against the
        // outgoing plan: remember its live target so the switch completion can
        // replay it against the replacement once it settles. Scoped to the
        // current playback generation so a failed or superseded switch never
        // replays it, and a new session never inherits it.
        if (options.operation === "seek_reanchor") {
          pendingSeekRef.current = {
            positionSeconds: options.positionSeconds ?? 0,
            playbackAttemptId: playbackAttemptIdRef.current,
          };
        }
        return false;
      }
      if (replanInFlightRef.current) {
        const isPendingFailureRecovery =
          options.operation === "failure_recovery" || options.operation === "seek_failure_recovery";
        const isPendingOutputChange = options.operation === "output_change";
        const queuedOperation = pendingReplanRef.current?.options.operation;
        const hasQueuedFailureRecovery =
          queuedOperation === "failure_recovery" || queuedOperation === "seek_failure_recovery";
        const hasQueuedOutputChange = queuedOperation === "output_change";
        if (
          options.operation === "seek_reanchor" &&
          hasQueuedOutputChange &&
          pendingReplanRef.current
        ) {
          // The output refresh will open a fresh route at its position anyway;
          // fold a later seek into that queued intent rather than silently
          // dropping the viewer's newest target.
          pendingReplanRef.current = {
            ...pendingReplanRef.current,
            options: {
              ...pendingReplanRef.current.options,
              positionSeconds: options.positionSeconds,
            },
          };
          return false;
        }
        const isPendingUserIntent =
          options.operation === "track_change" || options.operation === "quality_change";
        const hasQueuedUserIntent =
          queuedOperation === "track_change" || queuedOperation === "quality_change";
        const shouldQueue =
          isPendingFailureRecovery ||
          (isPendingUserIntent && !hasQueuedFailureRecovery) ||
          (isPendingOutputChange && !hasQueuedFailureRecovery && !hasQueuedUserIntent) ||
          (options.operation === "seek_reanchor" &&
            !hasQueuedFailureRecovery &&
            !hasQueuedOutputChange &&
            !hasQueuedUserIntent);
        if (shouldQueue) {
          pendingReplanRef.current?.resolve(false);
          return new Promise<boolean>((resolve) => {
            pendingReplanRef.current = {
              options,
              loadSequence,
              retireSessionOnRefusal,
              resolve,
              planId: plan.plan_id,
              mediaFileId: liveSourceIdentityRef.current.fileId,
            };
          });
        }
        return false;
      }

      const isFailureRecovery =
        options.operation === "failure_recovery" || options.operation === "seek_failure_recovery";
      if (isFailureRecovery && attemptCountRef.current > MAX_ATTEMPT_COUNT_V3) {
        setState((current) => ({
          ...current,
          replanning: false,
          replanningQuality: false,
          errorTitle: "Playback failed",
          error: "Playback failed after repeated recovery attempts.",
          errorReason: null,
          errorRetryable: false,
        }));
        return false;
      }

      // On an intent change nothing failed, so the previous route stays
      // eligible: the loop guard and the recovery counter reset. Only a
      // recovery accumulates them.
      const attemptedPlanKeys = isFailureRecovery
        ? [...attemptedPlanKeysRef.current, plan.plan_attempt_key].slice(
            -MAX_ATTEMPTED_PLAN_KEYS_V3,
          )
        : [];
      const attemptCount = isFailureRecovery ? attemptCountRef.current : 1;

      // A subtitle pick the server already answered with selection_unavailable
      // is terminal for that (file, identity, inventory revision): the same
      // request against the same inventory cannot succeed, and replaying it is
      // the mid-stream loop this guard exists for. A new click, an inventory
      // that advanced, or a different track produces a different key.
      const subtitlePickKey =
        options.operation === "track_change" && options.subtitle
          ? subtitlePickRejectionKey(plan, options.subtitle)
          : null;
      if (
        subtitlePickKey !== null &&
        (subtitlePickRejectionsRef.current.get(subtitlePickKey) ?? 0) >=
          MAX_SUBTITLE_PICK_REJECTIONS_V3
      ) {
        surfaceSubtitlePickRejectionRef.current();
        return false;
      }

      const body = buildReplanRequestV3({
        ...options,
        extraClientFeatures: VIDEO_CLIENT_FEATURES_V3,
        plan,
        playbackAttemptId,
        replanRequestId: randomUUID(),
        planAttemptId: planAttemptIdRef.current,
        qualityPreference: qualityRef.current,
        attemptedPlanKeys,
        attemptCount,
        // State the viewer's current Auto intent on every replan so the
        // server's session flag cannot drift from the menu: a viewer who
        // started on an explicit pick and later re-armed Auto must still
        // rotate on a dead source.
        autoFallback: autoFallbackRef.current,
        bandwidthEstimateKbps: detectBandwidthEstimateKbpsV3(),
        bandwidthCapKbps: maxBitrateKbps,
        clientCapabilities,
        clientPlaybackContext,
      });

      // Sampled now, not when the request fails: a transport that failed is
      // torn down, and the pause that forces is not the viewer's.
      const playIntent = planTransportShownRef.current
        ? playbackPlayingRef.current
        : planAutoPlayRef.current;
      replanInFlightRef.current = true;
      beginAdoption(loadSequence);
      const isQualityReplan =
        options.operation === "quality_change" || options.operation === "output_change";
      setState((current) => ({
        ...current,
        replanning: true,
        replanningQuality: isQualityReplan,
        errorTitle: null,
        error: null,
        errorReason: null,
        errorRetryable: false,
      }));

      try {
        const decision = await replanV2(config, sessionId, body);

        // A version switch or a fresh start that landed while this was in
        // flight owns the session now; this plan is already superseded.
        if (loadSequence !== loadSequenceRef.current) return false;

        if (isFailureRecovery) {
          attemptedPlanKeysRef.current = attemptedPlanKeys;
          attemptCountRef.current = Math.min(attemptCount + 1, MAX_ATTEMPT_COUNT_V3 + 1);
        } else {
          attemptedPlanKeysRef.current = [];
          attemptCountRef.current = 1;
        }

        if (reconnect && !decision.playback_plan) return false;

        // Keep a refused-start subtitle pinned off across unrelated replans.
        // A successful explicit subtitle choice is the one action that clears
        // the marker and lets the new server-selected track take ownership.
        const adopted = adoptDecision(
          decision,
          options.operation === "track_change" && options.subtitle !== undefined ? null : undefined,
          isFailureRecovery,
        );
        if (
          subtitlePickKey !== null &&
          decision.playback_plan &&
          planRejectsSubtitleSelection(decision.playback_plan)
        ) {
          // The server kept playback alive but rejected the pick. That is not a
          // successful subtitles-off change: count it against the pick, surface
          // the failure, and let the ledger stop any replay of the same
          // request against the same inventory.
          subtitlePickRejectionsRef.current.set(
            subtitlePickKey,
            (subtitlePickRejectionsRef.current.get(subtitlePickKey) ?? 0) + 1,
          );
          surfaceSubtitlePickRejectionRef.current();
        } else if (subtitlePickKey !== null && adopted) {
          subtitlePickRejectionsRef.current.delete(subtitlePickKey);
        }
        if (
          !adopted &&
          isFailureRecovery &&
          isAdaptationUnavailableReason(decision.terminal?.reason ?? decision.outcome)
        ) {
          // The transport already failed and the server can build no route for
          // it. There is nothing left to render, so retire the broken plan:
          // keeping it would leave the player spinning over a stream the server
          // refuses to serve. The refusal itself is the error card's copy, and
          // its Retry re-issues a fresh start.
          retireActiveSession(sessionId);
          return false;
        }
        if (
          !adopted &&
          options.operation === "seek_reanchor" &&
          decision.terminal?.reason?.trim() === SEEK_REANCHOR_EVIDENCE_UNAVAILABLE_REASON
        ) {
          const chain = seekRebuildChainRef.current;
          const alreadyRebuilt =
            chain !== null &&
            (chain.originSessionId === sessionId || chain.replacementSessionId === sessionId);
          if (!alreadyRebuilt) {
            // The session's frozen source cannot be reopened, but the file
            // itself is fine: a fresh start re-resolves it at the seek target.
            // Retire the session the same way a reaped one is retired and
            // reuse `loadSession` for the transport, carrying the viewer's
            // tracks, the offered candidate and their version intent so the
            // rebuild lands on what they were watching.
            const restartPosition =
              typeof options.positionSeconds === "number" &&
              Number.isFinite(options.positionSeconds)
                ? options.positionSeconds
                : playbackPositionRef.current;
            // Read the offered candidate before retiring: the retirement
            // clears the display-only offer along with the outgoing source.
            const offeredVirtualURI =
              retryTargetRef.current?.offeredVirtualURI ??
              offeredVirtualUriRef.current ??
              undefined;
            const rebuildSelection: "auto" | "explicit" = autoFallbackRef.current
              ? "auto"
              : (retryTargetRef.current?.fileSelection ??
                (explicitFileSelection ? "explicit" : "auto"));
            seekRebuildChainRef.current = {
              originSessionId: sessionId,
              replacementSessionId: null,
            };
            retireActiveSession(sessionId);
            const outcome = await loadSession({
              preferredFileId: plan.requested_media_file_id,
              position: restartPosition,
              // The seek target is the viewer's intent; do not fall back to
              // the server's stored resume point for this file.
              forceStartPosition: true,
              allowPreserveExistingSessionOnError: false,
              replacementErrorMessage: "Failed to restart playback",
              initialErrorMessage: "Failed to restart playback",
              carriedAudioTrackId: plan.selected_tracks.audio?.id ?? null,
              carriedSubtitleTrackIndex: plan.selected_tracks.subtitle?.index ?? null,
              offeredVirtualURI,
              fileSelection: rebuildSelection,
              intentAt: null,
            });
            // The replacement belongs to the same recovery chain: a second
            // identical refusal on its session surfaces the error instead of
            // starting a third. Read the adopted session only when this start
            // actually won, so a superseded or refused rebuild records nothing.
            if (outcome.kind === "adopted" && sessionIdRef.current) {
              seekRebuildChainRef.current = {
                originSessionId: sessionId,
                replacementSessionId: sessionIdRef.current,
              };
            }
            return outcome.kind === "adopted";
          }
        }
        if (!adopted && retireSessionOnRefusal) {
          const pending = pendingReplanRef.current;
          const pendingCanValidateOutput =
            pending?.loadSequence === loadSequence &&
            pending.options.operation !== "seek_reanchor" &&
            pending.options.operation !== "seek_failure_recovery";
          if (pending && pendingCanValidateOutput) {
            // The output refusal proves the predecessor route is incompatible.
            // Let a queued planner-backed intent try the latest output evidence,
            // but require it to retire the session too if it is refused. Frozen
            // seek replans cannot validate new output evidence, so they must not
            // postpone retirement.
            pendingReplanRef.current = {
              ...pending,
              retireSessionOnRefusal: true,
            };
          } else {
            retireActiveSession(sessionId);
          }
        }
        return adopted;
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) return false;
        // A reaped or ended session cannot be replanned, and every retry against
        // it answers the same way. Retire it and rebuild once from the position
        // and selections the dead plan held, so the viewer keeps watching
        // instead of being pinned to a session that no longer exists.
        // A reconnect-mode replan rethrows so the reconnect cycle — not the
        // error banner — owns the failure.
        if (reconnect) throw err;
        if (isPlaybackSessionEndedError(err)) {
          // An administrator ended the session: leave rather than show an
          // error or recover into a fresh session.
          sessionEndedRef.current = true;
          setState((current) => ({
            ...current,
            replanning: false,
            connectionStatus: "ended",
            connectionErrorTitle: null,
            connectionError: null,
          }));
          return false;
        }
        const deadSession = isDeadPlaybackSessionError(err);
        if (deadSession && recoveredDeadSessionRef.current !== sessionId) {
          recoveredDeadSessionRef.current = sessionId;
          const restartPosition = playbackPositionRef.current;
          // Clear the dead plan/session first: the replacement start must not
          // race a replan built against the session that just died, and the
          // player must stop driving its stream URL.
          retireActiveSession(sessionId);
          void loadSession({
            preferredFileId: plan.requested_media_file_id,
            position: restartPosition,
            // The viewer was mid-playback; resume where the dead session left
            // off rather than at the server's stored resume point.
            forceStartPosition: true,
            allowPreserveExistingSessionOnError: false,
            replacementErrorMessage: "Failed to restart playback",
            initialErrorMessage: "Failed to restart playback",
            carriedAudioTrackId: plan.selected_tracks.audio?.id ?? null,
            carriedSubtitleTrackIndex: plan.selected_tracks.subtitle?.index ?? null,
            offeredVirtualURI:
              retryTargetRef.current?.offeredVirtualURI ??
              offeredVirtualUriRef.current ??
              undefined,
            // Preserve the viewer's version choice; the server must not
            // silently substitute another edition behind a rebuild. Use the
            // live Auto intent first (a mid-session re-arm never re-records
            // the retry target), then the most recent start's selection.
            fileSelection: autoFallbackRef.current
              ? "auto"
              : (retryTargetRef.current?.fileSelection ??
                (explicitFileSelection ? "explicit" : "auto")),
            intentAt: null,
          });
          return false;
        }
        if (deadSession) {
          // Already rebuilt once for this session. Retire the plan it left
          // behind and fall through to surface the copy rather than starting
          // again in a loop. The session is gone, so the player offers a fresh
          // start instead of retrying the dead id.
          retireActiveSession(sessionId);
          markSessionGone();
        }
        if (retireSessionOnRefusal && !deadSession) {
          console.error("Failed to refresh playback output", err);
          return false;
        }
        if (
          isFailureRecovery &&
          playbackStartedRef.current &&
          isRetryableReconnectError(err, "replan")
        ) {
          // The stream broke and the server could not be reached to replace
          // it: the connection failed, not necessarily the route. Reconnect
          // rather than strand the viewer on an error.
          beginReconnectRef.current(options.positionSeconds, playIntent, false);
          return false;
        }
        const nextError = describePlaybackSessionError(err, "Failed to update playback", "update");
        setState((current) => ({
          ...current,
          replanning: false,
          replanningQuality: false,
          errorTitle: nextError.title,
          error: nextError.message,
          errorReason: nextError.reason ?? null,
          errorRetryable: nextError.retryable ?? false,
        }));
        return false;
      } finally {
        replanInFlightRef.current = false;
        setState((current) =>
          current.replanning || current.replanningQuality
            ? { ...current, replanning: false, replanningQuality: false }
            : current,
        );

        const pendingReplan = pendingReplanRef.current;
        pendingReplanRef.current = null;
        if (pendingReplan?.loadSequence === loadSequenceRef.current) {
          // The queued op was built against the plan its `planId` names and the
          // live source its `mediaFileId` names. When the in-flight replan has
          // replaced that plan, an op may only be replayed if it carries no
          // plan-derived state: a seek target, a quality label and an output
          // refresh are resolved against the live plan, either by this
          // re-dispatch or by the server. A track selection bakes in a
          // plan-derived ordinal, and a failure recovery bakes in the plan to
          // exclude, so both are dropped once the plan identity they name is
          // gone. A rotation moves the effective source without changing the
          // plan id, so the live file id is checked too: the plan-bound ordinal
          // no longer names the same bytes once the transport rebinds.
          const planStillCurrent =
            pendingReplan.planId === planRef.current?.plan_id &&
            pendingReplan.mediaFileId === liveSourceIdentityRef.current.fileId;
          const pendingIsPlanBound =
            pendingReplan.options.operation === "failure_recovery" ||
            pendingReplan.options.operation === "seek_failure_recovery" ||
            pendingReplan.options.audio !== undefined ||
            pendingReplan.options.subtitle !== undefined;
          if (planStillCurrent || !pendingIsPlanBound) {
            // A capability change can queue behind a replan created by an older
            // render. Dispatch through the latest callback so its request carries
            // the current output evidence rather than the closed-over snapshot.
            void issueReplanRef
              .current(pendingReplan.options, pendingReplan.retireSessionOnRefusal)
              .then(pendingReplan.resolve);
          } else {
            pendingReplan.resolve(false);
          }
        } else {
          pendingReplan?.resolve(false);
        }
        // Last: a queued replan dispatched just above has already counted
        // itself in, so waiters are not woken between the two links of a chain.
        endAdoption(loadSequence);
        // Fold any push deferred while this replan owned the session. When a
        // queued replan was just dispatched this is a no-op (the guard sees it
        // in flight) and that link's own settle path flushes instead.
        flushDeferredPushesRef.current();
        // A held track pick waited for this plan. Resolve it now; if this was
        // the one no-op refresh it was allowed, a still-unmatched pick is
        // surfaced instead of lingering.
        flushPendingTrackPicksRef.current();
      }
    },
    [
      adoptDecision,
      beginAdoption,
      clientCapabilities,
      clientPlaybackContext,
      config,
      endAdoption,
      explicitFileSelection,
      loadSession,
      markSessionGone,
      maxBitrateKbps,
      retireActiveSession,
    ],
  );
  issueReplanRef.current = replan;

  const surfaceTrackPickFailure = useCallback((pick: PendingTrackPick) => {
    const kind = pick.kind === "audio" ? "audio" : "subtitle";
    trackSelectionNoticeIdRef.current += 1;
    setState((current) => ({
      ...current,
      trackSelectionNotice: {
        id: trackSelectionNoticeIdRef.current,
        title: "Couldn't change track",
        message: `That ${kind} track isn't available in the current playback plan. Pick another track.`,
      },
    }));
  }, []);
  // The rejection path above runs before this callback exists in render order;
  // bind it once per render so both entry points raise the same notice.
  surfaceSubtitlePickRejectionRef.current = () =>
    surfaceTrackPickFailure({
      kind: "subtitle",
      index: null,
      positionSeconds: playbackPositionRef.current,
      track: undefined,
      inventoryIdentity: "",
      refreshAttempted: true,
    });

  const holdTrackPick = useCallback((pick: PendingTrackPick) => {
    // One pick per kind: a second audio click supersedes the first, while an
    // audio and a subtitle pick are separate intents.
    pendingTrackPicksRef.current = [
      ...pendingTrackPicksRef.current.filter((pending) => pending.kind !== pick.kind),
      pick,
    ];
  }, []);

  /**
   * Re-issues held track picks against the plan that is current now.
   *
   * A pick captured before a seek reanchor or a version switch cannot keep the
   * plan-bound identity resolved at click time: the replacement plan re-mints
   * ordinals. The row descriptor the viewer clicked is matched against the new
   * inventory instead. A pick that still cannot be reconciled gets one no-op
   * plan refresh (a source rotation can leave the menu ahead of the plan), and
   * then surfaces instead of disappearing.
   */
  const flushPendingTrackPicks = useCallback(() => {
    if (switchingRef.current || replanInFlightRef.current) return;
    const plan = planRef.current;
    if (!plan) return;
    const pick = pendingTrackPicksRef.current[0];
    if (!pick) return;

    const livePick = { ...pick, positionSeconds: playbackPositionRef.current } as PendingTrackPick;
    const options = trackPickOptions(plan, livePick);
    if (options) {
      pendingTrackPicksRef.current = pendingTrackPicksRef.current.slice(1);
      void issueReplanRef.current(options);
      return;
    }
    const menuIdentity = menuIdentityRef.current;
    const sourceMoved =
      menuIdentity.fileId !== plan.effective_media_file_id ||
      (menuIdentity.uri != null && menuIdentity.uri !== (plan.effective_virtual_uri ?? null));
    // A provisional inventory is the same race on a stationary source: the
    // probe has published menu rows the plan still describes as declared
    // metadata, and the enumeration it owes is what this pick needs. One
    // no-op replan re-reads it before the pick surfaces.
    const provisionalInventory =
      plan.tracks_pending === true ||
      isInventoryProvisional(plan.inventory_status, plan.inventory_provenance) ||
      stateRef.current.audioInventoryProvisional ||
      stateRef.current.subtitleInventoryProvisional;
    if (!pick.refreshAttempted && (sourceMoved || provisionalInventory)) {
      // The menu followed a rotated source (or a probe the plan has not
      // refreshed yet). One no-op replan re-reads it; a pick that still has no
      // counterpart then surfaces below instead of looping or vanishing.
      pendingTrackPicksRef.current = [
        { ...pick, refreshAttempted: true },
        ...pendingTrackPicksRef.current.slice(1),
      ];
      void issueReplanRef.current({
        operation: "track_change",
        positionSeconds: playbackPositionRef.current,
      });
      return;
    }
    pendingTrackPicksRef.current = pendingTrackPicksRef.current.slice(1);
    surfaceTrackPickFailure(pick);
  }, [surfaceTrackPickFailure]);
  flushPendingTrackPicksRef.current = flushPendingTrackPicks;

  // A replacement plan is the point at which a held pick can be resolved
  // against the inventory that won. Run after React commits the new plan so
  // the refs and rendered menu agree.
  useEffect(() => {
    flushPendingTrackPicksRef.current();
  }, [state.planRevision, state.replanning, state.replacing]);

  useEffect(() => {
    if (!capabilitiesSettled) return;
    if (
      activeRequestKeyRef.current !== requestKey ||
      activeCapabilityRequestKeyRef.current === capabilityRequestKey
    ) {
      return;
    }
    activeCapabilityRequestKeyRef.current = capabilityRequestKey;

    const plan = planRef.current;
    const sessionId = sessionIdRef.current;
    if (!plan || !sessionId) {
      const startIntent = startIntentRef.current;
      const resumeFromAdoptedPlan = hasAdoptedPlanRef.current;
      void loadSession({
        preferredFileId: fileId,
        position: resumeFromAdoptedPlan ? playbackPositionRef.current : startIntent.position,
        forceStartPosition: resumeFromAdoptedPlan ? true : startIntent.forceStartPosition,
        allowPreserveExistingSessionOnError: false,
        replacementErrorMessage: "Failed to refresh playback output",
        initialErrorMessage: "Failed to refresh playback output",
        // Before any plan was adopted, the viewer is still waiting on the
        // Play they pressed, so the replacement start keeps its clock. After
        // one, video has been shown and nobody pressed Play for this start.
        intentAt: resumeFromAdoptedPlan ? null : startIntent.intentAt,
      });
      return;
    }

    if (!serverFeaturesRef.current.includes(FEATURE_OUTPUT_CHANGE_V3)) {
      return;
    }

    void replan(
      {
        operation: "output_change",
        positionSeconds: playbackPositionRef.current,
      },
      true,
    );
  }, [
    capabilityRequestKey,
    capabilitiesSettled,
    fileId,
    loadSession,
    replan,
    requestKey,
    retireActiveSession,
  ]);

  // Selecting a track from an offered release is a source transition, not a
  // track change on the playing release. Start the row again with a fresh
  // provider link and carry the chosen track identity across; a generic
  // failure_recovery has no way to say which offered candidate or track the
  // viewer picked and may land on another candidate's default.
  const loadOfferedRelease = useCallback(
    (
      position: number,
      tracks: {
        audioIndex?: number;
        carriedAudioTrackId?: string;
        subtitleIndex?: number;
        subtitleTrackId?: string;
        subtitleBurnIn?: boolean;
      },
    ) => {
      const plan = planRef.current;
      if (!plan) return;
      void loadSession({
        preferredFileId: plan.effective_media_file_id,
        position,
        forceStartPosition: true,
        allowPreserveExistingSessionOnError: true,
        replacementErrorMessage: "Failed to switch to the offered release",
        initialErrorMessage: "Failed to switch to the offered release",
        carriedAudioTrackId: tracks.carriedAudioTrackId ?? null,
        fileSelection: "auto",
        offeredVirtualURI: offeredVirtualUriRef.current ?? undefined,
        forceRelink: true,
        intentAt: Date.now(),
        tracks,
      });
    },
    [loadSession],
  );

  const switchAudioTrack = useCallback(
    (index: number, currentPosition: number) => {
      const plan = planRef.current;
      if (!plan) return;
      if (offeredVirtualUriRef.current) {
        // The menus are showing a release the row offers now, not the one the
        // transport plays. Start the offered release and carry the picked
        // track, so the server receives both the source transition and the
        // selection instead of a generic recovery that names neither.
        const offered = stateRef.current.planAudioTracks[index];
        loadOfferedRelease(currentPosition, {
          audioIndex: index,
          carriedAudioTrackId: offered?.track_id,
        });
        return;
      }
      // The menu renders the probed inventory `applyAudioInventory` may have
      // replaced, whose ordering a probe repair can change. Carry the plan's
      // canonical identity for the picked track — matched by signature, then
      // by language family when the menu no longer shows the plan's own list —
      // so a raw position cannot name a different language.
      const pick: PendingTrackPick = {
        kind: "audio",
        index,
        positionSeconds: currentPosition,
        track: stateRef.current.planAudioTracks[index],
        refreshAttempted: false,
      };
      if (switchingRef.current || replanInFlightRef.current) {
        // A seek reanchor or version switch owns the lane. The plan will be
        // replaced before this pick can be sent, so hold the row the viewer
        // clicked and re-resolve it against the plan that wins.
        holdTrackPick(pick);
        return;
      }
      const audio = resolveAudioTrackChange(
        plan.audio_tracks ?? [],
        stateRef.current.planAudioTracks,
        index,
      );
      if (!audio) {
        // The menu is ahead of the plan (or the plan is ahead of the menu).
        // Refresh the plan once and retry; if the row still has no counterpart,
        // surface it instead of dropping the pick.
        holdTrackPick(pick);
        flushPendingTrackPicksRef.current();
        return;
      }
      const current = plan.selected_tracks.audio;
      // Compare the resolved identity, not the menu position: after a probe
      // repair the already-playing track can sit at a different displayed
      // index, and comparing ordinals would drop the viewer's real pick (or
      // replan a track they already have).
      const isCurrent = audio.id ? current?.id === audio.id : current?.index === audio.index;
      if (isCurrent) return;
      void replan({
        operation: "track_change",
        positionSeconds: currentPosition,
        audio,
      });
    },
    [holdTrackPick, loadOfferedRelease, replan],
  );

  const changeSubtitleTrack = useCallback(
    (
      combinedIndex: number | null,
      currentPosition: number,
      displayedTrack?: PlayerSubtitleInfo | null,
    ) => {
      const plan = planRef.current;
      if (!plan) return;
      // The menu can list watch-detail fallback rows before the session
      // publishes its own inventory. Capture the row the viewer clicked from
      // the menu when the session list cannot name that slot, so a pick made
      // during a refresh still carries an identity to reconcile later.
      const clickedTrack =
        combinedIndex == null
          ? undefined
          : (stateRef.current.subtitleUrls.find((item) => item.index === combinedIndex) ??
            (displayedTrack?.index === combinedIndex ? displayedTrack : undefined));
      // A pick against a still-deferred inventory names a provisional slot:
      // replan validation runs against the plan's declared list, and the
      // server remaps or degrades there. The follow-up verified inventory
      // replaces the menu without touching this selection.
      if (offeredVirtualUriRef.current) {
        // Same rule as the audio menu: start the offered release and carry the
        // picked subtitle identity. Null is an explicit subtitles-off request;
        // passing a `tracks` object with no subtitle index keeps it off instead
        // of falling back to the new plan's initial subtitle.
        const track = clickedTrack;
        loadOfferedRelease(currentPosition, {
          audioIndex: plan.selected_tracks.audio?.index,
          carriedAudioTrackId: plan.selected_tracks.audio?.id,
          subtitleIndex: combinedIndex ?? undefined,
          subtitleTrackId: track?.track_id,
          subtitleBurnIn: track?.codec ? isBitmapCodec(track.codec) : undefined,
        });
        return;
      }
      // Null is an explicit subtitles-off request. A concrete ordinal is
      // resolved against the plan first, then reconciled from the menu's stale
      // slot by identity/language.
      const pick: PendingTrackPick = {
        kind: "subtitle",
        index: combinedIndex,
        positionSeconds: currentPosition,
        track: clickedTrack,
        inventoryIdentity: subtitleInventoryIdentity(plan.subtitle.inventory),
        refreshAttempted: false,
      };
      if (switchingRef.current || replanInFlightRef.current) {
        holdTrackPick(pick);
        return;
      }
      const subtitle =
        combinedIndex == null
          ? null
          : resolveSubtitleTrackChange(
              plan,
              stateRef.current.subtitleUrls,
              combinedIndex,
              pick.track,
            );
      if (combinedIndex != null && subtitle == null) {
        holdTrackPick(pick);
        flushPendingTrackPicksRef.current();
        return;
      }
      void replan({
        operation: "track_change",
        positionSeconds: currentPosition,
        subtitle,
      });
    },
    [holdTrackPick, loadOfferedRelease, replan],
  );

  const changeQuality = useCallback(
    (label: string, currentPosition: number) => {
      const normalized = label.trim() || QUALITY_ORIGINAL_V3;
      const previousPreference = qualityRef.current;
      qualityRef.current = normalized;
      setState((current) => ({ ...current, qualityPreference: normalized }));
      void replan({ operation: "quality_change", positionSeconds: currentPosition }).then(
        (adopted) => {
          if (adopted || qualityRef.current !== normalized) return;
          qualityRef.current = previousPreference;
          setState((current) =>
            current.qualityPreference === normalized
              ? { ...current, qualityPreference: previousPreference }
              : current,
          );
        },
      );
    },
    [replan],
  );

  const recoverFromFailure = useCallback(
    (failure: FailureV3, currentPosition: number) => {
      // While the server is unreachable a transport failure says nothing about
      // the route; the reconnect replaces the transport anyway.
      if (reconnectRef.current.active) return;
      reportEvent("plan_failed", {
        failureClassification: failure.classification,
        ...(failure.message ? { diagnostics: { message: failure.message } } : {}),
      });
      void replan({ operation: "failure_recovery", positionSeconds: currentPosition, failure });
    },
    [replan, reportEvent],
  );

  // -- Mid-stream reconnect --
  //
  // A stream that already played and then lost the server has not failed its
  // route, so this deliberately avoids `failure_recovery`, which would exclude
  // the route and could push a direct-play viewer onto a transcode. Each
  // attempt first asks for the current route again with a `track_change` that
  // changes nothing (the server keeps the route eligible and answers with a
  // fresh plan at the saved position). If the session did not survive — a
  // server restart answers 404 — it starts a new session at the same position
  // with the same tracks. Unreachable or overloaded answers back off; any
  // adopted plan ends the cycle; the viewer leaving or another start cancels
  // it.

  const scheduleReconnectAttempt = useCallback((delayMs: number) => {
    const reconnect = reconnectRef.current;
    if (reconnect.timer !== null) clearTimeout(reconnect.timer);
    const generation = reconnect.generation;
    reconnect.timer = setTimeout(() => {
      reconnect.timer = null;
      void runReconnectAttemptRef.current(generation);
    }, delayMs);
  }, []);

  const giveUpReconnect = useCallback(
    (failure: PlaybackSessionErrorState) => {
      endReconnect(false);
      setState((current) => ({
        ...current,
        replacing: false,
        replanning: false,
        connectionStatus: "lost",
        connectionErrorTitle: failure.title,
        connectionError: failure.message,
      }));
    },
    [endReconnect],
  );

  const beginReconnect = useCallback(
    (positionSeconds: number, resume: boolean, freshBudget: boolean) => {
      const reconnect = reconnectRef.current;
      if (reconnect.active) return;
      reconnect.active = true;
      reconnect.generation += 1;
      if (freshBudget || Date.now() - reconnect.recoveredAt >= RECONNECT_STABLE_MS) {
        reconnect.attempts = 0;
      }
      reconnect.positionSeconds = Math.max(0, positionSeconds);
      reconnect.resume = resume;
      if (reconnect.attempts >= RECONNECT_MAX_ATTEMPTS) {
        // The stream keeps dropping right after every recovery.
        giveUpReconnect(CONNECTION_LOST_ERROR);
        return;
      }
      setState((current) => ({
        ...current,
        connectionStatus: "reconnecting",
        connectionErrorTitle: null,
        connectionError: null,
      }));
      scheduleReconnectAttempt(freshBudget ? 0 : reconnectDelayMs(reconnect.attempts));
    },
    [giveUpReconnect, scheduleReconnectAttempt],
  );
  beginReconnectRef.current = beginReconnect;

  runReconnectAttemptRef.current = async (generation: number) => {
    const reconnect = reconnectRef.current;
    const isCurrent = () => reconnect.active && reconnect.generation === generation;
    if (!isCurrent()) return;
    // One request at a time: a start or replan already talking to the server
    // for this session decides first; check again once it has settled.
    if (replanInFlightRef.current || adoptionsInFlightRef.current.has(loadSequenceRef.current)) {
      scheduleReconnectAttempt(RECONNECT_BASE_DELAY_MS);
      return;
    }
    reconnect.attempts += 1;
    const retryLater = () => {
      if (reconnect.attempts >= RECONNECT_MAX_ATTEMPTS) {
        giveUpReconnect(CONNECTION_LOST_ERROR);
        return;
      }
      scheduleReconnectAttempt(reconnectDelayMs(reconnect.attempts));
    };
    const positionSeconds = reconnect.positionSeconds;

    if (planRef.current && sessionIdRef.current) {
      try {
        const adopted = await issueReplanRef.current(
          { operation: "track_change", positionSeconds },
          false,
          true,
        );
        // An adopted plan has already ended the cycle.
        if (adopted || !isCurrent()) return;
      } catch (error) {
        if (!isCurrent()) return;
        if (isPlaybackSessionEndedError(error)) {
          sessionEndedRef.current = true;
          endReconnect(false);
          setState((current) => ({
            ...current,
            replacing: false,
            replanning: false,
            connectionStatus: "ended",
            connectionErrorTitle: null,
            connectionError: null,
          }));
          return;
        }
        if (isRetryableReconnectError(error, "replan")) {
          retryLater();
          return;
        }
        // Any other answer means the session is gone (a restart, an expiry);
        // a fresh start at the same position is the authoritative check.
      }
    }

    const plan = planRef.current;
    const outcome = await loadSessionRef.current({
      preferredFileId: plan?.effective_media_file_id ?? stateRef.current.mediaFileId ?? undefined,
      position: positionSeconds,
      forceStartPosition: true,
      allowPreserveExistingSessionOnError: true,
      replacementErrorMessage: "Failed to restart playback after the connection was lost",
      initialErrorMessage: "Failed to restart playback",
      intentAt: null,
      reconnect: true,
      // The plan's selection is the viewer's: the player replans every
      // subtitle pick, sidecar or burned in, so the server's copy stays in
      // step with what is on screen. No subtitle there means subtitles off.
      tracks: plan ? reconnectTrackSelection(plan) : undefined,
    });
    if (!isCurrent()) return;
    if (outcome.kind === "failed") {
      if (isRetryableReconnectError(outcome.error, "start")) {
        retryLater();
        return;
      }
      giveUpReconnect(describePlaybackSessionError(outcome.error, CONNECTION_LOST_ERROR.message));
      return;
    }
    if (outcome.kind === "refused") {
      giveUpReconnect(outcome.failure);
    }
  };

  const recoverConnection = useCallback(
    (positionSeconds: number, resume: boolean) => {
      // Only a stream that exists can lose its connection, and a session the
      // server ended or answered gone stays that way.
      if (!planRef.current || !sessionIdRef.current || sessionEndedRef.current) return;
      if (sessionGoneRef.current) return;
      beginReconnect(positionSeconds, resume, false);
    },
    [beginReconnect],
  );

  const retryConnection = useCallback(() => {
    const reconnect = reconnectRef.current;
    beginReconnect(reconnect.positionSeconds, reconnect.resume, true);
  }, [beginReconnect]);

  /**
   * A session-scoped call confirmed the session the reconnect is rebuilding is
   * gone. The reconnect replaces a dead session with a fresh start, so run its
   * pending attempt now rather than letting the viewer wait out the backoff.
   * Only a timer-armed cycle is nudged: an attempt already in flight is
   * already doing the work, and there is nothing to hurry.
   */
  const expediteReconnect = useCallback(() => {
    const reconnect = reconnectRef.current;
    if (reconnect.active && reconnect.timer !== null) scheduleReconnectAttempt(0);
  }, [scheduleReconnectAttempt]);

  /**
   * Enters the lost state for a session the server has already answered that
   * it no longer has, when the reconnect cannot run (nothing has played yet,
   * so there is no position or transport to rebuild). Whenever a played stream
   * exists, the reconnect is the one recovery and runs instead.
   */
  const reportSessionLost = useCallback(() => {
    // Nothing played before the session was answered gone, so no reconnect
    // cycle recorded where the viewer asked to start. Carry where playback was
    // actually set to start and the adopted plan's play intent, so Retry
    // rebuilds the session there instead of at zero or at a stale cycle's
    // position. An adopted plan owns the server-resolved source position; only
    // an explicit start (forceStartPosition) keeps the request's own position.
    // Before any plan is adopted `playbackPositionRef` reflects the last known
    // source position — the request's until a player update or a re-anchor
    // moves it — so it is not necessarily the request position.
    const reconnect = reconnectRef.current;
    const startIntent = startIntentRef.current;
    const resumeFromAdoptedPlan = hasAdoptedPlanRef.current && !startIntent.forceStartPosition;
    reconnect.positionSeconds = resumeFromAdoptedPlan
      ? playbackPositionRef.current
      : startIntent.position;
    reconnect.resume = planAutoPlayRef.current;
    giveUpReconnect(CONNECTION_LOST_ERROR);
  }, [giveUpReconnect]);

  /**
   * Starts a fresh session at the position the viewer reached after the old
   * session was answered gone. A new start re-plans server-side from scratch;
   * a replan against the dead session could only answer the same way again.
   */
  const restartPlayback = useCallback(() => {
    const plan = planRef.current;
    const played = hasAdoptedPlanRef.current || plan !== null;
    const position = played ? playbackPositionRef.current : startIntentRef.current.position;
    const forceStartPosition = played ? true : startIntentRef.current.forceStartPosition;
    const deadSessionId = sessionIdRef.current;
    sessionGoneRef.current = false;
    sessionEndedRef.current = false;
    endReconnect(false);
    if (deadSessionId) {
      // The stop is best effort: a reaped session answers 404, which is exactly
      // the verdict that brought the viewer here.
      retireActiveSession(deadSessionId);
    }
    setState((current) => ({
      ...current,
      loading: true,
      errorTitle: null,
      error: null,
      errorReason: null,
      errorRetryable: false,
      connectionStatus: "connected",
      connectionErrorTitle: null,
      connectionError: null,
    }));
    void loadSession({
      preferredFileId: plan?.requested_media_file_id ?? stateRef.current.mediaFileId ?? undefined,
      position,
      forceStartPosition,
      allowPreserveExistingSessionOnError: false,
      replacementErrorMessage: "Failed to restart playback",
      initialErrorMessage: "Failed to restart playback",
      carriedAudioTrackId: plan?.selected_tracks.audio?.id ?? null,
      carriedSubtitleTrackIndex: plan?.selected_tracks.subtitle?.index ?? null,
      offeredVirtualURI:
        retryTargetRef.current?.offeredVirtualURI ?? offeredVirtualUriRef.current ?? undefined,
      // Preserve the viewer's version choice: a restart is not a licence for
      // the server to substitute another edition behind the dead session.
      fileSelection: autoFallbackRef.current
        ? "auto"
        : (retryTargetRef.current?.fileSelection ?? (explicitFileSelection ? "explicit" : "auto")),
      intentAt: null,
    });
  }, [endReconnect, explicitFileSelection, loadSession, retireActiveSession]);

  // Coming back online is the best moment to try again.
  const reconnecting = state.connectionStatus === "reconnecting";
  useEffect(() => {
    if (!reconnecting) return;
    const onOnline = () => {
      const reconnect = reconnectRef.current;
      if (reconnect.active && reconnect.timer !== null) scheduleReconnectAttempt(0);
    };
    window.addEventListener("online", onOnline);
    return () => window.removeEventListener("online", onOnline);
  }, [reconnecting, scheduleReconnectAttempt]);

  /**
   * Replans off a plan the server invalidated mid-playback.
   *
   * For a route failure this is an ordinary `failure_recovery`, deliberately:
   * that operation is what folds the current plan's attempt key into
   * `attempted_plan_keys`, so the route the server just disqualified is
   * excluded from the replacement plan without the client reasoning about
   * deliveries at all. The plan revision the adopted plan bumps rebuilds the
   * transport and restores the position, exactly as it does after a
   * client-detected failure.
   *
   * Cold-start default-audio reconciliation is the exception: nothing failed and
   * the route is healthy, the committed recipe simply plays the wrong audio
   * stream because a late probe reordered the inventory. Declaring that a
   * failure would exclude the very route that is working and push the session
   * onto a worse one, so it replans as a `track_change` — the operation that
   * already models an audio correction — with no failure payload and no route
   * exclusion. The server fills the corrected audio identity into that replan
   * from the decision it persisted (§6.1.1 of the v3 protocol), so the client
   * only has to carry its live position. Telemetry is unchanged either way:
   * `fallback_reason` reports the server's own reason verbatim, which already
   * tells the two cases apart.
   *
   * That replan also echoes the reason back as `answers_plan_invalidation`, so
   * the server can match it against the decision it is holding rather than
   * infer the pairing from the operation and the missing failure. It is the only
   * replan that does so: the field asserts "this is the answer to that
   * withdrawal", and every other replan — including the failure recovery below —
   * has no withdrawal to answer.
   *
   * A start or replan already in flight is waited out first. The server commits
   * a replacement plan and starts the copy-safety scan behind it *before* the
   * response reaches the client, so an invalidation can name a plan this client
   * has not adopted yet. Deciding against the plan currently on screen would
   * complete the command as a no-op and then let the pending response install
   * the very route the server just withdrew.
   */
  const invalidatePlan = useCallback(
    async (planId: string, reason: string, currentPosition: number): Promise<boolean> => {
      const settling = awaitAdoptionSettled();
      if (settling) await settling;
      const plan = planRef.current;
      if (!plan) return false;
      // The command names the plan the server invalidated. Once the client has
      // moved past it there is nothing to recover from, and replanning anyway
      // would evict a route the server never complained about. That stays true
      // for a plan id this client has never seen: it is a verdict for a route
      // that has already been replaced, not a reason to tear the session down.
      if (plan.plan_id !== planId) return true;
      const classification = reason.trim().slice(0, 64) || "plan_invalidated";
      reportEvent("plan_invalidated", { fallbackReason: classification });
      // Compare the trimmed reason against the server's own string, not the
      // truncated classification: a classification can only be a prefix of the
      // reconciliation reason if the server sent something longer, which is a
      // different reason and must keep failure semantics.
      if (reason.trim() === PLAN_INVALIDATED_DEFAULT_AUDIO_RECONCILIATION) {
        // The reason goes back on the wire verbatim, not trimmed to the
        // classification: it is how the server tells that this replan *is* the
        // answer to the decision it persisted, instead of falling back to a
        // weaker heuristic on the operation and the absence of a failure.
        return replan({
          operation: "track_change",
          positionSeconds: currentPosition,
          answersPlanInvalidation: reason.trim(),
        });
      }
      return replan({
        operation: "failure_recovery",
        positionSeconds: currentPosition,
        failure: { classification, message: "The server invalidated this plan." },
      });
    },
    [awaitAdoptionSettled, replan, reportEvent],
  );

  const reanchorSeek = useCallback(
    (positionSeconds: number): Promise<boolean> => {
      playbackPositionRef.current = positionSeconds;
      awaitingInitialPlayerPositionRef.current = false;
      reportEvent("seek_reanchor_requested");
      return replan({ operation: "seek_reanchor", positionSeconds });
    },
    [replan, reportEvent],
  );
  useEffect(() => {
    reanchorSeekRef.current = reanchorSeek;
  }, [reanchorSeek]);

  /**
   * Re-reads the subtitle inventory.
   *
   * There is no "refresh" operation in v3, and there does not need to be: a
   * `track_change` that changes nothing returns a fresh plan — inventory
   * included — without excluding the route already playing.
   */
  const refreshSubtitles = useCallback(
    async (currentPosition: number): Promise<boolean> => {
      if (!planRef.current) return false;
      return replan({ operation: "track_change", positionSeconds: currentPosition });
    },
    [replan],
  );

  /**
   * Folds a track the server pushed over the realtime socket into the plan's
   * inventory at the ordinal the server assigned it. The ordinal is never
   * derived client-side, which is why the payload carries the whole entry.
   */
  const applySubtitleTrack = useCallback(
    (track: SubtitleInventoryItemV3) => {
      const plan = planRef.current;
      if (!plan) return;
      const inventory = [
        ...plan.subtitle.inventory.filter((item) => item.combined_index !== track.combined_index),
        track,
      ].sort((a, b) => a.combined_index - b.combined_index);
      const nextPlan: PlanV3 = { ...plan, subtitle: { ...plan.subtitle, inventory } };
      planRef.current = nextPlan;
      setState((current) => ({
        ...current,
        plan: nextPlan,
        subtitleUrls: mapSubtitleInventory(inventory, nextPlan.effective_media_file_id, config),
      }));
    },
    [config],
  );

  /**
   * Fills in a richer probed audio inventory discovered after the plan landed.
   *
   * A catalog list is probe evidence, so it replaces a declared (provisional)
   * inventory even when it is shorter: the declared list may be synthesized or
   * stale, and only positive probe evidence resolves the provisional marker.
   * For a same-file refresh against an already-verified inventory, only a strict
   * superset is accepted — once the plan carries a full inventory it stays
   * authoritative, so a poorer catalog row never overwrites it. When `fileId`
   * names another file (a poll that resolved the effective virtual candidate
   * while the plan names the collapsed row) the inventory belongs to a different
   * target, so it replaces the menu even when it is not larger, and the live
   * identity is re-keyed to that file and resolved virtual URI so the version
   * menu follows the same source the inventory does. The plan object and its
   * revisions are untouched, so menus re-render while the transport keeps
   * playing.
   */
  const applyAudioInventory = useCallback(
    (tracks: PlayerAudioTrack[], fileId?: number | null, effectiveVirtualUri?: string | null) => {
      // Resolve the transition against the synchronous menu mirror, not the
      // rendered state: a poll that lands in the same tick as another push must
      // see the identity that push already committed, or it would misclassify a
      // same-file source as a cross-file rotation.
      const identity = menuIdentityRef.current;
      const sameFile = fileId == null || fileId === identity.fileId;
      const nextFileId = sameFile ? identity.fileId : (fileId ?? identity.fileId);
      // Keep the current URI when the caller names no candidate: an ordinary
      // version switch moves by id alone and must not clear a rotation's
      // effective candidate under it. A virtual poll always names the resolved
      // path, so a moved candidate re-keys here.
      const nextUri =
        effectiveVirtualUri === undefined ? identity.uri : (effectiveVirtualUri ?? null);
      const identityChanged = nextFileId !== identity.fileId || nextUri !== identity.uri;
      // A candidate URI that moved under the same file id is an authoritative
      // identity update even when the catalog list is the same length or empty:
      // the live source changed, so the menu must follow it. Commit the
      // transition synchronously and outside the updater; the revision scope is
      // generation-keyed and is deliberately not cleared here.
      if (identityChanged) {
        transitionSourceIdentity(nextFileId, nextUri);
        // A catalog poll is external state. Record the candidate it moved to,
        // with the transition clock, so a deferred source commit queued before
        // this fold cannot overwrite it even if a later plan adoption replaces
        // the menu mirror before the flush runs.
        recordPollApplied({ fileId: nextFileId, uri: nextUri });
      }
      setState((current) => {
        const plan = planRef.current;
        const planSelection = plan?.selected_tracks.audio ?? null;
        const planTracks = plan?.audio_tracks ?? [];
        if (identityChanged) {
          return {
            ...current,
            mediaFileId: nextFileId,
            effectiveVirtualUri: nextUri,
            planAudioTracks: tracks.map((track) => ({ ...track })),
            // The applied rows are the menu (and the audio menu's marker), not
            // the plan's ordinals. Map the committed selection onto them by
            // identity so a re-ordered probe list cannot light another language.
            audioTrackIndex: resolveAppliedAudioIndex(planTracks, tracks, planSelection),
            // An empty list on a moved identity carries no probe evidence, so
            // the menu stays marked rather than claiming a verified empty
            // inventory.
            audioInventoryProvisional: tracks.length === 0,
            // Positive probe evidence for the new source resolves the audio
            // side of a failure marker the previous source's timed-out
            // discovery left behind, but the marker survives while subtitles
            // are still provisional so the subtitle menu keeps its terminal
            // notice instead of restarting its spinner.
            inventoryFailed:
              tracks.length === 0 || current.subtitleInventoryProvisional
                ? current.inventoryFailed
                : false,
          };
        }
        // Same identity: only a strictly richer list (or the first probe
        // evidence) is allowed to replace the current menu, computed against
        // the queued state so batched folds compose.
        if (tracks.length === 0) return current;
        if (!current.audioInventoryProvisional && tracks.length <= current.planAudioTracks.length) {
          return current;
        }
        return {
          ...current,
          planAudioTracks: tracks.map((track) => ({ ...track })),
          audioTrackIndex: resolveAppliedAudioIndex(planTracks, tracks, planSelection),
          // Catalog tracks are probe-persisted, so a non-empty fold is the
          // probed evidence the provisional marker was waiting for. It only
          // ends a discovery-failure marker for the audio side: subtitles have
          // their own provisional evidence state, and a failure must survive an
          // audio-only recovery so the subtitle menu keeps its terminal notice
          // instead of falling back to an indefinite spinner. The audio menu
          // already ignores the shared flag once its own inventory is
          // confident, so gating only here needs no per-menu state.
          audioInventoryProvisional: false,
          inventoryFailed: current.subtitleInventoryProvisional ? current.inventoryFailed : false,
        };
      });
    },
    [recordPollApplied, transitionSourceIdentity],
  );

  // The source identity (file + URI) last folded with a verified inventory.
  // Guards the same-source declared-after-verified downgrade against interleaved
  // pushes in one tick: the provisional flag on rendered state lags, so the
  // synchronous identity is the authority for "this source is verified".
  const lastVerifiedIdentityRef = useRef<SourceIdentity | null>(null);

  const foldCommittedSource = useCallback(
    (
      source: {
        effectiveMediaFileId?: number | null;
        effectiveVirtualUri?: string | null;
        inventoryStatus?: string | null;
      },
      audioTracks: PlayerAudioTrack[],
    ) => {
      const identity = menuIdentityRef.current;
      // Resolve the next identity against the synchronous mirror; a deferred
      // source that folded earlier in the same tick has already moved it.
      const nextFileId = source.effectiveMediaFileId ?? identity.fileId;
      const nextUri = source.effectiveVirtualUri ?? identity.uri;
      const identityChanged = nextFileId !== identity.fileId || nextUri !== identity.uri;
      if (identityChanged) transitionSourceIdentity(nextFileId, nextUri);
      // Snapshot the verified identity *before* this fold stamps a new one, so
      // the downgrade guard below answers "was the source we are about to
      // replace already verified?" A verified push always wins; it is the
      // declared-after-verified case that must be suppressed.
      const verifiedAnchor = lastVerifiedIdentityRef.current;
      if (source.inventoryStatus === "verified") {
        lastVerifiedIdentityRef.current = { fileId: nextFileId, uri: nextUri };
      }
      setState((current) => {
        // Only positive probe evidence clears the marker. A declared push for
        // the same source after a verified one leaves the verified list and
        // its confident marker in place; re-marking it would flap the menus
        // disabled/enabled on every interleaved declared delivery.
        const verified = source.inventoryStatus === "verified";
        // A verified menu belongs to the candidate, not to the catalog row the
        // plan happened to name. An owner row materializing, or a duplicate row
        // being adopted, moves the file id but not the source; a declared push
        // under that row must not re-mark the verified menu as provisional.
        const sameVerifiedSource =
          verifiedAnchor !== null &&
          (verifiedAnchor.uri != null || nextUri != null
            ? verifiedAnchor.uri === nextUri
            : !identityChanged && verifiedAnchor.fileId === nextFileId);
        const declaredDowngrade = !verified && source.inventoryStatus != null && sameVerifiedSource;
        if (declaredDowngrade) {
          // Keep the verified list and its confident marker, but adopt the
          // row id the delivery names so later subtitle URLs and pushes key on
          // the source the session actually serves.
          if (!identityChanged) return current;
          return { ...current, mediaFileId: nextFileId, effectiveVirtualUri: nextUri };
        }
        const provisional =
          source.inventoryStatus == null ? current.audioInventoryProvisional : !verified;
        const richer = audioTracks.length > current.planAudioTracks.length;
        // A verified push is the probe landing: it replaces a declared list even
        // when shorter. A same-source push that is no richer and adds no
        // evidence leaves the menu alone.
        const replaceInventory =
          identityChanged || richer || (verified && current.audioInventoryProvisional);
        if (!identityChanged && !replaceInventory) return current;
        const plan = planRef.current;
        return {
          ...current,
          mediaFileId: nextFileId,
          effectiveVirtualUri: nextUri,
          // A moved effective source replaces the menu wholesale, including
          // with an empty list, so the previous release's tracks are never
          // shown under the new one.
          planAudioTracks: replaceInventory
            ? audioTracks.map((track) => ({ ...track }))
            : current.planAudioTracks,
          audioTrackIndex: replaceInventory
            ? resolveAppliedAudioIndex(
                plan?.audio_tracks ?? [],
                audioTracks,
                plan?.selected_tracks.audio ?? null,
              )
            : current.audioTrackIndex,
          audioInventoryProvisional: provisional,
        };
      });
    },
    [transitionSourceIdentity],
  );

  /**
   * Captures a push that arrived while a start/switch/replan owned the session.
   *
   * The entry records the adoption it belongs to — generation, session, the
   * arrival plan, and the *live* outgoing identity — so the flush can tell
   * which adoption it belongs to without reading the outgoing rendered state.
   * The live identity is captured separately because a rotation or poll can
   * move the source after the arrival plan landed, and the plan alone would
   * then name the wrong outgoing candidate.
   */
  const captureDeferredPush = useCallback((input: DeferredPushInput) => {
    const live = liveSourceIdentityRef.current;
    const lastDeferredSource = lastDeferredSourceIdentityRef.current;
    const base: DeferredPushBase = {
      seq: deferredPushSeqRef.current++,
      arrivalTransitionClock: identityTransitionClockRef.current,
      hasIdentity: identityNamesSource(input.identity),
      identity: input.identity,
      plan: planRef.current,
      outgoing: { fileId: live.fileId, uri: live.uri },
      outgoingFromDeferredSource:
        lastDeferredSource != null &&
        lastDeferredSource.fileId === live.fileId &&
        lastDeferredSource.uri === live.uri,
      generation: loadSequenceRef.current,
      sessionId: sessionIdRef.current,
    };
    deferredPushesRef.current.push(
      input.kind === "source"
        ? {
            ...base,
            kind: "source",
            source: input.source,
            audioTracks: input.audioTracks,
          }
        : { ...base, kind: "inventory", payload: input.payload },
    );
    // A deferred source commit still moves where the stream actually is. Record
    // it so a queued plan-bound replan built against the outgoing source sees
    // the rotation and is dropped, even though the rendered state has not moved.
    if (input.kind === "source" && identityNamesSource(input.identity)) {
      liveSourceIdentityRef.current = {
        fileId: input.identity.fileId ?? live.fileId,
        uri: input.identity.uri ?? live.uri,
      };
      lastDeferredSourceIdentityRef.current = liveSourceIdentityRef.current;
    }
  }, []);

  const applyCommittedSource = useCallback(
    (
      source: {
        effectiveMediaFileId?: number | null;
        effectiveVirtualUri?: string | null;
        inventoryStatus?: string | null;
      },
      audioTracks: PlayerAudioTrack[],
      identity?: SourceIdentity,
    ) => {
      // A start/replan/switch is rebuilding the session and its plan is the
      // authority for identity and inventory. A rotation on the outgoing
      // transport must not mutate the menus under the pending adoption, nor
      // move the live identity the chained-switch completion compares against.
      // Defer the push instead of discarding it: for a fast pending/declared
      // start the replacement plan may not name the candidate this push does
      // (the v2 wire omits `effective_virtual_uri`), so this is the only
      // carrier of the incoming source identity. When it actually moved the
      // source, the queued chained-switch position was captured against the
      // outgoing timeline, so discard that position and let the chained switch
      // seek from the live playhead.
      const currentState = stateRef.current;
      const liveIdentity = menuIdentityRef.current;
      if (switchingRef.current || replanInFlightRef.current || currentState.replacing) {
        const sameCandidate = identityNamesSameCandidate(
          {
            fileId: source.effectiveMediaFileId ?? null,
            uri: source.effectiveVirtualUri ?? null,
          },
          liveIdentity,
        );
        const movedSource =
          !sameCandidate &&
          ((source.effectiveMediaFileId != null &&
            source.effectiveMediaFileId !== liveIdentity.fileId) ||
            (source.effectiveVirtualUri != null &&
              source.effectiveVirtualUri !== liveIdentity.uri));
        if (movedSource) pendingSwitchPositionRef.current = null;
        captureDeferredPush({
          kind: "source",
          source,
          audioTracks,
          identity: identity ?? {
            fileId: source.effectiveMediaFileId ?? null,
            uri: source.effectiveVirtualUri ?? null,
          },
        });
        return;
      }
      foldCommittedSource(source, audioTracks);
    },
    [captureDeferredPush, foldCommittedSource],
  );

  /**
   * Replaces the plan's subtitle inventory with a server-pushed revision.
   *
   * Unlike {@link applySubtitleTrack}, which folds one ordinal into whatever
   * the plan already carried, this adopts the whole authoritative list the
   * live-inventory push carries, so a revision that drops, reorders, or empties
   * the tracks renders exactly what the server published. Only positive probe
   * evidence (`inventory_status: "verified"`) clears the provisional marker; a
   * declared push after a verified one is a stale same-source delivery and is
   * ignored instead of re-marking the menu. The plan object and its revisions
   * are untouched, so the transport keeps playing.
   */
  const applySubtitleInventory = useCallback(
    (
      inventory: SubtitleInventoryItemV3[],
      inventoryStatus?: string | null,
      fileId?: number | null,
      overrideVirtualUri?: string | null,
    ) => {
      const plan = planRef.current;
      if (!plan) return;
      const effectiveFileId = fileId ?? plan.effective_media_file_id;
      // The URI must travel with the fold, not be re-read from rendered state:
      // a rotation folded earlier in the same tick has moved the synchronous
      // mirror but not yet the rendered state, so stateRef would stamp the
      // verified anchor with the previous source's URI.
      const effectiveVirtualUri =
        overrideVirtualUri ?? stateRef.current.effectiveVirtualUri ?? plan.effective_virtual_uri;
      const verified = inventoryStatus === "verified";
      const sameFile = effectiveFileId === plan.effective_media_file_id;
      // A declared push after a verified one leaves the verified list and its
      // confident marker in place; re-marking it would flap the subtitle menu
      // disabled/enabled on every interleaved declared delivery. Same-source
      // means same identity (file + URI) as the last verified evidence, not
      // the UI provisional flag, which lags a same-tick pair. Snapshot before
      // this fold stamps a new verified identity (a verified push must win).
      const verifiedAnchor = lastVerifiedIdentityRef.current;
      const sameVerifiedSource =
        verifiedAnchor !== null &&
        (verifiedAnchor.uri != null || effectiveVirtualUri != null
          ? verifiedAnchor.uri === effectiveVirtualUri
          : verifiedAnchor.fileId === effectiveFileId);
      const declaredDowngrade =
        sameVerifiedSource && inventoryStatus != null && inventoryStatus !== "verified";
      if (declaredDowngrade) return;
      if (verified) {
        lastVerifiedIdentityRef.current = {
          fileId: effectiveFileId,
          uri: effectiveVirtualUri ?? null,
        };
      }
      const provisional =
        inventoryStatus == null ? stateRef.current.subtitleInventoryProvisional : !verified;
      // A verified push is the probe landing: it replaces a declared list even
      // when shorter. A same-source declared push only replaces when it is
      // richer or the menu is still declared.
      const replace =
        !sameFile ||
        verified ||
        inventory.length > plan.subtitle.inventory.length ||
        stateRef.current.subtitleInventoryProvisional;
      const nextPlan: PlanV3 = {
        ...plan,
        effective_media_file_id: effectiveFileId,
        effective_virtual_uri: effectiveVirtualUri,
        subtitle: { ...plan.subtitle, inventory },
      };
      if (replace) planRef.current = nextPlan;
      setState((current) => {
        if (!replace) {
          return provisional === current.subtitleInventoryProvisional
            ? current
            : { ...current, subtitleInventoryProvisional: provisional };
        }
        return {
          ...current,
          plan: nextPlan,
          subtitleUrls: mapSubtitleInventory(inventory, effectiveFileId, config),
          subtitleInventoryProvisional: provisional,
        };
      });
    },
    [config],
  );

  /**
   * Folds a display-only offered-release revision into the menus.
   *
   * When the session's pinned candidate vanishes from the provider listing but
   * the transport keeps playing it, the server offers the row's current
   * listing here. The payload carries only the bound row in
   * `effective_media_file_id` and leaves `effective_virtual_uri` empty, so
   * nothing re-keys the transport, while `audio_tracks` and
   * `subtitle_inventory` describe the release the row offers now. The fold
   * replaces the rendered track lists and subtitle URLs, records the offered
   * candidate for the menu selection guard, and records the revision. It
   * deliberately leaves `plan.effective_media_file_id`,
   * `plan.effective_virtual_uri`, `mediaFileId`, `transportRevision` and the
   * session id untouched, and never calls replan or reloads the element.
   */
  const foldOfferedInventory = useCallback(
    (payload: PlaybackInventoryUpdatedPayload, offeredUri: string) => {
      const scope = syncRevisionScope();
      if (payload.inventory_revision != null && scope.revisions.has(payload.inventory_revision)) {
        return;
      }
      const plan = planRef.current;
      if (!plan) return;
      const audioTracks = (payload.audio_tracks ?? []).map((track) => ({ ...track }));
      const subtitleInventory = payload.subtitle_inventory ?? [];
      const nextPlan: PlanV3 = {
        ...plan,
        // The playing identity is deliberately preserved: this is menu data for
        // another release, never for the served one.
        subtitle: { ...plan.subtitle, inventory: subtitleInventory },
      };
      planRef.current = nextPlan;
      offeredVirtualUriRef.current = offeredUri;
      setState((current) => ({
        ...current,
        plan: nextPlan,
        planAudioTracks: audioTracks,
        // The offered lists are the probe's own evidence for the row's current
        // listing, so the menus are no longer "loading".
        audioInventoryProvisional: false,
        subtitleInventoryProvisional: false,
        subtitleUrls: mapSubtitleInventory(subtitleInventory, plan.effective_media_file_id, config),
      }));
      if (payload.inventory_revision != null) {
        inventoryRevisionRef.current = payload.inventory_revision;
        scope.revisions.add(payload.inventory_revision);
      }
    },
    [config, syncRevisionScope],
  );

  /**
   * The shared inventory fold used by both a direct push and the deferred
   * flush.
   *
   * Callers have already cleared the switch/replan/source guards, so this only
   * performs the revision gate — a duplicate or out-of-order revision names one
   * already folded in and is a documented no-op — then folds the audio list
   * (with the identity it carries) and the complete subtitle inventory without
   * bumping the plan or transport revisions.
   */
  const foldInventoryUpdate = useCallback(
    (payload: PlaybackInventoryUpdatedPayload): boolean => {
      // A display-only offered release is a different animal: it carries
      // another release's tracks under the session's row id, so it must never
      // go through the identity-carrying fold. Route it to the menu-only fold
      // and return.
      const offeredUri = payload.offered_virtual_uri?.trim() ?? "";
      if (offeredUri) {
        foldOfferedInventory(payload, offeredUri);
        return true;
      }
      // A verified revision for the source the transport actually plays
      // retires a display-only offer. Otherwise the offered candidate would
      // keep intercepting ordinary picks after the live inventory recovered.
      // The retirement and the menu restore are one operation: a duplicate of a
      // revision already folded while the offer was showing must still fold the
      // live tracks back, not just clear the ref and return at the revision gate.
      const livePlan = planRef.current;
      let retiresOfferedSource = false;
      if (offeredVirtualUriRef.current && livePlan && payload.inventory_status === "verified") {
        const payloadUri = payload.effective_virtual_uri?.trim() ?? "";
        const liveUri = livePlan.effective_virtual_uri?.trim() ?? "";
        retiresOfferedSource =
          payloadUri !== ""
            ? payloadUri === liveUri
            : payload.effective_media_file_id != null &&
              payload.effective_media_file_id === livePlan.effective_media_file_id;
        if (retiresOfferedSource) offeredVirtualUriRef.current = null;
      }
      const scope = syncRevisionScope();
      const alreadyFolded =
        payload.inventory_revision != null && scope.revisions.has(payload.inventory_revision);
      const isTerminal =
        payload.inventory_status === "verified" || payload.inventory_status === "failed";
      if (alreadyFolded && !retiresOfferedSource) {
        // If the state is still pending or provisional, a redelivery of the terminal
        // revision must re-fold its verified tracks and clear pending, not early-return.
        const needsTerminalReconciliation =
          isTerminal &&
          (stateRef.current.inventoryPending ||
            (payload.inventory_status === "verified" &&
              (stateRef.current.audioInventoryProvisional ||
                stateRef.current.subtitleInventoryProvisional)));
        if (!needsTerminalReconciliation) {
          return isTerminal;
        }
      }
      const hasIdentity =
        payload.effective_media_file_id != null || payload.effective_virtual_uri != null;
      if (payload.audio_tracks !== undefined || hasIdentity) {
        foldCommittedSource(
          {
            effectiveMediaFileId: payload.effective_media_file_id ?? null,
            effectiveVirtualUri: payload.effective_virtual_uri ?? null,
            inventoryStatus: payload.inventory_status ?? null,
          },
          payload.audio_tracks ?? [],
        );
      }
      if (payload.subtitle_inventory !== undefined) {
        // Carry the payload's identity into the subtitle fold: after an A→B
        // rotation earlier in the same tick, the rendered state still names A,
        // and only the payload names the source B's inventory describes.
        applySubtitleInventory(
          payload.subtitle_inventory,
          payload.inventory_status ?? null,
          payload.effective_media_file_id ?? null,
          payload.effective_virtual_uri ?? null,
        );
      }
      if (payload.inventory_revision != null) {
        inventoryRevisionRef.current = payload.inventory_revision;
        scope.revisions.add(payload.inventory_revision);
      }
      // A terminal outcome (verified or failed) ends the deferred loading
      // state. The status values come from the session outcome the server
      // stored with the probe result; anything else leaves the pending flag
      // for the poll to resolve.
      if (payload.inventory_status === "verified") {
        inventoryPendingRef.current = false;
        const revision = payload.inventory_revision ?? null;
        inventoryRevisionRef.current = revision;
        setState((current) => {
          const verifiedFileId = payload.effective_media_file_id ?? menuIdentityRef.current.fileId;
          const verifiedUri = payload.effective_virtual_uri ?? menuIdentityRef.current.uri;
          if (verifiedFileId != null) {
            lastVerifiedIdentityRef.current = { fileId: verifiedFileId, uri: verifiedUri };
          }
          return {
            ...current,
            inventoryPending: false,
            inventoryFailed: false,
            inventoryRevision: revision,
          };
        });
      } else if (payload.inventory_status === "failed") {
        inventoryPendingRef.current = false;
        const revision = payload.inventory_revision ?? null;
        inventoryRevisionRef.current = revision;
        setState((current) => ({
          ...current,
          inventoryPending: false,
          inventoryFailed: true,
          inventoryRevision: revision,
        }));
      } else if (payload.inventory_revision != null) {
        const revision = payload.inventory_revision;
        setState((current) =>
          current.inventoryRevision === revision
            ? current
            : { ...current, inventoryRevision: revision },
        );
      }
      return true;
    },
    [applySubtitleInventory, foldCommittedSource, foldOfferedInventory, syncRevisionScope],
  );

  /**
   * Folds a live inventory revision pushed over the realtime socket into the
   * session's menus.
   *
   * A `source_committed` push re-keys the version and audio menus; this is the
   * same fold with probe evidence attached: identity and audio, then the
   * complete subtitle inventory. It shares the replacement-start guard with
   * {@link applyCommittedSource} — a pending switch owns the menus — and neither
   * bumps the plan or transport revision.
   */
  const applyInventoryUpdate = useCallback(
    (payload: PlaybackInventoryUpdatedPayload) => {
      const identity: SourceIdentity = {
        fileId: payload.effective_media_file_id ?? null,
        uri: payload.effective_virtual_uri ?? null,
      };
      const current = stateRef.current;
      const liveIdentity = menuIdentityRef.current;
      // A row-only move of the same concrete candidate is not a source move:
      // the probe can commit its evidence to the release's owner row while the
      // client still holds the row its plan named. Treating that as a rotation
      // would discard the delivered revision and leave the open session
      // discovering until it is reopened.
      const sameCandidate = identityNamesSameCandidate(identity, liveIdentity);
      const movedSource =
        !sameCandidate &&
        ((payload.effective_media_file_id != null &&
          payload.effective_media_file_id !== liveIdentity.fileId) ||
          (payload.effective_virtual_uri != null &&
            payload.effective_virtual_uri !== liveIdentity.uri));
      // The same adoption barrier as applyCommittedSource: a switch, an
      // in-flight replan, or a replacement start owns the session, so hold the
      // push and replay it against the plan that wins. Both event kinds must
      // observe one barrier, otherwise an inventory deferred by a replan can
      // flush before a newer source commit that folded immediately.
      if (switchingRef.current || replanInFlightRef.current || current.replacing) {
        if (movedSource) pendingSwitchPositionRef.current = null;
        captureDeferredPush({ kind: "inventory", payload, identity });
        return false;
      }
      // A display-only offered-release revision carries another release's
      // tracks under the session's bound row id and deliberately names no
      // candidate. Admit it when it names the same row on screen and genuinely
      // offers a candidate other than the one playing; the fold is display-only
      // and never moves identity. A packet whose row has moved, or that names
      // the playing candidate, is stale and dropped.
      const offeredUri = payload.offered_virtual_uri?.trim() ?? "";
      if (offeredUri) {
        const sameRow =
          payload.effective_media_file_id == null ||
          liveIdentity.fileId == null ||
          payload.effective_media_file_id === liveIdentity.fileId;
        if (sameRow && offeredUri !== liveIdentity.uri) {
          return foldInventoryUpdate(payload);
        }
        return false;
      }
      // No adoption in flight: a push that names another source than the player
      // is on is a stale delivery and is dropped. The synchronous mirror, not
      // the rendered state, is the live identity a rotation already committed.
      // The exception is a row-only move of the same concrete candidate: the
      // candidate URI is the release identity, so the revision still describes
      // the source on screen and is adopted (and its row becomes the live one)
      // rather than discarded.
      if (!sameCandidate) {
        if (
          (payload.effective_media_file_id != null &&
            liveIdentity.fileId != null &&
            payload.effective_media_file_id !== liveIdentity.fileId) ||
          (payload.effective_virtual_uri != null &&
            liveIdentity.uri != null &&
            payload.effective_virtual_uri !== liveIdentity.uri)
        ) {
          return false;
        }
      }
      // A file-only push cannot be tied to a candidate. When the live source
      // already names a concrete candidate on that same file, the file alone is
      // not proof of ownership and must not re-key the menus under that
      // candidate. This mirrors `deferredIdentityIsAdmissible` so a refused
      // file-only revision cannot slip back in through the direct path.
      if (
        payload.effective_media_file_id != null &&
        payload.effective_virtual_uri == null &&
        liveIdentity.uri != null &&
        payload.effective_media_file_id === liveIdentity.fileId
      ) {
        return false;
      }
      return foldInventoryUpdate(payload);
    },
    [captureDeferredPush, foldInventoryUpdate],
  );

  /**
   * Applies identity-carrying pushes that were deferred while a
   * start/switch/replan owned the session.
   *
   * Called from each operation's settle path and from an interval fallback, so
   * a push dropped during a fast pending/declared start is folded in the moment
   * the replacement plan lands rather than being lost. The queue is replayed in
   * arrival order. The settled plan is the adoption that won: a push captured
   * under another adoption generation or session is dropped, and a push whose
   * identity the plan cannot vouch for is dropped. A refused inventory revision
   * is left unrecorded, so a redelivery that the plan (or a later rotation) does
   * vouch for still folds; a genuinely applied revision is recorded by the fold
   * itself, so duplicate deliveries remain no-ops.
   */
  const flushDeferredPushes = useCallback(() => {
    // A start/replan owns the session while its adoption is in flight, and a
    // version switch sets `switchingRef` before it retires the outgoing plan.
    // The rendered `replacing` flag is stale at a settle path (it is set through
    // state), so the in-flight adoption count is the reliable signal here.
    const adoptionInFlight = adoptionsInFlightRef.current.has(loadSequenceRef.current);
    if (switchingRef.current || replanInFlightRef.current || adoptionInFlight) {
      return;
    }
    const plan = planRef.current;
    if (!plan) {
      deferredPushesRef.current = [];
      return;
    }
    const queue = deferredPushesRef.current.slice().sort((a, b) => a.seq - b.seq);
    if (queue.length === 0) return;
    deferredPushesRef.current = [];
    // The identity the menus carry when the flush starts. It captures movements
    // an outside push made while these entries waited (a poll folds without the
    // adoption barrier); it is snapshotted once so the entries replayed below do
    // not become a veto baseline for each other, which would invert arrival
    // order when two concrete candidates are queued.
    const appliedIdentity = menuIdentityRef.current;
    const handleRefusedPush = (entry: DeferredPush) => {
      // A deferred source commit had moved the live baseline when it was
      // captured. If nothing later moved it on, put it back on the winning plan
      // so a source the flush just refused is not left recorded as live.
      //
      // A refused inventory revision is deliberately *not* recorded as folded:
      // the refusal is a statement about this flush's settled plan, not about
      // the revision, and the identity can move later in the same generation
      // (a rotation onto a candidate the revision names). Recording it here
      // would swallow the redelivery that finally matches, leaving the menus
      // stale for the rest of the session. Only a genuinely applied revision is
      // recorded, by `foldInventoryUpdate`, so duplicates of applied revisions
      // stay suppressed while a refused one can still apply once it matches.
      if (
        entry.kind === "source" &&
        identityNamesSameSource(liveSourceIdentityRef.current, entry.identity)
      ) {
        liveSourceIdentityRef.current = {
          fileId: plan.effective_media_file_id,
          uri: plan.effective_virtual_uri ?? null,
        };
        lastDeferredSourceIdentityRef.current = null;
      }
    };
    for (const entry of queue) {
      // Only the adoption that won may be mutated. The rendered state still
      // describes the outgoing source here, so it is deliberately not consulted.
      if (entry.generation !== loadSequenceRef.current) continue;
      // A session change is normally foreign, but the replacement this entry
      // was queued behind adopts a new session id; an identity-carrying entry
      // captured across exactly that adoption is allowed through to the
      // identity checks below. An identity-less entry cannot be tied to any
      // adoption and is dropped with the outgoing session.
      if (
        entry.sessionId != null &&
        entry.sessionId !== sessionIdRef.current &&
        !(entry.plan != null && entry.hasIdentity)
      ) {
        continue;
      }
      // The settled plan is the primary authority. When it names a candidate
      // URI, that URI wins over the outgoing one (a replacement may retain the
      // effective candidate) and an exact match is kept. When it is URI-less,
      // the push's own file is the key, except for a same-file source the plan
      // cannot resolve. `appliedIdentity` is the menu identity at flush start, so
      // a push deferred before a poll folded a concrete candidate cannot
      // overwrite it. A URI-bearing source commit is judged by arrival order, but
      // only against state older than it: a poll that folded a candidate after
      // this entry was queued is external and newer and still vetoes the commit.
      // See deferredIdentityIsAdmissible.
      // A display-only offered release is admitted on its own terms at the
      // flush too: it names the same row and a candidate other than the settled
      // plan's, and folding it can only repaint the menus. The identity-aware
      // admission below is for pushes that carry a source identity, which an
      // offer deliberately does not.
      if (entry.kind === "inventory") {
        const offeredUri = entry.payload.offered_virtual_uri?.trim() ?? "";
        if (offeredUri) {
          const sameRow =
            entry.payload.effective_media_file_id == null ||
            plan.effective_media_file_id == null ||
            entry.payload.effective_media_file_id === plan.effective_media_file_id;
          if (sameRow && offeredUri !== (plan.effective_virtual_uri ?? null)) {
            foldInventoryUpdate(entry.payload);
          }
          continue;
        }
      }
      const pollApplied = lastPollAppliedRef.current;
      const appliedPollNewer =
        pollApplied != null && pollApplied.clock > entry.arrivalTransitionClock
          ? pollApplied.identity
          : null;
      if (
        !deferredIdentityIsAdmissible(
          entry.identity,
          plan,
          entry.plan,
          entry.outgoing,
          appliedIdentity,
          entry.kind === "source",
          appliedPollNewer,
          entry.kind === "source" && entry.outgoingFromDeferredSource,
          entry.kind === "inventory" && entry.payload.inventory_status === "verified",
        )
      ) {
        handleRefusedPush(entry);
        continue;
      }
      if (entry.kind === "source") {
        foldCommittedSource(entry.source, entry.audioTracks);
      } else {
        foldInventoryUpdate(entry.payload);
      }
    }
  }, [foldCommittedSource, foldInventoryUpdate]);
  flushDeferredPushesRef.current = flushDeferredPushes;

  const updatePlaybackState = useCallback((positionSeconds: number, playing: boolean) => {
    if (Number.isFinite(positionSeconds) && positionSeconds >= 0) {
      const isUninitializedPlayerZero =
        awaitingInitialPlayerPositionRef.current &&
        !playing &&
        positionSeconds === 0 &&
        playbackPositionRef.current > 0;
      if (!isUninitializedPlayerZero) {
        playbackPositionRef.current = positionSeconds;
        awaitingInitialPlayerPositionRef.current = false;
      }
    }
    if (playing) {
      playbackStartedRef.current = true;
      playbackPlayingRef.current = true;
    } else if (playbackStartedRef.current) {
      playbackPlayingRef.current = false;
    }
  }, []);

  const reportFirstFrame = useCallback(() => {
    // The current plan's transport is on screen: from here its reported state
    // is the viewer's intent.
    planTransportShownRef.current = true;
    const attempt = firstFrameRef.current;
    // Until a start's plan is adopted, the attempt id has already moved on and
    // the frame on screen belongs to the attempt being replaced.
    if (!attempt || attempt.reported || attempt.attemptId !== playbackAttemptIdRef.current) return;
    attempt.reported = true;
    reportEvent(
      "first_frame",
      attempt.intentAt === null
        ? undefined
        : { diagnostics: { first_frame_ms: Math.round(performance.now() - attempt.intentAt) } },
    );
  }, [reportEvent]);

  const setAutoFallback = useCallback((enabled: boolean) => {
    autoFallbackRef.current = enabled;
    setState((current) =>
      current.autoFallback === enabled ? current : { ...current, autoFallback: enabled },
    );
  }, []);

  const switchVersion = useCallback(
    (newFileId: number, currentPosition: number) => {
      if (!allowAlternateVersions) return;
      if (newFileId === stateRef.current.mediaFileId) return;
      // The viewer picked a concrete version, so Auto is no longer armed: the
      // server must not silently move off the chosen release.
      setAutoFallback(false);
      if (switchingRef.current) {
        // A switch is already in flight. Remember the newest target and the
        // live position it was clicked at; the completion handler starts the
        // switch to it once the current one settles (latest-wins, mirroring the
        // replan queue pattern).
        pendingSwitchFileIdRef.current = newFileId;
        pendingSwitchPositionRef.current = currentPosition;
        setState((current) => ({ ...current, pendingSwitchFileId: newFileId }));
        return;
      }
      switchingRef.current = true;
      setState((current) => ({ ...current, pendingSwitchFileId: newFileId }));
      // The viewer picked the version in the player: the new attempt's first
      // frame is timed from here.
      const intentAt = performance.now();

      (async () => {
        try {
          await loadSession({
            preferredFileId: newFileId,
            position: currentPosition,
            forceStartPosition: true,
            // The server retires the old session before it attempts a
            // replacement start. A failed response therefore cannot leave the
            // old plan active on the client.
            allowPreserveExistingSessionOnError: false,
            replacementErrorMessage: "Failed to switch playback version",
            initialErrorMessage: "Failed to switch version",
            // Carry the current audio selection across the version switch: the
            // server remaps the file-bound identity by track family onto the
            // new file instead of dropping it and auto-picking.
            carriedAudioTrackId: planRef.current?.selected_tracks.audio?.id ?? null,
            carriedSubtitleTrackIndex: resolveCarriedSubtitleIndexAcrossVersions(
              planRef.current,
              newFileId,
              versions,
            ),
            // A version switch is always an explicit user action: the server
            // must not silently substitute yet another version.
            fileSelection: "explicit",
            intentAt,
          });
        } finally {
          switchingRef.current = false;
          // A source push that landed while this switch owned the session was
          // held back; now that the replacement plan has settled, fold it in if
          // it still names the file that won.
          flushDeferredPushesRef.current();
          const rememberedSeek = pendingSeekRef.current;
          pendingSeekRef.current = null;
          const latest = pendingSwitchFileIdRef.current;
          const latestPosition = pendingSwitchPositionRef.current;
          pendingSwitchFileIdRef.current = null;
          pendingSwitchPositionRef.current = null;
          if (latest !== null && latest !== stateRef.current.mediaFileId) {
            // A chained switch wins: fold the remembered seek into its start
            // position instead of issuing a replan against the plan that is
            // about to be replaced.
            switchVersion(
              latest,
              rememberedSeek && rememberedSeek.playbackAttemptId === playbackAttemptIdRef.current
                ? rememberedSeek.positionSeconds
                : (latestPosition ?? playbackPositionRef.current),
            );
          } else if (
            rememberedSeek &&
            rememberedSeek.playbackAttemptId !== null &&
            rememberedSeek.playbackAttemptId === playbackAttemptIdRef.current &&
            planRef.current
          ) {
            // Only replay when the switch adopted the same playback attempt the
            // seek was refused in; a chained switch, a failed switch, or a
            // fresh attempt all discard it via the resets above.
            void reanchorSeekRef.current(rememberedSeek.positionSeconds);
          }
          // A track picked while the switch owned the lane is replayed against
          // the plan that won. A chained switch has set the guard again and
          // will flush once it settles.
          if (!switchingRef.current) flushPendingTrackPicksRef.current();
        }
      })();
    },
    [allowAlternateVersions, loadSession, setAutoFallback, versions],
  );

  /**
   * Re-issues the start that was refused with a retryable terminal.
   *
   * The retry differs from the original in one way: `force_relink` asks the
   * server to re-query the provider for the virtual source instead of replaying
   * the candidate it already had. A fresh attempt id is minted by `loadSession`
   * so the server cannot answer the retry from start idempotency. Double-taps
   * are ignored while a retry is in flight; a retry also refuses to disturb a
   * plan that is already playing.
   */
  const retryStart = useCallback(() => {
    if (retryingRef.current) return;
    if (planRef.current) return;
    const target = retryTargetRef.current;
    if (!target) return;

    retryingRef.current = true;
    setState((current) => ({
      ...current,
      retrying: true,
      errorTitle: null,
      error: null,
      errorReason: null,
      errorRetryable: false,
    }));

    void loadSession({
      preferredFileId: target.fileId,
      position: target.position,
      forceStartPosition: target.forceStartPosition,
      allowPreserveExistingSessionOnError: false,
      replacementErrorMessage: "Failed to retry playback",
      initialErrorMessage: "Failed to retry playback",
      carriedAudioTrackId: target.carriedAudioTrackId,
      offeredVirtualURI: target.offeredVirtualURI ?? undefined,
      // When Auto is armed the retry re-selects automatically, so the server
      // may walk alternate versions instead of replaying the dead pin.
      fileSelection: autoFallbackRef.current ? "auto" : target.fileSelection,
      forceRelink: true,
      intentAt: performance.now(),
    }).finally(() => {
      retryingRef.current = false;
      setState((current) => (current.retrying ? { ...current, retrying: false } : current));
    });
  }, [loadSession]);

  /**
   * The version menu's Auto entry. Arms automatic fallback and, when the
   * current source is already terminal (no active plan), recovers immediately
   * with an auto re-resolve. A healthy plan is left playing: Auto only ever
   * fires on a dead/unplayable source.
   */
  const selectAutoVersion = useCallback(() => {
    if (!allowAlternateVersions) return;
    setAutoFallback(true);
    if (!planRef.current) {
      retryStart();
    }
  }, [allowAlternateVersions, retryStart, setAutoFallback]);

  /**
   * Reads the session's live inventory from the plan's `inventory_url`.
   *
   * The folded revision rides as an ETag: a 200 carries the terminal
   * (verified or failed) list and folds through the same reducer the push
   * path uses, clearing the deferred loading state; a 304 means the probe
   * has not landed yet and leaves everything alone. Any other outcome —
   * network error, auth failure, missing URL — resolves false without
   * touching the pending flag, so a flaky poll can never end the loading
   * state on its own. Guards on the live session id so a response for a
   * replaced session is dropped.
   */
  const pollInventory = useCallback(async (): Promise<boolean> => {
    if (pollInventoryInflightRef.current) {
      return pollInventoryInflightRef.current;
    }
    const run = (async () => {
      const plan = planRef.current;
      const sessionId = sessionIdRef.current;
      const url = plan?.inventory_url ?? stateRef.current.inventoryUrl;
      if (!plan || !sessionId || !url) return false;
      const revision =
        inventoryRevisionRef.current ??
        plan.inventory_revision ??
        stateRef.current.inventoryRevision;
      const headers: Record<string, string> = { Accept: "application/json" };
      if (revision) headers["If-None-Match"] = revision;
      const teardownSignal = teardownAbortRef.current.signal;
      if (teardownSignal.aborted) return false;
      let response: Response;
      const timeoutSignal = AbortSignal.timeout(10_000);
      let signal: AbortSignal;
      let cleanupAbortListener: (() => void) | undefined;
      if (typeof AbortSignal.any === "function") {
        signal = AbortSignal.any([teardownSignal, timeoutSignal]);
      } else {
        const fallbackCtrl = new AbortController();
        const onTeardown = () => fallbackCtrl.abort();
        const onTimeout = () => fallbackCtrl.abort();
        teardownSignal.addEventListener("abort", onTeardown);
        timeoutSignal.addEventListener("abort", onTimeout);
        cleanupAbortListener = () => {
          teardownSignal.removeEventListener("abort", onTeardown);
          timeoutSignal.removeEventListener("abort", onTimeout);
        };
        signal = fallbackCtrl.signal;
      }
      let body: unknown;
      try {
        response = await playerFetchResponse(config, `${playerV2Origin(config)}${url}`, {
          headers: { ...headers, Accept: "application/json" },
          signal,
        });
        if (response.status === 304 || !response.ok) return false;
        body = await response.json();
      } catch {
        return false;
      } finally {
        cleanupAbortListener?.();
      }
      if (sessionIdRef.current !== sessionId || planRef.current !== plan) return false;
      if (typeof body !== "object" || body === null) return false;
      const record: Record<string, unknown> = body as Record<string, unknown>;
      const payload = {
        session_id: sessionId,
        inventory_revision:
          typeof record.inventory_revision === "string" ? record.inventory_revision : undefined,
        inventory_status:
          typeof record.inventory_status === "string" ? record.inventory_status : undefined,
        audio_tracks: Array.isArray(record.audio_tracks) ? record.audio_tracks : undefined,
        subtitle_inventory: Array.isArray(record.subtitle_inventory)
          ? record.subtitle_inventory
          : undefined,
        effective_media_file_id:
          typeof record.effective_media_file_id === "number"
            ? record.effective_media_file_id
            : undefined,
        effective_virtual_uri:
          typeof record.effective_virtual_uri === "string"
            ? record.effective_virtual_uri
            : undefined,
        virtual_source_revision:
          typeof record.virtual_source_revision === "string"
            ? record.virtual_source_revision
            : undefined,
      } satisfies PlaybackInventoryUpdatedPayload;
      const accepted = applyInventoryUpdate(payload);
      return (
        accepted &&
        !inventoryPendingRef.current &&
        (payload.inventory_status === "verified" || payload.inventory_status === "failed")
      );
    })();
    pollInventoryInflightRef.current = run;
    try {
      return await run;
    } finally {
      if (pollInventoryInflightRef.current === run) {
        pollInventoryInflightRef.current = null;
      }
    }
  }, [applyInventoryUpdate, config]);

  const markInventoryExhausted = useCallback(
    (expected?: { sessionId?: string | null; mediaFileId?: number | null }) => {
      // The identity is checked against the state this updater transitions, not
      // the refs: a replacement plan can already be queued when a stale timer
      // fires, so the rendered refs would still name the replaced session and
      // mark its replacement failed. This mirrors the deferred poll's identity
      // guard, with the queued update as the authority.
      setState((latest) => {
        if (
          expected &&
          (expected.sessionId !== latest.sessionId || expected.mediaFileId !== latest.mediaFileId)
        ) {
          return latest;
        }
        inventoryPendingRef.current = false;
        // The attempt budget is spent. That is only a *failure* while a menu is
        // still showing declared/loading tracks; once positive probe evidence
        // (a verified push, a refreshed plan, or a catalog list) has resolved
        // the inventory, the spent budget is moot and must not re-arm the
        // "discovery did not complete" notice above a correct list. Nothing is
        // owed then — no round is scheduled after exhaustion — so the pending
        // flag must not outlive it and read as discovery still in flight.
        if (!latest.audioInventoryProvisional && !latest.subtitleInventoryProvisional) {
          return latest.inventoryPending ? { ...latest, inventoryPending: false } : latest;
        }
        return { ...latest, inventoryPending: false, inventoryFailed: true };
      });
    },
    [],
  );

  /**
   * Hands the discovery badge back to a fresh poll round.
   *
   * A deferred round that exhausted can be followed by the catalog fallback
   * (WatchPage's legacy poll) still looking for probe evidence. Clearing the
   * terminal marker here keeps the menus on "Discovering…" while that retry is
   * actually in flight; the retry's own exhausted budget re-marks the failure.
   * Without this, the terminal "discovery did not complete" notice would sit
   * beside an active retry.
   */
  const markInventoryRetrying = useCallback(() => {
    setState((current) =>
      current.inventoryFailed ? { ...current, inventoryFailed: false } : current,
    );
  }, []);

  // Mirror the player's authoritative menus for the item page. The page can be
  // unmounted while this hook is alive, so a module-level snapshot is the only
  // carrier that survives the trip back from the player. The plan's inventory
  // is published under the effective file id it describes; the requested row
  // gets it too only while the plan still serves that row, because a
  // substituted (or lower-version) release's track table says nothing about the
  // version the viewer selected.
  useEffect(() => {
    // Declared rows are the plan's release listing, not probe evidence. They
    // are published as such so the item page can show them immediately and
    // still let verified rows win without ever regressing back.
    const provenance = {
      audioVerified: !state.audioInventoryProvisional,
      subtitleVerified: !state.subtitleInventoryProvisional,
    };
    for (const fileId of inventoryPublicationTargets(planRef.current, state.mediaFileId)) {
      publishPlaybackInventory(fileId, state.planAudioTracks, state.subtitleUrls, provenance);
    }
  }, [
    state.audioInventoryProvisional,
    state.mediaFileId,
    state.planAudioTracks,
    state.planRevision,
    state.subtitleInventoryProvisional,
    state.subtitleUrls,
  ]);

  return {
    ...state,
    browserEngine: browserEngine.capabilities,
    nativeOriginalSupport,
    switchVersion,
    selectAutoVersion,
    retryStart,
    switchAudioTrack,
    changeSubtitleTrack,
    changeQuality,
    recoverFromFailure,
    recoverConnection,
    retryConnection,
    expediteReconnect,
    reportSessionLost,
    markSessionGone,
    restartPlayback,
    invalidatePlan,
    reanchorSeek,
    refreshSubtitles,
    pollInventory,
    markInventoryExhausted,
    markInventoryRetrying,
    applySubtitleTrack,
    applyAudioInventory,
    applyCommittedSource,
    applyInventoryUpdate,
    updatePlaybackState,
    reportFirstFrame,
    reportEvent,
  };
}

/**
 * Whether two candidates belong to the same edition and part of one item.
 *
 * A version search that wants a different attribute (subtitles, a codec) may
 * substitute a candidate, but never another cut or another part of a multi-part
 * presentation: that would play a different thing. Empty group keys compare
 * equal, which is the ordinary single-file case.
 */
function isSamePresentation(a: PlayerFileVersion, b: PlayerFileVersion): boolean {
  if ((a.edition_key ?? "") !== (b.edition_key ?? "")) return false;
  if ((a.presentation_group_key ?? "") !== (b.presentation_group_key ?? "")) return false;
  return (a.presentation_part_index ?? 0) === (b.presentation_part_index ?? 0);
}

/** Whether a version publishes any subtitle track in its declared inventory. */
function hasSubtitleTracks(version: PlayerFileVersion): boolean {
  return (version.subtitle_tracks?.length ?? 0) > 0;
}

/**
 * Resolves the default file for the variant the viewer is resuming.
 *
 * This is edition/part selection, not adaptation: it answers "which cut of this
 * item" from client-held resume state. Which encode of that cut plays is the
 * server's decision.
 */
function selectDefaultVariantFile(
  playbackVariants: PlayerPlaybackVariant[],
  versions: PlayerFileVersion[],
  resumeHints?: ResumeHints,
): number | null {
  if (playbackVariants.length === 0) {
    return null;
  }

  let candidateVariants = playbackVariants;
  if (
    resumeHints?.lastEditionKey &&
    playbackVariants.some((variant) => variant.edition_key === resumeHints.lastEditionKey)
  ) {
    candidateVariants = playbackVariants.filter(
      (variant) => variant.edition_key === resumeHints.lastEditionKey,
    );
  } else if (playbackVariants.some((variant) => !variant.edition_key)) {
    candidateVariants = playbackVariants.filter((variant) => !variant.edition_key);
  }

  for (const variant of candidateVariants) {
    const firstPart = [...(variant.parts ?? [])].sort((a, b) => a.part_index - b.part_index)[0];
    if (!firstPart) {
      continue;
    }

    if (firstPart.default_file_id != null) {
      const known = versions.find((version) => version.file_id === firstPart.default_file_id);
      if (known) return known.file_id;
    }

    const firstVersion = (firstPart.versions ?? [])[0];
    if (firstVersion) return firstVersion.file_id;
  }

  return null;
}
