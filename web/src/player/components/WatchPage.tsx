import { isSourceFallbackReason } from "@/api/v2/watchTogetherSourceFallback";
import { playbackCapabilitiesV2 } from "../start-v2";
import { playerV2Origin } from "../player-v2";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { PlayerFileVersion, PlayerPlaybackStateChange, WatchPageProps } from "../types";
import type { PlaybackRealtimeEventEnvelope } from "../realtime-protocol";
import type { SubtitleInventoryItemV3 } from "../protocol-v3";
import { usePlaybackSession } from "../hooks/usePlaybackSession";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { resolvePlayableSubtitles } from "../utils/playableSubtitles";
import { patchVersionMarkers, resolveActiveVersionMarkers } from "../utils/watchPageMarkers";
import {
  buildSubtitleChoiceRequests,
  sendSubtitleChoiceRequest,
} from "../utils/subtitleChoicePersistence";
import { VideoPlayer } from "./VideoPlayer";
import { fetchWatchDetail } from "@/hooks/queries/items";
import { itemKeys } from "@/hooks/queries/keys";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useWatchTogetherRoomConnection } from "../hooks/useWatchTogetherRoomConnection";
import { toast } from "sonner";

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
export function WatchPage(props: WatchPageProps) {
  const config = usePlayerConfig();
  return props.watchTogetherRoomId ? (
    <WatchPartyPlaybackGate
      key={`${playerV2Origin(config)}:${props.watchTogetherRoomId}`}
      {...props}
    />
  ) : (
    <WatchPagePlayer {...props} />
  );
}

function WatchPartyPlaybackGate(props: WatchPageProps) {
  const config = usePlayerConfig();
  const [status, setStatus] = useState<"checking" | "supported" | "unsupported" | "failed">(
    "checking",
  );
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let cancelled = false;
    void playbackCapabilitiesV2(config)
      .then(({ features }) => {
        if (!cancelled) {
          setStatus(
            features.includes("watch_party_coordinator_v1") &&
              features.includes("fixed_media_file_v1")
              ? "supported"
              : "unsupported",
          );
        }
      })
      .catch(() => {
        if (!cancelled) setStatus("failed");
      });
    return () => {
      cancelled = true;
    };
  }, [config, attempt]);

  if (status === "supported") return <WatchPagePlayer {...props} />;
  return (
    <div className="bg-background fixed inset-0 z-50 flex items-center justify-center px-6">
      <div className="surface-panel-subtle flex max-w-md flex-col items-center gap-4 rounded-[1.8rem] px-8 py-8 text-center">
        <p className="text-base font-semibold text-white">
          {status === "checking" ? "Checking Watch Party support..." : "Watch Party unavailable"}
        </p>
        {status !== "checking" && (
          <p className="text-sm text-white/60">
            {status === "unsupported"
              ? "This server needs an update to support Watch Party."
              : "Unable to check Watch Party support. Please try again."}
          </p>
        )}
        {status === "failed" && (
          <button
            type="button"
            className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white"
            onClick={() => {
              setStatus("checking");
              setAttempt((value) => value + 1);
            }}
          >
            Try Again
          </button>
        )}
        <button
          type="button"
          className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white"
          onClick={() => {
            void props.onExit();
          }}
        >
          Go Back
        </button>
      </div>
    </div>
  );
}

