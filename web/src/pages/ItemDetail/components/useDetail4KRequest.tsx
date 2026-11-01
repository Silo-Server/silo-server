import { useState, type ReactNode } from "react";
import type { RequestMediaType } from "@/api/types";
import { CancelRequestDialog } from "@/components/CancelRequestDialog";
import { RequestSeasonsDialog } from "@/components/RequestSeasonsDialog";
import {
  useCancelMediaRequest,
  useCreateMediaRequest,
  useRequestFeatureStatus,
  useRequestMediaDetail,
} from "@/hooks/queries/useRequests";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { requestInputFromMediaResult } from "@/lib/mediaRequests";
import { Request4KStatus } from "./RequestActionBar";
import { useOwnCancellableRequest } from "./useOwnCancellableRequest";

interface Detail4KRequest {
  /** Requests the title in 4K, or opens the 4K season picker for a series. */
  onRequest4K?: () => void;
  /** Asks to cancel the viewer's own open 4K request. */
  onCancel4KRequest?: () => void;
  /** The title's 4K request state, for under the action bar. */
  status: ReactNode;
  /** The season picker and cancel confirmation. */
  dialogs: ReactNode;
}

/**
 * The 4K request of a title in the library. The library's copy may be HD,
 * and the server takes a 4K request for it, so the item page offers one as
 * the request page does for titles outside the library. The title's request
 * detail is read only while the viewer may make 4K requests at all.
 */
export function useDetail4KRequest(
  mediaType: RequestMediaType,
  tmdbID: number,
  title: string,
): Detail4KRequest {
  const { profile } = useCurrentProfile();
  const enabled = Boolean(profile?.id) && tmdbID > 0;
  // The shell's sidebar keeps the status fresh; read what it has.
  const featureStatus = useRequestFeatureStatus({ enabled, refetchOnMount: false });
  const allowed =
    enabled &&
    Boolean(featureStatus.data?.requests_enabled && featureStatus.data.requests_4k_allowed);
  const detail = useRequestMediaDetail(mediaType, tmdbID, { enabled: allowed }).data;
  const uhd = allowed ? detail?.request_4k : undefined;
  const own = useOwnCancellableRequest(mediaType, tmdbID, uhd, true);
  const createRequest = useCreateMediaRequest();
  const cancelRequest = useCancelMediaRequest();
  const [pickSeasons, setPickSeasons] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  // A series TMDB lists seasons for is requested season by season.
  const seasonPicker = mediaType === "series" && (detail?.seasons?.length ?? 0) > 0;

  let onRequest4K: (() => void) | undefined;
  // The menu closes on click; while the request is sending, a second click
  // would only be refused as already requested.
  if (detail && uhd?.requestable && !createRequest.isPending) {
    onRequest4K = seasonPicker
      ? () => setPickSeasons(true)
      : () => createRequest.mutate({ ...requestInputFromMediaResult(detail), is_4k: true });
  }

  return {
    onRequest4K,
    onCancel4KRequest: own ? () => setConfirmCancel(true) : undefined,
    status: <Request4KStatus state={uhd} />,
    dialogs: (
      <>
        {own && confirmCancel ? (
          <CancelRequestDialog
            title={`${title} (4K)`}
            open
            onOpenChange={setConfirmCancel}
            onConfirm={() => cancelRequest.mutate(own.id)}
            isPending={cancelRequest.isPending}
          />
        ) : null}
        {detail && seasonPicker ? (
          <RequestSeasonsDialog
            open={pickSeasons}
            onOpenChange={setPickSeasons}
            tmdbID={tmdbID}
            title={title}
            detail={detail}
            is4K
          />
        ) : null}
      </>
    ),
  };
}
