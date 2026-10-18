import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError } from "../player-fetch";
import { playerV2 } from "../player-v2";
import type { PlaybackSubtitleSyncUpdatedPayload } from "../realtime-protocol";
import {
  isSyncInProgress,
  storedSyncState,
  type StoredSubtitle,
  type SubtitleSyncJob,
  type SubtitleSyncState,
} from "../utils/subtitleSync";

export const SYNC_POLL_INTERVAL_MS = 3_000;
export const SYNC_POLL_LIMIT_MS = 5 * 60_000;
/** A realtime update this recent makes the next poll of that subtitle redundant. */
export const SYNC_PUSH_FRESH_MS = 4_000;
/**
 * The server follows a sync result with subtitle_timing_changed; that event,
 * arriving this soon after a result that already reloaded the track, reuses
 * the reload.
 */
export const SYNC_TIMING_COALESCE_MS = 5_000;

/** How far a job has come: queued, running, then finished. */
function jobStage(status: SubtitleSyncJob["status"]): number {
  if (status === "pending") return 0;
  return status === "running" ? 1 : 2;
}

/**
 * Whether a realtime update about job is older than the known job: a job
 * created before it, or the same job at an earlier stage (queued after it
 * ran, or running after it finished). Updates from different API servers,
 * or sent off the sync worker, can arrive out of order.
 */
function isOutdatedJob(job: SubtitleSyncJob, known: SubtitleSyncJob): boolean {
  if (job.id !== known.id) return job.created_at < known.created_at;
  return jobStage(job.status) < jobStage(known.status);
}

interface ObserveOptions {
  watch?: string;
  sentAt?: number;
  pushed?: boolean;
}

/** What the player knows about one syncable subtitle's timing and sync. */
export interface SubtitleSyncEntry {
  state: SubtitleSyncState;
  /** A sync or timing request is on the wire. */
  busy?: boolean;
  /** The server refused to let this viewer retime the subtitle (403, as in demo mode). */
  forbidden?: boolean;
  /** The subtitle's format cannot be synced (422). */
  unsupported?: boolean;
  /** The last action's failure, in plain words. */
  error?: string;
  /** Polling gave up after SYNC_POLL_LIMIT_MS; reopening the menu re-arms it. */
  pollExpired?: boolean;
  /**
   * A job this viewer started: a sync they asked for, or the automatic sync
   * of a subtitle they just downloaded or uploaded. The player reports its
   * progress and outcome to them.
   */
  watchedJobId?: string;
}

export interface SubtitleSync {
  /** The server can align subtitles to audio (capability state "available"). */
  syncAvailable: boolean;
  /** Entries by sync key. */
  entries: Readonly<Record<string, SubtitleSyncEntry>>;
  requestSync: (key: string) => Promise<void>;
  resetTiming: (key: string) => Promise<void>;
  /** Records a subtitle returned by a download or upload, following its sync. */
  remember: (subtitle: StoredSubtitle) => void;
  /** The server announced new timing (realtime); reload cues and re-read it. */
  timingChanged: (key: string) => void;
  /** The server reported a sync job's progress (realtime). */
  syncUpdated: (update: PlaybackSubtitleSyncUpdatedPayload) => void;
  /** Re-reads the file's subtitles, re-arming expired polls. */
  reload: () => void;
}

interface Options {
  playerConfig?: PlayerConfig;
  mediaFileId?: number;
  sessionId?: string | null;
  /** Sync keys of the syncable tracks in the current inventory. */
  syncKeys: readonly string[];
  /** Called once per observed timing change, so the track's cues reload. */
  onTimingChanged?: (key: string) => void;
}

// Known timing that a realtime event already reported as changed: the next
// read records it without asking for a second cue reload.
const TIMING_DIRTY = "dirty";

function timingKey(state: SubtitleSyncState): string {
  return `${state.timing.offset_ms}|${state.timing.scale}`;
}

