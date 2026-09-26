import { useCallback, useEffect, useMemo, useState } from "react";
import type { WatchTogetherRoomMember } from "@/lib/watchTogether";
import { useSeasonEpisodes } from "@/hooks/queries/episodes";
import { MovieDetailPanel } from "../picker/MovieDetailPanel";
import { SeriesDrilldown, spoilerRisk, type EpisodePick } from "../picker/SeriesDrilldown";
import { useMemberState } from "../picker/usePickerData";
import type { BrowseSelection } from "./BrowseShelf";

/** What the stage hands back once the viewer confirms: a movie or one episode. */
export interface CandidateChoice {
  content_id: string;
  content_type: "movie" | "episode";
  title: string;
  subtitle: string;
  poster_url: string;
  library_id?: number;
}

/**
 * A browse selection previewed on the stage. A movie shows its facts and
 * who has seen it with the confirm button; a series shows the episode list
 * with member dots, and picking an episode swaps the candidate to that
 * episode. Nothing reaches the server until the viewer confirms.
 */
export function CandidateStage(props: CandidateStageProps) {
  // Per-candidate state (episode ids, the picked episode) lives in a child
  // keyed on the item, so changing candidates resets it by remounting rather
  // than by an effect racing the drill-down's own reports.
  return <CandidateBody key={props.selection.card.content_id} {...props} />;
}

interface CandidateStageProps {
  selection: BrowseSelection;
  roomId: string;
  roomToken: string;
  members: WatchTogetherRoomMember[];
  /** "stage" in a host-pick lobby, "suggest" everywhere else. */
  verb: "stage" | "suggest";
  /** Title of what is currently staged, when confirming would replace it. */
  replaces?: string;
  busy: boolean;
  onConfirm: (choice: CandidateChoice) => void;
  onDismiss: () => void;
}

function CandidateBody({
  selection,
  roomId,
  roomToken,
  members,
  verb,
  replaces,
  busy,
  onConfirm,
  onDismiss,
}: CandidateStageProps) {
  const { card } = selection;
  const [episodeIds, setEpisodeIds] = useState<string[]>([]);
  const [pick, setPick] = useState<EpisodePick | null>(null);
  const isSeries = card.type === "series";
  const memberState = useMemberState(
    roomId,
    roomToken,
    members,
    isSeries ? episodeIds : [card.content_id],
  );
  const handleEpisodeIds = useCallback((ids: string[]) => setEpisodeIds(ids), []);
  const actionLabel = verb === "stage" ? "Stage it for the room" : "Suggest this";

  const seasonEpisodesQuery = useSeasonEpisodes(
    pick ? pick.series.content_id : undefined,
    pick ? pick.seasonNumber : -1,
  );
  const seasonEpisodes = useMemo(
    () => (seasonEpisodesQuery.data?.episodes ?? []).filter((e) => e.files.length > 0),
    [seasonEpisodesQuery.data?.episodes],
  );
  const risk = useMemo(
    () => (pick ? spoilerRisk(pick.episode, seasonEpisodes, members, memberState.byId) : null),
    [pick, seasonEpisodes, members, memberState.byId],
  );

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onDismiss();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onDismiss]);

  if (!isSeries) {
    return (
      <MovieDetailPanel
        card={card}
        members={members}
        memberState={memberState.byId.get(card.content_id)}
        memberStateLoading={memberState.isLoading}
        actionLabel={actionLabel}
        replaces={replaces}
        busy={busy}
        onConfirm={() =>
          onConfirm({
            content_id: card.content_id,
            content_type: "movie",
            title: card.title,
            subtitle: card.year ? String(card.year) : "",
            poster_url: card.poster_url ?? "",
            library_id: card.library_id,
          })
        }
        onClose={onDismiss}
      />
    );
  }

  return (
    <SeriesDrilldown
      series={card}
      members={members}
      memberStates={memberState.byId}
      onEpisodeIds={handleEpisodeIds}
      actionLabel={verb === "stage" ? "Stage it" : "Suggest"}
      initialSeason={selection.season}
      picked={pick}
      risk={risk}
      replaces={replaces}
      busy={busy}
      confirmLabel={actionLabel}
      onPick={setPick}
      onConfirm={() => {
        if (!pick) return;
        onConfirm({
          content_id: pick.episode.content_id,
          content_type: "episode",
          title: pick.episode.title,
          subtitle: `${pick.series.title} · S${pick.seasonNumber} E${pick.episode.episode_number}`,
          poster_url: pick.series.poster_url ?? "",
          library_id: pick.series.library_id,
        });
      }}
      onClose={onDismiss}
    />
  );
}
