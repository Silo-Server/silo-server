import { useMemo } from "react";
import type {
  CastMember,
  CrewMember,
  RequestMediaCastMember,
  RequestMediaDetail,
  RequestMediaResult,
  RequestMediaSeason,
} from "@/api/types";
import CastCarousel from "@/components/CastCarousel";
import PageBack from "@/components/PageBack";
import { MoreLikeThisRow } from "@/components/RecommendationGrid";
import RequestPosterCard from "@/components/RequestPosterCard";
import { SeasonStatus } from "@/components/RequestSeasonsDialog";
import { useCreateMediaRequest } from "@/hooks/queries/useRequests";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import {
  formatRequestSeasonMeta,
  requestInputFromMediaResult,
  tmdbImageURL,
} from "@/lib/mediaRequests";
import DetailHero from "./DetailHero";
import DetailLayout, { DetailSection } from "./DetailLayout";
import HeroCrewLine from "./components/HeroCrewLine";
import MetadataBadges from "./components/MetadataBadges";
import RequestActionBar from "./components/RequestActionBar";
import ScoreRow from "./components/ScoreRow";
import { SeasonRail } from "./SeasonCarousel";

interface ExternalTitleContentProps {
  item: RequestMediaDetail;
  /** Passed through to RequestActionBar: opens the library's copy. */
  libraryHref?: string;
}

/**
 * A movie or series known only from TMDB, laid out like a library title with
 * request actions in place of playback. TMDB supplies no thumbhash, so the
 * page keeps the default ambient tint.
 */
export default function ExternalTitleContent({ item, libraryHref }: ExternalTitleContentProps) {
  const isSeries = item.media_type === "series";
  const cast = useMemo(() => castFromTMDB(item.cast ?? []), [item.cast]);
  const crew = useMemo(
    () => crewFromTMDB(isSeries ? (item.creators ?? []) : item.director ? [item.director] : []),
    [isSeries, item.creators, item.director],
  );
  const recommendations = item.recommendations ?? [];

  return (
    <DetailLayout
      hero={
        <DetailHero
          title={item.title}
          topNav={<PageBack to="/requests" />}
          context={isSeries ? "Series" : "Movie"}
          studioLabel={pickStudioLabel(item)}
          backdropUrl={tmdbImageURL(item.backdrop_path, "original") ?? undefined}
          posterUrl={tmdbImageURL(item.poster_path, "w500") ?? undefined}
          tagline={item.tagline || undefined}
          metadata={
            <MetadataBadges
              year={yearLabel(item)}
              contentRating={item.content_rating || undefined}
              duration={!isSeries && item.runtime ? formatRuntimeMinutes(item.runtime) : undefined}
              seasonCount={isSeries ? item.number_of_seasons || undefined : undefined}
              episodeCount={isSeries ? item.number_of_episodes || undefined : undefined}
              status={isSeries ? item.status || undefined : undefined}
            />
          }
          scoreRow={
            <ScoreRow ratingTmdb={item.vote_average || undefined} tmdbVoteCount={item.vote_count} />
          }
          overview={item.overview}
          crewLine={
            <HeroCrewLine
              crew={crew}
              genres={item.genres}
              jobLabel={isSeries ? "Created by" : undefined}
            />
          }
          actions={<RequestActionBar item={item} libraryHref={libraryHref} />}
        />
      }
    >
      {isSeries && (item.seasons?.length ?? 0) > 0 && <TitleSeasons seasons={item.seasons!} />}

      {cast.length > 0 && (
        <DetailSection title="Cast">
          <CastCarousel cast={cast} />
        </DetailSection>
      )}

      {recommendations.length > 0 && <TitleRecommendations items={recommendations} />}
    </DetailLayout>
  );
}

/** A series' seasons with what the library has and what is requested, like the library's season row. */
function TitleSeasons({ seasons }: { seasons: RequestMediaSeason[] }) {
  return (
    <SeasonRail count={seasons.length}>
      {seasons.map((season) => {
        const name = `Season ${season.season_number}`;
        const poster = tmdbImageURL(season.poster_path);
        return (
          <li key={season.season_number} className="embla__slide w-[160px] shrink-0 sm:w-[170px]">
            <div className="media-card-image relative aspect-[2/3] overflow-hidden rounded-xl">
              {poster ? (
                <img
                  src={poster}
                  alt={name}
                  className="h-full w-full object-cover"
                  loading="lazy"
                  decoding="async"
                />
              ) : (
                <div className="text-muted-foreground bg-surface flex h-full items-center justify-center p-4 text-center text-sm font-medium">
                  {name}
                </div>
              )}
            </div>
            <p className="truncate px-0.5 pt-2.5 text-[13px] font-semibold">
              {season.name || name}
            </p>
            <p className="text-muted-foreground truncate px-0.5 text-xs">
              {formatRequestSeasonMeta(season)}
            </p>
            <SeasonStatus season={season} className="mt-0.5 block px-0.5" />
          </li>
        );
      })}
    </SeasonRail>
  );
}

/** "More Like This" for a TMDB title: request cards, each able to request its title. */
function TitleRecommendations({ items }: { items: RequestMediaResult[] }) {
  const createRequest = useCreateMediaRequest();
  return (
    <MoreLikeThisRow
      items={items}
      itemKey={(item) => `${item.media_type}-${item.tmdb_id}`}
      renderItem={(item) => (
        <RequestPosterCard
          variant="discover"
          fluid
          item={item}
          isSubmitting={
            createRequest.isPending &&
            createRequest.variables?.media_type === item.media_type &&
            createRequest.variables?.tmdb_id === item.tmdb_id
          }
          onRequest={() => createRequest.mutate(requestInputFromMediaResult(item))}
        />
      )}
    />
  );
}

function castFromTMDB(cast: RequestMediaCastMember[]): CastMember[] {
  return cast.map((member) => ({
    name: member.name,
    character: member.character ?? "",
    order: member.order,
    person_id: "",
    photo_url: tmdbImageURL(member.profile_path, "w185") ?? undefined,
  }));
}

/**
 * TMDB names a movie's director and a series' creators without person IDs,
 * so the crew line shows them unlinked. Library series credit their creators
 * as directors under a "Created by" label; this follows suit.
 */
function crewFromTMDB(names: string[]): CrewMember[] {
  return names.map((name) => ({ name, job: "Director", person_id: "" }));
}

/** A series spans its air years the way the library's series page shows them. */
function yearLabel(item: RequestMediaDetail): string | undefined {
  if (item.media_type === "series") {
    const firstYear = item.first_air_date?.slice(0, 4);
    const lastYear = item.last_air_date?.slice(0, 4);
    if (firstYear) {
      return lastYear && lastYear !== firstYear ? `${firstYear}–${lastYear}` : firstYear;
    }
  }
  return item.year ? String(item.year) : undefined;
}

function pickStudioLabel(item: RequestMediaDetail): string | undefined {
  if (item.media_type === "series" && item.networks && item.networks.length > 0) {
    return item.networks[0];
  }
  if (item.production_companies && item.production_companies.length > 0) {
    return item.production_companies[0];
  }
  return undefined;
}
