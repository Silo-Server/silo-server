import { useState } from "react";
import {
  ConnectionStateLabel,
  ConnectionStatusDot,
  type WatchTogetherConnectionState,
} from "@/components/watchtogether/ConnectionStatusDot";
import { EndWatchPartyDialog } from "@/components/watchtogether/EndWatchPartyDialog";
import { useT } from "@/i18n";
import type { GuestControlPolicy, WatchTogetherRoomSnapshot } from "@/lib/watchTogether";

interface WatchTogetherPanelProps {
  room: WatchTogetherRoomSnapshot | null;
  connectionState: WatchTogetherConnectionState;
  visible: boolean;
  onCopyInvite: () => void;
  onToggleGuestControl: (policy: GuestControlPolicy) => void;
  onEndRoom: () => void;
  onStopPlayback: () => void;
}

export function WatchTogetherPanel({
  room,
  connectionState,
  visible,
  onCopyInvite,
  onToggleGuestControl,
  onEndRoom,
  onStopPlayback,
}: WatchTogetherPanelProps) {
  const t = useT();
  const [endConfirmOpen, setEndConfirmOpen] = useState(false);
  const isHost = room?.self_can_manage_room === true;
  const policy = room?.guest_control_policy ?? "host_only";

  const policyLabel = isHost
    ? policy === "guest_play_pause"
      ? t("web.player.watchTogether.policy.guestsCanPause")
      : t("web.player.watchTogether.policy.hostOnly")
    : room?.self_can_control_transport
      ? t("web.player.watchTogether.policy.youCanPause")
      : t("web.player.watchTogether.policy.hostControls");

  return (
    <div
      aria-hidden={!visible}
      inert={!visible}
      className={`absolute top-4 right-4 z-50 w-56 rounded-xl border border-white/10 bg-black/80 p-3 text-white shadow-2xl backdrop-blur-xl transition-opacity duration-300 ${
        visible ? "opacity-100" : "pointer-events-none opacity-0"
      }`}
    >
      {/* Label + status */}
      <div className="flex items-center gap-2 text-[10px] font-semibold tracking-[0.16em] text-white/60 uppercase">
        <span>{t("web.player.watchTogether.title")}</span>
        <ConnectionStatusDot state={connectionState} className="size-1.5" />
        <span>
          <ConnectionStateLabel state={connectionState} />
        </span>
      </div>

      {/* Code + viewers */}
      <div className="mt-2 flex items-baseline gap-2">
        <span className="font-mono text-lg leading-none font-semibold tracking-wider">
          {room?.code ?? t("web.player.watchTogether.codePending")}
        </span>
        {room ? (
          <span className="text-xs text-white/50">
            {t("web.player.watchTogether.viewerCount", { count: room.member_count })}
          </span>
        ) : null}
      </div>

      {/* Policy */}
      {room ? (
        <div className="mt-1.5 text-[11px] leading-snug text-white/50">{policyLabel}</div>
      ) : (
        <div className="mt-1.5 text-[11px] leading-snug text-white/50">
          {t("web.player.watchTogether.syncingRoom")}
        </div>
      )}

      {room?.members?.length ? (
        <ul
          aria-label={t("web.player.watchTogether.viewersLabel")}
          className="mt-3 max-h-40 space-y-1.5 overflow-y-auto text-xs"
        >
          {room.members.map((member) => (
            <li
              key={`${member.user_id}:${member.profile_id}`}
              className="flex items-center justify-between gap-2"
            >
              <span className="truncate">
                {member.is_self
                  ? t("web.player.watchTogether.selfName", { name: member.display_name })
                  : member.display_name}
              </span>
              <span className="shrink-0 text-white/60">
                {member.is_buffering
                  ? t("web.player.watchTogether.member.buffering")
                  : member.is_syncing
                    ? t("web.player.watchTogether.member.syncing")
                    : member.is_ready
                      ? t("web.player.watchTogether.member.ready")
                      : t("web.player.watchTogether.member.connected")}
              </span>
            </li>
          ))}
        </ul>
      ) : null}

      {/* Actions */}
      {isHost ? (
        <div className="mt-3 flex items-center gap-1 border-t border-white/8 pt-2.5">
          <button
            type="button"
            onClick={onCopyInvite}
            aria-label={t("web.player.watchTogether.copyInviteLabel")}
            className="rounded-md bg-white/12 px-2.5 py-1 text-[11px] font-medium text-white/90 transition-colors hover:bg-white/20"
          >
            {t("web.player.watchTogether.invite")}
          </button>
          <button
            type="button"
            onClick={() =>
              onToggleGuestControl(policy === "guest_play_pause" ? "host_only" : "guest_play_pause")
            }
            className="rounded-md bg-white/12 px-2.5 py-1 text-[11px] font-medium text-white/90 transition-colors hover:bg-white/20"
          >
            {policy === "guest_play_pause"
              ? t("web.player.watchTogether.setHostOnly")
              : t("web.player.watchTogether.allowPause")}
          </button>
          <button
            type="button"
            onClick={onStopPlayback}
            title={t("web.player.watchTogether.stopTitle")}
            className="ml-auto rounded-md bg-white/12 px-2.5 py-1 text-[11px] font-medium text-white/90 transition-colors hover:bg-white/20"
          >
            {t("web.player.watchTogether.stop")}
          </button>
          <button
            type="button"
            onClick={() => setEndConfirmOpen(true)}
            className="rounded-md bg-red-500/25 px-2.5 py-1 text-[11px] font-medium text-red-100 transition-colors hover:bg-red-500/35"
          >
            {t("web.player.watchTogether.end")}
          </button>
        </div>
      ) : null}

      <EndWatchPartyDialog
        open={endConfirmOpen}
        onOpenChange={setEndConfirmOpen}
        onConfirm={() => {
          setEndConfirmOpen(false);
          onEndRoom();
        }}
      />
    </div>
  );
}
