import { useState } from "react";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import { MiniBar } from "./MiniBar";
import { NowListening } from "./NowListening";

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
}: AudiobookPlayerProps) {
  const playback = useAudiobookPlayback({
    contentId,
    files,
    initialPositionSeconds,
    autoPlay,
  });
  const [mode, setMode] = useState<"mini" | "now-listening">("mini");

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
