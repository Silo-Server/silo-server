import { CircleAlert } from "lucide-react";
import "@/styles/player-hud.css";
import { isSourceFallbackReason } from "@/api/v2/watchTogetherSourceFallback";
import { playbackCapabilitiesV2 } from "../start-v2";
import { playerV2Origin } from "../player-v2";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { PlayerFileVersion, PlayerPlaybackStateChange, WatchPageProps } from "../types";
import type { PlaybackRealtimeEventEnvelope } from "../realtime-protocol";
import { usePlaybackSession } from "../hooks/usePlaybackSession";
import { useMarkerChangeGuard } from "../hooks/useMarkerChangeGuard";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { resolvePlayableSubtitles } from "../utils/playableSubtitles";
import { patchVersionMarkers, resolveActiveVersionMarkers } from "../utils/watchPageMarkers";
import { VideoPlayer } from "./VideoPlayer";
import { PlayerLoadingCover } from "./PlayerLoadingCover";
import { fetchWatchDetail } from "@/hooks/queries/items";
import { transientTrickplayError, useWatchTrickplay } from "@/hooks/queries/trickplay";
import { itemKeys } from "@/hooks/queries/keys";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useWatchTogetherRoomConnection } from "../hooks/useWatchTogetherRoomConnection";
import { useTrackChoicePersistence } from "../hooks/useTrackChoicePersistence";
import { useMarkerReportSettings } from "@/hooks/queries/markerReportSettings";
import { toast } from "sonner";
import { useT } from "@/i18n";

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
  const t = useT();
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
    <div className="hud fixed inset-0 z-50 flex items-center justify-center bg-black px-6">
      <div className="hud-card flex w-full max-w-md flex-col items-center gap-4 px-8 py-8 text-center">
        <p className="text-lg font-semibold">
          {status === "checking"
            ? t("web.player.watchTogether.checkingSupport")
            : t("web.player.watchTogether.unavailable")}
        </p>
        {status !== "checking" && (
          <p className="text-sm leading-relaxed text-[var(--hud-ash)]">
            {status === "unsupported"
              ? t("web.player.watchTogether.unsupported")
              : t("web.player.watchTogether.checkFailed")}
          </p>
        )}
        {status === "failed" && (
          <button
            type="button"
            className="hud-primary-btn"
            onClick={() => {
              setStatus("checking");
              setAttempt((value) => value + 1);
            }}
          >
            {t("web.player.watch.tryAgain")}
          </button>
        )}
        <button
          type="button"
          className="hud-secondary-btn"
          onClick={() => {
            void props.onExit();
          }}
        >
          {t("player.goBack")}
        </button>
      </div>
    </div>
  );
}