function WatchPagePlayer({
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
  initialSubtitleTrackIndexByFileId,
  initialBitmapSubtitleTrackIndexByFileId,
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
  seekIntervals,
  onReturnFromPostRoll,
  watchTogetherRoomId,
  watchTogetherRoomToken,
}: WatchPageProps) {
  const config = usePlayerConfig();
  const queryClient = useQueryClient();
  const playbackController = useWatchPlaybackController();
  const chapterRefreshAttemptsRef = useRef<Set<number>>(new Set());
  const handledSelectionRevisionRef = useRef<number | null>(null);
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
    initialSubtitleTrackIndexByFileId,
    initialBitmapSubtitleTrackIndexByFileId,
    !watchTogetherRoomId,
  );

  const fallbackHandledRef = useRef<string | null>(null);
  const [pendingFallbackKey, setPendingFallbackKey] = useState<string | null>(null);
  const fallbackRoom = watchTogetherConnection.room;
  const fallbackSource = watchTogetherConnection.fallbackSource;
  const fallbackReason = session.errorReason;
  const fallbackKey =
    watchTogetherRoomId &&
    watchTogetherRoomToken &&
    fallbackRoom &&
    fileId === fallbackRoom.selected_file_id &&
    isSourceFallbackReason(fallbackReason)
      ? `${watchTogetherRoomId}:${watchTogetherRoomToken}:${fallbackRoom.selection_revision}:${fileId}:${session.playbackAttemptId}:${fallbackReason}`
      : null;
  const fallingBack = fallbackKey !== null && pendingFallbackKey === fallbackKey;

  useEffect(() => {
    if (
      !fallbackKey ||
      fallbackHandledRef.current === fallbackKey ||
      !fallbackRoom ||
      !fileId ||
      !isSourceFallbackReason(fallbackReason) ||
      !fallbackRoom.members?.some((member) => member.is_self && member.connected) ||
      watchTogetherConnection.connectionState !== "connected"
    )
      return;
    fallbackHandledRef.current = fallbackKey;
    setPendingFallbackKey(fallbackKey);
    void playbackCapabilitiesV2(config)
      .then((capabilities) => {
        if (!capabilities.features.includes("watch_party_source_fallback_v1")) return null;
        return fallbackSource({
          selectionRevision: fallbackRoom.selection_revision,
          failedFileId: fileId,
          reason: fallbackReason,
        });
      })
      .catch(() => {
        // Keep the original playback refusal if there is no common fallback or
        // the request fails. A fresh playback attempt can try again.
      })
      .finally(() => {
        setPendingFallbackKey((current) => (current === fallbackKey ? null : current));
      });
  }, [
    config,
    fallbackKey,
    fallbackRoom,
    fallbackReason,
    fallbackSource,
    fileId,
    watchTogetherConnection.connectionState,
  ]);

  const initialSubtitleErrorKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!session.initialSubtitleError || !session.playbackAttemptId) return;
    const key = `${session.playbackAttemptId}:${session.initialSubtitleError}`;
    if (initialSubtitleErrorKeyRef.current === key) return;
    initialSubtitleErrorKeyRef.current = key;
    toast.error(session.initialSubtitleErrorTitle ?? "That subtitle track can't be used", {
      description: session.initialSubtitleError,
    });
  }, [session.initialSubtitleError, session.initialSubtitleErrorTitle, session.playbackAttemptId]);

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
        void sendSubtitleChoiceRequest(config, request).catch(() => {
          // Best effort.
        });
      }
    },
    [config, seriesContext, contentId, playableSubtitles, showForcedSubtitles],
  );

  useEffect(() => {
    chapterRefreshAttemptsRef.current.clear();
  }, [contentId, playbackRequestKey]);

  useEffect(() => {
    if (watchTogetherConnection.replacementReason) return;
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
    watchTogetherConnection.replacementReason,
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
      })
      .catch(() => {
        // Reconcile again on the next connection; keep the current markers meanwhile.
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
        marker_segments: nextSegments,
      } = event.payload;
      if (file_id !== session.mediaFileId) {
        return;
      }

      setPlaybackVersions((current) =>
        patchVersionMarkers(
          current,
          file_id,
          nextIntro,
          nextCredits,
          nextRecap,
          nextPreview,
          nextSegments,
        ),
      );
    },
    [session.mediaFileId],
  );

  // The plan is the player's contract: without one there is no transport, no
  // timeline and no track inventory to render against.
  if (!session.plan || !session.streamUrl || !session.sessionId) {
    if (session.loading || fallingBack) {
      return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black">
          <div className="flex flex-col items-center gap-3">
            <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/20 border-t-white" />
            <span className="text-sm text-white/60">
              {fallingBack ? "Finding a compatible version for everyone..." : "Loading player..."}
            </span>
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
      replanError={fallingBack ? null : session.error}
      replanErrorTitle={session.errorTitle}
      sessionId={session.sessionId}
      selectedVersion={selectedVersion}
      versions={playbackVersions}
      activeFileId={session.mediaFileId}
      chapters={activeChapters}
      onSwitchVersion={watchTogetherRoomId ? undefined : handleSwitchVersion}
      subtitleUrls={playableSubtitles}
      initialPosition={session.initialPosition}
      onQualitySelect={session.changeQuality}
      onSubtitleTrackChange={session.changeSubtitleTrack}
      onPlanFailure={session.recoverFromFailure}
      onPlanInvalidated={session.invalidatePlan}
      onReanchorSeek={session.reanchorSeek}
      onApplySubtitleTrack={session.applySubtitleTrack}
      preferredSubtitleLanguage={preferredSubtitleLanguage}
      preferredSubtitleTrackSignature={preferredSubtitleTrackSignature}
      subtitleMode={session.initialSubtitleError ? "off" : subtitleMode}
      showForcedSubtitles={session.initialSubtitleError ? false : showForcedSubtitles}
      profileLanguage={profileLanguage}
      intro={activeMarkers.intro}
      introSkipMode={introSkipMode}
      credits={activeMarkers.credits}
      recap={activeMarkers.recap}
      autoSkipRecap={autoSkipRecap}
      preview={activeMarkers.preview}
      markerSegments={selectedVersion?.marker_segments}
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
      seekIntervals={seekIntervals}
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
