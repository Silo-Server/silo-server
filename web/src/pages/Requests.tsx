import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Search, Sparkles, X } from "lucide-react";
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
import type { MediaRequest, RequestDiscoverySection, RequestMediaResult } from "@/api/types";
import {
  useCreateMediaRequest,
  useMyMediaRequests,
  useRequestDiscovery,
  useRequestSearch,
} from "@/hooks/queries/requests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { cn } from "@/lib/utils";
import { requestInputFromMediaResult } from "@/lib/mediaRequests";

type MineBucketKey = "motion" | "completed" | "issues";

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

export default function Requests() {
  useDocumentTitle("Requests");

  const [mediaType, setMediaType] = useState<"movie" | "series">("movie");
  const [searchInput, setSearchInput] = useState("");
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [searchPage, setSearchPage] = useState(1);

  const discovery = useRequestDiscovery();
  const search = useRequestSearch(mediaType, submittedQuery, searchPage);
  const mine = useMyMediaRequests({ limit: 100 });
  const createRequest = useCreateMediaRequest();

  const isSearching = submittedQuery.length > 0;

  function handleSearch(event: FormEvent) {
    event.preventDefault();
    setSubmittedQuery(searchInput.trim());
    setSearchPage(1);
  }

  function clearSearch() {
    setSearchInput("");
    setSubmittedQuery("");
    setSearchPage(1);
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
          onMediaTypeChange={setMediaType}
          searchInput={searchInput}
          onSearchInputChange={setSearchInput}
          onSubmit={handleSearch}
          onClear={clearSearch}
          isSearching={isSearching}
        />
      </div>

      <Tabs defaultValue="discover">
        <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
          <TabsList variant="line" className="border-border w-full justify-start border-b">
            <TabsTrigger value="discover" className="px-3 text-[13px]">
              Discover
            </TabsTrigger>
            <TabsTrigger value="mine" className="px-3 text-[13px]">
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
          {isSearching ? (
            <SearchResultsView
              query={submittedQuery}
              mediaType={mediaType}
              page={searchPage}
              onPageChange={setSearchPage}
              isLoading={search.isLoading || search.isFetching}
              isError={search.isError}
              totalPages={search.data?.total_pages ?? 0}
              results={search.data?.results ?? []}
              pendingTMDBID={createRequest.variables?.tmdb_id}
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
                  pendingTMDBID={createRequest.variables?.tmdb_id}
                  isSubmitting={createRequest.isPending}
                  onRequest={submitRequest}
                />
              ))}
            </div>
          )}
        </TabsContent>

        <TabsContent value="mine" className="space-y-8 pt-2">
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
  mediaType: "movie" | "series";
  onMediaTypeChange: (value: "movie" | "series") => void;
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
        onValueChange={(value) => onMediaTypeChange(value as "movie" | "series")}
      >
        <SelectTrigger className="border-border/60 bg-background/40 h-10 w-full rounded-xl border text-sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
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
  pendingTMDBID,
  isSubmitting,
  onRequest,
}: {
  section: RequestDiscoverySection;
  pendingTMDBID?: number;
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
            isSubmitting={isSubmitting && pendingTMDBID === item.tmdb_id}
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
  results,
  pendingTMDBID,
  isSubmitting,
  onRequest,
}: {
  query: string;
  mediaType: "movie" | "series";
  page: number;
  onPageChange: (page: number) => void;
  isLoading: boolean;
  isError: boolean;
  totalPages: number;
  results: RequestMediaResult[];
  pendingTMDBID?: number;
  isSubmitting: boolean;
  onRequest: (item: RequestMediaResult) => void;
}) {
  return (
    <div className="space-y-5 px-4 sm:px-6 lg:px-10 xl:px-12">
      <div className="flex items-baseline gap-2">
        <span className="text-muted-foreground text-[10px] font-semibold tracking-[0.22em] uppercase">
          Search · {mediaType === "series" ? "Series" : "Movies"}
        </span>
      </div>
      <div>
        <h2 className="text-foreground text-xl font-semibold tracking-tight">
          Results for <span className="italic">"{query}"</span>
        </h2>
      </div>

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
          detail={`No ${mediaType === "series" ? "series" : "movies"} matched "${query}".`}
        />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-x-4 gap-y-6 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 2xl:grid-cols-7">
            {results.map((item) => (
              <RequestPosterCard
                key={`${item.media_type}-${item.tmdb_id}`}
                variant="discover"
                item={item}
                isSubmitting={isSubmitting && pendingTMDBID === item.tmdb_id}
                onRequest={() => onRequest(item)}
              />
            ))}
          </div>

          {totalPages > 1 && (
            <div className="flex items-center justify-between gap-3 pt-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => onPageChange(Math.max(1, page - 1))}
                disabled={page <= 1 || isLoading}
              >
                Previous
              </Button>
              <span className="text-muted-foreground text-xs tabular-nums">
                Page {page} of {totalPages}
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
    <div className="grid grid-cols-2 gap-x-4 gap-y-6 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 2xl:grid-cols-7">
      {Array.from({ length: 14 }).map((_, i) => (
        <div key={i}>
          <Skeleton className="aspect-[2/3] w-full rounded-xl" />
          <Skeleton className="mt-2 h-4 w-3/4 rounded" />
          <Skeleton className="mt-1 h-3 w-1/2 rounded" />
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

function groupMineRequests(requests: MediaRequest[]) {
  const buckets: Record<MineBucketKey, MediaRequest[]> = {
    motion: [],
    completed: [],
    issues: [],
  };
  for (const request of requests) {
    if (
      request.outcome === "declined" ||
      request.outcome === "cancelled" ||
      request.outcome === "failed"
    ) {
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
    if (
      request.outcome === "declined" ||
      request.outcome === "cancelled" ||
      request.outcome === "failed"
    ) {
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
