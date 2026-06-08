import { useQuery } from "@tanstack/react-query";

import { fetchEbookReaderProgress } from "@/reader/FoliateBookReader";

export function useEbookReaderProgress(contentID: string | undefined) {
  return useQuery({
    queryKey: ["ebook-reader-progress", contentID],
    queryFn: () => fetchEbookReaderProgress(contentID!),
    enabled: !!contentID,
  });
}
