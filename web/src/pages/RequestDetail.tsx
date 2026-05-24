import { useMemo } from "react";
import { useNavigate, useParams } from "react-router";
import { ArrowLeft, Check, Clock, Loader2, Plus, Star } from "lucide-react";
import MediaCarousel from "@/components/MediaCarousel";
import RequestPosterCard from "@/components/RequestPosterCard";
import DetailHero from "@/pages/ItemDetail/DetailHero";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { RequestMediaCastMember, RequestMediaDetail, RequestMediaResult } from "@/api/types";
import { useCreateMediaRequest, useRequestMediaDetail } from "@/hooks/queries/requests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { cn } from "@/lib/utils";
import { getInitials } from "@/lib/text";
import { formatRequestReason, formatRequestStatus, tmdbImageURL } from "@/lib/mediaRequests";

export default function RequestDetail() {
  const navigate = useNavigate();
  const params = useParams<{ mediaType: string; tmdbId: string }>();
  const mediaType = (params.mediaType === "series" ? "series" : "movie") as "movie" | "series";
  const tmdbID = Number(params.tmdbId) || 0;

  const detail = useRequestMediaDetail(mediaType, tmdbID);
  const createRequest = useCreateMediaRequest();

  useDocumentTitle(detail.data?.title ?? "Request");

  if (detail.isLoading) {
    return <RequestDetailSkeleton />;
  }

  if (detail.isError || !detail.data) {
    return (
      <div className="page-shell space-y-3 py-12 text-center">
        <p className="text-foreground text-base font-semibold">Couldn't load this title.</p>
        <p className="text-muted-foreground text-sm">
          The TMDB record may be temporarily unavailable.
        </p>
        <div className="pt-4">
          <Button variant="outline" size="sm" onClick={() => navigate(-1)}>
            <ArrowLeft className="h-4 w-4" />
            Back
          </Button>
        </div>
      </div>
    );
  }

  const item = detail.data;
  const backdropUrl = tmdbImageURL(item.backdrop_path, "original") ?? undefined;
  const posterUrl = tmdbImageURL(item.poster_path, "w500") ?? undefined;
  const studioLabel = pickStudioLabel(item);

  return (
    <div>
      <DetailHero
        title={item.title}
        context={<RequestContext mediaType={mediaType} />}
        studioLabel={studioLabel}
        backdropUrl={backdropUrl}
        posterUrl={posterUrl}
        tagline={item.tagline || undefined}
        metadata={<MetaPills item={item} />}
        scoreRow={<RequestScoreRow item={item} />}
        crewLine={<RequestCrewLine item={item} />}
        overview={item.overview}
        actions={
          <RequestActions
            item={item}
            isSubmitting={
              createRequest.isPending && createRequest.variables?.tmdb_id === item.tmdb_id
            }
            onRequest={() =>
              createRequest.mutate({
                media_type: item.media_type,
                tmdb_id: item.tmdb_id,
                title: item.title,
                year: item.year || undefined,
                overview: item.overview || undefined,
                poster_path: item.poster_path || undefined,
                backdrop_path: item.backdrop_path || undefined,
              })
            }
            onBack={() => navigate(-1)}
          />
        }
      />

      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        {item.cast && item.cast.length > 0 && (
          <section>
            <h2 className="mb-5 text-xl font-semibold tracking-tight">Cast</h2>
            <RequestCastRow cast={item.cast} />
          </section>
        )}

        {item.recommendations && item.recommendations.length > 0 && (
          <RecommendationsRow
            recommendations={item.recommendations}
            pendingTMDBID={createRequest.variables?.tmdb_id}
            isSubmitting={createRequest.isPending}
            onRequest={(rec) =>
              createRequest.mutate({
                media_type: rec.media_type,
                tmdb_id: rec.tmdb_id,
                title: rec.title,
                year: rec.year || undefined,
                overview: rec.overview || undefined,
                poster_path: rec.poster_path || undefined,
                backdrop_path: rec.backdrop_path || undefined,
              })
            }
          />
        )}
      </div>
    </div>
  );
}

function RequestContext({ mediaType }: { mediaType: "movie" | "series" }) {
  return (
    <span className="text-muted-foreground inline-flex items-center gap-1.5 text-[11px] font-semibold tracking-[0.22em] uppercase">
      Request · {mediaType === "series" ? "Series" : "Movie"}
    </span>
  );
}

function MetaPills({ item }: { item: RequestMediaDetail }) {
  const pills: string[] = [];
  if (item.year) pills.push(String(item.year));
  if (item.content_rating) pills.push(item.content_rating);
  if (item.media_type === "movie" && item.runtime) pills.push(formatDuration(item.runtime));
  if (item.media_type === "series" && item.number_of_seasons)
    pills.push(`${item.number_of_seasons} Season${item.number_of_seasons === 1 ? "" : "s"}`);
  if (item.media_type === "series" && item.status) pills.push(item.status);

  return (
    <div className="flex flex-wrap items-center gap-2">
      {pills.map((pill) => (
        <span
          key={pill}
          className="border-border/60 bg-card/40 inline-flex items-center rounded-md border px-2 py-0.5 text-[12px] font-medium tracking-normal"
        >
          {pill}
        </span>
      ))}
      {(item.genres ?? []).slice(0, 4).map((genre) => (
        <span
          key={genre}
          className="text-muted-foreground inline-flex items-center rounded-md px-1 py-0.5 text-[12px]"
        >
          {genre}
        </span>
      ))}
    </div>
  );
}

