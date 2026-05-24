import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AudiobookListResponse } from "@/lib/audiobooks/types";

interface UseAudiobookLibraryOpts {
  limit?: number;
  offset?: number;
  enabled?: boolean;
}

export function useAudiobookLibrary(opts: UseAudiobookLibraryOpts = {}) {
  const { limit = 50, offset = 0, enabled = true } = opts;
  return useQuery<AudiobookListResponse>({
    queryKey: ["audiobooks", "list", limit, offset],
    enabled,
    queryFn: () => {
      const params = new URLSearchParams({
        limit: String(limit),
        offset: String(offset),
      });
      return api<AudiobookListResponse>(`/audiobooks?${params.toString()}`);
    },
  });
}
