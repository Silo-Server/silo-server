import {
  createContext,
  lazy,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  getAccessToken,
  getAuthContextVersion,
  getOrCreateDeviceId,
  getProfileToken,
  refreshAuthentication,
} from "@/api/client";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
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
  autoPlay?: boolean;
}

interface ActiveAudiobookPlayback extends AudiobookPlaybackStartInput {
  requestKey: number;
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
  const [activeRequest, setActiveRequest] = useState<ActiveAudiobookPlayback | null>(null);
  const [active, setActive] = useState<AudiobookPlayerStatus | null>(null);
  const [controls, setControls] = useState<AudiobookPlayerControls | null>(null);
  const { profile } = useCurrentProfile();
  const profileId = profile?.id ?? null;
  const playbackProfileRef = useRef<string | null>(null);
  const playerConfig = useMemo<PlayerConfig>(
    () => ({
      apiBaseUrl: "/api/v1",
      getAccessToken: () => getAccessToken(),
      getProfileId: () => storage.get(storage.KEYS.PROFILE_ID),
      getProfileToken: () => getProfileToken(),
      getDeviceId: () => getOrCreateDeviceId(),
      refreshToken: refreshAuthentication,
      getAuthContext: getAuthContextVersion,
    }),
    [],
  );

  const startPlayback = useCallback((input: AudiobookPlaybackStartInput) => {
    setControls(null);
    setActive(null);
    setActiveRequest((previous) => ({
      ...input,
      requestKey:
        previous?.contentId === input.contentId
          ? previous.requestKey
          : (previous?.requestKey ?? 0) + 1,
    }));
  }, []);

  const stopPlayback = useCallback(() => {
    setControls(null);
    setActive(null);
    setActiveRequest(null);
  }, []);

  // The book belongs to the profile that started it: a profile switch must
  // not keep it playing or report its progress under the new profile.
  useEffect(() => {
    if (!activeRequest) {
      playbackProfileRef.current = null;
      return;
    }
    if (playbackProfileRef.current === null) {
      playbackProfileRef.current = profileId;
      return;
    }
    if (playbackProfileRef.current !== profileId) {
      playbackProfileRef.current = null;
      stopPlayback();
    }
  }, [activeRequest, profileId, stopPlayback]);

  const toggleActivePlayback = useCallback(() => {
    controls?.togglePlay();
  }, [controls]);

  const value = useMemo<AudiobookPlaybackControllerValue>(
    () => ({
      active,
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
              autoPlay={activeRequest.autoPlay}
              onClose={stopPlayback}
              onPlaybackStateChange={setActive}
              onControlsChange={setControls}
            />
          </Suspense>
        </PlayerConfigProvider>
      )}
    </AudiobookPlaybackControllerContext.Provider>
  );
}
