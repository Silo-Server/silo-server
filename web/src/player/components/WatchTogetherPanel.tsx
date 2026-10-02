import { useState } from "react";
import {
  ConnectionStateLabel,
  ConnectionStatusDot,
  type WatchTogetherConnectionState,
} from "@/components/watchtogether/ConnectionStatusDot";
import { EndWatchPartyDialog } from "@/components/watchtogether/EndWatchPartyDialog";
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
  const [endConfirmOpen, setEndConfirmOpen] = useState(false);
  const isHost = room?.self_can_manage_room === true;
  const policy = room?.guest_control_policy ?? "host_only";

  const policyLabel = isHost
    ? policy === "guest_play_pause"
      ? "Guests can pause & resume"
      : "Only you control playback"
    : room?.self_can_control_transport
      ? "You can pause & resume"
      : "Host controls playback";

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
        <span>Watch Party</span>
        <ConnectionStatusDot state={connectionState} className="size-1.5" />
        <span>
          <ConnectionStateLabel state={connectionState} />
        </span>
      </div>

      {/* Code + viewers */}
      <div className="mt-2 flex items-baseline gap-2">
        <span className="font-mono text-lg leading-none font-semibold tracking-wider">
          {room?.code ?? "..."}
        </span>
        {room ? (
          <span className="text-xs text-white/50">
            {room.member_count} {room.member_count === 1 ? "viewer" : "viewers"}
          </span>
        ) : null}
      </div>

      {/* Policy */}
      {room ? (
        <div className="mt-1.5 text-[11px] leading-snug text-white/50">{policyLabel}</div>
      ) : (
        <div className="mt-1.5 text-[11px] leading-snug text-white/50">Syncing room state</div>
      )}

      {room?.members?.length ? (
        <ul aria-label="Viewers" className="mt-3 max-h-40 space-y-1.5 overflow-y-auto text-xs">
          {room.members.map((member) => (
            <li
              key={`${member.user_id}:${member.profile_id}`}
              className="flex items-center justify-between gap-2"
            >
              <span className="truncate">
                {member.display_name}
                {member.is_self ? " (you)" : ""}
              </span>
              <span className="shrink-0 text-white/60">
                {member.is_buffering
                  ? "Buffering"
                  : member.is_syncing
                    ? "Syncing"
                    : member.is_ready
                      ? "Ready"
                      : "Connected"}
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
            aria-label="Copy invite link"
            className="rounded-md bg-white/12 px-2.5 py-1 text-[11px] font-medium text-white/90 transition-colors hover:bg-white/20"
          >
            Invite
          </button>
          <button
            type="button"
            onClick={() =>
              onToggleGuestControl(policy === "guest_play_pause" ? "host_only" : "guest_play_pause")
            }
            className="rounded-md bg-white/12 px-2.5 py-1 text-[11px] font-medium text-white/90 transition-colors hover:bg-white/20"
          >
            {policy === "guest_play_pause" ? "Host Only" : "Allow Pause"}
          </button>
          <button
            type="button"
            onClick={onStopPlayback}
            title="Stop for everyone and go back to the room to pick something else"
            className="ml-auto rounded-md bg-white/12 px-2.5 py-1 text-[11px] font-medium text-white/90 transition-colors hover:bg-white/20"
          >
            Stop
          </button>
          <button
            type="button"
            onClick={() => setEndConfirmOpen(true)}
            className="rounded-md bg-red-500/25 px-2.5 py-1 text-[11px] font-medium text-red-100 transition-colors hover:bg-red-500/35"
          >
            End
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
