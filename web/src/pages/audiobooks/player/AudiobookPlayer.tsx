import { useEffect, useRef, useState } from "react";
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
    onStopRequested: onClose,
  });
  const [mode, setMode] = useState<"mini" | "now-listening">("mini");
  const lastPlaybackStateEmitRef = useRef<{
    emittedAt: number;
    duration: number;
    hasFile: boolean;
    playing: boolean;
  } | null>(null);
  const playbackStateTimerRef = useRef<number | null>(null);

  useEffect(() => {
    onControlsChange?.({ togglePlay: playback.togglePlay });
    return () => onControlsChange?.(null);
  }, [onControlsChange, playback.togglePlay]);

  useEffect(() => {
    return () => {
      if (playbackStateTimerRef.current != null) {
        window.clearTimeout(playbackStateTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (!onPlaybackStateChange) {
      return;
    }

    const status: AudiobookPlayerStatus = {
      contentId,
      playing: playback.playing,
      currentTime: playback.currentTime,
      duration: playback.duration,
      hasFile: playback.hasFile,
    };
    const last = lastPlaybackStateEmitRef.current;
    const statusChanged =
      !last ||
      last.playing !== status.playing ||
      last.duration !== status.duration ||
      last.hasFile !== status.hasFile;
    const now = Date.now();

    const emit = () => {
      lastPlaybackStateEmitRef.current = {
        emittedAt: Date.now(),
        duration: status.duration,
        hasFile: status.hasFile,
        playing: status.playing,
      };
      onPlaybackStateChange(status);
    };

    if (!last || statusChanged || now - last.emittedAt >= 1000) {
      if (playbackStateTimerRef.current != null) {
        window.clearTimeout(playbackStateTimerRef.current);
        playbackStateTimerRef.current = null;
      }
      emit();
      return;
    }

    if (playbackStateTimerRef.current == null) {
      playbackStateTimerRef.current = window.setTimeout(
        () => {
          playbackStateTimerRef.current = null;
          emit();
        },
        Math.max(0, 1000 - (now - last.emittedAt)),
      );
    }
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
