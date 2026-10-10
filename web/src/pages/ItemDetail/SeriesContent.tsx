import { useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import type { ItemDetail } from "@/api/types";
import { useStartShuffle } from "@/hooks/queries/shuffles";
import { useRefreshItemMetadata } from "@/hooks/queries/items";
import { useSimilarItems } from "@/hooks/queries/recommendations";
import { useItemEpisodes, useSeasons } from "@/hooks/queries/episodes";
import { useLibraryCapabilities } from "@/hooks/queries/admin/libraries";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useAuth } from "@/hooks/useAuth";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useMissingSeasonsRequestable } from "@/hooks/useCanRequest";
import { RequestSeasonsDialog } from "@/components/RequestSeasonsDialog";
import CastCarousel from "@/components/CastCarousel";
import CrewList from "@/components/CrewList";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import MatchItemDialog from "@/components/MatchItemDialog";
import SplitItemDialog from "@/components/SplitItemDialog";
import PageBack from "@/components/PageBack";
import RecommendationGrid from "@/components/RecommendationGrid";
import DetailHero from "./DetailHero";
import DetailLayout, { DetailSection } from "./DetailLayout";
import { useDetailWatchTogether } from "@/pages/watchtogether/DetailWatchTogether";
import { useOnViewTranslation } from "@/hooks/useOnViewTranslation";
import SeasonCarousel from "./SeasonCarousel";
import SeasonEpisodeGrid from "./components/SeasonEpisodeGrid";
import MetadataBadges from "./components/MetadataBadges";
import TrailersSection from "./components/TrailersSection";
import ExtrasSection from "./components/ExtrasSection";
import ScoreRow from "./components/ScoreRow";
import HeroCrewLine from "./components/HeroCrewLine";
import MediaUserActionBar from "./components/MediaUserActionBar";
import { SeasonCarouselSkeleton, RecommendationGridSkeleton } from "./components/SectionSkeletons";
import { getSeasonDisplayTitle, resolveSeriesPrimaryAction } from "./itemDetailLayout";
import { canCurateMetadata as canCurateMetadataForUser } from "@/lib/permissions";
import { cn } from "@/lib/utils";
import { hasMachineTranslation } from "./components/machineTranslation";

/** Series lead with their creators; one without Creator credits keeps showing its directors. */
const SERIES_LEAD_JOBS = ["Creator", "Director"] as const;

/**
 * The series Crew section lists every creator ahead of the usual crew. Season
 * and episode pages keep CrewList's default jobs, so creators stay on the series.
 */
const SERIES_CREW_JOBS = ["Creator", "Director", "Writer", "Producer"] as const;

