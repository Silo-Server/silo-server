import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import type { ItemDetail } from "@/api/types";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { useSimilarItems } from "@/hooks/queries/recommendations";
import { useItemEpisodes, useSeasons } from "@/hooks/queries/episodes";
import { useContinueWatching } from "@/hooks/queries/progress";
import { useSetRating, useDeleteRating } from "@/hooks/queries/ratings";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useAuth } from "@/hooks/useAuth";
import CastCarousel from "@/components/CastCarousel";
import CrewList from "@/components/CrewList";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MatchItemDialog from "@/components/MatchItemDialog";
import PageBack from "@/components/PageBack";
import RecommendationGrid from "@/components/RecommendationGrid";
import DetailHero from "./DetailHero";
import SeasonCarousel from "./SeasonCarousel";
import SeasonEpisodeGrid from "./components/SeasonEpisodeGrid";
import MetadataBadges from "./components/MetadataBadges";
import ScoreRow from "./components/ScoreRow";
import HeroCrewLine from "./components/HeroCrewLine";
import ActionBar from "./components/ActionBar";
import { SeasonCarouselSkeleton, RecommendationGridSkeleton } from "./components/SectionSkeletons";
import { getSeasonDisplayTitle, resolveSeriesPrimaryAction } from "./itemDetailLayout";
import { getWatchedActionLabel } from "./watchedState";
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";