function RequestScoreRow({ item }: { item: RequestMediaDetail }) {
  if (!item.vote_average) return null;
  return (
    <div className="text-muted-foreground flex items-center gap-3 text-[13px]">
      <span className="inline-flex items-center gap-1.5">
        <Star className="h-3.5 w-3.5 fill-amber-300/90 text-amber-300/90" />
        <span className="text-foreground tabular-nums">{item.vote_average.toFixed(1)}</span>
        <span className="opacity-60">TMDB</span>
      </span>
      {item.vote_count ? (
        <span className="tabular-nums opacity-70">{formatVoteCount(item.vote_count)} votes</span>
      ) : null}
    </div>
  );
}

function RequestCrewLine({ item }: { item: RequestMediaDetail }) {
  const parts: { label: string; value: string }[] = [];
  if (item.director) parts.push({ label: "Director", value: item.director });
  if (item.creators && item.creators.length > 0)
    parts.push({ label: "Created by", value: item.creators.join(", ") });
  if (item.networks && item.networks.length > 0)
    parts.push({ label: "Network", value: item.networks.join(", ") });

  if (parts.length === 0) return null;

  return (
    <div className="text-muted-foreground flex flex-wrap gap-x-5 gap-y-1 text-[13px]">
      {parts.map((part) => (
        <span key={part.label} className="inline-flex gap-1.5">
          <span className="opacity-60">{part.label}:</span>
          <span className="text-foreground/90">{part.value}</span>
        </span>
      ))}
    </div>
  );
}

function RequestActions({
  item,
  isSubmitting,
  onRequest,
  onBack,
}: {
  item: RequestMediaDetail;
  isSubmitting: boolean;
  onRequest: () => void;
  onBack: () => void;
}) {
  const requestable = item.request.requestable;
  const statusLabel = item.request.status ? formatRequestStatus(item.request.status) : null;
  const reasonLabel =
    !requestable && !item.request.status ? formatRequestReason(item.request.reason) : null;
  const availableInLibrary = item.availability === "available" && !item.request.status;

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        variant="ghost"
        size="sm"
        onClick={onBack}
        className="text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" />
        Back
      </Button>

      {requestable ? (
        <Button
          onClick={onRequest}
          disabled={isSubmitting}
          className="h-11 rounded-full px-6 text-sm font-semibold"
        >
          {isSubmitting ? (
            <>
              <Loader2 className="h-4 w-4 animate-spin" />
              Submitting
            </>
          ) : (
            <>
              <Plus className="h-4 w-4 stroke-[2.5]" />
              Request {item.media_type === "series" ? "series" : "movie"}
            </>
          )}
        </Button>
      ) : availableInLibrary ? (
        <StatusBlock
          tone="emerald"
          icon={<Check className="h-4 w-4 stroke-[2.5]" />}
          label="Already in your library"
        />
      ) : statusLabel ? (
        <StatusBlock
          tone={statusToneForStatus(item.request.status!)}
          icon={<Clock className="h-4 w-4" />}
          label={statusLabel}
        />
      ) : (
        <StatusBlock
          tone="zinc"
          icon={<Clock className="h-4 w-4" />}
          label={reasonLabel ?? "Unavailable"}
        />
      )}

      {item.imdb_id ? (
        <a
          href={`https://www.imdb.com/title/${item.imdb_id}`}
          target="_blank"
          rel="noreferrer"
          className="text-muted-foreground hover:text-foreground border-border/60 inline-flex items-center rounded-full border px-3 py-1.5 text-[12px] font-medium tracking-wide transition-colors"
        >
          IMDb
        </a>
      ) : null}
      <a
        href={`https://www.themoviedb.org/${item.media_type === "series" ? "tv" : "movie"}/${item.tmdb_id}`}
        target="_blank"
        rel="noreferrer"
        className="text-muted-foreground hover:text-foreground border-border/60 inline-flex items-center rounded-full border px-3 py-1.5 text-[12px] font-medium tracking-wide transition-colors"
      >
        TMDB
      </a>
    </div>
  );
}

const STATUS_TONES: Record<"amber" | "sky" | "emerald" | "zinc", string> = {
  amber: "bg-amber-500/15 text-amber-100 ring-amber-400/40",
  sky: "bg-sky-500/15 text-sky-100 ring-sky-400/40",
  emerald: "bg-emerald-500/15 text-emerald-100 ring-emerald-400/40",
  zinc: "bg-zinc-700/60 text-zinc-200 ring-zinc-500/40",
};

