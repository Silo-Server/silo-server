import type { AudiobookFile } from "@/lib/audiobooks/types";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import { MiniBar } from "./MiniBar";

export interface AudiobookPlayerProps {
  contentId: string;
  title?: string;
  posterUrl?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
  onExpand?: () => void;
}

export default function AudiobookPlayer({
  contentId,
  title,
  posterUrl,
  files,
  initialPositionSeconds = 0,
  autoPlay = true,
  onClose,
  onExpand,
}: AudiobookPlayerProps) {
  const playback = useAudiobookPlayback({
    contentId,
    files,
    initialPositionSeconds,
    autoPlay,
  });

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
      <MiniBar
        contentId={contentId}
        title={title}
        posterUrl={posterUrl}
        playback={playback}
        onClose={onClose}
        onExpand={onExpand}
      />
    </>
  );
}