export default function SeriesContent({ item }: { item: ItemDetail & { type: "series" } }) {
  const navigate = useNavigate();
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const canCurateMetadata = canCurateMetadataForUser(user);

  const isFavorite = item.user_state?.is_favorite ?? false;
  const inWatchlist = item.user_state?.in_watchlist ?? false;
  const toggleFavoriteMutation = useToggleFavorite(item.content_id);
  const toggleWatchlistMutation = useToggleWatchlist(item.content_id);
  const refreshMetadataMutation = useRefreshItemMetadata();
  const watchedMutation = useWatchedStateMutation(item);
  const setRatingMutation = useSetRating(item.content_id);
  const deleteRatingMutation = useDeleteRating(item.content_id);

  const [editOpen, setEditOpen] = useState(false);
  const [matchOpen, setMatchOpen] = useState(false);
  const { data: seasonsData, isLoading: seasonsLoading } = useSeasons(item.content_id);
  const { data: similarData, isLoading: similarLoading } = useSimilarItems(item.content_id);
  const seasons = useMemo(() => seasonsData?.seasons ?? [], [seasonsData?.seasons]);
  const { items: continueWatchingItems } = useContinueWatching();

  const handleRatingChange = (rating: number | null) => {
    if (rating === null) {
      deleteRatingMutation.mutate();
    } else {
      setRatingMutation.mutate(rating);
    }
  };

  const title = item.title ?? "";
  const firstYear = item.first_air_date?.slice(0, 4);
  const lastYear = item.last_air_date?.slice(0, 4);
  const yearDisplay = firstYear
    ? lastYear && lastYear !== firstYear
      ? `${firstYear}\u2013${lastYear}`
      : firstYear
    : "";

  const firstNetwork = (item.networks ?? [])[0];
  const episodeCount = seasons.reduce((sum, s) => sum + s.episode_count, 0);
  const singleSeason = seasons.length === 1 ? seasons[0] : undefined;

  const primaryAction = useMemo(
    () =>
      resolveSeriesPrimaryAction({
        seriesId: item.content_id,
        seasons,
        continueWatching: continueWatchingItems
          .filter((entry) => entry.detail?.series_id === item.content_id)
          .map((entry) => ({
            contentId: entry.detail?.content_id ?? entry.progress.media_item_id,
            seriesId: entry.detail?.series_id,
            title: entry.detail?.title ?? "",
          })),
      }),
    [continueWatchingItems, item.content_id, seasons],
  );
  const primaryActionEpisodesQuery = useItemEpisodes(primaryAction.targetSeasonId);
  const primaryActionEpisodes = primaryActionEpisodesQuery.data;
  const primaryActionLoading =
    !!primaryAction.targetSeasonId && primaryActionEpisodesQuery.isLoading;
  const singleSeasonEpisodesQuery = useItemEpisodes(singleSeason?.content_id);
  const singleSeasonEpisodeLinkState = singleSeason
    ? {
        parentSeasonHref: `/item/${singleSeason.content_id}`,
        parentSeasonLabel: getSeasonDisplayTitle(singleSeason),
      }
    : undefined;

  const resolvedPrimaryHref = useMemo(() => {
    if (primaryAction.directHref) return primaryAction.directHref;

    const episodes = primaryActionEpisodes?.episodes ?? [];
    if (episodes.length === 0 || primaryAction.targetEpisodeNumber == null) {
      return undefined;
    }

    const targetIndex = Math.max(
      0,
      Math.min(primaryAction.targetEpisodeNumber - 1, episodes.length - 1),
    );
    const targetEpisode = episodes[targetIndex];
    return targetEpisode ? `/watch/${targetEpisode.content_id}` : undefined;
  }, [primaryAction.directHref, primaryAction.targetEpisodeNumber, primaryActionEpisodes]);

  return (
    <div>
      <DetailHero
        title={title}
        topNav={<PageBack />}
        context="Series"
        studioLabel={firstNetwork}
        backdropUrl={item.backdrop_url}
        backdropThumbhash={item.backdrop_thumbhash}
        posterUrl={item.poster_url}
        posterThumbhash={item.poster_thumbhash}
        logoUrl={item.logo_url}
        tagline={item.tagline || undefined}
        metadata={
          <MetadataBadges
            year={yearDisplay || undefined}
            contentRating={item.content_rating || undefined}
            seasonCount={seasons.length || undefined}
            episodeCount={episodeCount || undefined}
          />
        }
        scoreRow={
          <ScoreRow
            ratingImdb={item.rating_imdb}
            ratingRtCritic={item.rating_rt_critic}
            ratingRtAudience={item.rating_rt_audience}
          />
        }
        overview={item.overview}
        crewLine={
          <HeroCrewLine crew={item.crew ?? []} genres={item.genres} jobLabel="Created by" />
        }
        actions={
          <ActionBar
            contentId={item.content_id}
            playHref={resolvedPrimaryHref}
            playLabel={primaryAction.label}
            playLoading={primaryActionLoading}
            watchedLabel={getWatchedActionLabel(item)}
            onToggleWatched={() => watchedMutation.mutate(!(item.user_data?.played ?? false))}
            isUpdatingWatched={watchedMutation.isPending}
            onToggleFavorite={() => toggleFavoriteMutation.mutate(isFavorite)}
            isFavorite={isFavorite}
            onToggleWatchlist={() => toggleWatchlistMutation.mutate(inWatchlist)}
            inWatchlist={inWatchlist}
            onRefresh={
              canCurateMetadata
                ? (mode) =>
                    refreshMetadataMutation.mutate({
                      item,
                      mode,
                      onReplaced: (contentID) => navigate(`/item/${contentID}`, { replace: true }),
                    })
                : undefined
            }
            isRefreshing={refreshMetadataMutation.isPending}
            isAdmin={isAdmin}
            canCurateMetadata={canCurateMetadata}
            onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
            onMatchItem={canCurateMetadata ? () => setMatchOpen(true) : undefined}
            rating={item.user_rating ?? null}
            onRatingChange={handleRatingChange}
          />
        }
      />

      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        {seasonsLoading ? (
          <SeasonCarouselSkeleton />
        ) : singleSeason ? (
          <section>
            <div className="mb-5 flex items-center justify-between gap-3">
              <h2 className="text-xl font-semibold tracking-tight">Episodes</h2>
              <span className="text-muted-foreground text-sm">
                {singleSeason.episode_count} total
              </span>
            </div>
            <SeasonEpisodeGrid
              episodes={singleSeasonEpisodesQuery.data?.episodes ?? []}
              isLoading={singleSeasonEpisodesQuery.isLoading}
              episodeLinkState={singleSeasonEpisodeLinkState}
            />
          </section>
        ) : (
          seasons.length > 0 && <SeasonCarousel seasons={seasons} />
        )}
        {item.cast && item.cast.length > 0 && (
          <div>
            <h2 className="mb-5 text-xl font-semibold tracking-tight">Cast</h2>
            <CastCarousel cast={item.cast} />
          </div>
        )}
        {item.crew && item.crew.length > 0 && <CrewList crew={item.crew} />}

        {similarLoading ? (
          <RecommendationGridSkeleton />
        ) : (
          similarData?.items &&
          similarData.items.length > 0 && (
            <div>
              <h2 className="mb-5 text-xl font-semibold tracking-tight">More Like This</h2>
              <RecommendationGrid items={similarData.items} />
            </div>
          )
        )}
      </div>
      {canCurateMetadata && (
        <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />
      )}
      {canCurateMetadata && (
        <MatchItemDialog
          key={item.content_id}
          item={item}
          open={matchOpen}
          onOpenChange={setMatchOpen}
        />
      )}
    </div>
  );
}
