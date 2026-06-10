import type { PlayMethod } from "./types";

const preconnectedOrigins = new Set<string>();

/**
 * Warm the connection (DNS + TCP + TLS) to a stream origin as soon as it is
 * known. In distributed deployments the stream URL points at a proxy node the
 * browser has never contacted, and without this the first manifest request
 * pays all the handshakes after the transcode has already started.
 */
export function preconnectToStreamOrigin(streamUrl: string): void {
  if (!streamUrl.startsWith("http://") && !streamUrl.startsWith("https://")) return;
  let origin: string;
  try {
    origin = new URL(streamUrl).origin;
  } catch {
    return;
  }
  if (typeof document === "undefined" || origin === window.location.origin) return;
  if (preconnectedOrigins.has(origin)) return;
  preconnectedOrigins.add(origin);

  const link = document.createElement("link");
  link.rel = "preconnect";
  link.href = origin;
  // hls.js fetches are anonymous-mode CORS requests; the warmed connection
  // is only reused when the preconnect uses the same credentials mode.
  link.crossOrigin = "anonymous";
  document.head.appendChild(link);
}

export function buildPlayerStreamUrl(
  apiBaseUrl: string,
  streamPath: string,
  token: string | null,
  playMethod: PlayMethod,
  initialPosition: number,
): string {
  const params = new URLSearchParams();

  if (token) {
    params.set("token", token);
  }

  if (playMethod === "remux" && initialPosition > 0) {
    params.set("seek", initialPosition.toFixed(3));
  }

  const query = params.toString();
  const base =
    streamPath.startsWith("http://") || streamPath.startsWith("https://")
      ? streamPath
      : `${apiBaseUrl}${streamPath}`;
  return `${base}${query ? `?${query}` : ""}`;
}
