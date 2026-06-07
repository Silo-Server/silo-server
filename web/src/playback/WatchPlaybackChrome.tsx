import {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { flushSync } from "react-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Pause, PictureInPicture2, Play, SkipBack, SkipForward, Tv, X } from "lucide-react";
import { useLocation, useNavigate } from "react-router";
import type { WatchDetail } from "@/api/types";
import { getAccessToken, getProfileToken } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Slider } from "@/components/ui/slider";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { fetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useContinueWatching } from "@/hooks/queries/progress";
import { useEffectiveSettings } from "@/hooks/queries/settings";
import { useWatchDetail } from "@/hooks/queries/items";
import { catalogKeys } from "@/hooks/queries/keys";
import { applyPlaybackProgressToCache } from "@/hooks/queries/playbackProgressCache";
import { invalidatePlaybackSurfaceQueries } from "@/hooks/queries/playbackSurfaceRefresh";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { PlayerConfigProvider, WatchPage } from "@/player";
import type {
  EpisodeRef,
  PlaybackExitState,
  PlayerConfig,
  PlayerPictureInPictureChange,
} from "@/player";
import { useSeriesEpisodes } from "@/player/hooks/useSeriesEpisodes";
import { PlayingNextScreen } from "@/player/components/PlayingNextScreen";
import { formatTime } from "@/player/components/SeekBar";
import { storage } from "@/utils/storage";
import { WatchPlaybackControllerContext } from "./watchPlaybackContext";
import type { WatchPlaybackControllerValue } from "./watchPlaybackContext";
import type { WatchPlaybackSnapshot, WatchPlaybackTransportControls } from "./watchPlaybackReducer";
import { createEmptyPlaybackState, watchPlaybackReducer } from "./watchPlaybackReducer";
import {
  buildWatchHref,
  buildWatchItemHref,
  buildWatchPageProps,
  createWatchRouteRequest,
  type WatchPlaybackStartInput,
  type WatchRouteRequest,
} from "@/pages/watchRouteHelpers";
import { canEditMarkers as canEditMarkersForUser } from "@/lib/permissions";

const AUTO_SKIP_INTRO_KEY = "playback.auto_skip_intro";

function resolveDeviceOverrideBool(
  setting: { effective_value: string; has_device_override: boolean } | undefined,
  profileDefault: boolean,
) {
  if (setting?.has_device_override) {
    return setting.effective_value === "true";
  }
  return profileDefault;
}

function normalizeWatchPlaybackRequest(
  input: WatchPlaybackStartInput | WatchRouteRequest,
): WatchRouteRequest {
  return "requestKey" in input ? input : createWatchRouteRequest(input);
}

function buildPlaybackSubtitle(
  request: WatchRouteRequest,
  item?: {
    title: string;
    year?: number;
    series_title?: string;
    season_number?: number;
    episode_number?: number;
  },
) {
  if (!item) {
    return undefined;
  }

  if (item.series_title && item.season_number != null && item.episode_number != null) {
    return `S${item.season_number}:E${item.episode_number}${item.title ? ` · ${item.title}` : ""}`;
  }

  if (item.year) {
    return String(item.year);
  }

  if (request.libraryId != null) {
    return `Library ${request.libraryId}`;
  }

  return undefined;
}

function findNextPlaybackPartFileId(
  item: WatchDetail | undefined,
  currentFileId?: number | null,
): number | null {
  if (!item?.playback_variants?.length || !currentFileId) {
    return null;
  }

  for (const variant of item.playback_variants) {
    const parts = [...(variant.parts ?? [])].sort((a, b) => a.part_index - b.part_index);
    const currentPartIndex = parts.findIndex((part) =>
      (part.versions ?? []).some((version) => version.file_id === currentFileId),
    );
    if (currentPartIndex === -1) {
      continue;
    }

    const nextPart = parts[currentPartIndex + 1];
    if (!nextPart) {
      return null;
    }
    if (nextPart.default_file_id != null) {
      return nextPart.default_file_id;
    }
    return nextPart.versions?.[0]?.file_id ?? null;
  }

  return null;
}

