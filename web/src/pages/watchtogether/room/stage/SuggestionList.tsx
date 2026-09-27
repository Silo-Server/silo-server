import { useState } from "react";
import { Play, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { decodeThumbhash } from "@/lib/thumbhash";
import type { WatchTogetherSuggestion } from "@/lib/watchTogether";

/** Suggestions in the server's order: votes desc, then oldest first. */
export function rankSuggestions(suggestions: WatchTogetherSuggestion[]) {
  return [...suggestions].sort((a, b) => {
    if (b.vote_count !== a.vote_count) return b.vote_count - a.vote_count;
    return a.created_at.localeCompare(b.created_at);
  });
}

function SuggestionRow({
  suggestion,
  rank,
  maxVotes,
  isHost,
  isOwn,
  suggesterName,
  canVote,
  busy,
  onVoteToggle,
  onPromote,
  promoteLabel,
  onDelete,
  showActions,
  showVotes,
}: {
  suggestion: WatchTogetherSuggestion;
  rank: number;
  maxVotes: number;
  isHost: boolean;
  isOwn: boolean;
  suggesterName: string;
  canVote: boolean;
  busy: boolean;
  onVoteToggle: () => void;
  onPromote?: () => void;
  promoteLabel: string;
  onDelete?: () => void;
  /** Whether any row in this list can show actions; keeps columns aligned. */
  showActions: boolean;
  /** Vote controls and tallies only mean something in a vote room. */
  showVotes: boolean;
}) {
  const [loaded, setLoaded] = useState(false);
  const share = maxVotes > 0 ? suggestion.vote_count / maxVotes : 0;
  return (
    <li className="group/row flex items-center gap-3 rounded-lg px-2 py-2 transition-colors hover:bg-white/[0.03]">
      {showVotes ? (
        <button
          type="button"
          onClick={onVoteToggle}
          disabled={!canVote || busy}
          aria-pressed={suggestion.voted_by_me}
          aria-label={
            suggestion.voted_by_me
              ? `Remove your vote for ${suggestion.title}`
              : `Vote for ${suggestion.title}`
          }
          className={`flex size-7 shrink-0 items-center justify-center rounded-full border transition-colors ${
            suggestion.voted_by_me
              ? "border-primary bg-primary text-primary-foreground"
              : "border-white/25 text-transparent hover:border-white/50"
          } disabled:opacity-60`}
        >
          <span aria-hidden="true" className="text-xs">
            ✓
          </span>
        </button>
      ) : null}
      <div className="bg-surface h-12 w-8 shrink-0 overflow-hidden rounded">
        {suggestion.poster_url ? (
          <img
            src={suggestion.poster_url}
            alt=""
            loading="lazy"
            onLoad={() => setLoaded(true)}
            className={`h-full w-full object-cover transition-opacity ${loaded ? "opacity-100" : "opacity-0"}`}
          />
        ) : null}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className="truncate text-sm font-semibold">{suggestion.title}</span>
          {suggestion.subtitle ? (
            <span className="text-muted-foreground truncate text-xs">{suggestion.subtitle}</span>
          ) : null}
          {showVotes && rank === 0 && suggestion.vote_count > 0 ? (
            <span className="rounded-full bg-amber-400/15 px-1.5 py-0.5 text-[10px] font-semibold tracking-wide text-amber-300 uppercase">
              Leading
            </span>
          ) : null}
        </div>
        <div className="text-muted-foreground mt-0.5 truncate text-xs">
          {suggesterName}
          {suggestion.note ? <span className="italic">: “{suggestion.note}”</span> : null}
        </div>
      </div>
      {showVotes ? (
        <>
          <div className="hidden w-32 shrink-0 items-center gap-2 sm:flex">
            <div className="bg-surface-raised h-1.5 flex-1 overflow-hidden rounded-full">
              <div
                className="bg-foreground/70 h-full rounded-full transition-[width]"
                style={{ width: `${Math.round(share * 100)}%` }}
              />
            </div>
          </div>
          <span className="text-muted-foreground w-14 shrink-0 text-right text-xs tabular-nums">
            {suggestion.vote_count} {suggestion.vote_count === 1 ? "vote" : "votes"}
          </span>
        </>
      ) : null}
      {/* Every row reserves the same action slot, whether or not this viewer
          can act on it, so the vote column lines up down the list. */}
      {showActions ? (
        <div className="flex w-[7.5rem] shrink-0 items-center justify-end gap-1">
          {isHost && onPromote ? (
            <Button
              type="button"
              size="sm"
              onClick={onPromote}
              disabled={busy}
              className="pointer-reveal gap-1.5"
            >
              <Play className="size-3" />
              {promoteLabel}
            </Button>
          ) : null}
          {(isHost || isOwn) && onDelete ? (
            <button
              type="button"
              onClick={onDelete}
              disabled={busy}
              aria-label={`Remove ${suggestion.title}`}
              className="pointer-reveal text-muted-foreground rounded p-1 hover:text-red-300"
            >
              <Trash2 className="size-3.5" />
            </button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

export function SuggestionList({
  suggestions,
  isHost,
  currentProfileId,
  memberNames,
  canVote,
  busyId,
  onVoteToggle,
  onPromote,
  promoteLabel = "Play this",
  onDelete,
  emptyText,
  showVotes = true,
}: {
  suggestions: WatchTogetherSuggestion[];
  isHost: boolean;
  currentProfileId: string;
  memberNames: Map<string, string>;
  canVote: boolean;
  busyId: string | null;
  onVoteToggle: (suggestion: WatchTogetherSuggestion) => void;
  onPromote?: (id: string) => void;
  /** What the host's per-row button does: "Play this" or "Queue this". */
  promoteLabel?: string;
  onDelete?: (id: string) => void;
  emptyText: string;
  /** Hide vote controls and tallies outside vote rooms. */
  showVotes?: boolean;
}) {
  const ranked = rankSuggestions(suggestions);
  const maxVotes = ranked[0]?.vote_count ?? 0;
  // The host can act on every row; a guest only on their own. Either way the
  // slot is reserved on all rows once anyone in this list can act.
  const showActions =
    (isHost && (!!onPromote || !!onDelete)) ||
    (!!onDelete && ranked.some((s) => s.suggester_profile_id === currentProfileId));
  if (ranked.length === 0) {
    return <p className="text-muted-foreground px-2 py-6 text-center text-sm">{emptyText}</p>;
  }
  return (
    <ul className="flex flex-col gap-1">
      {ranked.map((suggestion, index) => (
        <SuggestionRow
          key={suggestion.id}
          suggestion={suggestion}
          rank={index}
          maxVotes={maxVotes}
          isHost={isHost}
          isOwn={suggestion.suggester_profile_id === currentProfileId}
          suggesterName={
            memberNames.get(`${suggestion.suggester_user_id}:${suggestion.suggester_profile_id}`) ??
            "Someone"
          }
          canVote={canVote}
          busy={busyId === suggestion.id}
          onVoteToggle={() => onVoteToggle(suggestion)}
          onPromote={onPromote ? () => onPromote(suggestion.id) : undefined}
          promoteLabel={promoteLabel}
          onDelete={onDelete ? () => onDelete(suggestion.id) : undefined}
          showActions={showActions}
          showVotes={showVotes}
        />
      ))}
    </ul>
  );
}

// decodeThumbhash is re-exported for poster placeholders in sibling components.
export { decodeThumbhash };