function actionError(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

/**
 * Tracks the timing and latest sync job of a file's syncable subtitles
 * (stored ones and subtitle files next to the media) for the player: loads
 * them, follows running jobs through realtime updates with polling as the
 * fallback, and performs "Sync to audio" and "Reset timing". Results that land
 * after the file, session, account, or profile changed are dropped, matching
 * the subtitle search modal's guards.
 */
export function useSubtitleSync({
  playerConfig,
  mediaFileId,
  sessionId,
  syncKeys,
  onTimingChanged,
}: Options): SubtitleSync {
  const [entries, setEntries] = useState<Record<string, SubtitleSyncEntry>>({});
  const [syncAvailable, setSyncAvailable] = useState(false);
  const [reloadToken, setReloadToken] = useState(0);
  const knownTimingRef = useRef(new Map<string, string>());
  const pollStartedRef = useRef(new Map<string, number>());
  const pushedAtRef = useRef(new Map<string, number>());
  // When a realtime result last reloaded each subtitle's track.
  const pushReloadRef = useRef(new Map<string, number>());
  const pollingRef = useRef(new Set<string>());
  // Reads asked for while one was in flight: that one may answer from
  // before what prompted them, so another read follows it.
  const rereadRef = useRef(new Set<string>());
  // Every observation takes the next number; a read remembers the number
  // current when it was sent, so its answer cannot undo a newer one.
  const observationRef = useRef(0);
  const observedRef = useRef(new Map<string, number>());
  // The job each subtitle's latest observation carried, ahead of a render.
  const jobsRef = useRef(new Map<string, SubtitleSyncJob>());
  const generationRef = useRef(0);
  const onTimingChangedRef = useRef(onTimingChanged);
  onTimingChangedRef.current = onTimingChanged;

  // A new file, session, or host invalidates everything in flight.
  useEffect(() => {
    // The counter is not a DOM ref; bumping it again on cleanup is the point,
    // so late results from this context are dropped after unmount too.
    const generation = generationRef;
    generation.current++;
    knownTimingRef.current = new Map();
    pollStartedRef.current = new Map();
    pushedAtRef.current = new Map();
    pushReloadRef.current = new Map();
    pollingRef.current = new Set();
    rereadRef.current = new Set();
    observedRef.current = new Map();
    jobsRef.current = new Map();
    setEntries({});
    return () => {
      generation.current++;
    };
  }, [playerConfig, mediaFileId, sessionId]);

  /** Returns a predicate that is true while the captured context still holds. */
  const capture = useCallback(() => {
    const generation = generationRef.current;
    const auth = playerConfig?.getAuthContext?.();
    const profile = playerConfig?.getProfileId();
    const pin = playerConfig?.getProfileToken?.();
    return () =>
      generation === generationRef.current &&
      auth === playerConfig?.getAuthContext?.() &&
      profile === playerConfig?.getProfileId() &&
      pin === playerConfig?.getProfileToken?.();
  }, [playerConfig]);

  const entriesRef = useRef(entries);
  entriesRef.current = entries;

  const patch = useCallback((key: string, update: Partial<SubtitleSyncEntry>) => {
    setEntries((prev) => {
      const entry = prev[key];
      return entry ? { ...prev, [key]: { ...entry, ...update } } : prev;
    });
  }, []);

  /**
   * Records a fresh server view of a subtitle and reports a timing change.
   * `watch` marks a job this viewer started. `sentAt` is the observation
   * number a read was sent at: a realtime update that arrived while it was
   * in flight is newer than its answer, which is then dropped. `pushed`
   * marks a realtime update: when it reloads the track, the
   * subtitle_timing_changed event that follows it does not reload it again.
   */
  const observe = useCallback(
    (state: SubtitleSyncState, { watch, sentAt, pushed }: ObserveOptions = {}) => {
      const key = state.key;
      if (sentAt !== undefined && (observedRef.current.get(key) ?? 0) > sentAt) {
        if (watch) {
          setEntries((prev) =>
            prev[key] ? { ...prev, [key]: { ...prev[key], watchedJobId: watch } } : prev,
          );
        }
        return;
      }
      observedRef.current.set(key, ++observationRef.current);
      if (state.sync) jobsRef.current.set(key, state.sync);
      else jobsRef.current.delete(key);
      const timing = timingKey(state);
      const previous = knownTimingRef.current.get(key);
      knownTimingRef.current.set(key, timing);
      const inProgress = isSyncInProgress(state.sync?.status);
      if (!inProgress) pollStartedRef.current.delete(key);
      setEntries((prev) => ({
        ...prev,
        [key]: {
          ...prev[key],
          state,
          pollExpired: inProgress && prev[key]?.pollExpired,
          watchedJobId: watch ?? prev[key]?.watchedJobId,
        },
      }));
      if (previous !== undefined && previous !== TIMING_DIRTY && previous !== timing) {
        if (pushed) pushReloadRef.current.set(key, Date.now());
        onTimingChangedRef.current?.(key);
      }
    },
    [],
  );

  /**
   * Forgets a subtitle the server no longer has, so no progress of a job
   * that will never finish stays on screen. An observation since `sentAt`
   * is newer than the read that found it gone and keeps it. Forgetting
   * counts as an observation: an older answer cannot bring it back.
   */
  const forget = useCallback((key: string, sentAt: number) => {
    if ((observedRef.current.get(key) ?? 0) > sentAt) return;
    observedRef.current.set(key, ++observationRef.current);
    knownTimingRef.current.delete(key);
    jobsRef.current.delete(key);
    pollStartedRef.current.delete(key);
    pushReloadRef.current.delete(key);
    setEntries((prev) => {
      if (!(key in prev)) return prev;
      const { [key]: _gone, ...rest } = prev;
      return rest;
    });
  }, []);

  // Sync is a server-wide capability; read it once per host config.
  useEffect(() => {
    if (!playerConfig) return;
    setSyncAvailable(false);
    let cancelled = false;
    void (async () => {
      try {
        const res = await playerV2(playerConfig, "GET /api/v2/subtitles/sync/status", {});
        if (!cancelled) setSyncAvailable(res?.state === "available");
      } catch {
        if (!cancelled) setSyncAvailable(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [playerConfig]);

  // Load the file's syncable subtitles whenever the inventory gains or loses
  // one. An inventory without any still reads the list while subtitles are
  // known, so the server says which of them are gone.
  const syncKeysKey = useMemo(() => [...syncKeys].sort().join(","), [syncKeys]);
  useEffect(() => {
    if (!playerConfig || !mediaFileId) return;
    if (syncKeysKey === "" && Object.keys(entriesRef.current).length === 0) return;
    const current = capture();
    const controller = new AbortController();
    const sentAt = observationRef.current;
    void (async () => {
      try {
        const res = await playerV2(playerConfig, "GET /api/v2/subtitles/{media_file_id}/sync", {
          path: { media_file_id: String(mediaFileId) },
          signal: controller.signal,
        });
        if (!current() || controller.signal.aborted) return;
        const states = res?.subtitles ?? [];
        for (const state of states) observe(state, { sentAt });
        // The list names every subtitle of the file that can be synced.
        const listed = new Set(states.map((state) => state.key));
        for (const key of Object.keys(entriesRef.current)) {
          if (!listed.has(key)) forget(key, sentAt);
        }
      } catch {
        /* Sync status is decoration; the tracks still play without it. */
      }
    })();
    return () => controller.abort();
  }, [playerConfig, mediaFileId, sessionId, syncKeysKey, reloadToken, capture, observe, forget]);

  const readOne = useCallback(
    async (key: string) => {
      if (!playerConfig || !mediaFileId) return;
      if (pollingRef.current.has(key)) {
        rereadRef.current.add(key);
        return;
      }
      const current = capture();
      // Release on the sets this read used: a context reset swaps in new
      // ones, so the release can never unlock a newer context's read.
      const locks = pollingRef.current;
      const rereads = rereadRef.current;
      locks.add(key);
      try {
        do {
          const sentAt = observationRef.current;
          try {
            const res = await playerV2(
              playerConfig,
              "GET /api/v2/subtitles/{media_file_id}/sync/{key}",
              {
                path: { media_file_id: String(mediaFileId), key },
              },
            );
            if (current() && res?.subtitle) observe(res.subtitle, { sentAt });
          } catch (err) {
            if (!current()) return;
            if (err instanceof PlayerFetchError && err.status === 404) {
              // The subtitle is gone, and its jobs with it.
              forget(key, sentAt);
            } else {
              // Any other failure stops polling; a later reload re-arms it.
              patch(key, { pollExpired: true });
            }
            return;
          }
        } while (rereads.delete(key) && current());
      } finally {
        locks.delete(key);
        rereads.delete(key);
      }
    },
    [playerConfig, mediaFileId, capture, observe, patch, forget],
  );

  // Poll every running job until it ends, the limit passes, or the context
  // changes. Realtime updates usually arrive first; a subtitle that had one
  // recently skips the poll.
  const pollingKey = useMemo(
    () =>
      Object.values(entries)
        .filter((entry) => isSyncInProgress(entry.state.sync?.status) && !entry.pollExpired)
        .map((entry) => entry.state.key)
        .sort()
        .join(","),
    [entries],
  );
  useEffect(() => {
    if (pollingKey === "") return;
    const keys = pollingKey.split(",");
    const timer = window.setInterval(() => {
      const now = Date.now();
      for (const key of keys) {
        // The first tick starts the clock; remember() and reload() clear it.
        let started = pollStartedRef.current.get(key);
        if (started === undefined) {
          started = now;
          pollStartedRef.current.set(key, started);
        }
        if (now - started >= SYNC_POLL_LIMIT_MS) {
          pollStartedRef.current.delete(key);
          patch(key, { pollExpired: true });
        } else if (now - (pushedAtRef.current.get(key) ?? 0) >= SYNC_PUSH_FRESH_MS) {
          void readOne(key);
        }
      }
    }, SYNC_POLL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [pollingKey, readOne, patch]);

  const remember = useCallback(
    (subtitle: StoredSubtitle) => {
      const state = storedSyncState(subtitle);
      pollStartedRef.current.delete(state.key);
      const sync = state.sync;
      observe(state, { watch: sync && isSyncInProgress(sync.status) ? sync.id : undefined });
    },
    [observe],
  );

  const reload = useCallback(() => {
    pollStartedRef.current = new Map();
    setEntries((prev) => {
      const next: Record<string, SubtitleSyncEntry> = {};
      for (const [key, entry] of Object.entries(prev)) next[key] = { ...entry, pollExpired: false };
      return next;
    });
    setReloadToken((token) => token + 1);
  }, []);

  const timingChanged = useCallback(
    (key: string) => {
      const reloadedAt = pushReloadRef.current.get(key);
      pushReloadRef.current.delete(key);
      // Right after a realtime result reloaded the track, this event is
      // usually about that same change. The read below still reloads it if
      // the timing differs from the one just loaded.
      if (reloadedAt === undefined || Date.now() - reloadedAt >= SYNC_TIMING_COALESCE_MS) {
        knownTimingRef.current.set(key, TIMING_DIRTY);
        onTimingChangedRef.current?.(key);
      }
      void readOne(key);
    },
    [readOne],
  );

  const syncUpdated = useCallback(
    (update: PlaybackSubtitleSyncUpdatedPayload) => {
      const entry = entriesRef.current[update.sync_key];
      if (!entry) {
        // Not loaded yet (a track the inventory just gained): read them all.
        setReloadToken((token) => token + 1);
        return;
      }
      const known = jobsRef.current.get(update.sync_key);
      if (known && isOutdatedJob(update.job, known)) return;
      pushedAtRef.current.set(update.sync_key, Date.now());
      observe({ ...entry.state, timing: update.timing, sync: update.job }, { pushed: true });
    },
    [observe],
  );

  const handleActionError = useCallback(
    (key: string, err: unknown, fallback: string) => {
      if (err instanceof PlayerFetchError && err.status === 403) {
        patch(key, { busy: false, forbidden: true, error: undefined });
      } else if (err instanceof PlayerFetchError && err.status === 422) {
        patch(key, { busy: false, unsupported: true, error: "This format can't be synced." });
      } else if (err instanceof PlayerFetchError && err.status === 412) {
        patch(key, { busy: false, error: "The subtitle changed. Try again." });
      } else {
        patch(key, { busy: false, error: actionError(err, fallback) });
      }
    },
    [patch],
  );

  const requestSync = useCallback(
    async (key: string) => {
      if (!playerConfig || !mediaFileId) return;
      const current = capture();
      patch(key, { busy: true, error: undefined, pollExpired: false });
      pollStartedRef.current.delete(key);
      const sentAt = observationRef.current;
      try {
        const res = await playerV2(
          playerConfig,
          "POST /api/v2/subtitles/{media_file_id}/sync/{key}",
          {
            path: { media_file_id: String(mediaFileId), key },
          },
        );
        if (!current() || !res?.subtitle) return;
        observe(res.subtitle, { watch: res.subtitle.sync?.id, sentAt });
        patch(key, { busy: false });
      } catch (err) {
        if (current()) handleActionError(key, err, "Sync failed");
      }
    },
    [playerConfig, mediaFileId, capture, patch, observe, handleActionError],
  );

  const resetTiming = useCallback(
    async (key: string) => {
      if (!playerConfig || !mediaFileId) return;
      const current = capture();
      const path = { media_file_id: String(mediaFileId), key };
      patch(key, { busy: true, error: undefined });
      try {
        let etag: string | null = null;
        await playerV2(playerConfig, "GET /api/v2/subtitles/{media_file_id}/sync/{key}", {
          path,
          onResponse: (response) => {
            etag = response.headers.get("ETag");
          },
        });
        if (!current()) return;
        if (!etag) throw new Error("Couldn't read the subtitle's current version.");
        const sentAt = observationRef.current;
        const res = await playerV2(
          playerConfig,
          "PUT /api/v2/subtitles/{media_file_id}/sync/{key}/timing",
          { path, headers: { "If-Match": etag }, body: { offset_ms: 0, scale: 1 } },
        );
        if (!current()) return;
        if (res?.subtitle) observe(res.subtitle, { sentAt });
        patch(key, { busy: false });
      } catch (err) {
        if (current()) handleActionError(key, err, "Couldn't reset timing");
      }
    },
    [playerConfig, mediaFileId, capture, patch, observe, handleActionError],
  );

  return useMemo(
    () => ({
      syncAvailable,
      entries,
      requestSync,
      resetTiming,
      remember,
      timingChanged,
      syncUpdated,
      reload,
    }),
    [
      syncAvailable,
      entries,
      requestSync,
      resetTiming,
      remember,
      timingChanged,
      syncUpdated,
      reload,
    ],
  );
}
