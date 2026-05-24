import type { AudiobookFile } from "@/lib/audiobooks/types";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import { MiniBar } from "./MiniBar";

export interface AudiobookPlayerProps {
  contentId: string;
  title?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
}

export default function AudiobookPlayer({
  contentId,
  title,
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
      <MiniBar title={title} playback={playback} onClose={onClose} />
    </>
  );
}
