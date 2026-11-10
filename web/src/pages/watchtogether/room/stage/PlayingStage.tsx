import { useEffect, useState } from "react";
import { Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { numberText, useT } from "@/i18n";
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
  return numberText(h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`);
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
  const t = useT();
  const position = useRoomPosition(room, serverTimeOffsetMs);
  const stateLabel =
    room.playback_state === "playing"
      ? t("web.watchTogether.playing.playing")
      : room.playback_state === "waiting"
        ? t("web.watchTogether.playing.waiting")
        : t("web.watchTogether.playing.paused");
  return (
    <div className="flex flex-col gap-4">
      <StagedHero
        contentId={room.selected_content_id!}
        libraryId={room.selected_library_id}
        eyebrow={t("web.watchTogether.playing.eyebrow")}
        eyebrowTone="live"
        caption={t("web.watchTogether.playing.caption", {
          clock: formatClock(position),
          state: stateLabel,
          count: room.member_count,
          count_text: room.member_count,
        })}
      >
        <Button type="button" onClick={onRejoin} className="gap-2">
          <Play className="size-3.5" />
          {t("web.watchTogether.playing.rejoin")}
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
            {stopping
              ? t("web.watchTogether.playing.stopping")
              : t("web.watchTogether.playing.stop")}
          </Button>
        ) : null}
        <span className="text-muted-foreground text-sm">
          {isHost
            ? t("web.watchTogether.playing.hostNote")
            : t("web.watchTogether.playing.guestNote")}
        </span>
      </StagedHero>
      <section className="surface-panel-subtle rounded-xl px-3 py-3">
        <div className="flex items-center justify-between px-2 pb-2">
          <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            {t("web.watchTogether.playing.afterTitle")}
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
          emptyText={t("web.watchTogether.playing.empty")}
        />
      </section>
    </div>
  );
}
