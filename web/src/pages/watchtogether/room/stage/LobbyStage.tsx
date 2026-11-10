import { Check, Play, Vote } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import type {
  GuestControlPolicy,
  WatchTogetherRoomSnapshot,
  WatchTogetherSuggestion,
} from "@/lib/watchTogether";
import { SuggestionList } from "./SuggestionList";
import { InviteBar } from "./InviteBar";
import { ReadyCheckRow } from "./ReadyCheckRow";
import { StagedHero } from "./StagedHero";
import { hostMember, guestReadyCount, selfMember } from "../members";

/** The empty lobby: the host's two moves, or what guests can do meanwhile. */
export function LobbyEmptyStage({
  room,
  isHost,
  onOpenVoting,
}: {
  room: WatchTogetherRoomSnapshot;
  isHost: boolean;
  onOpenVoting: () => void;
}) {
  const t = useT();
  const host = hostMember(room)?.display_name ?? t("watchParty.theHost");
  return (
    <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl border border-dashed border-white/10 px-6 py-10 text-center">
      <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
        {t("web.watchTogether.lobby.roomOpen")}
      </p>
      <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">
        {t("web.watchTogether.lobby.nothingOn")}
      </h2>
      <p className="text-muted-foreground max-w-md text-sm">
        {isHost
          ? t("web.watchTogether.lobby.hostBody")
          : t("web.watchTogether.lobby.guestBody", { host })}
      </p>
      {isHost ? (
        <Button type="button" variant="outline" onClick={onOpenVoting} className="mt-1 gap-2">
          <Vote className="size-4" />
          {t("web.watchTogether.lobby.openVoting")}
        </Button>
      ) : null}
    </div>
  );
}

/** A lobby with a staged item: host starts, guests mark ready. */
export function LobbyStagedStage({
  room,
  isHost,
  busy,
  onStart,
  onChange,
  onSetReady,
  onCopyInvite,
  onSetPolicy,
}: {
  room: WatchTogetherRoomSnapshot;
  isHost: boolean;
  busy: boolean;
  onStart: () => void;
  onChange: () => void;
  onSetReady: (ready: boolean) => void;
  onCopyInvite: () => void;
  onSetPolicy: (policy: GuestControlPolicy) => void;
}) {
  const t = useT();
  const contentId = room.selected_content_id!;
  const { ready, total } = guestReadyCount(room);
  const host = hostMember(room)?.display_name ?? t("watchParty.theHost");
  const self = selfMember(room);
  return (
    <div className="flex flex-col gap-4">
      <StagedHero
        contentId={contentId}
        libraryId={room.selected_library_id}
        eyebrow={
          isHost
            ? t("web.watchTogether.lobby.upNext")
            : t("web.watchTogether.lobby.upNextGuest", { host })
        }
        caption={
          isHost
            ? total > 0
              ? t("web.watchTogether.presence.ready", {
                  ready: ready,
                  total,
                  total_text: total,
                })
              : t("web.watchTogether.lobby.waitingGuest")
            : t("web.watchTogether.lobby.guestCaption", {
                host,
                ready: ready,
                total,
                total_text: total,
              })
        }
      >
        {isHost ? (
          <>
            <Button type="button" onClick={onStart} disabled={busy} className="gap-2">
              <Play className="size-3.5" />
              {total > 0
                ? t("web.watchTogether.lobby.startEveryone", {
                    ready: ready,
                    total,
                    total_text: total,
                  })
                : t("web.watchTogether.lobby.startAlone")}
            </Button>
            <Button type="button" variant="outline" onClick={onChange} disabled={busy}>
              {t("web.watchTogether.common.change")}
            </Button>
          </>
        ) : (
          <>
            <Button
              type="button"
              variant={self?.lobby_ready ? "outline" : "default"}
              onClick={() => onSetReady(!self?.lobby_ready)}
              className="gap-2"
              aria-pressed={self?.lobby_ready === true}
            >
              <Check className="size-3.5" />
              {self?.lobby_ready
                ? t("web.watchTogether.lobby.readyDone")
                : t("watchParty.button.imReady")}
            </Button>
          </>
        )}
      </StagedHero>
      <ReadyCheckRow room={room} />
      <InviteBar
        room={room}
        isHost={isHost}
        onCopyInvite={onCopyInvite}
        onSetPolicy={onSetPolicy}
      />
    </div>
  );
}

/**
 * Guests' suggestions in a host-pick lobby. The host is still the one who
 * decides, so a row's action queues it as the staged pick rather than
 * playing it; guests see what has been put forward and can pull their own.
 */
export function LobbySuggestions({
  suggestions,
  isHost,
  currentProfileId,
  memberNames,
  busyId,
  queueing,
  onQueue,
  onDelete,
}: {
  suggestions: WatchTogetherSuggestion[];
  isHost: boolean;
  currentProfileId: string;
  memberNames: Map<string, string>;
  busyId: string | null;
  queueing: boolean;
  onQueue: (suggestion: WatchTogetherSuggestion) => void;
  onDelete: (id: string) => void;
}) {
  const t = useT();
  if (suggestions.length === 0) return null;
  const byId = new Map(suggestions.map((s) => [s.id, s]));
  return (
    <section
      className="surface-panel-subtle rounded-xl px-3 py-3"
      aria-label={t("web.watchTogether.lobby.suggestions")}
    >
      <div className="flex items-baseline justify-between gap-3 px-2 pb-2">
        <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
          {t("web.watchTogether.lobby.suggestionsCount", { count: suggestions.length })}
        </h3>
        <span className="text-muted-foreground text-xs">
          {isHost ? t("watchParty.strip.manager") : t("watchParty.strip.guest")}
        </span>
      </div>
      <SuggestionList
        suggestions={suggestions}
        isHost={isHost}
        currentProfileId={currentProfileId}
        memberNames={memberNames}
        canVote={false}
        showVotes={false}
        busyId={queueing ? (busyId ?? null) : busyId}
        onVoteToggle={() => {}}
        onPromote={
          isHost
            ? (id) => {
                const s = byId.get(id);
                if (s) onQueue(s);
              }
            : undefined
        }
        promoteLabel={t("web.watchTogether.lobby.queueThis")}
        onDelete={onDelete}
        emptyText=""
      />
    </section>
  );
}
