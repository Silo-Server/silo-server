import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import type { AudiobookFile } from "@/lib/audiobooks/types";
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

const AudiobookPlaybackControllerContext =
  createContext<AudiobookPlaybackControllerValue | null>(null);

export function useAudiobookPlaybackController() {
  return useContext(AudiobookPlaybackControllerContext);
}

export function AudiobookPlaybackProvider({ children }: { children: ReactNode }) {
  const [activeRequest, setActiveRequest] = useState<ActiveAudiobookPlayback | null>(null);
  const [active, setActive] = useState<AudiobookPlayerStatus | null>(null);
  const [controls, setControls] = useState<AudiobookPlayerControls | null>(null);

  const startPlayback = useCallback((input: AudiobookPlaybackStartInput) => {
    setControls(null);
    setActive({
      contentId: input.contentId,
      playing: false,
      currentTime: input.initialPositionSeconds ?? 0,
      duration: 0,
      hasFile: input.files.length > 0,
    });
    setActiveRequest((previous) => ({
      ...input,
      requestKey: (previous?.requestKey ?? 0) + 1,
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
      )}
    </AudiobookPlaybackControllerContext.Provider>
  );
}
