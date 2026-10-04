import { useCallback, useEffect, useState } from "react";
import type { PlayerSubtitleInfo } from "../types";
import { getLanguageName } from "../utils/languageNames";
import {
  describeTiming,
  isSyncInProgress,
  sameTiming,
  syncFailureMessage,
  syncKeyOf,
  syncPhaseLabel,
  syncProgressPercent,
  type SubtitleSyncJob,
  type SubtitleTiming,
} from "../utils/subtitleSync";
import type { SubtitleSync, SubtitleSyncEntry } from "./useSubtitleSync";

/** What the player's sync indicator shows. */
export interface SubtitleSyncNotice {
  /** Stays the same while one job runs, so the indicator updates in place. */
  id: string;
  tone: "progress" | "success" | "info" | "warning";
  title: string;
  detail?: string;
  /** 0..100 while a job runs. */
  percent?: number;
}

export const SYNC_NOTICE_VISIBLE_MS = 6_000;
export const SYNC_WARNING_VISIBLE_MS = 9_000;
/** How long "Applying new timing" waits for the reloaded cues before it says done anyway. */
export const SYNC_APPLY_TIMEOUT_MS = 8_000;

interface Options {
  sync: SubtitleSync;
  tracks: readonly PlayerSubtitleInfo[];
  /** Sync key of the track on screen. */
  activeKey: string | null;
  /** Cue revision of each sync key: it grows each time a track's timing changes. */
  cueRevisions: Readonly<Record<string, number>>;
  /** Load state of the track on screen ("loading", "ready", ...). */
  loadState: string;
}

/** What one render tells the feedback about sync state and the screen. */
export interface FeedbackInput {
  /** The most recent job this viewer started, with its subtitle. */
  watched: {
    key: string;
    name: string;
    job: SubtitleSyncJob;
    timing: SubtitleTiming;
    revision: number;
  } | null;
  activeKey: string | null;
  activeRevision: number;
  /** The entry of the track on screen. */
  active: SubtitleSyncEntry | null;
  loadState: string;
}

interface Applying {
  key: string;
  jobId: string;
  detail: string;
  /** The track's cue revision when the job started; a later one that loads shows the result. */
  startRevision: number;
}

export interface FeedbackState {
  input: FeedbackInput | null;
  notice: SubtitleSyncNotice | null;
  /** Watched jobs whose outcome was announced. */
  announced: ReadonlySet<string>;
  /** Watched synced jobs whose corrected cues are showing. */
  applied: ReadonlySet<string>;
  /** Cue revision of a watched job's track when the job was first seen. */
  startRevision: ReadonlyMap<string, number>;
  /**
   * The cue revision of each track whose cues finished loading last. In the
   * render where a timing change bumps a revision, the load state still
   * describes the old cues; only a "loading" to "ready" pass after it means
   * the corrected cues are on screen.
   */
  loadedRevision: ReadonlyMap<string, number>;
  applying: Applying | null;
  /** A timing change of the track on screen nobody here asked for, until it shows. */
  foreign: { key: string; revision: number } | null;
}

export const initialFeedbackState: FeedbackState = {
  input: null,
  notice: null,
  announced: new Set(),
  applied: new Set(),
  startRevision: new Map(),
  loadedRevision: new Map(),
  applying: null,
  foreign: null,
};

function sameNotice(a: SubtitleSyncNotice | null, b: SubtitleSyncNotice | null): boolean {
  return (
    a === b ||
    (a !== null &&
      b !== null &&
      a.id === b.id &&
      a.tone === b.tone &&
      a.title === b.title &&
      a.detail === b.detail &&
      a.percent === b.percent)
  );
}

function sameInput(a: FeedbackInput | null, b: FeedbackInput): boolean {
  return (
    a !== null &&
    a.activeKey === b.activeKey &&
    a.activeRevision === b.activeRevision &&
    a.active === b.active &&
    a.loadState === b.loadState &&
    a.watched?.job === b.watched?.job &&
    a.watched?.key === b.watched?.key &&
    a.watched?.name === b.watched?.name &&
    a.watched?.timing === b.watched?.timing &&
    a.watched?.revision === b.watched?.revision
  );
}

