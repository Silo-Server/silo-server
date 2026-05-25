import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useSearchParams } from "react-router";
import { Search, Sparkles, X } from "lucide-react";
import BrandCarousel from "@/components/BrandCarousel";
import MediaCarousel from "@/components/MediaCarousel";
import RequestPosterCard from "@/components/RequestPosterCard";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type {
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestDiscoverySection,
  RequestMediaResult,
  RequestSearchMediaType,
} from "@/api/types";
import {
  useCreateMediaRequest,
  useDiscoverGenres,
  useDiscoverNetworks,
  useDiscoverStudios,
  useMyMediaRequests,
  useRequestDiscovery,
  useRequestSearch,
} from "@/hooks/queries/useRequests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { cn } from "@/lib/utils";
import { formatRequestStatus, requestInputFromMediaResult } from "@/lib/mediaRequests";

type MineBucketKey = "motion" | "completed" | "issues";
type RequestTab = "discover" | "yours";
type StatusGuideItem = {
  description: string;
  tone: string;
};

const REQUEST_TABS = ["discover", "yours"] as const;

const MINE_BUCKET_META: Record<MineBucketKey, { title: string; eyebrow: string; accent: string }> =
  {
    motion: {
      title: "In motion",
      eyebrow: "On their way",
      accent: "text-amber-200/90",
    },
    completed: {
      title: "Landed in your library",
      eyebrow: "Ready to watch",
      accent: "text-emerald-200/90",
    },
    issues: {
      title: "Needs attention",
      eyebrow: "Hit a snag",
      accent: "text-red-200/90",
    },
  };

const REQUEST_PROGRESS_GUIDE: Array<StatusGuideItem & { status: MediaRequestStatus }> = [
  {
    status: "pending",
    description: "Waiting for an admin to approve the request.",
    tone: "bg-amber-500/15 text-amber-100 ring-amber-400/40",
  },
  {
    status: "approved",
    description: "Approved, but not yet sent to the download automation.",
    tone: "bg-emerald-500/15 text-emerald-100 ring-emerald-400/40",
  },
  {
    status: "queued",
    description: "Sent to the request automation and waiting for download/import activity.",
    tone: "bg-sky-500/15 text-sky-100 ring-sky-400/40",
  },
  {
    status: "downloading",
    description: "Downloading or importing now.",
    tone: "bg-sky-500/20 text-sky-100 ring-sky-400/50",
  },
  {
    status: "completed",
    description: "In your Silo library and ready to watch.",
    tone: "bg-emerald-500/20 text-emerald-100 ring-emerald-400/40",
  },
];

const REQUEST_ISSUE_GUIDE: Array<
  StatusGuideItem & {
    outcome: Extract<MediaRequestOutcome, "declined" | "cancelled" | "failed">;
    label: string;
  }
> = [
  {
    outcome: "declined",
    label: "Declined",
    description: "An admin declined the request.",
    tone: "bg-zinc-700/60 text-zinc-200 ring-zinc-500/40",
  },
  {
    outcome: "cancelled",
    label: "Cancelled",
    description: "The request was cancelled before completion.",
    tone: "bg-zinc-700/60 text-zinc-200 ring-zinc-500/40",
  },
  {
    outcome: "failed",
    label: "Failed",
    description:
      "Silo or the external request automation hit an error. If details are available, they appear on the request card.",
    tone: "bg-red-500/15 text-red-100 ring-red-400/40",
  },
];

