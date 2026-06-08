import { useQuery } from "@tanstack/react-query";

import {
  ebookReaderProgressQueryKey,
  fetchEbookReaderProgress,
} from "@/reader/FoliateBookReader";

export function useEbookReaderProgress(contentID: string | undefined) {
  return useQuery({
    queryKey: ebookReaderProgressQueryKey(contentID),
    queryFn: () => fetchEbookReaderProgress(contentID!),
    enabled: !!contentID,
  });
}
