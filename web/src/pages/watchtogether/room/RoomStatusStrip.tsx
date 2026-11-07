import { useState } from "react";
import { ChevronDown, Copy, Link as LinkIcon, LogOut } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  ConnectionStateLabel,
  ConnectionStatusDot,
  type WatchTogetherConnectionState,
} from "@/components/watchtogether/ConnectionStatusDot";
import { EndWatchPartyDialog } from "@/components/watchtogether/EndWatchPartyDialog";
import { useT, type MessageKey } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";
import type { WatchTogetherRoomSnapshot, WatchTogetherSelectionMode } from "@/lib/watchTogether";

const modeLabels: Record<WatchTogetherSelectionMode, MessageKey> = {
  host_pick: "web.watchTogether.common.hostPicks",
  vote: "watchParty.options.everyoneVotes",
};

export function RoomStatusStrip({
  room,
  connectionState,
  isHost,
  canSwitchMode,
  onSwitchMode,
  onLeave,
  onEnd,
  ending,
  onCopyInvite,
}: {
  room: WatchTogetherRoomSnapshot | null;
  connectionState: WatchTogetherConnectionState;
  isHost: boolean;
  canSwitchMode: boolean;
  onSwitchMode: (mode: WatchTogetherSelectionMode) => void;
  onLeave: () => void;
  onEnd: () => Promise<void>;
  ending: boolean;
  onCopyInvite?: () => void;
}) {
  const t = useT();
  const [endOpen, setEndOpen] = useState(false);
  const count = room?.member_count ?? 0;
  const phaseLabel =
    room?.phase === "playing"
      ? t("web.watchTogether.strip.playing", { count, count_text: String(count) })
      : room?.selection_mode === "vote"
        ? t("web.watchTogether.strip.voting", { count, count_text: String(count) })
        : t("web.watchTogether.strip.live", { count, count_text: String(count) });

  return (
    <div className="border-border/60 flex items-center gap-2 border-b px-4 py-2.5 sm:px-5">
      <div className="flex shrink-0 items-center gap-2 text-sm font-medium">
        <span
          aria-hidden="true"
          className={`size-2 rounded-full ${room ? "bg-emerald-400" : "bg-white/25"}`}
        />
        {room ? phaseLabel : t("web.watchTogether.connection.connecting")}
      </div>

      <div className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
        {room ? (
          <button
            type="button"
            onClick={() => {
              void copyTextToClipboard(room.code)
                .then(() =>
                  toast.success(t("web.watchTogether.common.codeCopied", { code: room.code })),
                )
                .catch(() => toast.error(t("web.watchTogether.common.codeCopyFailed")));
            }}
            className="bg-surface-raised hover:bg-accent flex shrink-0 items-center gap-1.5 rounded-full border border-white/10 px-3 py-1 font-mono text-xs font-semibold tracking-[0.2em] whitespace-nowrap transition-colors"
            aria-label={t("web.watchTogether.strip.copyCode", { code: room.code })}
          >
            {room.code}
            <Copy className="text-muted-foreground size-3" />
          </button>
        ) : null}

        {room && onCopyInvite ? (
          <button
            type="button"
            onClick={onCopyInvite}
            className="text-muted-foreground hover:text-foreground flex shrink-0 items-center gap-1.5 rounded-full border border-white/10 px-3 py-1 text-xs whitespace-nowrap transition-colors"
            aria-label={t("web.watchTogether.strip.copyInvite")}
          >
            <LinkIcon className="size-3" />
            <span className="hidden sm:inline">{t("web.watchTogether.strip.inviteLink")}</span>
          </button>
        ) : null}

        {room ? (
          isHost && canSwitchMode ? (
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  className="text-muted-foreground hover:text-foreground hidden shrink-0 items-center gap-1 rounded-full border border-white/10 px-3 py-1 text-xs whitespace-nowrap transition-colors min-[400px]:flex"
                  aria-label={t("web.watchTogether.strip.changeMode")}
                >
                  {t(modeLabels[room.selection_mode])}
                  <span className="hidden sm:inline">{t("web.watchTogether.strip.modeYou")}</span>
                  <ChevronDown className="size-3" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-56">
                {(Object.keys(modeLabels) as WatchTogetherSelectionMode[]).map((mode) => (
                  <DropdownMenuItem
                    key={mode}
                    disabled={mode === room.selection_mode}
                    onSelect={() => onSwitchMode(mode)}
                  >
                    {t(modeLabels[mode])}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : (
            <span className="text-muted-foreground hidden shrink-0 rounded-full border border-white/10 px-3 py-1 text-xs whitespace-nowrap min-[400px]:inline">
              {t(modeLabels[room.selection_mode])}
            </span>
          )
        ) : null}
      </div>

      <div className="ms-auto flex shrink-0 items-center gap-2">
        <span className="text-muted-foreground flex items-center gap-1.5 text-xs">
          <ConnectionStatusDot state={connectionState} />
          <span className="hidden sm:inline">
            <ConnectionStateLabel state={connectionState} />
          </span>
        </span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onLeave}
          className="gap-1.5"
          aria-label={t("web.watchTogether.strip.leaveLabel")}
        >
          <LogOut className="size-3.5" />
          <span className="hidden sm:inline">{t("web.watchTogether.strip.leave")}</span>
        </Button>
        {isHost ? (
          <Button
            type="button"
            variant="destructive"
            size="sm"
            onClick={() => setEndOpen(true)}
            disabled={ending}
            aria-label={t("web.watchTogether.strip.endRoom")}
          >
            <span className="sm:hidden">{t("web.watchTogether.strip.end")}</span>
            <span className="hidden sm:inline">{t("web.watchTogether.strip.endRoom")}</span>
          </Button>
        ) : null}
      </div>

      <EndWatchPartyDialog
        open={endOpen}
        onOpenChange={setEndOpen}
        onConfirm={() => {
          void onEnd().finally(() => setEndOpen(false));
        }}
        isPending={ending}
      />
    </div>
  );
}
