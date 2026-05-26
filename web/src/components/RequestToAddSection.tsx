import { useState } from "react";
import { Link } from "react-router";
import { Film, Sparkles, Tv } from "lucide-react";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useCreateMediaRequest, useRequestSearch } from "@/hooks/queries/useRequests";
import type { RequestMediaResult } from "@/api/types";
import {
  formatRequestReason,
  formatRequestStatus,
  requestInputFromMediaResult,
  tmdbImageURL,
} from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import RequestPosterCard from "./RequestPosterCard";

function cardKey(item: Pick<RequestMediaResult, "media_type" | "tmdb_id">): string {
  return `${item.media_type}-${item.tmdb_id}`;
}

function nonRequestableLabel(item: RequestMediaResult): string {
  if (item.request.status) {
    return formatRequestStatus(item.request.status);
  }
  if (item.request.reason) {
    return formatRequestReason(item.request.reason);
  }
  return "Blocked";
}

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
  const search = useRequestSearch("all", query, 1, {
    enabled: discoveryEnabled,
    requireProfile: true,
    staleTime: 5 * 60 * 1000,
  });

  if (!discoveryEnabled) return null;
  if (search.isError && !search.data) return null;

  const filtered = (search.data?.results ?? []).filter((item) => item.availability !== "available");
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
  const unavailableLabel = requestable ? null : nonRequestableLabel(item);

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
          title={unavailableLabel ?? "Blocked"}
        >
          {unavailableLabel}
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
  const count = items.length;
  const createRequest = useCreateMediaRequest();
  // Track each in-flight card key independently; the shared `useMutation`
  // observer overwrites its `variables` on every `mutate` call, so rapid
  // clicks on different cards would otherwise trample each other's spinner.
  const [pendingKeys, setPendingKeys] = useState<ReadonlySet<string>>(new Set());
  const submitCard = (item: RequestMediaResult) => {
    const key = cardKey(item);
    setPendingKeys((prev) => {
      const next = new Set(prev);
      next.add(key);
      return next;
    });
    createRequest.mutate(requestInputFromMediaResult(item), {
      onSettled: () => {
        setPendingKeys((prev) => {
          if (!prev.has(key)) return prev;
          const next = new Set(prev);
          next.delete(key);
          return next;
        });
      },
    });
  };
  return (
    <section
      className={cn(
        "relative overflow-hidden rounded-[28px] border border-amber-400/[0.14]",
        "bg-[radial-gradient(120%_60%_at_50%_0%,rgba(245,158,11,0.07)_0%,rgba(245,158,11,0.015)_45%,transparent_75%)]",
        "shadow-[inset_0_1px_0_0_rgba(255,255,255,0.04),0_28px_60px_-44px_rgba(0,0,0,0.7)]",
        "px-4 pt-7 pb-7 sm:px-7 sm:pt-8",
        libraryHadHits && "mt-12! sm:mt-16!",
      )}
    >
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-16 top-0 h-px bg-gradient-to-r from-transparent via-amber-300/45 to-transparent"
      />

      <header className="mb-7 flex flex-wrap items-end justify-between gap-x-5 gap-y-3">
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-amber-200/85">
            <Sparkles className="h-3.5 w-3.5" strokeWidth={2.2} aria-hidden />
            <span className="text-[10px] font-semibold tracking-[0.24em] uppercase">
              {libraryHadHits ? "Discover · Outside your library" : "Outside your library"}
            </span>
          </div>
          <h2 className="font-display text-foreground text-[clamp(1.25rem,1.6vw,1.55rem)] leading-tight font-semibold tracking-tight">
            {libraryHadHits ? "Request to Add" : "Not in your library, but you can request"}
          </h2>
        </div>
        <span className="inline-flex items-center gap-1.5 self-end rounded-full border border-amber-400/15 bg-amber-400/[0.06] px-2.5 py-1 text-[11px] font-medium tracking-wide text-amber-100/75 tabular-nums">
          {count} {count === 1 ? "result" : "results"}
        </span>
      </header>

      <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8">
        {items.map((item) => {
          const key = cardKey(item);
          return (
            <RequestPosterCard
              key={key}
              variant="discover"
              item={item}
              isSubmitting={pendingKeys.has(key)}
              onRequest={() => submitCard(item)}
              fluid
            />
          );
        })}
      </div>
    </section>
  );
}
