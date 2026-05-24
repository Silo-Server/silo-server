import { Link } from "react-router";
import { Check, Film, Loader2, Plus, Tv } from "lucide-react";
import type { MediaRequest, RequestMediaResult } from "@/api/types";
import { cn } from "@/lib/utils";
import { formatRequestReason, formatRequestStatus, tmdbImageURL } from "@/lib/mediaRequests";

const POSTER_WIDTH = "w-[148px] sm:w-[164px] lg:w-[184px]";

type DiscoverProps = {
  variant: "discover";
  item: RequestMediaResult;
  isSubmitting: boolean;
  onRequest: () => void;
};

type MineProps = {
  variant: "mine";
  request: MediaRequest;
};

export type RequestPosterCardProps = DiscoverProps | MineProps;

export default function RequestPosterCard(props: RequestPosterCardProps) {
  if (props.variant === "mine") {
    return <MineCard request={props.request} />;
  }
  return (
    <DiscoverCard item={props.item} isSubmitting={props.isSubmitting} onRequest={props.onRequest} />
  );
}

function DiscoverCard({
  item,
  isSubmitting,
  onRequest,
}: {
  item: RequestMediaResult;
  isSubmitting: boolean;
  onRequest: () => void;
}) {
  const poster = tmdbImageURL(item.poster_path);
  const requestable = item.request.requestable;
  const statusLabel = item.request.status ? formatRequestStatus(item.request.status) : null;
  const reasonLabel =
    !requestable && !item.request.status ? formatRequestReason(item.request.reason) : null;
  const availableInLibrary = item.availability === "available" && !item.request.status;

  return (
    <Link
      to={`/requests/${item.media_type}/${item.tmdb_id}`}
      className={cn(
        "group/req-card relative block focus:outline-none focus-visible:outline-none",
        POSTER_WIDTH,
      )}
    >
      <PosterFrame
        poster={poster}
        title={item.title}
        mediaType={item.media_type}
        dim={!requestable}
      >
        <TypeBadge mediaType={item.media_type} />

        {/* Status ribbon for already-requested or in-library items */}
        {statusLabel ? (
          <StatusRibbon status={item.request.status!} label={statusLabel} />
        ) : availableInLibrary ? (
          <StatusRibbon status="completed" label="In library" />
        ) : reasonLabel ? (
          <StatusRibbon status="blocked" label={reasonLabel} />
        ) : null}

        {/* Hover Request overlay — only for requestable items */}
        {requestable && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 flex translate-y-2 items-end justify-center bg-gradient-to-t from-black/85 via-black/50 to-transparent p-3 opacity-0 transition-all duration-200 ease-out group-focus-within/req-card:translate-y-0 group-focus-within/req-card:opacity-100 group-hover/req-card:translate-y-0 group-hover/req-card:opacity-100">
            <button
              type="button"
              disabled={isSubmitting}
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                onRequest();
              }}
              className="pointer-events-auto inline-flex items-center gap-1.5 rounded-full bg-white px-3.5 py-1.5 text-[12px] font-semibold tracking-wide text-black shadow-lg shadow-black/40 transition-all hover:scale-[1.03] active:scale-[0.97] disabled:opacity-70"
            >
              {isSubmitting ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  Sending
                </>
              ) : (
                <>
                  <Plus className="h-3.5 w-3.5 stroke-[2.5]" />
                  Request
                </>
              )}
            </button>
          </div>
        )}
      </PosterFrame>

      <CardMeta title={item.title} year={item.year} rating={item.vote_average} />
    </Link>
  );
}

function MineCard({ request }: { request: MediaRequest }) {
  const poster = tmdbImageURL(request.poster_path);
  const isDownloading = request.status === "downloading";
  const isCompleted = request.status === "completed";
  const isFailed =
    request.outcome === "failed" ||
    request.outcome === "declined" ||
    request.outcome === "cancelled";

  return (
    <Link
      to={`/requests/${request.media_type}/${request.tmdb_id}`}
      className={cn(
        "group/req-card relative block focus:outline-none focus-visible:outline-none",
        POSTER_WIDTH,
      )}
    >
      <PosterFrame
        poster={poster}
        title={request.title}
        mediaType={request.media_type}
        dim={isFailed}
      >
        <TypeBadge mediaType={request.media_type} />
        <StatusRibbon
          status={isFailed ? "blocked" : request.status}
          label={isFailed ? formatOutcome(request.outcome) : formatRequestStatus(request.status)}
        />

        {/* Animated indicator at the bottom of the poster */}
        {isDownloading && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-1 overflow-hidden bg-black/40">
            <div className="downloading-shimmer h-full bg-sky-400" />
          </div>
        )}
        {isCompleted && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 flex items-center justify-center bg-gradient-to-t from-emerald-950/90 via-emerald-900/40 to-transparent p-3">
            <span className="inline-flex items-center gap-1 rounded-full bg-emerald-500/15 px-2.5 py-0.5 text-[11px] font-semibold tracking-wide text-emerald-200 ring-1 ring-emerald-400/30">
              <Check className="h-3 w-3 stroke-[2.5]" />
              Ready to watch
            </span>
          </div>
        )}
      </PosterFrame>

      <CardMeta title={request.title} year={request.year} />

      {request.last_error ? (
        <p
          className="mt-1 line-clamp-2 text-[11px] leading-tight text-red-300/90"
          title={request.last_error}
        >
          {request.last_error}
        </p>
      ) : null}
    </Link>
  );
}

