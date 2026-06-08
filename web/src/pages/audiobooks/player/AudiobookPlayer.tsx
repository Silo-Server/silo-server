import { useEffect, useState } from "react";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import { MiniBar } from "./MiniBar";
import { NowListening } from "./NowListening";

export interface AudiobookPlayerStatus {
  contentId: string;
  playing: boolean;
  currentTime: number;
  duration: number;
  hasFile: boolean;
}

export interface AudiobookPlayerControls {
  togglePlay: () => void;
}

export interface AudiobookPlayerProps {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
  onPlaybackStateChange?: (status: AudiobookPlayerStatus) => void;
  onControlsChange?: (controls: AudiobookPlayerControls | null) => void;
}

export default function AudiobookPlayer({
  contentId,
  title,
  author,
  narrator,
  posterUrl,
  files,
  initialPositionSeconds = 0,
  autoPlay = true,
  onClose,
  onPlaybackStateChange,
  onControlsChange,
}: AudiobookPlayerProps) {
  const playback = useAudiobookPlayback({
    contentId,
    files,
    initialPositionSeconds,
    autoPlay,
  });
  const [mode, setMode] = useState<"mini" | "now-listening">("mini");

  useEffect(() => {
    onControlsChange?.({ togglePlay: playback.togglePlay });
    return () => onControlsChange?.(null);
  }, [onControlsChange, playback.togglePlay]);

  useEffect(() => {
    onPlaybackStateChange?.({
      contentId,
      playing: playback.playing,
      currentTime: playback.currentTime,
      duration: playback.duration,
      hasFile: playback.hasFile,
    });
  }, [
    contentId,
    onPlaybackStateChange,
    playback.currentTime,
    playback.duration,
    playback.hasFile,
    playback.playing,
  ]);

  return (
    <>
      {playback.hasFile && (
        <audio
          ref={playback.audioRef}
          src={playback.streamUrl}
          preload="metadata"
          style={{ display: "none" }}
        />
      )}
      {mode === "mini" ? (
        <MiniBar
          contentId={contentId}
          title={title}
          posterUrl={posterUrl}
          playback={playback}
          onClose={onClose}
          onExpand={() => setMode("now-listening")}
        />
      ) : (
        <NowListening
          contentId={contentId}
          title={title}
          author={author}
          narrator={narrator}
          posterUrl={posterUrl ?? ""}
          playback={playback}
          onCollapse={() => setMode("mini")}
        />
      )}
    </>
  );
}