/** Shows the success the applying job earned once its cues show. */
export function finishApplying(state: FeedbackState): FeedbackState {
  const applying = state.applying;
  if (!applying) return state;
  return {
    ...state,
    applying: null,
    applied: new Set(state.applied).add(applying.jobId),
    notice: {
      id: applying.jobId,
      tone: "success",
      title: "Subtitles synced",
      detail: applying.detail,
    },
  };
}

function outcomeNotice(watched: NonNullable<FeedbackInput["watched"]>): SubtitleSyncNotice | null {
  const { job, name } = watched;
  switch (job.status) {
    case "synced":
      return {
        id: job.id,
        tone: "success",
        title: `${name} subtitles synced`,
        detail: describeTiming(watched.timing),
      };
    case "already_synced":
      return { id: job.id, tone: "info", title: `${name} subtitles already match the audio` };
    case "no_match":
      return {
        id: job.id,
        tone: "warning",
        title: `${name} subtitles don't match the audio`,
        detail: "They're probably for another release. The timing wasn't changed.",
      };
    case "failed":
      return {
        id: job.id,
        tone: "warning",
        title: `Couldn't sync ${name} subtitles`,
        detail: syncFailureMessage(job.failure),
      };
  }
  return null;
}

/**
 * Advances the feedback by one render's input: the progress, then the
 * outcome, of a sync this viewer started; when it changed the subtitle on
 * screen, the moment its corrected cues show; and a note when someone else's
 * change to the subtitle on screen shows.
 */
export function stepFeedback(prev: FeedbackState, input: FeedbackInput): FeedbackState {
  const before = prev.input;
  let next: FeedbackState = { ...prev, input };
  const show = (notice: SubtitleSyncNotice | null) => {
    if (!sameNotice(next.notice, notice)) next = { ...next, notice };
  };

  if (
    input.activeKey &&
    before?.activeKey === input.activeKey &&
    (before.loadState === "loading" || before.loadState === "refreshing") &&
    input.loadState === "ready"
  ) {
    next.loadedRevision = new Map(next.loadedRevision).set(input.activeKey, input.activeRevision);
  }
  const loadedAfter = (key: string, revision: number) =>
    (next.loadedRevision.get(key) ?? -1) > revision;

  const watched = input.watched;
  if (watched) {
    const { job } = watched;
    if (!next.startRevision.has(job.id)) {
      next.startRevision = new Map(next.startRevision).set(job.id, watched.revision);
    }
    if (isSyncInProgress(job.status)) {
      show({
        id: job.id,
        tone: "progress",
        title: `Syncing ${watched.name} subtitles`,
        detail: syncPhaseLabel(job) ?? undefined,
        percent: syncProgressPercent(job) ?? 0,
      });
    } else if (!next.announced.has(job.id)) {
      next.announced = new Set(next.announced).add(job.id);
      if (job.status === "synced" && watched.key === input.activeKey) {
        next.applying = {
          key: watched.key,
          jobId: job.id,
          detail: describeTiming(watched.timing),
          startRevision: next.startRevision.get(job.id) ?? watched.revision,
        };
        show({ id: job.id, tone: "progress", title: "Applying new timing…", percent: 100 });
      } else {
        show(outcomeNotice(watched));
      }
    }
  } else {
    // The watched job is gone, with the file or session it belonged to:
    // nothing will finish its progress notice.
    if (next.applying) next = { ...next, applying: null };
    if (next.notice?.tone === "progress") show(null);
  }

  if (
    next.applying &&
    (next.applying.key !== input.activeKey ||
      loadedAfter(next.applying.key, next.applying.startRevision))
  ) {
    next = finishApplying(next);
  }

  if (
    input.activeKey &&
    before?.activeKey === input.activeKey &&
    input.activeRevision > before.activeRevision
  ) {
    const job = input.active?.state.sync;
    const own =
      input.active?.watchedJobId !== undefined &&
      job?.id === input.active.watchedJobId &&
      (isSyncInProgress(job.status) || (job.status === "synced" && !next.applied.has(job.id)));
    // A subtitle synced automatically, the first time anyone played it,
    // changes without a word: the viewer only sees it line up.
    const automatic =
      job?.trigger === "auto" &&
      job.status === "synced" &&
      sameTiming(input.active?.state.timing, job.result ?? undefined);
    if (!own && !automatic)
      next.foreign = { key: input.activeKey, revision: before.activeRevision };
  }
  if (next.foreign) {
    if (next.foreign.key !== input.activeKey) {
      next.foreign = null;
    } else if (loadedAfter(next.foreign.key, next.foreign.revision)) {
      next.foreign = null;
      show({
        id: `timing:${input.activeKey}:${input.activeRevision}`,
        tone: "info",
        title: "Subtitle timing updated",
      });
    }
  }
  return next;
}

