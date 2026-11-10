import { Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
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
  const t = useT();
  const ranked = rankSuggestions(suggestions);
  const leader = ranked[0];
  const totalVotes = suggestions.reduce((sum, s) => sum + s.vote_count, 0);
  const hostName =
    room.members?.find((m) => m.is_host)?.display_name ?? t("web.watchTogether.voting.theHost");

  return (
    <div className="flex flex-col gap-4">
      {leader ? (
        <StagedHero
          contentId={leader.content_id}
          eyebrow={
            leader.vote_count > 0
              ? t("web.watchTogether.voting.leading", {
                  votes: leader.vote_count,
                  total: totalVotes,
                  total_text: totalVotes,
                })
              : t("web.watchTogether.voting.firstSuggestion")
          }
          eyebrowTone={leader.vote_count > 0 ? "leading" : "muted"}
          caption={
            isHost
              ? t("web.watchTogether.voting.hostCaption")
              : t("web.watchTogether.voting.guestCaption", { host: hostName })
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
              {t("web.watchTogether.voting.playForEveryone", { title: leader.title })}
            </Button>
          ) : null}
        </StagedHero>
      ) : (
        <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl px-6 py-10 text-center">
          <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            {t("web.watchTogether.voting.open")}
          </p>
          <h2 className="text-xl font-semibold tracking-tight">
            {t("web.watchTogether.voting.noSuggestions")}
          </h2>
          <p className="text-muted-foreground max-w-sm text-sm">
            {isHost
              ? t("web.watchTogether.voting.emptyHost")
              : t("web.watchTogether.voting.emptyGuest", { host: hostName })}
          </p>
        </div>
      )}
      <section className="surface-panel-subtle rounded-xl px-3 py-3">
        <div className="flex items-center justify-between px-2 pb-2">
          <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            {t("web.watchTogether.voting.ranked")}
          </h3>
          <span className="text-muted-foreground text-xs">
            {t("web.watchTogether.voting.oneVote")}
          </span>
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
          emptyText={t("web.watchTogether.voting.empty")}
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
