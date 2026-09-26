import { Copy, Link as LinkIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { copyTextToClipboard } from "@/lib/clipboard";
import {
  buildWatchTogetherInviteUrl,
  type GuestControlPolicy,
  type WatchTogetherRoomSnapshot,
} from "@/lib/watchTogether";

export function InviteBar({
  room,
  isHost,
  onCopyInvite,
  onSetPolicy,
}: {
  room: WatchTogetherRoomSnapshot;
  isHost: boolean;
  onCopyInvite: () => void;
  onSetPolicy: (policy: GuestControlPolicy) => void;
}) {
  const inviteUrl = buildWatchTogetherInviteUrl(room.invite_path);
  const guestsCanPause = room.guest_control_policy === "guest_play_pause";
  return (
    <div className="surface-panel-subtle flex flex-wrap items-center gap-3 rounded-xl px-4 py-3">
      <span className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
        Invite
      </span>
      {inviteUrl ? (
        <code className="bg-surface-raised text-muted-foreground min-w-0 flex-1 truncate rounded-md px-3 py-1.5 text-xs">
          {inviteUrl.replace(/^https?:\/\//, "")}
        </code>
      ) : (
        <span className="text-muted-foreground text-xs">Share the code to let people in.</span>
      )}
      {inviteUrl ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onCopyInvite}
          className="gap-1.5"
        >
          <LinkIcon className="size-3.5" />
          Link
        </Button>
      ) : null}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="gap-1.5"
        onClick={() => {
          void copyTextToClipboard(room.code)
            .then(() => toast.success(`Room code ${room.code} copied`))
            .catch(() => toast.error("Could not copy the code"));
        }}
      >
        <Copy className="size-3.5" />
        Code
      </Button>
      {isHost ? (
        <label className="ml-auto flex items-center gap-2 text-sm">
          <Switch
            checked={guestsCanPause}
            onCheckedChange={(checked) => onSetPolicy(checked ? "guest_play_pause" : "host_only")}
            aria-label="Guests can pause"
          />
          Guests can pause
        </label>
      ) : (
        <span className="text-muted-foreground ml-auto text-xs">
          {guestsCanPause ? "Guests can pause" : "Host controls playback"}
        </span>
      )}
    </div>
  );
}