function buildPlaybackReturnHref(request: WatchRouteRequest): string {
  return request.returnHref ?? buildWatchItemHref(request);
}

function buildWatchLocationState(request: WatchRouteRequest) {
  if (
    request.returnHref == null &&
    request.audioTrackIndex == null &&
    request.prePlaySubtitleMode == null &&
    request.prePlaySubtitleSelection == null
  ) {
    return undefined;
  }

  return {
    watchReturnHref: request.returnHref,
    audioTrackIndex: request.audioTrackIndex,
    prePlaySubtitleMode: request.prePlaySubtitleMode,
    prePlaySubtitleSelection: request.prePlaySubtitleSelection ?? null,
  };
}

export function WatchPlaybackProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate();
  const [state, dispatch] = useReducer(watchPlaybackReducer, undefined, createEmptyPlaybackState);
  const stateRef = useRef(state);
  const suppressNextPictureInPictureExitRef = useRef<string | null>(null);

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  const syncRouteRequest = useCallback((request: WatchRouteRequest) => {
    dispatch({ type: "SYNC_ROUTE_REQUEST", request });
  }, []);

  const handleRouteExit = useCallback((requestKey: string) => {
    dispatch({ type: "ROUTE_LEFT", requestKey });
  }, []);

  const minimizePlayback = useCallback(() => {
    const requestKey = stateRef.current.request?.requestKey;
    if (!requestKey) {
      return;
    }

    dispatch({ type: "MINIMIZE_PLAYBACK", requestKey });
  }, []);

  const startPlayback = useCallback(
    (input: WatchPlaybackStartInput | WatchRouteRequest) => {
      const request = normalizeWatchPlaybackRequest(input);
      const current = stateRef.current;
      const currentRequestKey = current.request?.requestKey ?? null;
      const hasActivePictureInPicture =
        current.pictureInPictureActive &&
        typeof document !== "undefined" &&
        !!document.pictureInPictureElement;

      const continueStartPlayback = (forceForeground = false) => {
        if (
          !forceForeground &&
          current.request &&
          current.mode !== "foreground" &&
          current.mode !== "post-roll"
        ) {
          dispatch({
            type: "START_PLAYBACK",
            request,
            mode: "background-bar",
          });
          return;
        }

        // When transitioning from post-roll, explicitly set foreground state
        // before navigating so the new route picks up the correct mode immediately.
        if (current.mode === "post-roll") {
          dispatch({
            type: "START_PLAYBACK",
            request,
            mode: "foreground",
          });
        }

        navigate(buildWatchHref(request), {
          state: buildWatchLocationState(request),
          viewTransition: true,
        });
      };

      if (hasActivePictureInPicture) {
        if (current.mode !== "foreground" && currentRequestKey) {
          suppressNextPictureInPictureExitRef.current = currentRequestKey;
        }

        document
          .exitPictureInPicture()
          .catch(() => {
            if (
              current.mode !== "foreground" &&
              suppressNextPictureInPictureExitRef.current === currentRequestKey
            ) {
              suppressNextPictureInPictureExitRef.current = null;
            }
          })
          .finally(() => {
            window.setTimeout(() => {
              if (
                current.mode !== "foreground" &&
                suppressNextPictureInPictureExitRef.current === currentRequestKey
              ) {
                suppressNextPictureInPictureExitRef.current = null;
              }
              continueStartPlayback(true);
            }, 0);
          });
        return;
      }

      continueStartPlayback();
    },
    [navigate],
  );

  const stopPlayback = useCallback(() => {
    suppressNextPictureInPictureExitRef.current = null;
    if (typeof document !== "undefined" && document.pictureInPictureElement) {
      document.exitPictureInPicture().catch(() => {});
    }
    dispatch({ type: "STOP_PLAYBACK" });
  }, []);

  const exitPlayback = useCallback((_options?: { destinationHref?: string }) => {
    suppressNextPictureInPictureExitRef.current = null;
    if (typeof document !== "undefined" && document.pictureInPictureElement) {
      document.exitPictureInPicture().catch(() => {});
    }
    dispatch({ type: "EXIT_PLAYBACK" });
  }, []);

  const enterPostRoll = useCallback((requestKey: string) => {
    dispatch({ type: "ENTER_POSTROLL", requestKey });
  }, []);

  const returnToWatch = useCallback(() => {
    const current = stateRef.current;
    const request = current.request;
    if (!request) return;

    if (
      current.pictureInPictureActive &&
      typeof document !== "undefined" &&
      document.pictureInPictureElement
    ) {
      dispatch({ type: "REQUEST_RETURN_TO_WATCH", requestKey: request.requestKey });
      document.exitPictureInPicture().catch(() => {});
      return;
    }

    navigate(buildWatchHref(request), {
      replace: true,
      state: buildWatchLocationState(request),
      viewTransition: true,
    });
  }, [navigate]);

  const setPictureInPictureActive = useCallback(
    (requestKey: string, change: PlayerPictureInPictureChange) => {
      if (change.active) {
        dispatch({ type: "ENTER_PIP", requestKey });
        return;
      }

      const suppressed = suppressNextPictureInPictureExitRef.current === requestKey;
      if (suppressed) {
        suppressNextPictureInPictureExitRef.current = null;
      }

      dispatch({
        type: "LEAVE_PIP",
        requestKey,
        playbackContinues: change.playbackContinues,
        suppressed,
      });
    },
    [],
  );

  const clearPendingReturnNavigation = useCallback((requestKey: string) => {
    dispatch({ type: "CLEAR_PENDING_RETURN_NAVIGATION", requestKey });
  }, []);

  const updatePlaybackSnapshot = useCallback(
    (requestKey: string, snapshot: WatchPlaybackSnapshot) => {
      dispatch({ type: "UPDATE_SNAPSHOT", requestKey, snapshot });
    },
    [],
  );

  const setTransportControls = useCallback(
    (requestKey: string, controls: WatchPlaybackTransportControls | null) => {
      dispatch({ type: "SET_TRANSPORT", requestKey, transport: controls });
    },
    [],
  );

  const value = useMemo<WatchPlaybackControllerValue>(
    () => ({
      state,
      hasDetachedPlayback:
        !!state.request && state.mode !== "foreground" && state.mode !== "post-roll",
      isBackgroundBarVisible:
        !!state.request && state.mode !== "foreground" && state.mode !== "post-roll",
      startPlayback,
      minimizePlayback,
      exitPlayback,
      stopPlayback,
      enterPostRoll,
      returnToWatch,
      syncRouteRequest,
      handleRouteExit,
      setPictureInPictureActive,
      clearPendingReturnNavigation,
      updatePlaybackSnapshot,
      setTransportControls,
    }),
    [
      state,
      startPlayback,
      minimizePlayback,
      exitPlayback,
      stopPlayback,
      enterPostRoll,
      returnToWatch,
      syncRouteRequest,
      handleRouteExit,
      setPictureInPictureActive,
      clearPendingReturnNavigation,
      updatePlaybackSnapshot,
      setTransportControls,
    ],
  );

  return (
    <WatchPlaybackControllerContext.Provider value={value}>
      {children}
    </WatchPlaybackControllerContext.Provider>
  );
}

