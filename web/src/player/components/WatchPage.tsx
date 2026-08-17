import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { PlayerFileVersion, PlayerPlaybackStateChange, WatchPageProps } from "../types";
import type { PlaybackRealtimeEventEnvelope } from "../realtime-protocol";
import type { SubtitleInventoryItemV3 } from "../protocol-v3";
import { usePlaybackSession } from "../hooks/usePlaybackSession";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { playerFetch } from "../player-fetch";
import { resolvePlayableSubtitles } from "../utils/playableSubtitles";
import { patchVersionMarkers, resolveActiveVersionMarkers } from "../utils/watchPageMarkers";
import { buildSubtitleChoiceRequests } from "../utils/subtitleChoicePersistence";
import { VideoPlayer } from "./VideoPlayer";
import { fetchWatchDetail } from "@/hooks/queries/items";
import { itemKeys } from "@/hooks/queries/keys";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useWatchTogetherRoomConnection } from "../hooks/useWatchTogetherRoomConnection";

function patchChapterThumbnail(
  versions: PlayerFileVersion[],
  fileId: number,
  chapterIndex: number,
  thumbnailUrl: string,
  thumbnailThumbhash?: string,
): PlayerFileVersion[] {
  let changed = false;
  const nextVersions = versions.map((version) => {
    if (version.file_id !== fileId || !version.chapters?.length) {
      return version;
    }

    let versionChanged = false;
    const nextChapters = version.chapters.map((chapter) => {
      if (chapter.index !== chapterIndex) {
        return chapter;
      }
      if (
        chapter.thumbnail_url === thumbnailUrl &&
        chapter.thumbnail_thumbhash === thumbnailThumbhash
      ) {
        return chapter;
      }
      changed = true;
      versionChanged = true;
      return {
        ...chapter,
        thumbnail_url: thumbnailUrl,
        thumbnail_thumbhash: thumbnailThumbhash,
      };
    });

    return versionChanged ? { ...version, chapters: nextChapters } : version;
  });

  return changed ? nextVersions : versions;
}

/**
 * WatchPage is the top-level player component.
 * Starts a playback session, then renders the VideoPlayer once the stream is ready.
 */
