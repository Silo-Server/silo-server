import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { toast } from "sonner";
import { t } from "@/i18n";
import {
  buildWatchTogetherInviteUrl,
  type GuestControlPolicy,
  type WatchTogetherRoomSnapshot,
} from "@/lib/watchTogether";
import { copyTextToClipboard } from "@/lib/clipboard";

/**
 * Copies the room invite link to the clipboard with toast feedback.
 * Returns false when the invite link is not available yet (no toast is shown),
 * so callers can surface their own "not ready" message.
 */
export async function copyWatchTogetherInvite(
  invitePath: string | null | undefined,
  roomCode?: string | null,
): Promise<boolean> {
  const inviteUrl = buildWatchTogetherInviteUrl(invitePath);
  if (!inviteUrl) {
    return false;
  }
  try {
    await copyTextToClipboard(inviteUrl);
    toast.success(
      roomCode
        ? t("web.watchTogether.actions.inviteCopiedWithCode", { code: roomCode })
        : t("web.watchTogether.actions.inviteCopied"),
    );
  } catch {
    toast.error(t("web.watchTogether.actions.inviteCopyFailed"));
  }
  return true;
}

/** Applies a guest-control policy change with toast feedback. */
export async function setWatchTogetherGuestControl(
  updatePolicy: (policy: GuestControlPolicy) => Promise<WatchTogetherRoomSnapshot | null>,
  policy: GuestControlPolicy,
): Promise<void> {
  const authority = captureProfileRequestContext();
  try {
    const nextRoom = await updatePolicy(policy);
    if (nextRoom && authority && isCapturedProfileAuthorityActive(authority)) {
      toast.success(
        nextRoom.guest_control_policy === "guest_play_pause"
          ? t("web.watchTogether.actions.guestControlOn")
          : t("web.watchTogether.actions.guestControlOff"),
      );
    }
  } catch (error) {
    if (error instanceof StaleApiRequestContextError) return;
    if (authority && isCapturedProfileAuthorityActive(authority))
      toast.error(
        error instanceof Error ? error.message : t("web.watchTogether.actions.updateRoomFailed"),
      );
  }
}

/** Stops playback for everyone, keeping the room open, with toast feedback. */
export async function stopWatchTogetherPlayback(
  stopPlayback: () => Promise<WatchTogetherRoomSnapshot | null>,
): Promise<WatchTogetherRoomSnapshot | null> {
  const authority = captureProfileRequestContext();
  try {
    const room = await stopPlayback();
    if (room && authority && isCapturedProfileAuthorityActive(authority))
      toast.success(t("web.watchTogether.actions.stoppedRoomOpen"));
    return room;
  } catch (error) {
    if (error instanceof StaleApiRequestContextError) return null;
    if (authority && isCapturedProfileAuthorityActive(authority))
      toast.error(
        error instanceof Error ? error.message : t("web.watchTogether.actions.stopFailed"),
      );
    return null;
  }
}

/** Ends the watch party with toast feedback. */
export async function endWatchTogetherRoom(closeRoom: () => Promise<void>): Promise<void> {
  const authority = captureProfileRequestContext();
  try {
    await closeRoom();
    if (authority && isCapturedProfileAuthorityActive(authority))
      toast.success(t("web.watchTogether.actions.roomEnded"));
  } catch (error) {
    if (error instanceof StaleApiRequestContextError) return;
    if (authority && isCapturedProfileAuthorityActive(authority))
      toast.error(
        error instanceof Error ? error.message : t("web.watchTogether.actions.endRoomFailed"),
      );
  }
}

/** Reports only the current promotion receipt, without assuming it started playback. */
export async function promoteWatchTogetherWithFeedback(
  promote: (id: string) => Promise<WatchTogetherRoomSnapshot | null>,
  id: string,
): Promise<void> {
  const authority = captureProfileRequestContext();
  try {
    const room = await promote(id);
    if (room && authority && isCapturedProfileAuthorityActive(authority))
      toast.success(t("web.watchTogether.actions.selectionUpdated"));
  } catch (error) {
    if (error instanceof StaleApiRequestContextError) return;
    if (authority && isCapturedProfileAuthorityActive(authority))
      toast.error(
        error instanceof Error
          ? error.message
          : t("web.watchTogether.actions.startSuggestionFailed"),
      );
  }
}
