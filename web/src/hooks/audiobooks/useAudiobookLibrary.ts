import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
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

interface UseInfiniteAudiobookLibraryOpts {
  limit?: number;
  enabled?: boolean;
  genre?: string;
}

export function useInfiniteAudiobookLibrary(opts: UseInfiniteAudiobookLibraryOpts = {}) {
  const { limit = 60, enabled = true, genre = "" } = opts;
  return useInfiniteQuery<AudiobookListResponse>({
    queryKey: ["audiobooks", "list-infinite", limit, genre],
    enabled,
    initialPageParam: 0,
    queryFn: ({ pageParam = 0 }) => {
      const params = new URLSearchParams({
        limit: String(limit),
        offset: String(pageParam),
      });
      if (genre) params.set("genre", genre);
      return api<AudiobookListResponse>(`/audiobooks?${params.toString()}`);
    },
    getNextPageParam: (lastPage) => {
      const next = lastPage.offset + lastPage.items.length;
      return next < lastPage.total ? next : undefined;
    },
  });
}
