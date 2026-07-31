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

/**
 * Makes a server-issued stream path loadable by a native media element, which
 * cannot set an Authorization header, by appending the access token as a query
 * parameter.
 *
 * Under protocol v3 the plan's `stream.url` is already fully anchored by the
 * server — the seek position, the stream token, and every other routing
 * decision are baked in. This helper must not add playback semantics of its
 * own; it only carries authentication.
 */
export function buildPlayerStreamUrl(
  apiBaseUrl: string,
  streamPath: string,
  token: string | null,
): string {
  const base =
    streamPath.startsWith("http://") || streamPath.startsWith("https://")
      ? streamPath
      : `${apiBaseUrl}${streamPath}`;
  if (!token) {
    return base;
  }
  const query = new URLSearchParams({ token }).toString();
  // The backend stream URL may already carry its own query string (e.g. the
  // `?st=<streamtoken>` reconstruct token for integrated-mode direct/remux).
  // Join with `&` in that case so we don't clobber it into `st=X?token=Y`.
  const separator = base.includes("?") ? "&" : "?";
  return `${base}${separator}${query}`;
}