export function WatchPlaybackHost() {
  const controller = useContext(WatchPlaybackControllerContext);
  if (!controller) {
    throw new Error("Watch playback host is unavailable outside WatchPlaybackProvider");
  }

  const {
    state,
    clearPendingReturnNavigation,
    exitPlayback,
    minimizePlayback,
    setPictureInPictureActive,
    stopPlayback,
    updatePlaybackSnapshot,
    setTransportControls,
  } = controller;
  const queryClient = useQueryClient();
  const location = useLocation();
  const navigate = useNavigate();
  const { user } = useAuth();
  const { profile: currentProfile } = useCurrentProfile();
  const canEditMarkers = canEditMarkersForUser(user);
  const { data: effectivePlaybackSettings = {} } = useEffectiveSettings(currentProfile?.id, [
    AUTO_SKIP_INTRO_KEY,
  ]);
  const request = state.request;
  const isForegroundMode = request != null && state.mode === "foreground";
  const { data: item, error } = useWatchDetail(
    request?.contentId,
    request?.fileId,
    request?.libraryId,
  );
  const [renderedSession, setRenderedSession] = useState<{
    request: WatchRouteRequest;
    item: WatchDetail;
  } | null>(null);

  useEffect(() => {
    if (!request) {
      setRenderedSession(null);
      return;
    }

    if (item && item.content_id === request.contentId) {
      setRenderedSession({ request, item });
    }
  }, [item, request]);

  const hasResolvedItemForRequest = !!request && !!item && item.content_id === request.contentId;
  const activeRequest =
    hasResolvedItemForRequest || isForegroundMode ? request : (renderedSession?.request ?? null);
  const activeItem =
    hasResolvedItemForRequest || isForegroundMode
      ? hasResolvedItemForRequest
        ? item
        : null
      : (renderedSession?.item ?? null);
  const isForeground = activeRequest != null && state.mode === "foreground";
  const requestKey = activeRequest?.requestKey ?? null;

  const playerConfig = useMemo<PlayerConfig>(
    () => ({
      apiBaseUrl: "/api/v1",
      getAccessToken: () => getAccessToken(),
      getProfileId: () => storage.get(storage.KEYS.PROFILE_ID),
      getProfileToken: () => getProfileToken(),
    }),
    [],
  );

  useEffect(() => {
    if (!requestKey) return;

    return () => {
      void invalidatePlaybackSurfaceQueries(queryClient);
    };
  }, [queryClient, requestKey]);

  useEffect(() => {
    if (!request || state.pendingReturnNavigation !== request.requestKey) {
      return;
    }

    const fallbackItemHref = buildWatchItemHref(request);
    const returnHref = request.returnHref ?? fallbackItemHref;
    const currentHref = `${location.pathname}${location.search}`;
    if (currentHref === returnHref) {
      clearPendingReturnNavigation(request.requestKey);
      return;
    }

    let cancelled = false;

    const prefetchAndNavigate = async () => {
      if (request.returnHref) {
        clearPendingReturnNavigation(request.requestKey);
        navigate(returnHref, { replace: true });
        return;
      }

      try {
        await queryClient.fetchQuery({
          queryKey: catalogKeys.itemDetail(request.contentId, request.libraryId),
          queryFn: () => fetchCatalogItemDetail(request.contentId, request.libraryId),
        });
      } catch {
        // Best effort; still navigate so PiP flow is not blocked by a failed prefetch.
      }

      if (!cancelled) {
        clearPendingReturnNavigation(request.requestKey);
        navigate(fallbackItemHref, { replace: true });
      }
    };

    void prefetchAndNavigate();

    return () => {
      cancelled = true;
    };
  }, [
    clearPendingReturnNavigation,
    location.pathname,
    location.search,
    navigate,
    queryClient,
    request,
    state.pendingReturnNavigation,
  ]);

  useEffect(() => {
    if (!request || state.mode === "foreground" || !state.shouldReturnToWatchPage) {
      return;
    }

    navigate(buildWatchHref(request), {
      replace: true,
      state: buildWatchLocationState(request),
    });
  }, [navigate, request, state.mode, state.shouldReturnToWatchPage]);

  const applyExitStateToCache = useCallback(
    (exitState?: PlaybackExitState) => {
      const contentId = activeItem?.content_id ?? activeRequest?.contentId;
      if (!contentId || !exitState || exitState.positionSeconds <= 0) {
        return;
      }

      applyPlaybackProgressToCache(queryClient, {
        contentId,
        positionSeconds: exitState.positionSeconds,
        durationSeconds: exitState.durationSeconds,
        lastFileId: exitState.lastFileId,
        lastResolution: exitState.lastResolution,
        lastHDR: exitState.lastHDR,
        lastCodecVideo: exitState.lastCodecVideo,
        lastEditionKey: exitState.lastEditionKey,
      });
    },
    [activeItem, activeRequest, queryClient],
  );

  const handleExit = useCallback(
    async (exitState?: PlaybackExitState) => {
      applyExitStateToCache(exitState);

      if (state.mode !== "foreground") {
        exitPlayback(
          exitState?.destinationHref ? { destinationHref: exitState.destinationHref } : undefined,
        );
        return;
      }

      if (activeRequest) {
        if (exitState?.destinationHref) {
          exitPlayback({ destinationHref: exitState.destinationHref });
          navigate(exitState.destinationHref, {
            replace: true,
            viewTransition: true,
          });
          return;
        }

        if (activeRequest.roomId && activeRequest.roomToken) {
          exitPlayback();
          navigate(`/rooms/${activeRequest.roomId}?room_token=${activeRequest.roomToken}`, {
            replace: true,
            viewTransition: true,
            state: {
              suppressAutoStartSelection: {
                contentId: activeRequest.contentId,
                fileId: activeRequest.fileId,
                libraryId: activeRequest.libraryId,
              },
            },
          });
          return;
        }

        exitPlayback();
        navigate(buildPlaybackReturnHref(activeRequest), {
          replace: true,
          viewTransition: true,
        });
        return;
      }

      exitPlayback();
      navigate(-1);
    },
    [activeRequest, applyExitStateToCache, exitPlayback, navigate, state.mode],
  );

  const handleMinimize = useCallback(
    async (exitState?: PlaybackExitState) => {
      applyExitStateToCache(exitState);

      if (state.mode !== "foreground") {
        return;
      }

      const returnHref = activeRequest ? buildPlaybackReturnHref(activeRequest) : "/";
      flushSync(() => {
        minimizePlayback();
      });
      navigate(returnHref, {
        replace: true,
        viewTransition: true,
      });
    },
    [activeRequest, applyExitStateToCache, minimizePlayback, navigate, state.mode],
  );

  const handleNavigateEpisode = useCallback(
    (nextContentId: string) => {
      if (!activeRequest) return;
      if (activeRequest.roomId && activeRequest.roomToken) {
        navigate(`/rooms/${activeRequest.roomId}?room_token=${activeRequest.roomToken}`, {
          replace: true,
          viewTransition: true,
        });
        return;
      }

      controller.startPlayback({
        contentId: nextContentId,
        libraryId: activeRequest.libraryId,
      });
    },
    [activeRequest, controller, navigate],
  );

  // -- Series episodes for next-episode navigation --
  const seriesId = activeItem?.series_id;
  const currentSeason = activeItem?.season_number ?? 0;
  const { episodes: seriesEpisodes } = useSeriesEpisodes(
    seriesId,
    currentSeason,
    activeRequest?.libraryId,
  );

  // Find the next episode from the populated list.
  const nextEpisodeRef = useMemo<EpisodeRef | null>(() => {
    if (!activeItem?.series_id || !seriesEpisodes.length) return null;
    const idx = seriesEpisodes.findIndex(
      (ep) =>
        ep.seasonNumber === (activeItem.season_number ?? 0) &&
        ep.episodeNumber === (activeItem.episode_number ?? 0),
    );
    if (idx < 0 || idx >= seriesEpisodes.length - 1) return null;
    return seriesEpisodes[idx + 1] ?? null;
  }, [activeItem, seriesEpisodes]);

  // -- Continue watching for On Deck carousel --
  const isPostRoll = state.mode === "post-roll";
  const { items: continueWatchingItems } = useContinueWatching(undefined, {
    enabled: isPostRoll,
  });

  const requestKeyValue = activeRequest?.requestKey ?? "";

  // -- Early post-roll trigger (30s before end) --
  const POST_ROLL_SECONDS_BEFORE_END = 30;
  const postRollEnteredRef = useRef(false);
  const [postRollVideoEnded, setPostRollVideoEnded] = useState(false);

  // Refs to avoid stale closures in time-based callbacks.
  const modeRef = useRef(state.mode);
  useEffect(() => {
    modeRef.current = state.mode;
  }, [state.mode]);
  const seriesIdRef = useRef(activeItem?.series_id);
  useEffect(() => {
    seriesIdRef.current = activeItem?.series_id;
  }, [activeItem?.series_id]);

  // Reset post-roll tracking when the playback session changes.
  useEffect(() => {
    postRollEnteredRef.current = false;
    setPostRollVideoEnded(false);
  }, [requestKeyValue]);

  // -- Handle video ended → enter post-roll or exit --
  const handleEnded = useCallback(
    (exitState?: PlaybackExitState) => {
      if (!requestKeyValue) return;
      if (activeRequest?.roomId && activeRequest.roomToken) {
        stopPlayback();
        navigate(`/rooms/${activeRequest.roomId}?room_token=${activeRequest.roomToken}`, {
          replace: true,
          viewTransition: true,
        });
        return;
      }

      const nextPartFileId = findNextPlaybackPartFileId(
        activeItem ?? undefined,
        exitState?.lastFileId,
      );
      if (nextPartFileId && activeRequest) {
        applyExitStateToCache(exitState);
        controller.startPlayback({
          contentId: activeRequest.contentId,
          fileId: nextPartFileId,
          libraryId: activeRequest.libraryId,
          roomId: activeRequest.roomId,
          roomToken: activeRequest.roomToken,
          returnHref: activeRequest.returnHref,
        });
        return;
      }

      // If already in post-roll (triggered early), just mark video as ended
      // so PlayingNextScreen can start its autoplay countdown.
      if (modeRef.current === "post-roll") {
        setPostRollVideoEnded(true);
        return;
      }

      // Movies (no series_id) exit straight to the detail page — there's no
      // post-roll experience to fall through into.
      if (!activeItem?.series_id) {
        if (activeRequest) {
          stopPlayback();
          navigate(buildWatchItemHref(activeRequest));
        }
        return;
      }
      // Fallback: enter post-roll on ended if 30s threshold was missed.
      // Applies whether or not a next episode exists; the screen renders an
      // end-of-series state when nextEpisodeRef is null.
      postRollEnteredRef.current = true;
      setPostRollVideoEnded(true);
      controller.enterPostRoll(requestKeyValue);
    },
    [
      requestKeyValue,
      activeRequest,
      activeItem,
      applyExitStateToCache,
      stopPlayback,
      navigate,
      controller,
    ],
  );

  const handlePostRollClose = useCallback(() => {
    if (activeRequest) {
      stopPlayback();
      navigate(buildWatchItemHref(activeRequest));
    }
  }, [activeRequest, stopPlayback, navigate]);

  const handlePictureInPictureChange = useCallback(
    (change: PlayerPictureInPictureChange) => {
      if (!requestKeyValue) return;
      setPictureInPictureActive(requestKeyValue, change);
    },
    [requestKeyValue, setPictureInPictureActive],
  );
  const handlePlaybackStateChange = useCallback(
    (snapshot: WatchPlaybackSnapshot) => {
      if (!requestKeyValue) return;
      updatePlaybackSnapshot(requestKeyValue, snapshot);

      // Enter post-roll early when approaching end of a series episode.
      // Fires regardless of whether a next episode exists so the end-of-
      // series case still gets a graceful overlay instead of an HLS tail loop.
      if (
        !postRollEnteredRef.current &&
        seriesIdRef.current &&
        modeRef.current === "foreground" &&
        snapshot.duration > 0 &&
        snapshot.currentTime > 0 &&
        snapshot.duration - snapshot.currentTime <= POST_ROLL_SECONDS_BEFORE_END
      ) {
        postRollEnteredRef.current = true;
        controller.enterPostRoll(requestKeyValue);
      }
    },
    [requestKeyValue, updatePlaybackSnapshot, controller],
  );
  const handlePlaybackTransportReady = useCallback(
    (controls: WatchPlaybackTransportControls | null) => {
      if (!requestKeyValue) return;
      setTransportControls(requestKeyValue, controls);
    },
    [requestKeyValue, setTransportControls],
  );

  const handleReturnFromPostRoll = useCallback(() => {
    if (!activeRequest) return;
    // Keep postRollEnteredRef true so the time-based trigger doesn't
    // immediately re-enter post-roll on the next timeupdate.
    setPostRollVideoEnded(false);
    controller.syncRouteRequest(activeRequest);
  }, [activeRequest, controller]);

  if (!request) {
    return null;
  }

  if (!activeRequest || !activeItem) {
    if (!isForeground) {
      return null;
    }

    return (
      <div className="bg-background fixed inset-0 z-50 flex items-center justify-center px-6">
        <div className="surface-panel-subtle animate-in fade-in flex min-w-[260px] flex-col items-center gap-4 rounded-[1.8rem] px-8 py-7 text-center duration-300">
          <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/20 border-t-white" />
          <div className="space-y-1">
            <p className="text-sm font-medium text-white">Preparing playback</p>
            <p className="text-xs text-white/55">
              Loading stream details, subtitles, and resume state.
            </p>
          </div>
        </div>
      </div>
    );
  }

  if (error && isForeground) {
    return (
      <div className="bg-background fixed inset-0 z-50 flex items-center justify-center px-6">
        <div className="surface-panel-subtle flex max-w-md flex-col items-center gap-4 rounded-[1.8rem] px-8 py-8 text-center">
          <div className="space-y-2">
            <p className="text-base font-semibold text-white">Playback unavailable</p>
            <div className="text-sm text-white/60">
              {error instanceof Error ? error.message : "Item not found"}
            </div>
          </div>
          <button
            onClick={() => navigate(-1)}
            type="button"
            className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-white/20"
          >
            Go Back
          </button>
        </div>
      </div>
    );
  }

  const watchPageProps = buildWatchPageProps({
    request: activeRequest,
    item: activeItem,
    currentProfile,
    seriesEpisodes,
  });
  const autoSkipIntro = resolveDeviceOverrideBool(
    effectivePlaybackSettings[AUTO_SKIP_INTRO_KEY],
    watchPageProps.autoSkipIntro ?? false,
  );

  const playerDisplayMode =
    state.mode === "foreground" ? "foreground" : isPostRoll ? "postroll" : "detached";

  return (
    <PlayerConfigProvider config={playerConfig}>
      {(isForeground || isPostRoll) && <WatchPlaybackTitle title={activeItem.title} />}
      <WatchPage
        {...watchPageProps}
        autoSkipIntro={autoSkipIntro}
        canEditMarkers={canEditMarkers}
        playbackRequestKey={requestKeyValue}
        onNavigateEpisode={handleNavigateEpisode}
        onEnded={handleEnded}
        onExit={handleExit}
        onMinimize={handleMinimize}
        displayMode={playerDisplayMode}
        autoEnterPictureInPicture={state.autoEnterPictureInPicture}
        onPictureInPictureChange={handlePictureInPictureChange}
        onPlaybackStateChange={handlePlaybackStateChange}
        onPlaybackTransportReady={handlePlaybackTransportReady}
        onReturnFromPostRoll={isPostRoll ? handleReturnFromPostRoll : undefined}
      />
      {isPostRoll && (
        <PlayingNextScreen
          seriesId={activeItem.series_id}
          seriesTitle={activeItem.series_title}
          nextEpisode={nextEpisodeRef ?? undefined}
          continueWatchingItems={continueWatchingItems}
          videoEnded={postRollVideoEnded}
          onPlayNow={
            nextEpisodeRef ? () => handleNavigateEpisode(nextEpisodeRef.contentId) : undefined
          }
          onPlayItem={(contentId: string) => handleNavigateEpisode(contentId)}
          onClose={handlePostRollClose}
        />
      )}
    </PlayerConfigProvider>
  );
}

