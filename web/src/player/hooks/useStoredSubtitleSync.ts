import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError } from "../player-fetch";
import { playerV2 } from "../player-v2";
import {
  isSyncInProgress,
  type StoredSubtitle,
  type SubtitleSyncJob,
} from "../utils/storedSubtitleSync";

export const SYNC_POLL_INTERVAL_MS = 3_000;
export const SYNC_POLL_LIMIT_MS = 5 * 60_000;

/** What the player knows about one stored subtitle's timing and sync. */
export interface StoredSubtitleSyncEntry {
  subtitle: StoredSubtitle;
  /** A sync or timing request is on the wire. */
  busy?: boolean;
  /** The server refused to let this viewer retime the subtitle (403). */
  forbidden?: boolean;
  /** The subtitle's format cannot be synced (422). */
  unsupported?: boolean;
  /** The last action's failure, in plain words. */
  error?: string;
  /** Polling gave up after SYNC_POLL_LIMIT_MS; reopening the menu re-arms it. */
  pollExpired?: boolean;
}

export interface StoredSubtitleSync {
  /** The server can align subtitles to audio (capability state "available"). */
  syncAvailable: boolean;
  entries: Readonly<Record<string, StoredSubtitleSyncEntry>>;
  requestSync: (id: string) => Promise<void>;
  resetTiming: (id: string) => Promise<void>;
  /** Records a subtitle returned by a download or upload, polling its sync. */
  remember: (subtitle: StoredSubtitle) => void;
  /** The server announced new timing (realtime); reload cues and re-read it. */
  timingChanged: (id: string) => void;
  /** Re-reads the file's stored subtitles, re-arming expired polls. */
  reload: () => void;
}

interface Options {
  playerConfig?: PlayerConfig;
  mediaFileId?: number;
  sessionId?: string | null;
  /** Stored IDs of the downloaded tracks in the current inventory. */
  storedIds: readonly string[];
  /** Called once per observed timing change, so the track's cues reload. */
  onTimingChanged?: (id: string) => void;
}

// Known timing that a realtime event already reported as changed: the next
// read records it without asking for a second cue reload.
const TIMING_DIRTY = "dirty";

function timingKey(subtitle: StoredSubtitle): string {
  return `${subtitle.timing.offset_ms}|${subtitle.timing.scale}`;
}

