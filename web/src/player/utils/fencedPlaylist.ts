import type {
  HlsConfig,
  Loader,
  LoaderCallbacks,
  LoaderConfiguration,
  LoaderContext,
  LoaderResponse,
  LoaderStats,
  PlaylistLoaderConstructor,
  PlaylistLoaderContext,
} from "hls.js";

/** Query parameter the server puts on every segment URI to name its generation. */
export const GENERATION_QUERY_PARAM = "sgen";

/**
 * How long the playlist gate waits before fetching a fenced playlist again.
 * The server rotates the generation before it restarts FFmpeg, so a short
 * wait is enough for the manifest route to serve the successor's URIs.
 */
export const FENCED_PLAYLIST_RETRY_DELAY_MS = 500;

/**
 * Playlist refetches the gate attempts while the server still names the fenced
 * generation. A restart that takes longer than this is left to the ordinary
 * path instead of holding the player forever.
 */
export const FENCED_PLAYLIST_MAX_RETRIES = 8;

/** The generation a segment URI names, or null when it carries no `sgen`. */
export function generationTokenFromSegmentUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    return new URL(url, "http://localhost").searchParams.get(GENERATION_QUERY_PARAM);
  } catch {
    return null;
  }
}

function playlistUris(body: string): string[] {
  const uris: string[] = [];
  for (const rawLine of body.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) continue;
    if (line.startsWith("#")) {
      // EXT-X-MAP/KEY/MEDIA carry their resource in a URI attribute.
      const match = /URI="([^"]+)"/.exec(line);
      if (match?.[1]) uris.push(match[1]);
      continue;
    }
    uris.push(line);
  }
  return uris;
}

/**
 * Whether a playlist still addresses `token`. A media playlist lists segment
 * URIs carrying the token; a multivariant playlist lists variants that do not,
 * so the answer is false and the gate lets it through to be followed.
 */
export function playlistNamesGeneration(body: string, token: string, baseUrl: string): boolean {
  if (!body || !token) return false;
  return playlistUris(body).some((uri) => {
    try {
      return new URL(uri, baseUrl).searchParams.get(GENERATION_QUERY_PARAM) === token;
    } catch {
      return false;
    }
  });
}

/**
 * The generation a transport's playlists must stop naming. Shared by every
 * playlist loader hls.js creates for one transport (manifest, level, audio and
 * subtitle playlists), because a fence applies to the whole generation.
 */
export interface PlaylistGenerationGate {
  /** Hold playlists that still list `token`; null lifts the gate. */
  fence(token: string | null): void;
  /** The generation currently fenced, or null. */
  readonly fencedToken: string | null;
  /** The gate spent its refetch budget without a rebuilt generation. */
  exhausted(): void;
}

export function createPlaylistGenerationGate(
  onExhausted: () => void = () => {},
): PlaylistGenerationGate {
  let fencedToken: string | null = null;
  return {
    fence(token) {
      fencedToken = token;
    },
    get fencedToken() {
      return fencedToken;
    },
    exhausted() {
      onExhausted();
    },
  };
}

export interface FencedPlaylistLoaderOptions {
  retryDelayMs?: number;
  maxRetries?: number;
  schedule?: (callback: () => void, delayMs: number) => unknown;
  cancel?: (handle: unknown) => void;
}

/**
 * Builds the playlist loader class for one transport.
 *
 * A 412 names a segment URL whose `sgen` the server has replaced. hls.js
 * normally answers that by refetching the playlist, but a manifest request
 * that lands while the successor generation is still being built names the
 * old generation again, and hls.js then requests (and retries) the same dead
 * segment URL. The gate refuses to hand hls.js such a playlist: it refetches
 * the playlist until its URIs name the live generation, so the only segment
 * request after a fence is the one made against rebuilt URLs.
 *
 * The base loader is hls.js's own loader class, so authentication, redirects,
 * byte ranges and abort semantics stay the library's.
 */
