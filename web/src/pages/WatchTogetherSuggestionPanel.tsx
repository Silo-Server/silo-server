import { useCallback, useState } from "react";
import { Play, Search, Trash2, TriangleIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { WatchTogetherSuggestion } from "@/lib/watchTogether";
import { toast } from "sonner";

interface SuggestionPanelProps {
  suggestions: WatchTogetherSuggestion[];
  isHost: boolean;
  currentProfileId: string;
  onVote: (id: string) => Promise<void>;
  onUnvote: (id: string) => Promise<void>;
  onDelete: (id: string) => Promise<void>;
  onPromote: (id: string) => Promise<void>;
  onOpenSearch: () => void;
}

function SuggestionCard({
  suggestion,
  isHost,
  isOwn,
  isLoading,
  onVoteToggle,
  onPromote,
  onDelete,
}: {
  suggestion: WatchTogetherSuggestion;
  isHost: boolean;
  isOwn: boolean;
  isLoading: boolean;
  onVoteToggle: () => void;
  onPromote: () => void;
  onDelete: () => void;
}) {
  const [posterLoaded, setPosterLoaded] = useState(false);

  return (
    <div className="group/suggestion relative flex flex-col">
      {/* Poster */}
      <div className="media-card-image relative aspect-[2/3] overflow-hidden">
        {suggestion.poster_url ? (
          <img
            src={suggestion.poster_url}
            alt={suggestion.title}
            className={`h-full w-full object-cover transition-opacity duration-300 ${
              posterLoaded ? "opacity-100" : "opacity-0"
            }`}
            loading="lazy"
            onLoad={() => setPosterLoaded(true)}
          />
        ) : (
          <div className="bg-surface text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-xs">
            <span className="line-clamp-3 font-medium">{suggestion.title}</span>
          </div>
        )}
        <div className="from-background/80 pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t to-transparent" />

        {/* Vote badge */}
        <button
          type="button"
          onClick={onVoteToggle}
          disabled={isLoading}
          aria-pressed={suggestion.voted_by_me}
          aria-label={
            suggestion.voted_by_me
              ? `Remove vote for ${suggestion.title}`
              : `Vote for ${suggestion.title}`
          }
          className={`absolute top-2 right-2 z-10 flex items-center gap-1 rounded-full border px-2 py-1 text-[11px] font-semibold tabular-nums backdrop-blur-sm transition-all duration-200 ${
            suggestion.voted_by_me
              ? "border-primary/40 bg-primary/20 text-primary"
              : "border-white/20 bg-black/50 text-white/80 hover:border-white/40 hover:bg-black/70"
          }`}
        >
          <TriangleIcon className={`size-2.5 ${suggestion.voted_by_me ? "fill-primary" : ""}`} />
          {suggestion.vote_count}
        </button>

        {/* Host: Play overlay */}
        {isHost ? (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-black/0 opacity-0 transition-all duration-200 group-focus-within/suggestion:bg-black/40 group-focus-within/suggestion:opacity-100 group-hover/suggestion:bg-black/40 group-hover/suggestion:opacity-100">
            <button
              type="button"
              onClick={onPromote}
              disabled={isLoading}
              aria-label={`Play ${suggestion.title} for everyone`}
              className="pointer-events-auto z-10 flex size-10 items-center justify-center rounded-full border border-white/30 bg-white/15 text-white backdrop-blur-sm transition-transform duration-200 group-hover/suggestion:scale-100 focus-visible:opacity-100"
            >
              <Play className="size-4 fill-white" />
            </button>
          </div>
        ) : null}

        {/* Delete button */}
        {isOwn || isHost ? (
          <button
            type="button"
            onClick={onDelete}
            disabled={isLoading}
            aria-label={`Remove suggestion ${suggestion.title}`}
            className="absolute top-2 left-2 z-10 flex size-6 items-center justify-center rounded-full border border-white/15 bg-black/50 text-white/60 opacity-0 backdrop-blur-sm transition-all duration-200 group-focus-within/suggestion:opacity-100 group-hover/suggestion:opacity-100 hover:border-red-500/40 hover:bg-red-500/20 hover:text-red-300 focus-visible:opacity-100"
            title="Remove suggestion"
          >
            <Trash2 className="size-3" />
          </button>
        ) : null}
      </div>

      {/* Title */}
      <div className="px-0.5 pt-2.5">
        <div className="truncate text-[13px] font-semibold tracking-tight">{suggestion.title}</div>
        <div className="text-muted-foreground mt-0.5 text-[11px] font-medium tracking-[0.12em] uppercase">
          {suggestion.subtitle || (suggestion.content_type === "episode" ? "Episode" : "Movie")}
        </div>
      </div>
    </div>
  );
}

export function WatchTogetherSuggestionPanel({
  suggestions,
  isHost,
  currentProfileId,
  onVote,
  onUnvote,
  onDelete,
  onPromote,
  onOpenSearch,
}: SuggestionPanelProps) {
  const [loadingId, setLoadingId] = useState<string | null>(null);

  const handleVoteToggle = useCallback(
    async (suggestion: WatchTogetherSuggestion) => {
      setLoadingId(suggestion.id);
      try {
        if (suggestion.voted_by_me) {
          await onUnvote(suggestion.id);
        } else {
          await onVote(suggestion.id);
        }
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "Vote failed");
      } finally {
        setLoadingId(null);
      }
    },
    [onVote, onUnvote],
  );

  const handleDelete = useCallback(
    async (id: string) => {
      setLoadingId(id);
      try {
        await onDelete(id);
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "Failed to remove suggestion");
      } finally {
        setLoadingId(null);
      }
    },
    [onDelete],
  );

  const handlePromote = useCallback(
    async (id: string) => {
      setLoadingId(id);
      try {
        await onPromote(id);
        toast.success("Playing suggestion for everyone");
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "Failed to start suggestion");
      } finally {
        setLoadingId(null);
      }
    },
    [onPromote],
  );

  return (
    <section className="rounded-xl border border-white/10 bg-white/[0.02] px-5 py-5">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <div className="bg-primary/10 text-primary flex size-8 items-center justify-center rounded-lg">
            <TriangleIcon className="size-3.5 fill-current" />
          </div>
          <div>
            <h2 className="text-lg font-semibold tracking-tight">Suggestions</h2>
            <p className="text-muted-foreground mt-0.5 text-sm">
              {suggestions.length === 0
                ? "No suggestions yet — search for something to add."
                : `${suggestions.length} suggestion${suggestions.length !== 1 ? "s" : ""} from the room`}
            </p>
          </div>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onOpenSearch}
          className="shrink-0 gap-1.5"
        >
          <Search className="size-3.5" />
          Suggest
        </Button>
      </div>

      {suggestions.length > 0 ? (
        <div className="mt-5 grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6">
          {suggestions.map((suggestion) => {
            const isOwn = suggestion.suggester_profile_id === currentProfileId;
            const isLoading = loadingId === suggestion.id;

            return (
              <SuggestionCard
                key={suggestion.id}
                suggestion={suggestion}
                isHost={isHost}
                isOwn={isOwn}
                isLoading={isLoading}
                onVoteToggle={() => void handleVoteToggle(suggestion)}
                onPromote={() => void handlePromote(suggestion.id)}
                onDelete={() => void handleDelete(suggestion.id)}
              />
            );
          })}
        </div>
      ) : null}
    </section>
  );
}
