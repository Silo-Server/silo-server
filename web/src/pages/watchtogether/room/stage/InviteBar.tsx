import { Copy, Link as LinkIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/i18n";
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
  const t = useT();
  const inviteUrl = buildWatchTogetherInviteUrl(room.invite_path);
  const guestsCanPause = room.guest_control_policy === "guest_play_pause";
  return (
    <div className="surface-panel-subtle flex flex-wrap items-center gap-3 rounded-xl px-4 py-3">
      <span className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
        {t("web.watchTogether.inviteBar.label")}
      </span>
      {inviteUrl ? (
        <code className="bg-surface-raised text-muted-foreground min-w-0 flex-1 truncate rounded-md px-3 py-1.5 text-xs">
          {inviteUrl.replace(/^https?:\/\//, "")}
        </code>
      ) : (
        <span className="text-muted-foreground text-xs">
          {t("web.watchTogether.inviteBar.shareCode")}
        </span>
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
          {t("web.watchTogether.inviteBar.link")}
        </Button>
      ) : null}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="gap-1.5"
        onClick={() => {
          void copyTextToClipboard(room.code)
            .then(() =>
              toast.success(t("web.watchTogether.common.codeCopied", { code: room.code })),
            )
            .catch(() => toast.error(t("web.watchTogether.common.codeCopyFailed")));
        }}
      >
        <Copy className="size-3.5" />
        {t("web.watchTogether.inviteBar.code")}
      </Button>
      {isHost ? (
        <label className="ms-auto flex items-center gap-2 text-sm">
          <Switch
            checked={guestsCanPause}
            onCheckedChange={(checked) => onSetPolicy(checked ? "guest_play_pause" : "host_only")}
            aria-label={t("web.watchTogether.inviteBar.guestsCanPause")}
          />
          {t("web.watchTogether.inviteBar.guestsCanPause")}
        </label>
      ) : (
        <span className="text-muted-foreground ms-auto text-xs">
          {guestsCanPause
            ? t("web.watchTogether.inviteBar.guestsCanPause")
            : t("web.watchTogether.inviteBar.hostControls")}
        </span>
      )}
    </div>
  );
}
