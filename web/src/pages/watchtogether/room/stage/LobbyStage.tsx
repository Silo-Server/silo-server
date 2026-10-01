import { Check, Play, Vote } from "lucide-react";
import { Button } from "@/components/ui/button";
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
  const host = hostMember(room)?.display_name ?? "The host";
  return (
    <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl border border-dashed border-white/10 px-6 py-10 text-center">
      <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
        Room is open
      </p>
      <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Nothing on yet</h2>
      <p className="text-muted-foreground max-w-md text-sm">
        {isHost
          ? "Pick something below and it lands here for everyone to see. Or let the room decide."
          : `${host} is deciding. Anything you pick below goes in as a suggestion.`}
      </p>
      {isHost ? (
        <Button type="button" variant="outline" onClick={onOpenVoting} className="mt-1 gap-2">
          <Vote className="size-4" />
          Open the floor to votes
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
  const contentId = room.selected_content_id!;
  const { ready, total } = guestReadyCount(room);
  const host = hostMember(room)?.display_name ?? "The host";
  const self = selfMember(room);
  return (
    <div className="flex flex-col gap-4">
      <StagedHero
        contentId={contentId}
        libraryId={room.selected_library_id}
        eyebrow={isHost ? "Up next" : `Up next · ${host} queued this`}
        caption={
          isHost
            ? total > 0
              ? `${ready} of ${total} ready`
              : "Waiting for a guest to join"
            : `Starts when ${host} presses play · ${ready} of ${total} ready`
        }
      >
        {isHost ? (
          <>
            <Button type="button" onClick={onStart} disabled={busy} className="gap-2">
              <Play className="size-3.5" />
              {total > 0 ? `Start for everyone · ${ready}/${total} ready` : "Start alone anyway"}
            </Button>
            <Button type="button" variant="outline" onClick={onChange} disabled={busy}>
              Change
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
              {self?.lobby_ready ? "Ready ✓" : "I'm ready"}
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
  if (suggestions.length === 0) return null;
  const byId = new Map(suggestions.map((s) => [s.id, s]));
  return (
    <section className="surface-panel-subtle rounded-xl px-3 py-3" aria-label="Suggestions">
      <div className="flex items-baseline justify-between gap-3 px-2 pb-2">
        <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
          Suggestions · {suggestions.length}
        </h3>
        <span className="text-muted-foreground text-xs">
          {isHost ? "Queue one to put it up next." : "The host decides what plays."}
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
        promoteLabel="Queue this"
        onDelete={onDelete}
        emptyText=""
      />
    </section>
  );
}
