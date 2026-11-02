/** How the engine must interpret decoded video samples. */
export type EngineTransfer = "sdr" | "pq" | "hlg";

/**
 * Everything the engine needs to play one `original_http` plan, resolved on
 * the page from the plan and handed to the engine as plain data.
 */
export interface EngineSourceSpec {
  url: string;
  headers: Record<string, string>;
  /** Server container name: `mkv` (Matroska and WebM) or `mp4`. */
  container: string;
  /** Plan runtime; the engine falls back to the container's own when null. */
  durationSeconds: number | null;
  /** Source position playback starts from. */
  startSeconds: number;
  video: {
    codec: string;
    width: number;
    height: number;
    transfer: EngineTransfer;
    /**
     * Drop Dolby Vision RPU and enhancement-layer NAL units before decoding,
     * so an ordinary HEVC decoder sees only the base layer.
     */
    stripDolbyVision: boolean;
  };
  /** The selected audio track, by ordinal among the file's audio tracks. */
  audio: { ordinal: number; codec: string; channels: number | null } | null;
}

/**
 * Fetch errors quote the stream URL, which carries the stream token and, for
 * API-local routes, the access token. A failure message goes to the server and
 * its logs, so the URL is left out: the plan's own URL wherever it appears,
 * any absolute URL, and any relative path with a token in its query.
 */
export function redactStreamUrls(message: string, streamUrl?: string): string {
  const withoutStream = streamUrl ? message.split(streamUrl).join("<stream URL>") : message;
  return withoutStream
    .replace(/https?:\/\/\S+?(?=:?(?:\s|$))/g, "<stream URL>")
    .replace(/(^|\s)\/\S*?[?&](?:token|st)=\S+?(?=:?(?:\s|$))/g, "$1<stream URL>");
}
