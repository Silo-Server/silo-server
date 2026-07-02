import { toast } from "sonner";
import {
  buildWatchTogetherInviteUrl,
  type GuestControlPolicy,
  type WatchTogetherRoomSnapshot,
} from "@/lib/watchTogether";

async function copyTextToClipboard(text: string) {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  // Fallback for insecure contexts / older browsers.
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  try {
    if (!document.execCommand("copy")) {
      throw new Error("Copy command failed");
    }
  } finally {
    textarea.remove();
  }
}

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
    toast.success(`Invite copied. Room code ${roomCode ?? ""}`.trim());
  } catch {
    toast.error("Failed to copy invite link");
  }
  return true;
}

/** Applies a guest-control policy change with toast feedback. */
export async function setWatchTogetherGuestControl(
  updatePolicy: (policy: GuestControlPolicy) => Promise<WatchTogetherRoomSnapshot | null>,
  policy: GuestControlPolicy,
): Promise<void> {
  try {
    const nextRoom = await updatePolicy(policy);
    if (nextRoom) {
      toast.success(
        nextRoom.guest_control_policy === "guest_play_pause"
          ? "Guests can now pause and resume"
          : "Room is now host controlled",
      );
    }
  } catch (error) {
    toast.error(error instanceof Error ? error.message : "Failed to update room");
  }
}

/** Ends the watch party with toast feedback. */
export async function endWatchTogetherRoom(closeRoom: () => Promise<void>): Promise<void> {
  try {
    await closeRoom();
    toast.success("Room ended");
  } catch (error) {
    toast.error(error instanceof Error ? error.message : "Failed to end room");
  }
}