export default function SeriesContent({
  item,
  showAdvisoryAge,
}: {
  item: ItemDetail & { type: "series" };
  showAdvisoryAge?: boolean;
}) {
  const { translating: overviewTranslating, onTranslate: onTranslateOverview } =
    useOnViewTranslation(item);
  const navigate = useNavigate();
  const { startShuffle } = useStartShuffle();
  const { search } = useLocation();
  // Follow the item to its new content ID, keeping the query string (such as
  // ?libraryId=) so the page keeps its library scope.
  const followReplacedItem = (contentID: string) =>
    navigate({ pathname: `/item/${contentID}`, search }, { replace: true });
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = useIsActingAdmin();
  const capabilities = useLibraryCapabilities(isAdmin).data;
  const canManageTrickplay =
    capabilities?.trickplay === true && capabilities.trickplay_supported === true;
  const { profile: currentProfile } = useCurrentProfile();
  const canCurateMetadata = canCurateMetadataForUser(user, currentProfile);

  const refreshMetadataMutation = useRefreshItemMetadata();

  const [editOpen, setEditOpen] = useState(false);
  const [matchOpen, setMatchOpen] = useState(false);
  const [splitOpen, setSplitOpen] = useState(false);
  const [requestSeasonsOpen, setRequestSeasonsOpen] = useState(false);
  const tmdbID = Number(item.tmdb_id) || 0;
  const canRequestSeasons = useMissingSeasonsRequestable(Boolean(currentProfile?.id) && tmdbID > 0);
  const { data: seasonsData, isLoading: seasonsLoading } = useSeasons(item.content_id);
  const { data: similarData, isLoading: similarLoading } = useSimilarItems(item.content_id);
  const seasons = useMemo(() => seasonsData?.seasons ?? [], [seasonsData?.seasons]);

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

  const primaryAction = resolveSeriesPrimaryAction(item);
  const singleSeasonEpisodesQuery = useItemEpisodes(singleSeason?.content_id);
  const singleSeasonEpisodeLinkState = singleSeason
    ? {
        parentSeasonHref: `/item/${singleSeason.content_id}`,
        parentSeasonLabel: getSeasonDisplayTitle(singleSeason),
      }
    : undefined;

  // The party's default episode is the one the play button starts. The server
  // resolves each season's target with the same rule as the series target, so
  // the season holding it is the one whose own target matches. Its episode
  // list (shared with the grid on single-season shows) names the episode.
  const nextUpSeason = item.play_content_id
    ? seasons.find((season) => season.play_content_id === item.play_content_id)
    : undefined;
  const nextUpEpisode = useItemEpisodes(nextUpSeason?.content_id).data?.episodes.find(
    (episode) => episode.content_id === item.play_content_id,
  );
  const watchTogether = useDetailWatchTogether({
    item,
    target: nextUpEpisode
      ? {
          content_id: nextUpEpisode.content_id,
          title: nextUpEpisode.title,
          subtitle: `S${nextUpEpisode.season_number} E${nextUpEpisode.episode_number}`,
          poster_url: item.poster_url,
          poster_thumbhash: item.poster_thumbhash,
        }
      : null,
    seriesId: item.content_id,
    initialSeasonNumber: nextUpSeason?.season_number,
  });

  return (
    <DetailLayout
      hero={
        <div className="episode-detail-viewport series-detail-viewport">
          <DetailHero
            variant="series"
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
                advisoryAge={showAdvisoryAge ? (item.advisory_age ?? undefined) : undefined}
                advisorySource={item.advisory_source || undefined}
                seasonCount={seasons.length || undefined}
                episodeCount={episodeCount || undefined}
              />
            }
            scoreRow={<ScoreRow ratings={item.ratings} />}
            overview={item.overview}
            overviewTranslating={overviewTranslating}
            onTranslateOverview={onTranslateOverview}
            overviewMachineTranslated={hasMachineTranslation(item.machine_translated_fields)}
            taglineMachineTranslated={hasMachineTranslation(
              item.machine_translated_fields,
              "tagline",
            )}
            crewLine={
              <HeroCrewLine
                crew={item.crew ?? []}
                genres={item.genres}
                jobLabel="Created by"
                leadJobs={SERIES_LEAD_JOBS}
              />
            }
            actions={
              <MediaUserActionBar
                compactMobile
                item={item}
                contentId={item.content_id}
                watchTogether={watchTogether.menu}
                playHref={primaryAction.href}
                playLabel={primaryAction.label}
                onRefresh={
                  canCurateMetadata
                    ? (mode) =>
                        refreshMetadataMutation.mutate({
                          item,
                          mode,
                          onReplaced: followReplacedItem,
                        })
                    : undefined
                }
                isRefreshing={refreshMetadataMutation.isPending}
                isAdmin={isAdmin}
                canManageTrickplay={canManageTrickplay}
                canCurateMetadata={canCurateMetadata}
                onEditMetadata={canCurateMetadata ? () => setEditOpen(true) : undefined}
                onMatchItem={canCurateMetadata ? () => setMatchOpen(true) : undefined}
                onSplitItem={canCurateMetadata ? () => setSplitOpen(true) : undefined}
                onRequestSeasons={canRequestSeasons ? () => setRequestSeasonsOpen(true) : undefined}
                onShuffle={
                  episodeCount > 1
                    ? () => startShuffle({ kind: "series", id: item.content_id })
                    : undefined
                }
              />
            }
          />
          {canRequestSeasons ? (
            <RequestSeasonsDialog
              open={requestSeasonsOpen}
              onOpenChange={setRequestSeasonsOpen}
              tmdbID={tmdbID}
              title={title}
            />
          ) : null}

          {(seasonsLoading || seasons.length > 0) && (
            <div
              className={cn(
                "page-shell series-detail-navigation",
                !singleSeason && "series-detail-rail",
              )}
              role="region"
              aria-label="Seasons and episodes"
            >
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
                <SeasonCarousel seasons={seasons} />
              )}
            </div>
          )}
        </div>
      }
      overlays={
        <>
          {canCurateMetadata && (
            <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />
          )}
          {canCurateMetadata && (
            <MatchItemDialog
              key={item.content_id}
              item={item}
              open={matchOpen}
              onOpenChange={setMatchOpen}
              onReplaced={followReplacedItem}
            />
          )}
          {canCurateMetadata && (
            <SplitItemDialog
              key={`split-${item.content_id}`}
              item={item}
              open={splitOpen}
              onOpenChange={setSplitOpen}
            />
          )}
          {watchTogether.sheet}
        </>
      }
    >
      {item.videos && item.videos.length > 0 && <TrailersSection videos={item.videos} />}

      {item.extras && item.extras.length > 0 && <ExtrasSection extras={item.extras} />}

      {item.cast && item.cast.length > 0 && (
        <DetailSection title="Cast">
          <CastCarousel cast={item.cast} prefetchPeople />
        </DetailSection>
      )}
      {item.crew && item.crew.length > 0 && <CrewList crew={item.crew} jobs={SERIES_CREW_JOBS} />}

      {similarLoading ? (
        <RecommendationGridSkeleton />
      ) : (
        similarData?.items &&
        similarData.items.length > 0 && <RecommendationGrid items={similarData.items} />
      )}
    </DetailLayout>
  );
}