export function WatchPage({
  contentId,
  title,
  year,
  playbackRequestKey,
  fileId,
  libraryId,
  versions,
  playbackVariants = [],
  subtitles,
  initialPosition,
  forceInitialPosition,
  qualityPreference,
  maxBitrateKbps,
  explicitAudioTrackIndex,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles,
  profileLanguage,
  introSkipMode,
  autoSkipRecap,
  autoPlayNextPreview,
  canEditMarkers,
  seriesContext,
  onNavigateEpisode,
  onEnded,
  onExit,
  onMinimize,
  resumeHints,
  displayMode,
  onPictureInPictureChange,
  autoEnterPictureInPicture,
  onPlaybackStateChange,
  onPlaybackTransportReady,
  onReturnFromPostRoll,
  watchTogetherRoomId,
  watchTogetherRoomToken,
}: WatchPageProps) {
  const config = usePlayerConfig();
  const queryClient = useQueryClient();
  const playbackController = useWatchPlaybackController();
  const chapterRefreshAttemptsRef = useRef<Set<number>>(new Set());
  const handledSelectionRevisionRef = useRef<number | null>(null);
  const markerRealtimeReconcileKeyRef = useRef<string | null>(null);
  const [playbackVersions, setPlaybackVersions] = useState(versions);
  const [realtimeConnectionState, setRealtimeConnectionState] = useState<
    "disconnected" | "connecting" | "connected"
  >("disconnected");
  const watchTogetherConnection = useWatchTogetherRoomConnection({
    roomId: watchTogetherRoomId,
    roomToken: watchTogetherRoomToken,
  });

  useEffect(() => {
    setPlaybackVersions(versions);
  }, [versions]);

  const session = usePlaybackSession(
    playbackRequestKey ??
      JSON.stringify([contentId, fileId ?? null, initialPosition, forceInitialPosition]),
    playbackVersions,
    playbackVariants,
    fileId,
    initialPosition,
    forceInitialPosition,
    qualityPreference,
    maxBitrateKbps,
    resumeHints,
    explicitAudioTrackIndex,
  );

  const audioTracks = useMemo(
    () => playbackVersions.find((v) => v.file_id === session.mediaFileId)?.audio_tracks ?? [],
    [playbackVersions, session.mediaFileId],
  );
  const playableSubtitles = useMemo(
    () => resolvePlayableSubtitles(session.subtitleUrls, subtitles),
    [session.subtitleUrls, subtitles],
  );

  const handleSwitchVersion = useCallback(
    (newFileId: number, currentPosition: number) => {
      session.switchVersion(newFileId, currentPosition);
    },
    [session],
  );

  const activePlaybackVersion = useMemo(
    () => playbackVersions.find((version) => version.file_id === session.mediaFileId),
    [playbackVersions, session.mediaFileId],
  );

  const handleEnded = useCallback(() => {
    onEnded?.({
      positionSeconds: session.durationSeconds ?? 0,
      durationSeconds: session.durationSeconds ?? undefined,
      lastFileId: session.mediaFileId,
      lastResolution: activePlaybackVersion?.resolution,
      lastHDR: activePlaybackVersion?.hdr,
      lastCodecVideo: activePlaybackVersion?.codec_video,
      lastEditionKey: activePlaybackVersion?.edition_key,
    });
  }, [activePlaybackVersion, onEnded, session.durationSeconds, session.mediaFileId]);

  const handleSwitchAudio = useCallback(
    (index: number, currentPosition: number) => {
      session.switchAudioTrack(index, currentPosition);
    },
    [session],
  );

  const updatePlaybackState = session.updatePlaybackState;
  const handlePlaybackStateChange = useCallback(
    (state: PlayerPlaybackStateChange) => {
      updatePlaybackState(state.currentTime, state.playing);
      onPlaybackStateChange?.(state);
    },
    [onPlaybackStateChange, updatePlaybackState],
  );

  /**
   * Persists an in-player subtitle choice for the whole series.
   *
   * buildSubtitleChoiceRequests decides what a pick is worth storing and
   * where; this only issues the requests. They are independent on purpose: a
   * failed settings write must not cost the user the track they picked, and a
   * failed track write must not cost them the language, so each is best effort
   * on its own rather than one composite request that half-applies.
   */
  const handleSubtitleChanged = useCallback(
    (index: number | null, inventoryTrack?: SubtitleInventoryItemV3) => {
      const requests = buildSubtitleChoiceRequests({
        seriesId: seriesContext?.seriesId ?? contentId,
        index,
        tracks: playableSubtitles,
        inventoryTrack,
        showForcedSubtitles,
      });
      for (const request of requests) {
        void playerFetch(config, request.path, {
          method: "PUT",
          body: JSON.stringify(request.body),
        }).catch(() => {
          // Best effort.
        });
      }
    },
    [config, seriesContext, contentId, playableSubtitles, showForcedSubtitles],
  );

  useEffect(() => {
    chapterRefreshAttemptsRef.current.clear();
    markerRealtimeReconcileKeyRef.current = null;
  }, [contentId, playbackRequestKey]);

  useEffect(() => {
    const room = watchTogetherConnection.room;
    if (!watchTogetherRoomId || !watchTogetherRoomToken || !room) {
      handledSelectionRevisionRef.current = null;
      return;
    }

    const sameSelection =
      room.selected_content_id === contentId &&
      room.selected_file_id === fileId &&
      room.selected_library_id === libraryId;
    if (sameSelection) {
      handledSelectionRevisionRef.current = room.selection_revision;
      return;
    }
    if (room.phase !== "playing" || !room.selected_content_id) {
      return;
    }
    if (handledSelectionRevisionRef.current === room.selection_revision) {
      return;
    }

    handledSelectionRevisionRef.current = room.selection_revision;
    playbackController.startPlayback({
      contentId: room.selected_content_id,
      fileId: room.selected_file_id,
      libraryId: room.selected_library_id,
      roomId: watchTogetherRoomId,
      roomToken: watchTogetherRoomToken,
      restart: true,
    });
  }, [
    contentId,
    fileId,
    libraryId,
    playbackController,
    watchTogetherConnection.room,
    watchTogetherRoomId,
    watchTogetherRoomToken,
  ]);

  useEffect(() => {
    if (!session.sessionId || !session.mediaFileId || session.loading || session.replacing) {
      return;
    }

    const activeVersion = playbackVersions.find(
      (version) => version.file_id === session.mediaFileId,
    );
    if (!activeVersion || (activeVersion.chapters?.length ?? 0) > 0) {
      return;
    }

    if (chapterRefreshAttemptsRef.current.has(session.mediaFileId)) {
      return;
    }
    chapterRefreshAttemptsRef.current.add(session.mediaFileId);

    void queryClient.fetchQuery({
      queryKey: itemKeys.watchDetail(contentId, fileId, libraryId),
      queryFn: () => fetchWatchDetail(contentId, fileId, libraryId),
      staleTime: 0,
    });
  }, [
    contentId,
    fileId,
    libraryId,
    queryClient,
    session.loading,
    session.mediaFileId,
    session.replacing,
    session.sessionId,
    playbackVersions,
  ]);

  useEffect(() => {
    if (
      realtimeConnectionState !== "connected" ||
      !session.sessionId ||
      !session.mediaFileId ||
      session.loading ||
      session.replacing
    ) {
      return;
    }

    const activeFileId = session.mediaFileId;
    const reconcileKey = `${session.sessionId}:${activeFileId}`;
    if (markerRealtimeReconcileKeyRef.current === reconcileKey) {
      return;
    }
    markerRealtimeReconcileKeyRef.current = reconcileKey;

    let cancelled = false;
    void queryClient
      .fetchQuery({
        queryKey: itemKeys.watchDetail(contentId, activeFileId, libraryId),
        queryFn: () => fetchWatchDetail(contentId, activeFileId, libraryId),
        staleTime: 0,
      })
      .then((detail) => {
        if (!cancelled) {
          setPlaybackVersions(detail.versions);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [
    contentId,
    libraryId,
    queryClient,
    realtimeConnectionState,
    session.loading,
    session.mediaFileId,
    session.replacing,
    session.sessionId,
  ]);

  const handleRealtimeEvent = useCallback(
    (event: PlaybackRealtimeEventEnvelope) => {
      if (event.name === "chapter_thumbnail_ready") {
        const { file_id, chapter_index, thumbnail_url, thumbnail_thumbhash } = event.payload;
        if (file_id !== session.mediaFileId) {
          return;
        }

        setPlaybackVersions((current) =>
          patchChapterThumbnail(
            current,
            file_id,
            chapter_index,
            thumbnail_url,
            thumbnail_thumbhash,
          ),
        );
        return;
      }

      if (event.name !== "markers_updated") {
        return;
      }

      const {
        file_id,
        intro: nextIntro,
        credits: nextCredits,
        recap: nextRecap,
        preview: nextPreview,
      } = event.payload;
      if (file_id !== session.mediaFileId) {
        return;
      }

      setPlaybackVersions((current) =>
        patchVersionMarkers(current, file_id, nextIntro, nextCredits, nextRecap, nextPreview),
      );
    },
    [session.mediaFileId],
  );

  // The plan is the player's contract: without one there is no transport, no
  // timeline and no track inventory to render against.
  if (!session.plan || !session.streamUrl || !session.sessionId) {
    if (session.loading) {
      return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black">
          <div className="flex flex-col items-center gap-3">
            <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/20 border-t-white" />
            <span className="text-sm text-white/60">Loading player...</span>
          </div>
        </div>
      );
    }

    return (
      <div className="bg-background fixed inset-0 z-50 flex items-center justify-center px-6">
        <div className="surface-panel-subtle flex max-w-md flex-col items-center gap-4 rounded-[1.8rem] px-8 py-8 text-center">
          <div className="space-y-2">
            <p className="text-base font-semibold text-white">
              {session.errorTitle ?? "Playback unavailable"}
            </p>
            <p className="text-sm text-white/60">
              {session.error ?? "Silo could not start playback."}
            </p>
          </div>
          <button
            onClick={() => {
              void onExit();
            }}
            type="button"
            className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-white/20"
          >
            Go Back
          </button>
        </div>
      </div>
    );
  }

  // Find the duration of the selected file so the player knows the total
  // length even when the stream is chunked (no Content-Length header).
  const selectedDuration =
    session.durationSeconds ??
    playbackVersions.find((v) => v.file_id === session.mediaFileId)?.duration ??
    playbackVersions[0]?.duration;
  const selectedVersion =
    playbackVersions.find((v) => v.file_id === session.mediaFileId) ?? playbackVersions[0];
  const activeChapters =
    (playbackVersions.find((v) => v.file_id === session.mediaFileId) ?? selectedVersion)
      ?.chapters ?? [];
  const activeMarkers = resolveActiveVersionMarkers(selectedVersion);

  return (
    <VideoPlayer
      title={title}
      year={year}
      streamUrl={session.streamUrl}
      plan={session.plan}
      planRevision={session.planRevision}
      shouldAutoPlay={session.shouldAutoPlay}
      replanning={session.replanning}
      replanError={session.error}
      replanErrorTitle={session.errorTitle}
      sessionId={session.sessionId}
      selectedVersion={selectedVersion}
      versions={playbackVersions}
      activeFileId={session.mediaFileId}
      chapters={activeChapters}
      onSwitchVersion={handleSwitchVersion}
      subtitleUrls={playableSubtitles}
      initialPosition={session.initialPosition}
      onQualitySelect={session.changeQuality}
      onSubtitleTrackChange={session.changeSubtitleTrack}
      onPlanFailure={session.recoverFromFailure}
      onReanchorSeek={session.reanchorSeek}
      onApplySubtitleTrack={session.applySubtitleTrack}
      preferredSubtitleLanguage={preferredSubtitleLanguage}
      preferredSubtitleTrackSignature={preferredSubtitleTrackSignature}
      subtitleMode={subtitleMode}
      showForcedSubtitles={showForcedSubtitles}
      profileLanguage={profileLanguage}
      intro={activeMarkers.intro}
      introSkipMode={introSkipMode}
      credits={activeMarkers.credits}
      recap={activeMarkers.recap}
      autoSkipRecap={autoSkipRecap}
      preview={activeMarkers.preview}
      autoPlayNextPreview={autoPlayNextPreview}
      canEditMarkers={canEditMarkers}
      onMarkersEdited={(fileId, markers) =>
        setPlaybackVersions((current) =>
          patchVersionMarkers(
            current,
            fileId,
            markers.intro,
            markers.credits,
            markers.recap,
            markers.preview,
          ),
        )
      }
      duration={selectedDuration}
      // The session's preference, not the caller's: the server normalizes what
      // was requested and the menu has to light up whatever it settled on.
      qualityPreference={session.qualityPreference}
      seriesContext={seriesContext}
      onNavigateEpisode={onNavigateEpisode}
      displayMode={displayMode}
      onPictureInPictureChange={onPictureInPictureChange}
      autoEnterPictureInPicture={autoEnterPictureInPicture}
      onPlaybackStateChange={handlePlaybackStateChange}
      onPlaybackTransportReady={onPlaybackTransportReady}
      onRealtimeEvent={handleRealtimeEvent}
      onRealtimeConnectionStateChange={setRealtimeConnectionState}
      onExit={onExit}
      onMinimize={onMinimize}
      onEnded={handleEnded}
      onRefreshSubtitles={session.refreshSubtitles}
      audioTracks={audioTracks}
      activeAudioIndex={session.audioTrackIndex}
      onAudioSelect={handleSwitchAudio}
      onSubtitleChanged={handleSubtitleChanged}
      onReturnFromPostRoll={onReturnFromPostRoll}
      watchTogetherRoomId={watchTogetherRoomId}
      watchTogetherConnection={watchTogetherConnection}
    />
  );
}
