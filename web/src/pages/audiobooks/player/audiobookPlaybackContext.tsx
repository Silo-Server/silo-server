import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { getAccessToken, getProfileToken } from "@/api/client";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { PlayerConfigProvider, type PlayerConfig } from "@/player";
import { storage } from "@/utils/storage";
import AudiobookPlayer, {
  type AudiobookPlayerControls,
  type AudiobookPlayerStatus,
} from "./AudiobookPlayer";

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
  const playerConfig = useMemo<PlayerConfig>(
    () => ({
      apiBaseUrl: "/api/v1",
      getAccessToken: () => getAccessToken(),
      getProfileId: () => storage.get(storage.KEYS.PROFILE_ID),
      getProfileToken: () => getProfileToken(),
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
        </PlayerConfigProvider>
      )}
    </AudiobookPlaybackControllerContext.Provider>
  );
}
