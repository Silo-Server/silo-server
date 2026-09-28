import { useEffect, useState } from "react";
import { Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { WatchTogetherRoomSnapshot, WatchTogetherSuggestion } from "@/lib/watchTogether";
import { StagedHero } from "./StagedHero";
import { SuggestionList } from "./SuggestionList";

function formatClock(totalSeconds: number) {
  const s = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const mm = String(m).padStart(2, "0");
  const ss = String(sec).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}

/** Live position from the room anchor, advanced locally while playing. */
export function useRoomPosition(room: WatchTogetherRoomSnapshot, serverTimeOffsetMs: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (room.playback_state !== "playing") return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [room.playback_state]);
  const anchoredAt = Date.parse(room.anchor_updated_at);
  if (room.playback_state !== "playing" || !Number.isFinite(anchoredAt)) {
    return room.anchor_position_seconds;
  }
  const elapsed = (now + serverTimeOffsetMs - anchoredAt) / 1000;
  return room.anchor_position_seconds + Math.max(0, elapsed);
}

/**
 * The room page while playback runs: for someone who backed out of the
 * player, a way back in, plus suggestions for what comes after.
 */
export function PlayingStage({
  room,
  suggestions,
  serverTimeOffsetMs,
  isHost,
  currentProfileId,
  memberNames,
  busyId,
  onRejoin,
  onStop,
  stopping = false,
  onVoteToggle,
  onDelete,
}: {
  room: WatchTogetherRoomSnapshot;
  suggestions: WatchTogetherSuggestion[];
  serverTimeOffsetMs: number;
  isHost: boolean;
  currentProfileId: string;
  memberNames: Map<string, string>;
  busyId: string | null;
  onRejoin: () => void;
  /** Host only: stop for everyone and return the room to the lobby. */
  onStop?: () => void;
  stopping?: boolean;
  onVoteToggle: (suggestion: WatchTogetherSuggestion) => void;
  onDelete: (id: string) => void;
}) {
  const position = useRoomPosition(room, serverTimeOffsetMs);
  const stateLabel =
    room.playback_state === "playing"
      ? "Playing"
      : room.playback_state === "waiting"
        ? "Waiting for everyone to buffer"
        : "Paused";
  return (
    <div className="flex flex-col gap-4">
      <StagedHero
        contentId={room.selected_content_id!}
        libraryId={room.selected_library_id}
        eyebrow="Now playing"
        eyebrowTone="live"
        caption={`${formatClock(position)} · ${stateLabel} · ${room.member_count} here`}
      >
        <Button type="button" onClick={onRejoin} className="gap-2">
          <Play className="size-3.5" />
          Rejoin playback
        </Button>
        {isHost && onStop ? (
          <Button
            type="button"
            variant="outline"
            onClick={onStop}
            disabled={stopping}
            className="gap-2"
          >
            <Square className="size-3.5" />
            {stopping ? "Stopping…" : "Stop for everyone"}
          </Button>
        ) : null}
        <span className="text-muted-foreground text-sm">
          {isHost
            ? "Playback keeps going for the others. Stopping brings everyone back here to pick something else."
            : "Playback keeps going for the others. Rejoining puts you at the room's position."}
        </span>
      </StagedHero>
      <section className="surface-panel-subtle rounded-xl px-3 py-3">
        <div className="flex items-center justify-between px-2 pb-2">
          <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            After this · still open for suggestions
          </h3>
        </div>
        <SuggestionList
          suggestions={suggestions}
          isHost={isHost}
          currentProfileId={currentProfileId}
          memberNames={memberNames}
          canVote
          busyId={busyId}
          onVoteToggle={onVoteToggle}
          onDelete={onDelete}
          emptyText="Nothing queued for after. Browse below to suggest something."
        />
      </section>
    </div>
  );
}