function actionError(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

/**
 * Tracks the timing and latest sync job of a file's stored subtitles for the
 * player: loads them, polls running jobs, and performs "Sync subtitle" and
 * "Reset timing". Results that land after the file, session, account, or
 * profile changed are dropped, matching the subtitle search modal's guards.
 */
export function useStoredSubtitleSync({
  playerConfig,
  mediaFileId,
  sessionId,
  storedIds,
  onTimingChanged,
}: Options): StoredSubtitleSync {
  const [entries, setEntries] = useState<Record<string, StoredSubtitleSyncEntry>>({});
  const [syncAvailable, setSyncAvailable] = useState(false);
  const [reloadToken, setReloadToken] = useState(0);
  const knownTimingRef = useRef(new Map<string, string>());
  const pollStartedRef = useRef(new Map<string, number>());
  const pollingRef = useRef(new Set<string>());
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
    pollingRef.current = new Set();
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

  const patch = useCallback((id: string, update: Partial<StoredSubtitleSyncEntry>) => {
    setEntries((prev) => {
      const entry = prev[id];
      return entry ? { ...prev, [id]: { ...entry, ...update } } : prev;
    });
  }, []);

  /** Records a fresh server view of a subtitle and reports a timing change. */
  const observe = useCallback((subtitle: StoredSubtitle) => {
    const id = subtitle.id;
    const key = timingKey(subtitle);
    const previous = knownTimingRef.current.get(id);
    knownTimingRef.current.set(id, key);
    const inProgress = isSyncInProgress(subtitle.sync?.status);
    if (!inProgress) pollStartedRef.current.delete(id);
    setEntries((prev) => ({
      ...prev,
      [id]: { ...prev[id], subtitle, pollExpired: inProgress && prev[id]?.pollExpired },
    }));
    if (previous !== undefined && previous !== TIMING_DIRTY && previous !== key) {
      onTimingChangedRef.current?.(id);
    }
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

  // Load the file's stored subtitles whenever the inventory gains or loses one.
  const storedIdsKey = useMemo(() => [...storedIds].sort().join(","), [storedIds]);
  useEffect(() => {
    if (!playerConfig || !mediaFileId || storedIdsKey === "") return;
    const current = capture();
    const controller = new AbortController();
    void (async () => {
      try {
        const res = await playerV2(playerConfig, "GET /api/v2/subtitles/{media_file_id}", {
          path: { media_file_id: String(mediaFileId) },
          signal: controller.signal,
        });
        if (!current() || controller.signal.aborted) return;
        for (const subtitle of res?.subtitles ?? []) observe(subtitle);
      } catch {
        /* Sync status is decoration; the tracks still play without it. */
      }
    })();
    return () => controller.abort();
  }, [playerConfig, mediaFileId, sessionId, storedIdsKey, reloadToken, capture, observe]);

  const readOne = useCallback(
    async (id: string) => {
      if (!playerConfig || pollingRef.current.has(id)) return;
      const current = capture();
      // Release on the set this read locked: a context reset swaps in a new
      // set, so the release can never unlock a newer context's read.
      const locks = pollingRef.current;
      locks.add(id);
      try {
        const res = await playerV2(playerConfig, "GET /api/v2/subtitles/stored/{id}/sync", {
          path: { id },
        });
        if (current() && res?.subtitle) observe(res.subtitle);
      } catch {
        // A lost subtitle or file stops polling; a later reload re-arms it.
        if (current()) patch(id, { pollExpired: true });
      } finally {
        locks.delete(id);
      }
    },
    [playerConfig, capture, observe, patch],
  );

  // Poll every running job until it ends, the limit passes, or the context changes.
  const pollingKey = useMemo(
    () =>
      Object.values(entries)
        .filter((entry) => isSyncInProgress(entry.subtitle.sync?.status) && !entry.pollExpired)
        .map((entry) => entry.subtitle.id)
        .sort()
        .join(","),
    [entries],
  );
  useEffect(() => {
    if (pollingKey === "") return;
    const ids = pollingKey.split(",");
    const timer = window.setInterval(() => {
      for (const id of ids) {
        // The first tick starts the clock; remember() and reload() clear it.
        let started = pollStartedRef.current.get(id);
        if (started === undefined) {
          started = Date.now();
          pollStartedRef.current.set(id, started);
        }
        if (Date.now() - started >= SYNC_POLL_LIMIT_MS) {
          pollStartedRef.current.delete(id);
          patch(id, { pollExpired: true });
        } else {
          void readOne(id);
        }
      }
    }, SYNC_POLL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [pollingKey, readOne, patch]);

  const remember = useCallback(
    (subtitle: StoredSubtitle) => {
      pollStartedRef.current.delete(subtitle.id);
      observe(subtitle);
    },
    [observe],
  );

  const reload = useCallback(() => {
    pollStartedRef.current = new Map();
    setEntries((prev) => {
      const next: Record<string, StoredSubtitleSyncEntry> = {};
      for (const [id, entry] of Object.entries(prev)) next[id] = { ...entry, pollExpired: false };
      return next;
    });
    setReloadToken((token) => token + 1);
  }, []);

  const timingChanged = useCallback(
    (id: string) => {
      knownTimingRef.current.set(id, TIMING_DIRTY);
      onTimingChangedRef.current?.(id);
      void readOne(id);
    },
    [readOne],
  );

  const handleActionError = useCallback(
    (id: string, err: unknown, fallback: string) => {
      if (err instanceof PlayerFetchError && err.status === 403) {
        patch(id, { busy: false, forbidden: true, error: undefined });
      } else if (err instanceof PlayerFetchError && err.status === 422) {
        patch(id, { busy: false, unsupported: true, error: "This format can't be synced." });
      } else if (err instanceof PlayerFetchError && err.status === 412) {
        patch(id, { busy: false, error: "The subtitle changed. Try again." });
      } else {
        patch(id, { busy: false, error: actionError(err, fallback) });
      }
    },
    [patch],
  );

  const requestSync = useCallback(
    async (id: string) => {
      if (!playerConfig) return;
      const current = capture();
      patch(id, { busy: true, error: undefined, pollExpired: false });
      pollStartedRef.current.delete(id);
      try {
        const res = await playerV2(playerConfig, "POST /api/v2/subtitles/stored/{id}/sync", {
          path: { id },
        });
        if (!current()) return;
        const job: SubtitleSyncJob | undefined = res?.job;
        setEntries((prev) => {
          const entry = prev[id];
          if (!entry) return prev;
          return {
            ...prev,
            [id]: { ...entry, busy: false, subtitle: { ...entry.subtitle, sync: job } },
          };
        });
      } catch (err) {
        if (current()) handleActionError(id, err, "Sync failed");
      }
    },
    [playerConfig, capture, patch, handleActionError],
  );

  const resetTiming = useCallback(
    async (id: string) => {
      if (!playerConfig) return;
      const current = capture();
      patch(id, { busy: true, error: undefined });
      try {
        let etag: string | null = null;
        await playerV2(playerConfig, "GET /api/v2/subtitles/stored/{id}/metadata", {
          path: { id },
          onResponse: (response) => {
            etag = response.headers.get("ETag");
          },
        });
        if (!current()) return;
        if (!etag) throw new Error("Couldn't read the subtitle's current version.");
        const res = await playerV2(playerConfig, "PUT /api/v2/subtitles/stored/{id}/timing", {
          path: { id },
          headers: { "If-Match": etag },
          body: { offset_ms: 0, scale: 1 },
        });
        if (!current()) return;
        if (res?.subtitle) observe(res.subtitle);
        patch(id, { busy: false });
      } catch (err) {
        if (current()) handleActionError(id, err, "Couldn't reset timing");
      }
    },
    [playerConfig, capture, patch, observe, handleActionError],
  );

  return useMemo(
    () => ({ syncAvailable, entries, requestSync, resetTiming, remember, timingChanged, reload }),
    [syncAvailable, entries, requestSync, resetTiming, remember, timingChanged, reload],
  );
}
