import { useState } from "react";
import {
  Ban,
  Bell,
  BellOff,
  CircleCheck,
  Clock,
  Hourglass,
  Library,
  type LucideIcon,
  Plus,
  X,
} from "lucide-react";
import type { MediaRequest, RequestMediaDetail } from "@/api/types";
import { CancelRequestDialog } from "@/components/CancelRequestDialog";
import { RequestDownloadProgress } from "@/components/RequestDownloadProgress";
import { RequestSeasonsDialog } from "@/components/RequestSeasonsDialog";
import {
  useCancelMediaRequest,
  useCreateMediaRequest,
  useMediaRequest,
  useMyMediaRequests,
  useToggleRequestFollow,
} from "@/hooks/queries/useRequests";
import { useAuth } from "@/hooks/useAuth";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import {
  canCancelOwnRequest,
  formatRequestDisplayState,
  formatRequestReason,
  requestDisplayState,
  requestInputFromMediaResult,
  type RequestDisplayState,
} from "@/lib/mediaRequests";
import ActionBar, {
  type ActionBarLink,
  type ActionBarPrimaryAction,
  type ActionBarSecondaryAction,
} from "./ActionBar";

const STATE_ICONS: Record<RequestDisplayState, LucideIcon> = {
  pending: Clock,
  approved: CircleCheck,
  processing: Hourglass,
  partially_available: Library,
  available: Library,
  declined: Ban,
  cancelled: Ban,
  failed: Ban,
};

interface RequestActionBarProps {
  item: RequestMediaDetail;
  /**
   * Opens the library's copy of the title. Set only when the page could not
   * confirm whether the viewer may open it; a copy they cannot open reads as
   * a disabled "In the library", with no link.
   */
  libraryHref?: string;
}

/**
 * The action row for a title outside the library: Request in the Play pill's
 * place, the request's state once there is one, cancelling or following that
 * request, and how far its download is. Owns the request mutations so their
 * pending state re-renders only this row, as MediaUserActionBar does for
 * library items.
 */
export default function RequestActionBar({ item, libraryHref }: RequestActionBarProps) {
  const navigate = useViewTransitionNavigate();
  const createRequest = useCreateMediaRequest();
  const cancelRequest = useCancelMediaRequest();
  const toggleFollow = useToggleRequestFollow();
  const ownRequest = useOwnCancellableRequest(item);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [pickSeasons, setPickSeasons] = useState(false);
  // A series TMDB lists seasons for is requested season by season.
  const seasonPicker = item.media_type === "series" && (item.seasons?.length ?? 0) > 0;

  const state = requestDisplayState(item.request.status, undefined, item.request.state);
  const inLibrary =
    item.availability === "available" || state === "available" || Boolean(item.library_content_id);
  const following = item.request.following === true;
  // Someone else's open request: the viewer can ask to hear when it lands
  // instead of requesting the title again.
  const canFollow =
    state !== undefined &&
    state !== "available" &&
    item.request.reason === "already_requested" &&
    !item.request.requested_by_viewer;

  let primaryAction: ActionBarPrimaryAction;
  if (item.request.requestable) {
    primaryAction = {
      label: item.media_type === "series" ? "Request series" : "Request movie",
      icon: Plus,
      pending: createRequest.isPending,
      onClick: seasonPicker
        ? () => setPickSeasons(true)
        : () => createRequest.mutate(requestInputFromMediaResult(item)),
    };
  } else if (inLibrary) {
    primaryAction = libraryHref
      ? { label: "Open in library", icon: Library, onClick: () => navigate(libraryHref) }
      : { label: "In the library", icon: Library, disabled: true };
  } else if (state) {
    primaryAction = {
      // The viewer's side of a pending request: they asked, nobody has answered.
      label: state === "pending" ? "Requested" : formatRequestDisplayState(state),
      icon: STATE_ICONS[state],
      disabled: true,
    };
  } else {
    primaryAction = { label: formatRequestReason(item.request.reason), icon: Ban, disabled: true };
  }

  const secondaryActions: ActionBarSecondaryAction[] = [];
  if (ownRequest) {
    secondaryActions.push({
      id: "cancel",
      label: "Cancel request",
      icon: X,
      pending: cancelRequest.isPending,
      onClick: () => setConfirmCancel(true),
    });
  }
  if (canFollow) {
    secondaryActions.push({
      id: "follow",
      label: following ? "Stop notifying me" : "Notify me when available",
      icon: following ? BellOff : Bell,
      pressed: following,
      pending: toggleFollow.isPending,
      onClick: () =>
        toggleFollow.mutate({
          mediaType: item.media_type,
          tmdbID: item.tmdb_id,
          follow: !following,
        }),
    });
  }

  const links: ActionBarLink[] = [];
  if (item.imdb_id) {
    links.push({ label: "IMDb", href: `https://www.imdb.com/title/${item.imdb_id}` });
  }
  links.push({
    label: "TMDB",
    href: `https://www.themoviedb.org/${item.media_type === "series" ? "tv" : "movie"}/${item.tmdb_id}`,
  });

  return (
    <>
      <ActionBar primaryAction={primaryAction} secondaryActions={secondaryActions} links={links} />
      {item.request.download ? (
        <RequestDownloadProgress download={item.request.download} className="mt-3 max-w-xs" />
      ) : null}
      {ownRequest ? (
        <CancelRequestDialog
          title={item.title}
          open={confirmCancel}
          onOpenChange={setConfirmCancel}
          onConfirm={() => cancelRequest.mutate(ownRequest.id)}
          isPending={cancelRequest.isPending}
        />
      ) : null}
      {seasonPicker ? (
        <RequestSeasonsDialog
          open={pickSeasons}
          onOpenChange={setPickSeasons}
          tmdbID={item.tmdb_id}
          title={item.title}
          detail={item}
        />
      ) : null}
    </>
  );
}

/**
 * The viewer's own request for this title, while they can still cancel it.
 * The detail names the request when it is the account's own (or the viewer is
 * an admin), so that request is read directly and kept only if the account
 * made it; an account's active requests can run past one list page. A detail
 * without the ID falls back to the account's active requests. The title's
 * download progress comes from the detail, so another request downloading is
 * no reason to read the list again.
 */
function useOwnCancellableRequest(item: RequestMediaDetail): MediaRequest | undefined {
  const { user } = useAuth();
  const mayCancel = item.request.status === "pending" || item.request.status === "approved";
  const requestID = item.request.request_id;
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
  return mine.data?.find(
    (request) =>
      request.media_type === item.media_type &&
      request.tmdb_id === item.tmdb_id &&
      canCancelOwnRequest(request),
  );
}
