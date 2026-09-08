import type { AudiobookChapterIntent } from "@/player/bound-client-timeline";
import {
  createContext,
  lazy,
  Suspense,
  useCallback,
  useContext,
  useMemo,
  useEffect,
  useState,
  useRef,
  type ReactNode,
} from "react";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  getAccessToken,
  getAuthContextVersion,
  getOrCreateDeviceId,
  getProfileToken,
  refreshAuthentication,
} from "@/api/client";
import { toast } from "sonner";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { initialPlaybackCapabilities, offerPendingInitialStart } from "@/player/initial-v2";
import { offerPendingPlaybackStops } from "@/player/session-mutations";
import type { PlaybackMutationContext } from "@/player/context/PlayerConfigContext";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { PlayerConfigProvider, type PlayerConfig } from "@/player/context/PlayerConfigContext";
import { storage } from "@/utils/storage";
import type { AudiobookPlayerControls, AudiobookPlayerStatus } from "./AudiobookPlayer";

const AudiobookPlayer = lazy(() => import("./AudiobookPlayer"));

export interface AudiobookPlaybackStartInput {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  initialChapter?: AudiobookChapterIntent;
  autoPlay?: boolean;
}

interface ActiveAudiobookPlayback extends AudiobookPlaybackStartInput {
  requestKey: number;
  authority: PlaybackMutationContext;
}

export interface AudiobookPlaybackControllerValue {
  active: AudiobookPlayerStatus | null;
  activeRequest: ActiveAudiobookPlayback | null;
  isBackgroundBarVisible: boolean;
  startPlayback: (input: AudiobookPlaybackStartInput) => void;
  stopPlayback: () => void;
  toggleActivePlayback: () => void;
}

const AudiobookPlaybackControllerContext = createContext<AudiobookPlaybackControllerValue | null>(
  null,
);

export function useAudiobookPlaybackController() {
  return useContext(AudiobookPlaybackControllerContext);
}

