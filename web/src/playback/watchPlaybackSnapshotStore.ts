import { createContext, useCallback, useContext, useSyncExternalStore } from "react";
import type { WatchRouteRequest } from "@/pages/watchRouteHelpers";

export interface WatchPlaybackSnapshot {
  currentTime: number;
  duration: number;
  playing: boolean;
}

/**
 * Holds the player's position, which it reports on every time update (about
 * 4 Hz). It is kept out of the playback reducer on purpose: every change to
 * that state changes the controller context, which each card on a browse page
 * and the whole playback host read. Only the background bar subscribes here.
 *
 * The provider points the store at the current request. A report for any
 * other request key is dropped, and a snapshot belongs to the request object
 * it was reported under, not just its key: replaying a title builds a new
 * request with the same key, and it starts without the previous run's
 * position, as it did when the reducer reset the snapshot with the rest of
 * the playback state.
 */
export interface WatchPlaybackSnapshotStore {
  setRequest: (request: WatchRouteRequest | null) => void;
  report: (requestKey: string, snapshot: WatchPlaybackSnapshot) => void;
  get: (request: WatchRouteRequest | null) => WatchPlaybackSnapshot | null;
  subscribe: (listener: () => void) => () => void;
}

export function createWatchPlaybackSnapshotStore(): WatchPlaybackSnapshotStore {
  let current: WatchRouteRequest | null = null;
  let latest: { request: WatchRouteRequest; snapshot: WatchPlaybackSnapshot } | null = null;
  const listeners = new Set<() => void>();

  return {
    // Readers pass the request they render, so a new request reads null
    // without a notification.
    setRequest: (request) => {
      current = request;
    },
    report: (requestKey, snapshot) => {
      const request = current;
      if (!request || request.requestKey !== requestKey) {
        return;
      }

      if (
        latest?.request === request &&
        latest.snapshot.currentTime === snapshot.currentTime &&
        latest.snapshot.duration === snapshot.duration &&
        latest.snapshot.playing === snapshot.playing
      ) {
        return;
      }

      latest = { request, snapshot };
      for (const listener of listeners) {
        listener();
      }
    },
    get: (request) => (latest && latest.request === request ? latest.snapshot : null),
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

export const WatchPlaybackSnapshotStoreContext = createContext<WatchPlaybackSnapshotStore | null>(
  null,
);

/**
 * Reads the snapshot reported for request and re-renders when it changes. A
 * null request reads null, and time updates then do not re-render the caller.
 */
export function useWatchPlaybackSnapshot(
  request: WatchRouteRequest | null,
): WatchPlaybackSnapshot | null {
  const store = useContext(WatchPlaybackSnapshotStoreContext);
  if (!store) {
    throw new Error("Watch playback snapshot is unavailable outside WatchPlaybackProvider");
  }

  const getSnapshot = useCallback(() => store.get(request), [request, store]);
  return useSyncExternalStore(store.subscribe, getSnapshot);
}