function StatusBlock({
  tone,
  icon,
  label,
}: {
  tone: "amber" | "sky" | "emerald" | "zinc";
  icon: React.ReactNode;
  label: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex h-11 items-center gap-2 rounded-full px-5 text-sm font-semibold ring-1",
        STATUS_TONES[tone],
      )}
    >
      {icon}
      {label}
    </span>
  );
}

function statusToneForStatus(status: string): "amber" | "sky" | "emerald" | "zinc" {
  switch (status) {
    case "pending":
      return "amber";
    case "approved":
    case "completed":
      return "emerald";
    case "queued":
    case "downloading":
      return "sky";
    default:
      return "zinc";
  }
}

function RequestCastRow({ cast }: { cast: RequestMediaCastMember[] }) {
  const sorted = useMemo(
    () =>
      cast
        .slice()
        .sort((a, b) => a.order - b.order)
        .slice(0, 24),
    [cast],
  );
  return (
    <div className="-mx-4 overflow-x-auto px-4 sm:-mx-6 sm:px-6 lg:-mx-10 lg:px-10 xl:-mx-12 xl:px-12">
      <ul role="list" className="flex w-max list-none gap-3">
        {sorted.map((member, index) => {
          const photo = tmdbImageURL(member.profile_path, "w185");
          return (
            <li key={`${member.name}-${index}`} className="w-[110px] shrink-0">
              <div className="media-card-image mb-2.5 aspect-[2/3] overflow-hidden rounded-lg">
                {photo ? (
                  <img
                    src={photo}
                    alt={member.name}
                    loading="lazy"
                    className="h-full w-full object-cover"
                  />
                ) : (
                  <div className="bg-surface text-muted-foreground flex h-full w-full items-center justify-center text-lg font-semibold">
                    {getInitials(member.name)}
                  </div>
                )}
              </div>
              <div className="px-0.5">
                <div className="text-foreground truncate text-[13px] font-medium">
                  {member.name}
                </div>
                {member.character ? (
                  <div className="text-muted-foreground truncate text-[11px]">
                    {member.character}
                  </div>
                ) : null}
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function RecommendationsRow({
  recommendations,
  pendingTMDBID,
  isSubmitting,
  onRequest,
}: {
  recommendations: RequestMediaResult[];
  pendingTMDBID?: number;
  isSubmitting: boolean;
  onRequest: (item: RequestMediaResult) => void;
}) {
  return (
    <section className="space-y-1">
      <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
        <span className="text-muted-foreground text-[10px] font-semibold tracking-[0.22em] uppercase">
          More like this
        </span>
      </div>
      <MediaCarousel title="Recommendations">
        {recommendations.map((item) => (
          <RequestPosterCard
            key={`${item.media_type}-${item.tmdb_id}`}
            variant="discover"
            item={item}
            isSubmitting={isSubmitting && pendingTMDBID === item.tmdb_id}
            onRequest={() => onRequest(item)}
          />
        ))}
      </MediaCarousel>
    </section>
  );
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

function formatDuration(minutes: number): string {
  if (minutes <= 0) return "";
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  if (h <= 0) return `${m}m`;
  return m === 0 ? `${h}h` : `${h}h ${m}m`;
}

function formatVoteCount(count: number): string {
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`;
  return String(count);
}

function RequestDetailSkeleton() {
  return (
    <div>
      <section className="border-border/10 relative isolate overflow-hidden border-b">
        <div className="absolute inset-0 bg-gradient-to-r from-[var(--background)] via-[var(--background)]/70 to-transparent" />
        <div className="absolute inset-0 bg-gradient-to-t from-[var(--background)] via-[var(--background)]/40 to-transparent" />
        <div className="page-shell-wide relative flex min-h-[60dvh] flex-col justify-end pt-28 pb-8 lg:min-h-[72dvh]">
          <div className="flex flex-col gap-6 lg:flex-row lg:items-end">
            <Skeleton className="aspect-[2/3] w-[170px] flex-shrink-0 rounded-lg sm:w-[220px]" />
            <div className="max-w-3xl flex-1 space-y-4">
              <Skeleton className="h-4 w-24" />
              <Skeleton className="h-10 w-80 max-w-full" />
              <Skeleton className="h-5 w-48" />
              <Skeleton className="h-4 w-full max-w-2xl" />
              <Skeleton className="h-4 w-5/6 max-w-xl" />
              <Skeleton className="h-4 w-3/4 max-w-lg" />
              <div className="flex gap-3 pt-2">
                <Skeleton className="h-11 w-40 rounded-full" />
                <Skeleton className="h-11 w-20 rounded-full" />
              </div>
            </div>
          </div>
        </div>
      </section>
      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        <div>
          <Skeleton className="mb-5 h-6 w-24 rounded" />
          <div className="flex gap-3 overflow-hidden">
            {Array.from({ length: 8 }).map((_, i) => (
              <Skeleton key={i} className="aspect-[2/3] w-[110px] shrink-0 rounded-lg" />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