export function WatchPlaybackBar() {
  const controller = useContext(WatchPlaybackControllerContext);
  if (!controller) {
    throw new Error("Watch playback bar is unavailable outside WatchPlaybackProvider");
  }

  const { state, isBackgroundBarVisible, returnToWatch, stopPlayback } = controller;
  const request = state.request;
  const snapshot = state.snapshot;
  const transport = state.transport;
  const { data: item } = useWatchDetail(request?.contentId, request?.fileId, request?.libraryId);
  const [scrubValue, setScrubValue] = useState<number | null>(null);

  if (!isBackgroundBarVisible || !request) {
    return null;
  }

  const title = item?.series_title ?? item?.title ?? "Preparing playback";
  const subtitle = buildPlaybackSubtitle(request, item);
  const displayedTime = scrubValue ?? snapshot?.currentTime ?? 0;

  return (
    <div className="pointer-events-none fixed inset-x-3 bottom-3 z-40 flex justify-center">
      <div className="glass-dark border-border/70 pointer-events-auto w-full max-w-4xl rounded-2xl border px-4 py-3 shadow-[0_24px_80px_-32px_rgba(0,0,0,0.7)] backdrop-blur-xl">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <div className="bg-primary/15 text-primary flex h-10 w-10 shrink-0 items-center justify-center rounded-xl">
                <Play className="ml-0.5 h-4 w-4 fill-current" />
              </div>
              <div className="min-w-0">
                <div className="truncate text-sm font-semibold text-white">{title}</div>
                <div className="text-xs text-white/60">
                  {subtitle ?? (snapshot ? "Background playback" : "Preparing playback")}
                </div>
              </div>
              {state.pictureInPictureActive && (
                <div className="hidden shrink-0 items-center gap-1 rounded-full bg-white/8 px-2.5 py-1 text-[11px] font-medium text-white/75 sm:flex">
                  <PictureInPicture2 className="h-3.5 w-3.5" />
                  PiP
                </div>
              )}
            </div>

            <div className="mt-3 space-y-1.5">
              <Slider
                value={[displayedTime]}
                min={0}
                max={Math.max(snapshot?.duration ?? 0, 0)}
                step={1}
                thumbLabels={["Playback position"]}
                disabled={!transport || !snapshot || snapshot.duration <= 0}
                className="[&_[data-slot=slider-range]]:bg-primary -my-2 py-2 [&_[data-slot=slider-thumb]]:size-4 [&_[data-slot=slider-thumb]]:border-white/60 [&_[data-slot=slider-thumb]]:bg-white [&_[data-slot=slider-thumb]]:shadow-[0_2px_10px_rgba(0,0,0,0.35)] [&_[data-slot=slider-track]]:h-1.5 [&_[data-slot=slider-track]]:bg-white/10"
                onValueChange={([value]) => {
                  setScrubValue(value ?? 0);
                }}
                onValueCommit={([value]) => {
                  const nextValue = value ?? 0;
                  setScrubValue(null);
                  transport?.seekTo(nextValue);
                }}
              />
              <div className="flex items-center justify-between text-[11px] text-white/55 tabular-nums">
                <span>{formatTime(displayedTime)}</span>
                <span>{snapshot ? formatTime(snapshot.duration) : "0:00"}</span>
              </div>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Button
              variant="glass"
              size="icon"
              className="h-10 w-10 rounded-full"
              onClick={() => transport?.seekBy(-10)}
              disabled={!transport}
              title="Back 10 seconds"
            >
              <SkipBack className="h-4 w-4" />
            </Button>
            <Button
              className="h-10 rounded-full px-4"
              onClick={() => transport?.playPause()}
              disabled={!transport}
            >
              {snapshot?.playing ? (
                <Pause className="mr-2 h-4 w-4" />
              ) : (
                <Play className="mr-2 h-4 w-4 fill-current" />
              )}
              {snapshot?.playing ? "Pause" : "Play"}
            </Button>
            <Button
              variant="glass"
              size="icon"
              className="h-10 w-10 rounded-full"
              onClick={() => transport?.seekBy(10)}
              disabled={!transport}
              title="Forward 10 seconds"
            >
              <SkipForward className="h-4 w-4" />
            </Button>
            <Button
              variant="glass"
              className="h-10 rounded-full px-4"
              onClick={returnToWatch}
              title="Return to player"
            >
              <Tv className="mr-2 h-4 w-4" />
              Watch
            </Button>
            <Button
              variant="glass"
              size="icon"
              className="h-10 w-10 rounded-full"
              onClick={stopPlayback}
              title="Stop playback"
            >
              <X className="h-4 w-4" />
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

function WatchPlaybackTitle({ title }: { title: string }) {
  useDocumentTitle(title);
  return null;
}
