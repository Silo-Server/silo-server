import { Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import type {
  GuestControlPolicy,
  WatchTogetherRoomSnapshot,
  WatchTogetherSuggestion,
} from "@/lib/watchTogether";
import { InviteBar } from "./InviteBar";
import { StagedHero } from "./StagedHero";
import { rankSuggestions, SuggestionList } from "./SuggestionList";

/**
 * A vote-mode lobby. The leader is shown as the staged item client-side;
 * the host's "Play this" on any row is a promotion, which the server allows
 * for every suggestion, not only the leader.
 */
export function VotingStage({
  room,
  suggestions,
  isHost,
  currentProfileId,
  memberNames,
  busyId,
  promoting,
  onVoteToggle,
  onPromote,
  onDelete,
  onCopyInvite,
  onSetPolicy,
}: {
  room: WatchTogetherRoomSnapshot;
  suggestions: WatchTogetherSuggestion[];
  isHost: boolean;
  currentProfileId: string;
  memberNames: Map<string, string>;
  busyId: string | null;
  promoting: boolean;
  onVoteToggle: (suggestion: WatchTogetherSuggestion) => void;
  onPromote: (id: string) => void;
  onDelete: (id: string) => void;
  onCopyInvite: () => void;
  onSetPolicy: (policy: GuestControlPolicy) => void;
}) {
  const ranked = rankSuggestions(suggestions);
  const leader = ranked[0];
  const totalVotes = suggestions.reduce((sum, s) => sum + s.vote_count, 0);
  const hostName = room.members?.find((m) => m.is_host)?.display_name ?? "the host";

  return (
    <div className="flex flex-col gap-4">
      {leader ? (
        <StagedHero
          contentId={leader.content_id}
          eyebrow={
            leader.vote_count > 0
              ? `Leading · ${leader.vote_count} of ${totalVotes} votes`
              : "First suggestion"
          }
          eyebrowTone={leader.vote_count > 0 ? "leading" : "muted"}
          caption={
            isHost
              ? "Start the leader, or play any suggestion below"
              : `Starts when ${hostName} presses play`
          }
        >
          {isHost ? (
            <Button
              type="button"
              onClick={() => onPromote(leader.id)}
              disabled={promoting}
              className="gap-2"
            >
              <Play className="size-3.5" />
              Play {leader.title} for everyone
            </Button>
          ) : null}
        </StagedHero>
      ) : (
        <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl px-6 py-10 text-center">
          <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            Voting is open
          </p>
          <h2 className="text-xl font-semibold tracking-tight">No suggestions yet</h2>
          <p className="text-muted-foreground max-w-sm text-sm">
            Anyone can add one from the shelf below. The room votes, and{" "}
            {isHost ? "you start" : `${hostName} starts`} whatever wins.
          </p>
        </div>
      )}
      <section className="surface-panel-subtle rounded-xl px-3 py-3">
        <div className="flex items-center justify-between px-2 pb-2">
          <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            Suggestions · ranked by votes
          </h3>
          <span className="text-muted-foreground text-xs">One vote each · change it any time</span>
        </div>
        <SuggestionList
          suggestions={suggestions}
          isHost={isHost}
          currentProfileId={currentProfileId}
          memberNames={memberNames}
          canVote
          busyId={busyId}
          onVoteToggle={onVoteToggle}
          onPromote={isHost ? onPromote : undefined}
          onDelete={onDelete}
          emptyText="Nothing suggested yet. Browse below to add the first."
        />
      </section>
      <InviteBar
        room={room}
        isHost={isHost}
        onCopyInvite={onCopyInvite}
        onSetPolicy={onSetPolicy}
      />
    </div>
  );
}
