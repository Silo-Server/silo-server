import { useQuery } from "@tanstack/react-query";
import { V2ProblemError, V2TransportError, v2 } from "@/api/v2/request";
import { trickplayFromV2 } from "@/api/v2/trickplay";
import type { PlayerTrickplay } from "@/player/trickplay";
import { itemKeys } from "./keys";

/** Sheet URLs are refetched this long before they expire. */
const TRICKPLAY_REFRESH_MARGIN_MS = 5 * 60 * 1000;

/** Transient failures keep cached previews usable while the manifest recovers. */
export function transientTrickplayError(error: unknown): boolean {
  if (error instanceof V2ProblemError) return error.status === 429 || error.status >= 500;
  if (error instanceof V2TransportError)
    return error.status === 0 || error.status === 429 || error.status >= 500;
  return error instanceof TypeError;
}

function trickplayRetryDelay(error: unknown, fallback: number): number {
  const retryAfter = error instanceof V2ProblemError ? error.retryAfterSeconds : null;
  return retryAfter === null ? fallback : Math.max(fallback, retryAfter * 1000);
}

export async function fetchWatchTrickplay(
  id: string,
  fileId: number,
  options?: RequestInit,
): Promise<PlayerTrickplay> {
  const manifest = await v2("GET /api/v2/watch/{id}/trickplay", {
    path: { id },
    query: { file_id: String(fileId) },
    signal: options?.signal ?? undefined,
  });
  return trickplayFromV2(manifest);
}

/**
 * The seek-bar previews of the file being played, read only when its version
 * reports them. The manifest is read again before its sheet URLs expire.
 */
export function useWatchTrickplay(
  id: string | undefined,
  fileId: number | undefined,
  available: boolean,
) {
  return useQuery({
    queryKey: itemKeys.watchTrickplay(id ?? "", fileId ?? 0),
    queryFn: ({ signal }) => fetchWatchTrickplay(id!, fileId!, { signal }),
    enabled: !!id && !!fileId && available,
    staleTime: Infinity,
    retry: (failures, error) => failures < 2 && transientTrickplayError(error),
    retryDelay: (attempt, error) =>
      trickplayRetryDelay(error, Math.min(1000 * 2 ** attempt, 30_000)),
    refetchInterval: (query) => {
      if (query.state.status === "error") {
        return transientTrickplayError(query.state.error)
          ? trickplayRetryDelay(query.state.error, 60_000)
          : false;
      }
      const expiresAt = query.state.data?.expiresAt;
      if (!expiresAt || !Number.isFinite(expiresAt)) return false;
      return Math.max(60_000, expiresAt - Date.now() - TRICKPLAY_REFRESH_MARGIN_MS);
    },
  });
}