export function AudiobookPlaybackProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const { profile } = useCurrentProfile();
  const accountId = user?.id;
  const profileId = profile?.id;
  const [storedRequest, setActiveRequest] = useState<ActiveAudiobookPlayback | null>(null);
  const [active, setActive] = useState<AudiobookPlayerStatus | null>(null);
  const [controls, setControls] = useState<AudiobookPlayerControls | null>(null);
  const controlsRef = useRef<AudiobookPlayerControls | null>(null);
  const pendingReplacementRef = useRef<{ authority: PlaybackMutationContext } | null>(null);
  const updateControls = useCallback((next: AudiobookPlayerControls | null) => {
    controlsRef.current = next;
    setControls(next);
  }, []);
  useEffect(
    () => () => {
      pendingReplacementRef.current = null;
    },
    [],
  );
  const activeRequest = storedRequest?.authority.isCurrent() ? storedRequest : null;
  const playerConfig = useMemo<PlayerConfig>(
    () => ({
      apiBaseUrl: "/api/v1",
      capturePlaybackMutationContext: () => {
        const captured = captureProfileRequestContext();
        if (accountId == null || !captured || captured.profileId !== profileId) return null;
        return {
          accountId: String(accountId),
          profileId: captured.profileId,
          origin: captured.serverOrigin,
          isCurrent: () => isCapturedProfileAuthorityActive(captured),
        };
      },
      onPlaybackStartResolved: () => toast.dismiss("playback-start-pending"),
      onPlaybackStartError: (error, retry) =>
        toast.error("Playback unavailable", {
          id: "playback-start-pending",
          duration: Infinity,
          description: error.message,
          action: { label: "Retry", onClick: retry },
        }),
      onPlaybackStopError: (sessionId, error, retry) =>
        toast.error("Audiobook stop unconfirmed", {
          id: `audiobook-stop-${sessionId}`,
          description: error.message,
          action: { label: "Retry", onClick: retry },
        }),
      getAccessToken: () => getAccessToken(),
      getProfileId: () => storage.get(storage.KEYS.PROFILE_ID),
      getProfileToken: () => getProfileToken(),
      getDeviceId: () => getOrCreateDeviceId(),
      refreshToken: refreshAuthentication,
      getAuthContext: getAuthContextVersion,
    }),
    [accountId, profileId],
  );

  useEffect(() => {
    if (accountId == null || !profileId) return;
    let disposed = false;
    void initialPlaybackCapabilities(playerConfig)
      .then(async (cap) => {
        if (disposed || !cap.installation_id) return;
        await offerPendingInitialStart(playerConfig, cap);
        if (disposed) return;
        offerPendingPlaybackStops(playerConfig, cap.installation_id);
      })
      .catch(() => {
        /* A requested start surfaces unavailable; never falls back. */
      });
    return () => {
      disposed = true;
    };
  }, [accountId, profileId, playerConfig]);

  const startPlayback = useCallback(
    (input: AudiobookPlaybackStartInput) => {
      const authority = playerConfig.capturePlaybackMutationContext?.();
      if (!authority?.isCurrent()) {
        toast.error("Audiobook playback identity unavailable");
        return;
      }
      if (pendingReplacementRef.current?.authority.isCurrent()) return;
      const request = {
        ...input,
        files: input.files.map((file) => ({
          ...file,
          chapters: file.chapters?.map((chapter) => ({ ...chapter })),
        })),
        ...(input.initialChapter ? { initialChapter: { ...input.initialChapter } } : {}),
        authority,
        requestKey: (activeRequest?.requestKey ?? 0) + 1,
      };
      const install = () => {
        updateControls(null);
        setActive(null);
        setActiveRequest(request);
      };
      if (!activeRequest) {
        pendingReplacementRef.current = null;
        install();
        return;
      }
      const pending = { authority };
      pendingReplacementRef.current = pending;
      const isCurrent = () =>
        pendingReplacementRef.current === pending &&
        authority.isCurrent() &&
        activeRequest.authority.isCurrent();
      const finish = async () => {
        if (!isCurrent()) return;
        try {
          const previousControls = controlsRef.current;
          if (!previousControls) throw new Error("The current audiobook player is still loading");
          await previousControls.stopForReplacement();
          if (!isCurrent()) return;
          pendingReplacementRef.current = null;
          install();
        } catch (error) {
          if (isCurrent())
            toast.error("Audiobook change pending", {
              id: "audiobook-replacement-pending",
              description:
                error instanceof Error ? error.message : "The current audiobook has not stopped.",
              action: {
                label: "Retry",
                onClick: () => {
                  void finish();
                },
              },
            });
        }
      };
      void finish();
    },
    [activeRequest, playerConfig, updateControls],
  );

  const stopPlayback = useCallback(() => {
    pendingReplacementRef.current = null;
    updateControls(null);
    setActive(null);
    setActiveRequest(null);
  }, [updateControls]);

  const toggleActivePlayback = useCallback(() => {
    if (activeRequest) controls?.togglePlay();
  }, [activeRequest, controls]);

  const value = useMemo<AudiobookPlaybackControllerValue>(
    () => ({
      active: activeRequest ? active : null,
      activeRequest,
      isBackgroundBarVisible: Boolean(activeRequest),
      startPlayback,
      stopPlayback,
      toggleActivePlayback,
    }),
    [active, activeRequest, startPlayback, stopPlayback, toggleActivePlayback],
  );

  return (
    <AudiobookPlaybackControllerContext.Provider value={value}>
      {children}
      {activeRequest && (
        <PlayerConfigProvider config={playerConfig}>
          <Suspense fallback={null}>
            <AudiobookPlayer
              key={`${activeRequest.contentId}-${activeRequest.requestKey}`}
              contentId={activeRequest.contentId}
              title={activeRequest.title}
              author={activeRequest.author}
              narrator={activeRequest.narrator}
              posterUrl={activeRequest.posterUrl}
              files={activeRequest.files}
              initialPositionSeconds={activeRequest.initialPositionSeconds}
              initialChapter={activeRequest.initialChapter}
              autoPlay={activeRequest.autoPlay}
              onClose={stopPlayback}
              onPlaybackStateChange={setActive}
              onControlsChange={updateControls}
            />
          </Suspense>
        </PlayerConfigProvider>
      )}
    </AudiobookPlaybackControllerContext.Provider>
  );
}
