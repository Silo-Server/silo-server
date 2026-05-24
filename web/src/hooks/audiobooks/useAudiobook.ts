import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AudiobookDetailResponse } from "@/lib/audiobooks/types";

export function useAudiobook(contentId: string | undefined) {
  return useQuery<AudiobookDetailResponse>({
    queryKey: ["audiobooks", "detail", contentId],
    enabled: Boolean(contentId),
    queryFn: () => api<AudiobookDetailResponse>(`/audiobooks/${contentId}`),
  });
}