function WatchPagePlayer({
  contentId,
  title,
  year,
  backdrop,
  playbackRequestKey,
  fileId,
  versionPicked = false,
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
  introSkipMode,
  showMarkerConfidence = false,
  showMarkerSource = false,
  bufferingMode = "always",
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
  const t = useT();
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
    bufferingMode,
    versionPicked,
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
    toast.error(session.initialSubtitleErrorTitle ?? t("web.player.watch.subtitleTrackUnusable"), {
      description: session.initialSubtitleError,
    });
  }, [
    session.initialSubtitleError,
    session.initialSubtitleErrorTitle,
    session.playbackAttemptId,
    t,
  ]);

  const audioTracks = useMemo(
    () => playbackVersions.find((v) => v.file_id === session.mediaFileId)?.audio_tracks ?? [],
    [playbackVersions, session.mediaFileId],
  );
  const playableSubtitles = useMemo(
    () => resolvePlayableSubtitles(session.subtitleUrls, subtitles),
    [session.subtitleUrls, subtitles],
  );

  const handleSwitchVersion = useCallback(
    (newFileId: number, currentPosition: number, onAdopt?: (planRevision?: number) => void) => {
      session.switchVersion(newFileId, currentPosition, onAdopt);
    },
    [session],
  );

  const activePlaybackVersion = useMemo(
    () => playbackVersions.find((version) => version.file_id === session.mediaFileId),
    [playbackVersions, session.mediaFileId],
  );
  const trickplayAvailable = activePlaybackVersion?.trickplay_available === true;
  const trickplayQuery = useWatchTrickplay(
    contentId,
    session.mediaFileId ?? undefined,
    trickplayAvailable,
  );
  // A failed refresh retains query data. A terminal response withdraws the
  // cached previews; transient failures keep them until the query recovers.
  const trickplay =
    trickplayAvailable && (!trickplayQuery.isError || transientTrickplayError(trickplayQuery.error))
      ? (trickplayQuery.data ?? null)
      : null;
  const refetchTrickplay = trickplayQuery.refetch;
  const lastTrickplayRefresh = useRef<{ fileId: number | null; at: number } | null>(null);
  const trickplayRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    lastTrickplayRefresh.current = null;
    return () => {
      if (trickplayRefreshTimer.current !== null) clearTimeout(trickplayRefreshTimer.current);
      trickplayRefreshTimer.current = null;
    };
  }, [session.mediaFileId, trickplayAvailable]);
  useEffect(() => {
    // The query owns retry and polling delays after a manifest request fails.
    if (trickplayQuery.isError && trickplayRefreshTimer.current !== null) {
      clearTimeout(trickplayRefreshTimer.current);
      trickplayRefreshTimer.current = null;
    }
  }, [trickplayQuery.isError]);
  const handleTrickplayError = useCallback(() => {
    if (trickplayQuery.isError) return;
    const now = Date.now();
    const previous = lastTrickplayRefresh.current;
    const refresh = () => {
      trickplayRefreshTimer.current = null;
      lastTrickplayRefresh.current = { fileId: session.mediaFileId, at: Date.now() };
      void refetchTrickplay({ cancelRefetch: false });
    };
    if (previous?.fileId === session.mediaFileId && now - previous.at < 60_000) {
      if (trickplayRefreshTimer.current === null) {
        trickplayRefreshTimer.current = setTimeout(refresh, 60_000 - (now - previous.at));
      }
      return;
    }
    if (trickplayRefreshTimer.current !== null) clearTimeout(trickplayRefreshTimer.current);
    refresh();
  }, [refetchTrickplay, session.mediaFileId, trickplayQuery.isError]);

  const versionPinned = session.versionPinned;
  const handleEnded = useCallback(() => {
    onEnded?.({
      positionSeconds: session.durationSeconds ?? 0,
      durationSeconds: session.durationSeconds ?? undefined,
      lastFileId: session.mediaFileId,
      lastResolution: activePlaybackVersion?.resolution,
      lastHDR: activePlaybackVersion?.hdr,
      lastCodecVideo: activePlaybackVersion?.codec_video,
      lastEditionKey: activePlaybackVersion?.edition_key,
      versionPinned: versionPinned(),
    });
  }, [activePlaybackVersion, onEnded, session.durationSeconds, session.mediaFileId, versionPinned]);

  const { saveAudioChoice, saveSubtitleChoice } = useTrackChoicePersistence({
    seriesId: seriesContext?.seriesId ?? contentId,
    audioTracks,
    subtitleTracks: playableSubtitles,
  });
  // A viewer's audio pick from the player menu: switch now, save for the series.
  const handleSwitchAudio = useCallback(
    (index: number, currentPosition: number) => {
      session.switchAudioTrack(index, currentPosition);
      saveAudioChoice(index);
    },
    [saveAudioChoice, session],
  );

  const updatePlaybackState = session.updatePlaybackState;
  const handlePlaybackStateChange = useCallback(
    (state: PlayerPlaybackStateChange) => {
      updatePlaybackState(state.currentTime, state.playing);
      onPlaybackStateChange?.(state);
    },
    [onPlaybackStateChange, updatePlaybackState],
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
    // The room changed its selection; this viewer did not press Play.
    playbackController.startPlayback(
      {
        contentId: room.selected_content_id,
        fileId: room.selected_file_id,
        libraryId: room.selected_library_id,
        roomId: watchTogetherRoomId,
        roomToken: watchTogetherRoomToken,
        restart: true,
      },
      "automatic",
    );
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

    void queryClient
      .fetchQuery({
        queryKey: itemKeys.watchDetail(contentId, fileId, libraryId),
        queryFn: () => fetchWatchDetail(contentId, fileId, libraryId),
        staleTime: 0,
      })
      .catch(() => {
        // The result arrives through the versions prop. A failed read leaves
        // the chapters as they are. A read that a marker change restarted
        // rejects here only if this call joined it; its replacement still runs.
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

  const applyMarkerChange = useMarkerChangeGuard({
    contentId,
    fileId,
    libraryId,
    versions,
    setPlaybackVersions,
    connected: realtimeConnectionState === "connected",
    sessionId: session.sessionId,
    activeFileId: session.mediaFileId,
    busy: session.loading || session.replacing,
  });

  const handleRealtimeEvent = useCallback(
    (event: PlaybackRealtimeEventEnvelope) => {
      if (event.name === "marker_thumbnail_ready") {
        const image = event.payload;
        if (image.file_id !== session.mediaFileId || image.session_id !== session.sessionId) return;
        setPlaybackVersions((current) =>
          current.map((version) =>
            version.file_id !== image.file_id
              ? version
              : {
                  ...version,
                  marker_segments: version.marker_segments?.map((marker) =>
                    marker.kind === image.kind &&
                    marker.start_seconds === image.start_seconds &&
                    marker.end_seconds === image.end_seconds
                      ? {
                          ...marker,
                          thumbnail_url: image.thumbnail_url,
                          thumbnail_thumbhash: image.thumbnail_thumbhash,
                          thumbnail_capture_seconds: image.thumbnail_capture_seconds,
                        }
                      : marker,
                  ),
                },
          ),
        );
        return;
      }
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

      applyMarkerChange((current) =>
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
    [applyMarkerChange, session.mediaFileId, session.sessionId],
  );

  // The viewer's own TheIntroDB settings for the report control.
  //
  // This MUST stay above the session guard below. The guard returns early while
  // the playback session loads, so a hook placed after it runs on the second
  // render and not the first, and React rejects a component whose hook count
  // changes between renders — the whole player fails to mount.
  const markerReport = useMarkerReportSettings({ enabled: true });

  // The plan is the player's contract: without one there is no transport, no
  // timeline and no track inventory to render against.
  if (!session.plan || !session.streamUrl || !session.sessionId) {
    if (session.loading || fallingBack) {
      return (
        <PlayerLoadingCover
          backdrop={backdrop}
          label={
            fallingBack
              ? t("web.player.watch.findingCompatibleVersion")
              : t("web.player.watch.loadingPlayer")
          }
          className="fixed inset-0 z-50"
        >
          {fallingBack ? (
            <span className="text-sm text-[var(--hud-ash)]">
              {t("web.player.watch.findingCompatibleVersion")}
            </span>
          ) : null}
        </PlayerLoadingCover>
      );
    }

    return (
      <div className="hud fixed inset-0 z-50 flex items-center justify-center bg-black bg-[radial-gradient(ellipse_at_center,rgb(255_255_255/0.06),transparent_60%)] px-6">
        <div
          role="alert"
          className="hud-card flex w-full max-w-lg flex-col items-center gap-6 px-10 py-10 text-center"
        >
          <span
            aria-hidden="true"
            className="flex size-14 items-center justify-center rounded-full bg-white/[0.08] text-[var(--hud-lamp)]"
          >
            <CircleAlert className="size-7" strokeWidth={1.75} />
          </span>
          <div className="space-y-2.5">
            <p className="text-xl font-semibold tracking-tight">
              {session.errorTitle ?? t("web.player.watch.playbackUnavailable")}
            </p>
            <p className="text-[0.9375rem] leading-relaxed text-[var(--hud-ash)]">
              {session.error ?? t("web.player.watch.startFailed")}
            </p>
          </div>
          <button
            onClick={() => {
              void onExit();
            }}
            type="button"
            className="hud-primary-btn min-h-12 px-7"
          >
            {t("player.goBack")}
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
      backdrop={backdrop}
      streamUrl={session.streamUrl}
      plan={session.plan}
      planRevision={session.planRevision}
      browserEngine={session.browserEngine}
      nativeOriginalSupport={session.nativeOriginalSupport}
      shouldAutoPlay={session.shouldAutoPlay}
      replanning={session.replanning}
      replacing={session.replacing}
      replanError={fallingBack ? null : session.error}
      replanErrorTitle={session.errorTitle}
      sessionId={session.sessionId}
      selectedVersion={selectedVersion}
      versions={playbackVersions}
      activeFileId={session.mediaFileId}
      chapters={activeChapters}
      trickplay={trickplay}
      trickplayUpdatedAt={trickplayQuery.dataUpdatedAt}
      onTrickplayError={handleTrickplayError}
      onSwitchVersion={watchTogetherRoomId ? undefined : handleSwitchVersion}
      subtitleUrls={playableSubtitles}
      initialPosition={session.initialPosition}
      onQualitySelect={session.changeQuality}
      onSubtitleTrackChange={session.changeSubtitleTrack}
      onPlanFailure={session.recoverFromFailure}
      onConnectionLost={session.recoverConnection}
      onRetryConnection={session.retryConnection}
      connectionStatus={session.connectionStatus}
      connectionErrorTitle={session.connectionErrorTitle}
      connectionError={session.connectionError}
      onPlanInvalidated={session.invalidatePlan}
      onReanchorSeek={session.reanchorSeek}
      onSeek={session.reportSeek}
      onApplySubtitleTrack={session.applySubtitleTrack}
      preferredSubtitleLanguage={preferredSubtitleLanguage}
      preferredSubtitleTrackSignature={preferredSubtitleTrackSignature}
      subtitleMode={session.initialSubtitleError ? "off" : subtitleMode}
      showForcedSubtitles={session.initialSubtitleError ? false : showForcedSubtitles}
      intro={activeMarkers.intro}
      introSkipMode={introSkipMode}
      showMarkerConfidence={showMarkerConfidence}
      showMarkerSource={showMarkerSource}
      markerReportInstallationId={markerReport.installationId}
      markerReportContentId={contentId}
      bufferingMode={bufferingMode}
      bandwidthCapKbps={maxBitrateKbps}
      bufferingTitleId={contentId}
      playbackAttemptId={session.playbackAttemptId}
      onKeptEstimate={session.setKeptEstimate}
      onBufferingSummary={(plan, summary, pageEnding) =>
        session.reportEvent("plan_summary", {
          adoptionId: plan.adoption_id,
          diagnostics: { ...summary },
          keepalive: pageEnding,
        })
      }
      credits={activeMarkers.credits}
      recap={activeMarkers.recap}
      autoSkipRecap={autoSkipRecap}
      preview={activeMarkers.preview}
      markerSegments={selectedVersion?.marker_segments}
      autoPlayNextPreview={autoPlayNextPreview}
      canEditMarkers={canEditMarkers}
      onMarkersEdited={(editedFileId, markers) =>
        applyMarkerChange((current) =>
          patchVersionMarkers(
            current,
            editedFileId,
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
      onFirstFrame={session.reportFirstFrame}
      onRealtimeEvent={handleRealtimeEvent}
      onRealtimeConnectionStateChange={setRealtimeConnectionState}
      onExit={onExit}
      onMinimize={onMinimize}
      onEnded={handleEnded}
      onRefreshSubtitles={session.refreshSubtitles}
      audioTracks={audioTracks}
      activeAudioIndex={session.audioTrackIndex}
      onAudioSelect={handleSwitchAudio}
      onSubtitleChanged={saveSubtitleChoice}
      onReturnFromPostRoll={onReturnFromPostRoll}
      watchTogetherRoomId={watchTogetherRoomId}
      watchTogetherConnection={watchTogetherConnection}
    />
  );
}