export default function Requests() {
  useDocumentTitle("Requests");

  const [searchParams, setSearchParams] = useSearchParams();
  const submittedQuery = (searchParams.get("q") ?? "").trim();
  const activeTab = normalizeRequestTab(searchParams.get("tab"));
  const mediaType = normalizeRequestMediaType(
    searchParams.get("type") ?? searchParams.get("media_type"),
  );
  const searchPage = normalizeSearchPage(searchParams.get("page"));
  const searchQuery = activeTab === "discover" ? submittedQuery : "";
  const [searchInput, setSearchInput] = useState(submittedQuery);

  const discovery = useRequestDiscovery();
  const studios = useDiscoverStudios();
  const networks = useDiscoverNetworks();
  const genres = useDiscoverGenres();
  const search = useRequestSearch(mediaType, searchQuery, searchPage);
  const mine = useMyMediaRequests({ limit: 100 });
  const createRequest = useCreateMediaRequest();
  const pendingRequestKey = createRequest.variables
    ? mediaRequestKey(createRequest.variables.media_type, createRequest.variables.tmdb_id)
    : undefined;

  const hasSubmittedSearch = searchQuery.length > 1;

  useEffect(() => {
    setSearchInput(submittedQuery);
  }, [submittedQuery]);

  function updateRequestParams(
    update: (next: URLSearchParams) => void,
    options: { replace?: boolean } = {},
  ) {
    const next = new URLSearchParams(searchParams);
    update(next);
    setSearchParams(next, options);
  }

  function setActiveTab(value: string) {
    const nextTab = normalizeRequestTab(value);

    updateRequestParams((next) => {
      if (nextTab === "discover") {
        next.delete("tab");
      } else {
        next.set("tab", nextTab);
        next.delete("q");
        next.delete("page");
        next.delete("type");
        next.delete("media_type");
      }
    });
  }

  function handleSearch(event: FormEvent) {
    event.preventDefault();
    const normalizedQuery = searchInput.trim();
    if (normalizedQuery.length < 2) return;

    updateRequestParams((next) => {
      next.delete("tab");
      next.set("q", normalizedQuery);
      next.delete("page");
      if (mediaType === "all") {
        next.delete("type");
        next.delete("media_type");
      } else {
        next.set("type", mediaType);
        next.delete("media_type");
      }
    });
  }

  function handleMediaTypeChange(value: RequestSearchMediaType) {
    updateRequestParams((next) => {
      if (value === "all") {
        next.delete("type");
        next.delete("media_type");
      } else {
        next.set("type", value);
        next.delete("media_type");
      }
      next.delete("page");
    });
  }

  function clearSearch() {
    setSearchInput("");
    updateRequestParams((next) => {
      next.delete("q");
      next.delete("page");
      next.delete("type");
      next.delete("media_type");
      next.delete("tab");
    });
  }

  function setSearchPageParam(page: number) {
    updateRequestParams(
      (next) => {
        if (page <= 1) {
          next.delete("page");
        } else {
          next.set("page", String(page));
        }
      },
      { replace: true },
    );
  }

  function submitRequest(item: RequestMediaResult) {
    createRequest.mutate(requestInputFromMediaResult(item));
  }

  const buckets = useMemo(() => groupMineRequests(mine.data ?? []), [mine.data]);
  const mineCounts = useMemo(() => countMineStatuses(mine.data ?? []), [mine.data]);
  const totalMine = (mine.data ?? []).length;

  return (
    <div className="space-y-8 py-6 sm:py-8">
      <div className="space-y-8 px-4 sm:px-6 lg:px-10 xl:px-12">
        <PageHeader />

        <SearchBar
          mediaType={mediaType}
          onMediaTypeChange={handleMediaTypeChange}
          searchInput={searchInput}
          onSearchInputChange={setSearchInput}
          onSubmit={handleSearch}
          onClear={clearSearch}
          isSearching={hasSubmittedSearch}
        />
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
          <TabsList variant="line" className="border-border w-full justify-start border-b">
            <TabsTrigger value="discover" className="px-3 text-[13px]">
              Discover
            </TabsTrigger>
            <TabsTrigger value="yours" className="px-3 text-[13px]">
              Yours
              {totalMine > 0 && (
                <span className="bg-muted/80 text-muted-foreground ml-1.5 inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[10px] font-semibold tabular-nums">
                  {totalMine}
                </span>
              )}
            </TabsTrigger>
          </TabsList>
        </div>

        <TabsContent value="discover" className="space-y-8 pt-2">
          {hasSubmittedSearch ? (
            <SearchResultsView
              query={submittedQuery}
              mediaType={mediaType}
              page={searchPage}
              onPageChange={setSearchPageParam}
              isLoading={search.isLoading || search.isFetching}
              isError={search.isError}
              totalPages={search.data?.total_pages ?? 0}
              totalResults={search.data?.total_results ?? 0}
              results={search.data?.results ?? []}
              pendingRequestKey={pendingRequestKey}
              isSubmitting={createRequest.isPending}
              onRequest={submitRequest}
            />
          ) : discovery.isLoading ? (
            <DiscoveryCarouselSkeleton />
          ) : discovery.isError ? (
            <EmptyPanel
              title="Discovery is offline"
              detail="TMDB couldn't be reached. Try the search bar above, or refresh in a moment."
            />
          ) : (
            <div className="space-y-10">
              {(discovery.data ?? []).map((section) => (
                <DiscoverySectionRow
                  key={section.key}
                  section={section}
                  pendingRequestKey={pendingRequestKey}
                  isSubmitting={createRequest.isPending}
                  onRequest={submitRequest}
                />
              ))}
              <BrandCarousel
                kind="studio"
                title="Studios"
                cards={studios.data}
                isLoading={studios.isLoading}
                isError={studios.isError}
                onRetry={() => void studios.refetch()}
              />
              <BrandCarousel
                kind="network"
                title="Networks"
                cards={networks.data}
                isLoading={networks.isLoading}
                isError={networks.isError}
                onRetry={() => void networks.refetch()}
              />
              <BrandCarousel
                kind="genre"
                title="Genres"
                cards={genres.data}
                isLoading={genres.isLoading}
                isError={genres.isError}
                onRetry={() => void genres.refetch()}
              />
            </div>
          )}
        </TabsContent>

        <TabsContent value="yours" className="space-y-8 pt-2">
          {mine.isLoading ? (
            <DiscoveryCarouselSkeleton />
          ) : mine.isError ? (
            <EmptyPanel
              title="Couldn't load your requests"
              detail="Refresh in a moment, or check back later."
            />
          ) : totalMine === 0 ? (
            <EmptyMineState />
          ) : (
            <>
              <MineSummary counts={mineCounts} />
              <RequestStatusGuide />
              <div className="space-y-10">
                {(Object.keys(MINE_BUCKET_META) as MineBucketKey[]).map((key) => {
                  const items = buckets[key];
                  if (items.length === 0) return null;
                  return <MineBucketRow key={key} bucket={key} requests={items} />;
                })}
              </div>
            </>
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}

function RequestStatusGuide() {
  return (
    <section className="px-4 sm:px-6 lg:px-10 xl:px-12" aria-labelledby="request-status-guide">
      <div className="border-border/60 bg-card/40 rounded-2xl border px-4 py-4 sm:px-5">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="space-y-1 lg:w-[220px] lg:shrink-0">
            <h2 id="request-status-guide" className="text-foreground text-sm font-semibold">
              Status guide
            </h2>
            <p className="text-muted-foreground text-[13px] leading-5">
              Statuses update automatically as Silo checks the library and connected request
              integrations.
            </p>
          </div>
          <div className="grid min-w-0 flex-1 gap-5 md:grid-cols-2">
            <StatusGuideGroup title="Request progress">
              {REQUEST_PROGRESS_GUIDE.map((item) => (
                <StatusGuideRow
                  key={item.status}
                  label={formatRequestStatus(item.status)}
                  description={item.description}
                  tone={item.tone}
                />
              ))}
            </StatusGuideGroup>
            <StatusGuideGroup title="Needs attention">
              {REQUEST_ISSUE_GUIDE.map((item) => (
                <StatusGuideRow
                  key={item.outcome}
                  label={item.label}
                  description={item.description}
                  tone={item.tone}
                />
              ))}
            </StatusGuideGroup>
          </div>
        </div>
      </div>
    </section>
  );
}

function StatusGuideGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="text-muted-foreground text-xs font-medium">{title}</h3>
      <dl className="space-y-2">{children}</dl>
    </section>
  );
}

function StatusGuideRow({
  label,
  description,
  tone,
}: {
  label: string;
  description: string;
  tone: string;
}) {
  return (
    <div className="grid gap-1 sm:grid-cols-[118px_minmax(0,1fr)] sm:items-start sm:gap-3">
      <dt>
        <span
          className={cn(
            "inline-flex max-w-full items-center rounded-full px-2 py-0.5 text-[11px] leading-5 font-semibold ring-1",
            tone,
          )}
        >
          {label}
        </span>
      </dt>
      <dd className="text-muted-foreground text-[13px] leading-5">{description}</dd>
    </div>
  );
}

function PageHeader() {
  return (
    <header className="space-y-3">
      <div className="flex items-center gap-2">
        <Sparkles className="h-4 w-4 text-amber-300/80" />
        <span className="text-muted-foreground text-[11px] font-semibold tracking-[0.22em] uppercase">
          Your wishlist
        </span>
      </div>
      <h1 className="font-display text-foreground text-[clamp(1.8rem,3vw,2.6rem)] leading-[1.05] font-bold tracking-tight text-balance">
        Find something worth waiting for.
      </h1>
      <p className="text-muted-foreground max-w-xl text-sm leading-6">
        Browse what's trending, search the full TMDB catalog, and watch your requests move from{" "}
        <span className="text-foreground">pending</span> to{" "}
        <span className="text-foreground">ready</span>.
      </p>
    </header>
  );
}

function SearchBar({
  mediaType,
  onMediaTypeChange,
  searchInput,
  onSearchInputChange,
  onSubmit,
  onClear,
  isSearching,
}: {
  mediaType: RequestSearchMediaType;
  onMediaTypeChange: (value: RequestSearchMediaType) => void;
  searchInput: string;
  onSearchInputChange: (value: string) => void;
  onSubmit: (event: FormEvent) => void;
  onClear: () => void;
  isSearching: boolean;
}) {
  return (
    <form
      onSubmit={onSubmit}
      className="border-border/70 bg-card/60 grid items-center gap-2 rounded-2xl border p-2 shadow-[0_1px_0_0_rgba(255,255,255,0.04)_inset,0_20px_50px_-30px_rgba(0,0,0,0.7)] backdrop-blur-sm sm:grid-cols-[150px_minmax(0,1fr)_auto]"
    >
      <Select
        value={mediaType}
        onValueChange={(value) => onMediaTypeChange(value as RequestSearchMediaType)}
      >
        <SelectTrigger className="border-border/60 bg-background/40 h-10 w-full rounded-xl border text-sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All</SelectItem>
          <SelectItem value="movie">Movies</SelectItem>
          <SelectItem value="series">Series</SelectItem>
        </SelectContent>
      </Select>
      <div className="relative">
        <Search className="text-muted-foreground absolute top-1/2 left-3.5 h-4 w-4 -translate-y-1/2" />
        <Input
          value={searchInput}
          onChange={(event) => onSearchInputChange(event.target.value)}
          placeholder="Search TMDB by title…"
          className="border-border/60 bg-background/40 h-10 rounded-xl pr-10 pl-10 text-sm"
        />
        {(searchInput || isSearching) && (
          <button
            type="button"
            onClick={onClear}
            className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2.5 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded-full transition-colors"
            aria-label="Clear search"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <Button
        type="submit"
        disabled={searchInput.trim().length < 2}
        className="h-10 rounded-xl px-5"
      >
        Search
      </Button>
    </form>
  );
}

function DiscoverySectionRow({
  section,
  pendingRequestKey,
  isSubmitting,
  onRequest,
}: {
  section: RequestDiscoverySection;
  pendingRequestKey?: string;
  isSubmitting: boolean;
  onRequest: (item: RequestMediaResult) => void;
}) {
  const eyebrow = sectionEyebrow(section.key);
  return (
    <section className="space-y-1">
      <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
        <span className="text-muted-foreground text-[10px] font-semibold tracking-[0.22em] uppercase">
          {eyebrow}
        </span>
      </div>
      <MediaCarousel title={section.title}>
        {section.results.map((item) => (
          <RequestPosterCard
            key={`${item.media_type}-${item.tmdb_id}`}
            variant="discover"
            item={item}
            isSubmitting={
              isSubmitting && pendingRequestKey === mediaRequestKey(item.media_type, item.tmdb_id)
            }
            onRequest={() => onRequest(item)}
          />
        ))}
      </MediaCarousel>
    </section>
  );
}

function MineBucketRow({ bucket, requests }: { bucket: MineBucketKey; requests: MediaRequest[] }) {
  const meta = MINE_BUCKET_META[bucket];
  return (
    <section className="space-y-1">
      <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
        <span className={cn("text-[10px] font-semibold tracking-[0.22em] uppercase", meta.accent)}>
          {meta.eyebrow}
        </span>
      </div>
      <MediaCarousel title={meta.title}>
        {requests.map((request) => (
          <RequestPosterCard key={request.id} variant="mine" request={request} />
        ))}
      </MediaCarousel>
    </section>
  );
}

function SearchResultsView({
  query,
  mediaType,
  page,
  onPageChange,
  isLoading,
  isError,
  totalPages,
  totalResults,
  results,
  pendingRequestKey,
  isSubmitting,
  onRequest,
}: {
  query: string;
  mediaType: RequestSearchMediaType;
  page: number;
  onPageChange: (page: number) => void;
  isLoading: boolean;
  isError: boolean;
  totalPages: number;
  totalResults: number;
  results: RequestMediaResult[];
  pendingRequestKey?: string;
  isSubmitting: boolean;
  onRequest: (item: RequestMediaResult) => void;
}) {
  const typeLabel =
    mediaType === "series" ? "series" : mediaType === "movie" ? "movies" : "movies and series";
  const filterLabel = mediaType === "series" ? "Series" : mediaType === "movie" ? "Movies" : "All";
  const shown = results.length;
  const showCount = !isLoading && !isError && shown > 0;

  return (
    <div className="space-y-6 px-4 sm:px-6 lg:px-10 xl:px-12">
      <header className="border-border/50 flex flex-col gap-2 border-b pb-4">
        <span className="text-muted-foreground text-[10px] font-semibold tracking-[0.24em] uppercase">
          Search results · {filterLabel}
        </span>
        <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
          <h2 className="font-display text-foreground text-[clamp(1.4rem,2.2vw,1.9rem)] leading-[1.1] font-bold tracking-tight">
            <span className="text-muted-foreground/50 font-normal">“</span>
            {query}
            <span className="text-muted-foreground/50 font-normal">”</span>
          </h2>
          {showCount && (
            <span className="text-muted-foreground text-[12px] tabular-nums">
              {totalResults > 0 ? (
                <>
                  <span className="text-foreground/90 font-semibold">
                    {totalResults.toLocaleString()}
                  </span>{" "}
                  {totalResults === 1 ? typeLabel.slice(0, -1) : typeLabel}
                  {totalPages > 1 ? (
                    <span className="text-muted-foreground/70">
                      {" · "}
                      Page {page} of {totalPages}
                    </span>
                  ) : null}
                </>
              ) : (
                <>
                  {shown} on this page
                  {totalPages > 1 ? (
                    <span className="text-muted-foreground/70">
                      {" · "}
                      Page {page} of {totalPages}
                    </span>
                  ) : null}
                </>
              )}
            </span>
          )}
        </div>
      </header>

      {isError ? (
        <EmptyPanel
          title="Search failed"
          detail="TMDB search couldn't be loaded. Try again in a moment."
        />
      ) : isLoading ? (
        <SearchGridSkeleton />
      ) : results.length === 0 ? (
        <EmptyPanel
          title="Nothing found"
          detail={
            mediaType === "all"
              ? `No movies or series matched "${query}". Try a different spelling.`
              : `No ${typeLabel} matched "${query}". Try a different spelling or switch to ${mediaType === "series" ? "Movies" : "Series"}.`
          }
        />
      ) : (
        <>
          <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8">
            {results.map((item) => (
              <RequestPosterCard
                key={`${item.media_type}-${item.tmdb_id}`}
                variant="discover"
                item={item}
                isSubmitting={
                  isSubmitting &&
                  pendingRequestKey === mediaRequestKey(item.media_type, item.tmdb_id)
                }
                onRequest={() => onRequest(item)}
                fluid
              />
            ))}
          </div>

          {totalPages > 1 && (
            <div className="border-border/50 flex items-center justify-between gap-3 border-t pt-4">
              <Button
                variant="outline"
                size="sm"
                onClick={() => onPageChange(Math.max(1, page - 1))}
                disabled={page <= 1 || isLoading}
              >
                Previous
              </Button>
              <span className="text-muted-foreground text-xs tabular-nums">
                Page <span className="text-foreground font-semibold">{page}</span> of {totalPages}
              </span>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onPageChange(page + 1)}
                disabled={page >= totalPages || isLoading}
              >
                Next
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function MineSummary({
  counts,
}: {
  counts: { pending: number; inFlight: number; completed: number; issues: number };
}) {
  const chips: Array<{ label: string; value: number; tone: string }> = [];
  if (counts.pending > 0)
    chips.push({
      label: "Pending review",
      value: counts.pending,
      tone: "bg-amber-500/15 text-amber-100 ring-amber-400/40",
    });
  if (counts.inFlight > 0)
    chips.push({
      label: "In motion",
      value: counts.inFlight,
      tone: "bg-sky-500/15 text-sky-100 ring-sky-400/40",
    });
  if (counts.completed > 0)
    chips.push({
      label: "Ready to watch",
      value: counts.completed,
      tone: "bg-emerald-500/15 text-emerald-100 ring-emerald-400/40",
    });
  if (counts.issues > 0)
    chips.push({
      label: "Need attention",
      value: counts.issues,
      tone: "bg-red-500/15 text-red-100 ring-red-400/40",
    });

  if (chips.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-2 px-4 sm:px-6 lg:px-10 xl:px-12">
      {chips.map((chip) => (
        <span
          key={chip.label}
          className={cn(
            "inline-flex items-center gap-2 rounded-full px-3 py-1 text-[12px] font-medium ring-1",
            chip.tone,
          )}
        >
          <span className="text-foreground/95 tabular-nums">{chip.value}</span>
          <span className="opacity-80">{chip.label}</span>
        </span>
      ))}
    </div>
  );
}

function EmptyMineState() {
  return (
    <div className="border-border/60 bg-card/40 mx-4 flex flex-col items-center justify-center gap-3 rounded-2xl border border-dashed px-6 py-16 text-center sm:mx-6 lg:mx-10 xl:mx-12">
      <Sparkles className="h-6 w-6 text-amber-300/70" />
      <p className="text-foreground text-base font-semibold">Your wishlist is empty.</p>
      <p className="text-muted-foreground max-w-sm text-sm">
        Browse the Discover tab or search above. The moment you request something, it'll show up
        here with live status.
      </p>
    </div>
  );
}

function EmptyPanel({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="border-border/60 bg-card/40 mx-4 flex flex-col items-center justify-center gap-2 rounded-2xl border border-dashed px-6 py-12 text-center sm:mx-6 lg:mx-10 xl:mx-12">
      <p className="text-foreground text-sm font-semibold">{title}</p>
      <p className="text-muted-foreground max-w-sm text-sm leading-6">{detail}</p>
    </div>
  );
}

function DiscoveryCarouselSkeleton() {
  return (
    <div className="space-y-10">
      {Array.from({ length: 3 }).map((_, sectionIndex) => (
        <section key={sectionIndex} className="space-y-3">
          <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
            <Skeleton className="h-3 w-24 rounded" />
            <Skeleton className="mt-2 h-6 w-44 rounded" />
          </div>
          <div className="flex gap-4 overflow-hidden px-4 sm:px-6 lg:px-10 xl:px-12">
            {Array.from({ length: 7 }).map((_, i) => (
              <div key={i} className="w-[148px] shrink-0 sm:w-[164px] lg:w-[184px]">
                <Skeleton className="aspect-[2/3] w-full rounded-xl" />
                <Skeleton className="mt-2 h-4 w-3/4 rounded" />
                <Skeleton className="mt-1 h-3 w-1/2 rounded" />
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function SearchGridSkeleton() {
  return (
    <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8">
      {Array.from({ length: 16 }).map((_, i) => (
        <div key={i}>
          <Skeleton className="aspect-[2/3] w-full rounded-lg" />
          <Skeleton className="mt-2 h-4 w-3/4 rounded" />
        </div>
      ))}
    </div>
  );
}

function sectionEyebrow(key: string): string {
  if (key.startsWith("trending")) return "Trending now";
  if (key.startsWith("popular")) return "Crowd favorites";
  return "Discover";
}

function normalizeRequestTab(value: string | null): RequestTab {
  if (value === "mine") return "yours";
  if (REQUEST_TABS.includes(value as RequestTab)) return value as RequestTab;
  return "discover";
}

function normalizeRequestMediaType(value: string | null): RequestSearchMediaType {
  if (value === "movie" || value === "series") return value;
  return "all";
}

function normalizeSearchPage(value: string | null): number {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 1) return 1;
  return parsed;
}

function mediaRequestKey(mediaType: RequestMediaResult["media_type"], tmdbID: number): string {
  return `${mediaType}-${tmdbID}`;
}

function isIssueOutcome(outcome: MediaRequestOutcome): boolean {
  return outcome === "declined" || outcome === "cancelled" || outcome === "failed";
}

function groupMineRequests(requests: MediaRequest[]) {
  const buckets: Record<MineBucketKey, MediaRequest[]> = {
    motion: [],
    completed: [],
    issues: [],
  };
  for (const request of requests) {
    if (isIssueOutcome(request.outcome)) {
      buckets.issues.push(request);
    } else if (request.status === "completed") {
      buckets.completed.push(request);
    } else {
      buckets.motion.push(request);
    }
  }
  return buckets;
}

function countMineStatuses(requests: MediaRequest[]) {
  let pending = 0;
  let inFlight = 0;
  let completed = 0;
  let issues = 0;
  for (const request of requests) {
    if (isIssueOutcome(request.outcome)) {
      issues += 1;
    } else if (request.status === "completed") {
      completed += 1;
    } else if (request.status === "pending") {
      pending += 1;
    } else {
      inFlight += 1;
    }
  }
  return { pending, inFlight, completed, issues };
}
