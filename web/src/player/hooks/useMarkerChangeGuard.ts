import { useCallback, useEffect, useRef, type Dispatch, type SetStateAction } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { WatchDetail } from "@/api/types";
import { fetchWatchDetail } from "@/hooks/queries/items";
import { itemKeys } from "@/hooks/queries/keys";
import type { PlayerFileVersion } from "../types";

type VersionsChange = (current: PlayerFileVersion[]) => PlayerFileVersion[];

interface UseMarkerChangeGuardParams {
  contentId: string;
  /** The file and library the page's own watch-detail query is keyed by. */
  fileId: number | undefined;
  libraryId: number | undefined;
  /** The versions the page last rendered from that query. */
  versions: PlayerFileVersion[];
  setPlaybackVersions: Dispatch<SetStateAction<PlayerFileVersion[]>>;
  /** Whether the playback control socket is connected. */
  connected: boolean;
  sessionId: string | null | undefined;
  activeFileId: number | null | undefined;
  /** The playback session is loading or being replaced. */
  busy: boolean;
}

/**
 * Keeps a marker change the player applied itself, an in-player save or a
 * markers_updated event, from being replaced by a watch-detail read that began
 * before it. Marker events carry no revision, so only the order the player saw
 * them in tells which is newer.
 *
 * Returns the function that applies such a change. It also owns the read that
 * reconciles the player's versions each time the control socket connects.
 */
export function useMarkerChangeGuard({
  contentId,
  fileId,
  libraryId,
  versions,
  setPlaybackVersions,
  connected,
  sessionId,
  activeFileId,
  busy,
}: UseMarkerChangeGuardParams): (change: VersionsChange) => void {
  const queryClient = useQueryClient();
  // Counts the marker changes applied. A read that began before one is older
  // than it.
  const markerRevisionRef = useRef(0);

  useEffect(() => {
    if (!connected || !sessionId || !activeFileId || busy) {
      return;
    }

    let cancelled = false;
    const queryKey = itemKeys.watchDetail(contentId, activeFileId, libraryId);
    const reconcile = () => {
      const markerRevision = markerRevisionRef.current;
      // A read already running began at an unknown time, maybe before a marker
      // change, and fetchQuery would join it.
      const joined = queryClient.getQueryState(queryKey)?.fetchStatus === "fetching";
      void queryClient
        .fetchQuery({
          queryKey,
          queryFn: () => fetchWatchDetail(contentId, activeFileId, libraryId),
          staleTime: 0,
        })
        .then(
          (detail) => detail.versions,
          () => null,
        )
        .then((readVersions) => {
          if (cancelled) return;
          // A joined read, or one a marker change overtook, may be older than
          // the markers the player holds: read again.
          if (joined || markerRevisionRef.current !== markerRevision) {
            reconcile();
            return;
          }
          // After a failed read, reconcile again on the next connection and
          // keep the current markers meanwhile.
          if (readVersions) setPlaybackVersions(readVersions);
        });
    };
    reconcile();

    return () => {
      cancelled = true;
    };
  }, [
    activeFileId,
    busy,
    connected,
    contentId,
    libraryId,
    queryClient,
    sessionId,
    setPlaybackVersions,
  ]);

  return useCallback(
    (change: VersionsChange) => {
      markerRevisionRef.current += 1;
      const pageKey = itemKeys.watchDetail(contentId, fileId, libraryId);
      // A page read still in flight began before this change. Start it over,
      // so what it brings in is newer than the change.
      void queryClient.refetchQueries({ queryKey: pageKey, exact: true, fetchStatus: "fetching" });
      // A read that finished before this change can still be on its way to the
      // versions prop. Put the change on top of it there, or the player's copy
      // of that prop would bring it in without the change.
      const landed = queryClient.getQueryData<WatchDetail>(pageKey);
      if (landed && landed.versions !== versions) {
        queryClient.setQueryData<WatchDetail>(pageKey, {
          ...landed,
          versions: change(landed.versions),
        });
      }
      setPlaybackVersions(change);
    },
    [contentId, fileId, libraryId, queryClient, setPlaybackVersions, versions],
  );
}
