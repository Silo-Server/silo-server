import { Link } from "react-router";
import { Film, Tv } from "lucide-react";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import type { RequestMediaResult } from "@/api/types";
import { formatRequestReason, tmdbImageURL } from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import RequestPosterCard from "./RequestPosterCard";

const DIALOG_LIMIT = 4;
const GRID_LIMIT = 20;

export type RequestToAddSectionProps = {
  variant: "dialog" | "grid";
  query: string;
  /** True when the library search returned at least one hit. Drives header copy. */
  libraryHadHits: boolean;
};

export function RequestToAddSection({ variant, query, libraryHadHits }: RequestToAddSectionProps) {
  const { discoveryEnabled } = useCanRequest();
  const search = useRequestSearch("all", query, 1, { enabled: discoveryEnabled });

  if (!discoveryEnabled) return null;
  if (search.isError) return null;

  const filtered = (search.data?.results ?? []).filter(
    (item) => item.availability !== "available",
  );
  if (filtered.length === 0) return null;

  const limit = variant === "dialog" ? DIALOG_LIMIT : GRID_LIMIT;
  const visible = filtered.slice(0, limit);

  if (variant === "dialog") {
    return <DialogVariant items={visible} libraryHadHits={libraryHadHits} />;
  }
  return <GridVariant items={visible} libraryHadHits={libraryHadHits} />;
}

function HeaderCopy({ libraryHadHits, count }: { libraryHadHits: boolean; count: number }) {
  if (libraryHadHits) {
    return (
      <div className="text-muted-foreground flex items-center gap-2 px-3 pt-2 pb-1 text-[10px] font-medium tracking-[0.1em] uppercase">
        <span>Request to Add</span>
        <span className="bg-muted text-muted-foreground rounded-full px-1.5 text-[10px]">
          {count}
        </span>
      </div>
    );
  }

  return (
    <div className="px-3 pt-3 pb-1 text-[12px] text-amber-300/85">
      Not in your library, but you can request:
    </div>
  );
}

function DialogVariant({
  items,
  libraryHadHits,
}: {
  items: RequestMediaResult[];
  libraryHadHits: boolean;
}) {
  return (
    <div className="border-t border-white/5 pt-1">
      <HeaderCopy libraryHadHits={libraryHadHits} count={items.length} />
      <ul className="px-1 py-1">
        {items.map((item) => (
          <li key={`${item.media_type}-${item.tmdb_id}`}>
            <DialogRow item={item} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function DialogRow({ item }: { item: RequestMediaResult }) {
  const poster = tmdbImageURL(item.poster_path);
  const Icon = item.media_type === "series" ? Tv : Film;
  const requestable = item.request.requestable;
  const reasonLabel = !requestable
    ? item.request.reason
      ? formatRequestReason(item.request.reason)
      : "Blocked"
    : null;

  return (
    <Link
      to={`/requests/${item.media_type}/${item.tmdb_id}`}
      className="hover:bg-muted/80 flex w-full items-center gap-3 rounded-md px-3 py-2 text-left transition-colors"
    >
      <div
        className={cn(
          "bg-muted relative h-14 w-10 shrink-0 overflow-hidden rounded-md",
          !requestable && "opacity-70",
        )}
      >
        {poster ? (
          <img src={poster} alt="" className="h-full w-full object-cover" loading="lazy" />
        ) : (
          <div className="text-muted-foreground flex h-full items-center justify-center">
            <Icon className="h-4 w-4" />
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{item.title}</div>
        <div className="text-muted-foreground text-xs">
          {item.year ? `${item.year} · ` : ""}
          {item.media_type === "series" ? "Series" : "Movie"}
        </div>
      </div>
      {requestable ? (
        <span className="rounded-full border border-amber-400/30 bg-amber-400/15 px-2 py-0.5 text-[9px] font-semibold tracking-[0.5px] text-amber-300 uppercase">
          Request
        </span>
      ) : (
        <span
          className="rounded-full border border-white/10 bg-white/5 px-2 py-0.5 text-[9px] font-medium tracking-[0.5px] text-white/60 uppercase"
          title={reasonLabel ?? "Blocked"}
        >
          {reasonLabel}
        </span>
      )}
    </Link>
  );
}

function GridVariant({
  items,
  libraryHadHits,
}: {
  items: RequestMediaResult[];
  libraryHadHits: boolean;
}) {
  return (
    <section className="space-y-3">
      <div className="flex items-center gap-3 text-amber-300/85">
        <div className="h-px flex-1 bg-amber-400/20" />
        <h2 className="text-[11px] font-semibold tracking-[0.12em] uppercase">
          {libraryHadHits ? "Request to Add" : "Not in your library, but you can request"}
        </h2>
        <div className="h-px flex-1 bg-amber-400/20" />
      </div>
      <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8">
        {items.map((item) => (
          <RequestPosterCard
            key={`${item.media_type}-${item.tmdb_id}`}
            variant="discover"
            item={item}
            fluid
          />
        ))}
      </div>
    </section>
  );
}