export function createFencedPlaylistLoader(
  BaseLoader: new (config: HlsConfig) => Loader<LoaderContext>,
  gate: PlaylistGenerationGate,
  options: FencedPlaylistLoaderOptions = {},
): PlaylistLoaderConstructor {
  const retryDelayMs = options.retryDelayMs ?? FENCED_PLAYLIST_RETRY_DELAY_MS;
  const maxRetries = options.maxRetries ?? FENCED_PLAYLIST_MAX_RETRIES;
  const schedule =
    options.schedule ?? ((callback: () => void, delayMs: number) => setTimeout(callback, delayMs));
  const cancel =
    options.cancel ?? ((handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>));

  return class FencedPlaylistLoader implements Loader<PlaylistLoaderContext> {
    private readonly inner: Loader<LoaderContext>;
    private currentContext: PlaylistLoaderContext | null = null;
    private pending: unknown = null;

    constructor(config: HlsConfig) {
      this.inner = new BaseLoader(config);
    }

    get context(): PlaylistLoaderContext | null {
      return this.currentContext;
    }

    get stats(): LoaderStats {
      return this.inner.stats;
    }

    getCacheAge = () => this.inner.getCacheAge?.() ?? null;
    getResponseHeader = (name: string) => this.inner.getResponseHeader?.(name) ?? null;

    destroy() {
      this.clearPending();
      this.inner.destroy();
    }

    abort() {
      this.clearPending();
      this.inner.abort();
    }

    load(
      context: PlaylistLoaderContext,
      config: LoaderConfiguration,
      callbacks: LoaderCallbacks<PlaylistLoaderContext>,
    ) {
      this.clearPending();
      this.currentContext = context;
      this.loadWithGate(context, config, callbacks, 0);
    }

    private loadWithGate(
      context: PlaylistLoaderContext,
      config: LoaderConfiguration,
      callbacks: LoaderCallbacks<PlaylistLoaderContext>,
      attempt: number,
    ) {
      this.inner.load(context, config, {
        ...(callbacks as unknown as LoaderCallbacks<LoaderContext>),
        onSuccess: (
          response: LoaderResponse,
          stats: LoaderStats,
          loadedContext: LoaderContext,
          networkDetails: unknown,
        ) => {
          const body = typeof response.data === "string" ? response.data : null;
          const fencedToken = gate.fencedToken;
          if (
            body !== null &&
            fencedToken !== null &&
            playlistNamesGeneration(body, fencedToken, loadedContext.url)
          ) {
            if (attempt < maxRetries) {
              this.pending = schedule(() => {
                this.pending = null;
                this.inner.abort();
                this.loadWithGate(context, config, callbacks, attempt + 1);
              }, retryDelayMs);
              return;
            }
            // The rebuilt generation never appeared. Do not hand hls.js the
            // dead playlist: tell the transport owner and fail the playlist
            // load so the ordinary fence path can end the route.
            this.currentContext = null;
            gate.exhausted();
            callbacks.onError(
              { code: 412, text: "segment generation is stale" },
              loadedContext as PlaylistLoaderContext,
              null,
              stats,
            );
            return;
          }
          this.currentContext = null;
          callbacks.onSuccess(
            response,
            stats,
            loadedContext as PlaylistLoaderContext,
            networkDetails,
          );
        },
        onError: (error, errorContext, networkDetails, stats) => {
          this.currentContext = null;
          callbacks.onError(error, errorContext as PlaylistLoaderContext, networkDetails, stats);
        },
        onTimeout: (stats, timeoutContext, networkDetails) => {
          this.currentContext = null;
          callbacks.onTimeout(stats, timeoutContext as PlaylistLoaderContext, networkDetails);
        },
        ...(callbacks.onAbort
          ? {
              onAbort: (
                stats: LoaderStats,
                abortContext: LoaderContext,
                networkDetails: unknown,
              ) => {
                this.currentContext = null;
                callbacks.onAbort?.(stats, abortContext as PlaylistLoaderContext, networkDetails);
              },
            }
          : {}),
      });
    }

    private clearPending() {
      if (this.pending === null) return;
      cancel(this.pending);
      this.pending = null;
    }
  };
}