/** The most recent job this viewer started, among the file's subtitles. */
function watchedEntry(
  entries: Readonly<Record<string, SubtitleSyncEntry>>,
): SubtitleSyncEntry | null {
  let latest: SubtitleSyncEntry | null = null;
  for (const entry of Object.values(entries)) {
    const job = entry.state.sync;
    if (!entry.watchedJobId || job?.id !== entry.watchedJobId) continue;
    if (!latest || job.created_at > (latest.state.sync?.created_at ?? "")) latest = entry;
  }
  return latest;
}

/**
 * Turns subtitle sync state into one notice at a time for the viewer (see
 * stepFeedback). Finished notices leave on their own.
 */
export function useSubtitleSyncFeedback({
  sync,
  tracks,
  activeKey,
  cueRevisions,
  loadState,
}: Options): { notice: SubtitleSyncNotice | null; dismiss: () => void } {
  const entry = watchedEntry(sync.entries);
  const job = entry?.state.sync;
  const track = entry ? tracks.find((t) => syncKeyOf(t) === entry.state.key) : undefined;
  const input: FeedbackInput = {
    watched:
      entry && job
        ? {
            key: entry.state.key,
            name: track ? getLanguageName(track.language) || track.label : "Subtitles",
            job,
            timing: entry.state.timing,
            revision: cueRevisions[entry.state.key] ?? 0,
          }
        : null,
    activeKey,
    activeRevision: activeKey ? (cueRevisions[activeKey] ?? 0) : 0,
    active: activeKey ? (sync.entries[activeKey] ?? null) : null,
    loadState,
  };

  // The feedback follows what each render shows, so it advances during
  // render rather than in an effect.
  const [state, setState] = useState(initialFeedbackState);
  if (!sameInput(state.input, input)) {
    setState(stepFeedback(state, input));
  }

  const { notice, applying } = state;
  // A track that never reports its reload still gets its success.
  useEffect(() => {
    if (!applying) return;
    const timer = window.setTimeout(
      () =>
        setState((current) => (current.applying === applying ? finishApplying(current) : current)),
      SYNC_APPLY_TIMEOUT_MS,
    );
    return () => window.clearTimeout(timer);
  }, [applying]);
  useEffect(() => {
    if (!notice || notice.tone === "progress") return;
    const visible = notice.tone === "warning" ? SYNC_WARNING_VISIBLE_MS : SYNC_NOTICE_VISIBLE_MS;
    const timer = window.setTimeout(
      () =>
        setState((current) => (current.notice === notice ? { ...current, notice: null } : current)),
      visible,
    );
    return () => window.clearTimeout(timer);
  }, [notice]);

  const dismiss = useCallback(
    () => setState((current) => ({ ...current, notice: null })),
    [setState],
  );
  return { notice, dismiss };
}