function PosterFrame({
  poster,
  title,
  mediaType,
  dim,
  children,
}: {
  poster: string | null;
  title: string;
  mediaType: "movie" | "series";
  dim?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <div className="media-card-image relative aspect-[2/3]">
      {poster ? (
        <img
          src={poster}
          alt=""
          loading="lazy"
          className={cn(
            "h-full w-full object-cover transition-[transform,filter] duration-300 group-hover/req-card:scale-[1.04]",
            dim && "brightness-[0.85] saturate-[0.8]",
          )}
        />
      ) : (
        <div className="bg-muted text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-2 p-3 text-center">
          {mediaType === "series" ? <Tv className="h-7 w-7" /> : <Film className="h-7 w-7" />}
          <span className="line-clamp-3 text-xs font-medium">{title}</span>
        </div>
      )}
      {/* subtle bottom vignette for legibility behind badges */}
      <div className="pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t from-black/55 to-transparent opacity-90" />
      {children}
    </div>
  );
}

function TypeBadge({ mediaType }: { mediaType: "movie" | "series" }) {
  return (
    <span className="absolute top-2.5 left-2.5 inline-flex items-center gap-1 rounded-full border border-white/25 bg-black/80 px-2 py-0.5 text-[10px] leading-none font-semibold tracking-[0.14em] text-white uppercase shadow-sm shadow-black/40 backdrop-blur-md">
      {mediaType === "series" ? "Series" : "Movie"}
    </span>
  );
}

function CardMeta({ title, year, rating }: { title: string; year?: number; rating?: number }) {
  return (
    <div className="mt-2 min-w-0 px-0.5">
      <h3 className="text-foreground line-clamp-1 text-[13px] leading-tight font-semibold tracking-normal">
        {title}
      </h3>
      <div className="text-muted-foreground mt-0.5 flex items-center gap-1.5 text-[11px]">
        {year ? <span className="tabular-nums">{year}</span> : null}
        {year && rating ? <span className="opacity-50">·</span> : null}
        {rating ? (
          <span className="tabular-nums">
            <span className="text-amber-300/80">★</span> {rating.toFixed(1)}
          </span>
        ) : null}
      </div>
    </div>
  );
}

type RibbonKind = "pending" | "approved" | "queued" | "downloading" | "completed" | "blocked";

const RIBBON_STYLES: Record<RibbonKind, string> = {
  pending:
    "bg-amber-500/85 text-amber-50 ring-amber-300/60 [&_.dot]:bg-amber-200 [&_.dot]:animate-pulse",
  approved: "bg-emerald-500/85 text-emerald-50 ring-emerald-300/60 [&_.dot]:bg-emerald-200",
  queued: "bg-sky-500/85 text-sky-50 ring-sky-300/60 [&_.dot]:bg-sky-200 [&_.dot]:animate-pulse",
  downloading:
    "bg-sky-500/90 text-sky-50 ring-sky-300/70 [&_.dot]:bg-sky-200 [&_.dot]:animate-pulse",
  completed: "bg-emerald-500/90 text-emerald-50 ring-emerald-300/60 [&_.dot]:bg-emerald-200",
  blocked: "bg-zinc-800/85 text-zinc-50 ring-zinc-400/60 [&_.dot]:bg-zinc-300",
};

function StatusRibbon({ status, label }: { status: string; label: string }) {
  const kind = (RIBBON_STYLES[status as RibbonKind] ? status : "blocked") as RibbonKind;
  return (
    <span
      className={cn(
        "absolute top-2.5 right-2.5 inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] leading-none font-semibold tracking-[0.08em] uppercase shadow-sm ring-1 shadow-black/40 backdrop-blur-md",
        RIBBON_STYLES[kind],
      )}
    >
      <span className="dot inline-block h-1.5 w-1.5 rounded-full" />
      {label}
    </span>
  );
}

function formatOutcome(outcome: MediaRequest["outcome"]): string {
  switch (outcome) {
    case "declined":
      return "Declined";
    case "cancelled":
      return "Cancelled";
    case "failed":
      return "Failed";
    default:
      return "Active";
  }
}
