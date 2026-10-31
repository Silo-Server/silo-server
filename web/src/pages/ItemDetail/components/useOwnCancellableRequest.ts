import type { MediaRequest, RequestMediaType, RequestState } from "@/api/types";
import { useMediaRequest, useMyMediaRequests } from "@/hooks/queries/useRequests";
import { useAuth } from "@/hooks/useAuth";
import { canCancelOwnRequest } from "@/lib/mediaRequests";

/**
 * The viewer's own regular or 4K request for this title, as `state` names,
 * while they can still cancel it.
 * The detail names the request when it is the account's own (or the viewer is
 * an admin), so that request is read directly and kept only if the account
 * made it; an account's active requests can run past one list page. A detail
 * without the ID falls back to the account's active requests. The title's
 * download progress comes from the detail, so another request downloading is
 * no reason to read the list again.
 */
export function useOwnCancellableRequest(
  mediaType: RequestMediaType,
  tmdbID: number,
  state: RequestState | undefined,
  is4K: boolean,
): MediaRequest | undefined {
  const { user } = useAuth();
  const mayCancel = state?.status === "pending" || state?.status === "approved";
  const requestID = state?.request_id;
  const named = useMediaRequest(requestID, { enabled: mayCancel });
  const mine = useMyMediaRequests(
    { outcome: "active" },
    { enabled: mayCancel && !requestID, pollDownloads: false },
  );
  if (!mayCancel) return undefined;
  if (requestID) {
    const request = named.data;
    return request && request.requested_by_user_id === user?.id && canCancelOwnRequest(request)
      ? request
      : undefined;
  }
  // The title's regular and 4K requests are separate; match the one asked for.
  return mine.data?.find(
    (request) =>
      request.media_type === mediaType &&
      request.tmdb_id === tmdbID &&
      Boolean(request.is_4k) === is4K &&
      canCancelOwnRequest(request),
  );
}
