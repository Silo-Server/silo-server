import { useCallback, useState } from "react";
import type { RequestMediaResult } from "@/api/types";
import { useCreateMediaRequest } from "@/hooks/queries/useRequests";
import { requestInputFromMediaResult } from "@/lib/mediaRequests";

function resultKey(item: Pick<RequestMediaResult, "media_type" | "tmdb_id">): string {
  return `${item.media_type}-${item.tmdb_id}`;
}

/**
 * Requests TMDB results from a list of cards and tracks which cards have a
 * request in flight. Each card waits on its own call: a second `mutate()`
 * would detach the first call's callbacks, leaving that card on "Sending".
 * The shared mutation still shows the success and failure toasts.
 */
export function useSubmitMediaRequest() {
  const { mutateAsync } = useCreateMediaRequest();
  const [pendingKeys, setPendingKeys] = useState<ReadonlySet<string>>(() => new Set());

  const submit = useCallback(
    (item: RequestMediaResult) => {
      const key = resultKey(item);
      setPendingKeys((prev) => new Set(prev).add(key));
      mutateAsync(requestInputFromMediaResult(item))
        // The mutation's own onError already reported the failure.
        .catch(() => {})
        .finally(() => {
          setPendingKeys((prev) => {
            if (!prev.has(key)) return prev;
            const next = new Set(prev);
            next.delete(key);
            return next;
          });
        });
    },
    [mutateAsync],
  );

  const isSubmitting = useCallback(
    (item: RequestMediaResult) => pendingKeys.has(resultKey(item)),
    [pendingKeys],
  );

  return { submit, isSubmitting };
}
