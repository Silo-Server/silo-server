import { useState } from "react";
import { Link, useNavigate } from "react-router";
import type { ItemDetail } from "@/api/types";
import { useItemEpisodes } from "@/hooks/queries/episodes";
import { useRefreshItemMetadata, useWatchedStateMutation } from "@/hooks/queries/items";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useAuth } from "@/hooks/useAuth";
import CastCarousel from "@/components/CastCarousel";
import CrewList from "@/components/CrewList";
import EditMetadataDialog from "@/components/EditMetadataDialog";
import PageBack from "@/components/PageBack";
import DetailHero from "./DetailHero";
import MetadataBadges from "./components/MetadataBadges";
import ActionBar from "./components/ActionBar";
import DetailBreadcrumb from "./components/DetailBreadcrumb";
import SeasonEpisodeGrid from "./components/SeasonEpisodeGrid";
import type { EpisodeNavigationState } from "./itemDetailLayout";
import { getWatchedActionLabel } from "./watchedState";

function seasonLabel(seasonNumber: number, title?: string) {
  if (title) return title;
  if (seasonNumber === 0) return "Specials";
  return `Season ${seasonNumber}`;
}

export default function SeasonContent({ item }: { item: ItemDetail & { type: "season" } }) {
  const navigate = useNavigate();
  useAmbientColor(item.backdrop_thumbhash);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [editOpen, setEditOpen] = useState(false);
  const watchedMutation = useWatchedStateMutation(item);
  const refreshMetadataMutation = useRefreshItemMetadata();

  const {
    data: episodesData,
    isLoading: episodesLoading,
    error: episodesError,
  } = useItemEpisodes(item.content_id);

  const episodes = episodesData?.episodes ?? [];
  const seasonNumber = item.season_number ?? 0;
  const label = item.is_specials ? "Specials" : seasonLabel(seasonNumber, item.title);
  const seriesTitle = item.series_title ?? "Series";
  const seriesId = item.series_id;
  const firstEpisode = episodes[0];
  const episodeLinkState: EpisodeNavigationState = {
    parentSeasonHref: `/item/${item.content_id}`,
    parentSeasonLabel: label,
  };

  const displayTitle = `${seriesTitle}: ${label}`;

  const breadcrumb = (
    <DetailBreadcrumb
      segments={[
        { label: seriesTitle, href: seriesId ? `/item/${seriesId}` : "/" },
        { label: label },
      ]}
    />
  );

  const yearStr = item.air_date?.slice(0, 4);

  if (episodesError) {
    return (
      <div className="px-4 py-6 sm:px-6 sm:py-10 lg:px-12">
        <Link
          to={seriesId ? `/item/${seriesId}` : "/"}
          className="text-muted-foreground hover:text-foreground text-sm"
        >
          &larr; Back to {seriesTitle}
        </Link>
        <p className="text-muted-foreground mt-6 text-sm">
          {episodesError instanceof Error ? episodesError.message : "Season not found"}
        </p>
      </div>
    );
  }

  return (
    <div>
      <DetailHero
        variant="compact"
        title={displayTitle}
        topNav={<PageBack />}
        context={breadcrumb}
        backdropUrl={item.backdrop_url}
        backdropThumbhash={item.backdrop_thumbhash}
        posterUrl={item.poster_url}
        posterThumbhash={item.poster_thumbhash}
        logoUrl={item.logo_url}
        metadata={
          <MetadataBadges
            year={yearStr || undefined}
            episodeCount={item.episode_count ?? episodes.length}
          />
        }
        overview={item.overview}
        actions={
          <ActionBar
            contentId={item.content_id}
            playHref={firstEpisode ? `/watch/${firstEpisode.content_id}` : undefined}
            playLabel="Play First Episode"
            watchedLabel={getWatchedActionLabel(item)}
            onToggleWatched={() => watchedMutation.mutate(!(item.user_data?.played ?? false))}
            isUpdatingWatched={watchedMutation.isPending}
            onRefresh={(mode) =>
              refreshMetadataMutation.mutate({
                item,
                mode,
                onReplaced: (contentID) => navigate(`/item/${contentID}`, { replace: true }),
              })
            }
            isRefreshing={refreshMetadataMutation.isPending}
            isAdmin={isAdmin}
            onEditMetadata={isAdmin ? () => setEditOpen(true) : undefined}
          />
        }
      />

      <div className="page-shell py-8 sm:py-10">
        <div className="mb-5 flex items-center justify-between gap-3">
          <h2 className="text-xl font-semibold tracking-tight">Episodes</h2>
          <span className="text-muted-foreground text-sm">
            {item.episode_count ?? episodes.length} total
          </span>
        </div>

        <SeasonEpisodeGrid
          episodes={episodes}
          isLoading={episodesLoading}
          episodeLinkState={episodeLinkState}
        />

        {item.cast && item.cast.length > 0 && (
          <div className="mt-10">
            <h2 className="mb-4 text-xl font-semibold tracking-tight">Cast</h2>
            <CastCarousel cast={item.cast} />
          </div>
        )}

        {item.crew && item.crew.length > 0 && (
          <div className="mt-10">
            <CrewList crew={item.crew} />
          </div>
        )}
      </div>
      {isAdmin && <EditMetadataDialog item={item} open={editOpen} onOpenChange={setEditOpen} />}
    </div>
  );
}
