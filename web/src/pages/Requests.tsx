import { useMemo, useState } from "react";
import { Navigate, useSearchParams } from "react-router";
import { Film, Info, Library, RefreshCw, Tv } from "lucide-react";
import BrandCarousel from "@/components/BrandCarousel";
import { CancelRequestDialog } from "@/components/CancelRequestDialog";
import MediaCarousel from "@/components/MediaCarousel";
import { RequestDownloadProgress } from "@/components/RequestDownloadProgress";
import RequestPosterCard from "@/components/RequestPosterCard";
import { RequestStatusBadge } from "@/components/RequestStatusBadge";
import SearchBar from "@/components/SearchBar";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { MediaRequest, RequestDiscoverySection, RequestMediaResult } from "@/api/types";
import {
  useCancelMediaRequest,
  useDiscoverGenres,
  useDiscoverNetworks,
  useDiscoverStudios,
  useMyMediaRequests,
  useRequestDiscovery,
} from "@/hooks/queries/useRequests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useSubmitMediaRequest } from "@/hooks/useSubmitMediaRequest";
import { useUICustomization } from "@/hooks/useUICustomization";
import { formatRelativeTime } from "@/lib/date";
import { formatDate } from "@/lib/datetime";
import {
  canCancelOwnRequest,
  formatMediaType,
  formatRequestDate,
  formatSeasonList,
  formatSeasonProgress,
  requestDetailHref,
  requestDiscoverSectionHref,
  requestDisplayState,
  requestSearchHref,
  tmdbImageURL,
  type RequestDisplayState,
} from "@/lib/mediaRequests";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

type RequestTab = "discover" | "yours";
type RequestGroupKey = "attention" | "on_the_way" | "in_library" | "cancelled";

const REQUEST_TABS = ["discover", "yours"] as const;
const PAGE_GUTTER = "px-4 sm:px-6 lg:px-10 xl:px-12";

// Grouped by what happens next: problems first, then what is still coming,
// then what has arrived. A cancelled request needs nothing, so it goes last.
const REQUEST_GROUPS: Array<{ key: RequestGroupKey; title: string }> = [
  { key: "attention", title: "Needs attention" },
  { key: "on_the_way", title: "On the way" },
  { key: "in_library", title: "In your library" },
  { key: "cancelled", title: "Cancelled" },
];

// A request can be cancelled until it is sent to the download automation:
// while pending, and while approved but not yet sent (canCancelOwnRequest).
const STATUS_HELP: Array<{ state: RequestDisplayState; description: string }> = [
  {
    state: "pending",
    description:
      "Waiting for an admin. You can cancel it until it is sent to the download automation.",
  },
  {
    state: "approved",
    description: "Approved, not yet sent to the download automation. You can still cancel it.",
  },
  { state: "processing", description: "Sent to the download automation." },
  {
    state: "partially_available",
    description: "Some episodes of the requested seasons have arrived.",
  },
  { state: "available", description: "In your library and ready to watch." },
  { state: "declined", description: "An admin declined it." },
  { state: "cancelled", description: "Withdrawn before it was sent to the download automation." },
  { state: "failed", description: "Silo or the download automation hit an error." },
];

/**
 * The Requests hub. An old hub search URL (`/requests?q=…`) opens the app's
 * search page, where the "Request to add" section lists TMDB matches.
 */
export default function Requests() {
  const [searchParams] = useSearchParams();
  const legacyQuery = (searchParams.get("q") ?? "").trim();
  if (legacyQuery) {
    return (
      <Navigate
        to={requestSearchHref(
          legacyQuery,
          searchParams.get("type") ?? searchParams.get("media_type"),
        )}
        replace
      />
    );
  }
  return <RequestsHub />;
}

function RequestsHub() {
  useDocumentTitle("Requests");

  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = normalizeRequestTab(searchParams.get("tab"));
  const mine = useMyMediaRequests({ limit: 100 });
  const totalMine = mine.data?.length ?? 0;

  function setActiveTab(value: string) {
    const next = new URLSearchParams(searchParams);
    if (normalizeRequestTab(value) === "discover") {
      next.delete("tab");
    } else {
      next.set("tab", "yours");
    }
    setSearchParams(next);
  }

  return (
    <div className="pb-10">
      <header className={cn(PAGE_GUTTER, "pt-6 pb-4")}>
        <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h1 className="text-foreground text-2xl font-bold tracking-tight sm:text-3xl">
              Requests
            </h1>
            <p className="text-muted-foreground mt-1 text-sm">
              Ask for movies and series that aren&rsquo;t in the library yet, and follow your
              requests.
            </p>
          </div>
          {/* Search is the app's own search page, which lists titles to request. */}
          <div className="w-full sm:w-72">
            <SearchBar
              placeholder="Search movies and series"
              buildSearchHref={(query) => requestSearchHref(query)}
            />
          </div>
        </div>
      </header>

      <Tabs value={activeTab} onValueChange={setActiveTab} className="gap-6">
        <div className={PAGE_GUTTER}>
          <TabsList variant="line" className="border-border w-full justify-start border-b">
            <TabsTrigger value="discover" className="flex-none px-3">
              Discover
            </TabsTrigger>
            <TabsTrigger value="yours" className="flex-none px-3">
              Yours
              {totalMine > 0 && (
                <span className="bg-muted/80 text-muted-foreground ml-1.5 inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[10px] font-semibold tabular-nums">
                  {totalMine}
                </span>
              )}
            </TabsTrigger>
          </TabsList>
        </div>

        <TabsContent value="discover">
          <DiscoverTab />
        </TabsContent>

        <TabsContent value="yours">
          <YoursTab
            requests={mine.data ?? []}
            isLoading={mine.isLoading}
            isError={mine.isError}
            onRetry={() => void mine.refetch()}
            onBrowse={() => setActiveTab("discover")}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function DiscoverTab() {
  const discovery = useRequestDiscovery();
  const studios = useDiscoverStudios();
  const networks = useDiscoverNetworks();
  const genres = useDiscoverGenres();
  const { submit, isSubmitting } = useSubmitMediaRequest();

  if (discovery.isLoading) return <DiscoverRowsSkeleton />;

  return (
    <div className="space-y-10">
      {discovery.isError ? (
        <div className={cn(PAGE_GUTTER, "flex flex-col items-center gap-4 py-16 text-center")}>
          <p className="text-muted-foreground text-sm">
            TMDB couldn&rsquo;t be reached, so there is nothing to discover right now.
          </p>
          <button
            type="button"
            onClick={() => void discovery.refetch()}
            className="text-primary hover:text-primary/80 inline-flex items-center gap-2 text-sm font-medium"
          >
            <RefreshCw className="h-4 w-4" />
            Retry
          </button>
        </div>
      ) : (
        (discovery.data ?? []).map((section) => (
          <DiscoverRow
            key={section.key}
            section={section}
            isSubmitting={isSubmitting}
            onRequest={submit}
          />
        ))
      )}
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
  );
}

function DiscoverRow({
  section,
  isSubmitting,
  onRequest,
}: {
  section: RequestDiscoverySection;
  isSubmitting: (item: RequestMediaResult) => boolean;
  onRequest: (item: RequestMediaResult) => void;
}) {
  if (section.results.length === 0) return null;
  return (
    <MediaCarousel
      title={section.title}
      viewAllHref={section.total_pages > 1 ? requestDiscoverSectionHref(section.key) : undefined}
    >
      {section.results.map((item) => (
        <RequestPosterCard
          key={`${item.media_type}-${item.tmdb_id}`}
          variant="discover"
          item={item}
          isSubmitting={isSubmitting(item)}
          onRequest={() => onRequest(item)}
        />
      ))}
    </MediaCarousel>
  );
}

function YoursTab({
  requests,
  isLoading,
  isError,
  onRetry,
  onBrowse,
}: {
  requests: MediaRequest[];
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  onBrowse: () => void;
}) {
  const cancelRequest = useCancelMediaRequest();
  const [cancelTarget, setCancelTarget] = useState<MediaRequest | null>(null);
  const cancellingID = cancelRequest.isPending ? cancelRequest.variables : undefined;
  const groups = useMemo(() => groupRequests(requests), [requests]);

  let body;
  if (isLoading) {
    body = <RequestRowsSkeleton />;
  } else if (isError) {
    body = (
      <div className="flex flex-col items-center gap-4 py-16 text-center">
        <p className="text-muted-foreground text-sm">Couldn&rsquo;t load your requests.</p>
        <button
          type="button"
          onClick={onRetry}
          className="text-primary hover:text-primary/80 inline-flex items-center gap-2 text-sm font-medium"
        >
          <RefreshCw className="h-4 w-4" />
          Retry
        </button>
      </div>
    );
  } else if (requests.length === 0) {
    body = (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <p className="text-foreground text-sm font-medium">You haven&rsquo;t requested anything.</p>
        <p className="text-muted-foreground max-w-sm text-sm">
          Find a title on Discover or with search, and request it from its page. It shows up here
          with its status.
        </p>
        <Button variant="outline" size="sm" onClick={onBrowse}>
          Browse Discover
        </Button>
      </div>
    );
  } else {
    body = (
      <div className="space-y-8">
        {REQUEST_GROUPS.map(({ key, title }) => {
          const items = groups[key];
          if (items.length === 0) return null;
          const headingID = `requests-group-${key}`;
          return (
            <section key={key} aria-labelledby={headingID} className="space-y-1">
              <h2 id={headingID} className="text-foreground text-lg font-semibold">
                {title}
                <span className="text-muted-foreground ml-2 text-sm font-normal tabular-nums">
                  {items.length}
                </span>
              </h2>
              <ul className="divide-border/60 divide-y">
                {items.map((request) => (
                  <RequestRow
                    key={request.id}
                    request={request}
                    // Every request on this tab is the account's own.
                    onCancel={
                      canCancelOwnRequest(request) ? () => setCancelTarget(request) : undefined
                    }
                    isCancelling={cancellingID === request.id}
                  />
                ))}
              </ul>
            </section>
          );
        })}
      </div>
    );
  }

  return (
    <div className={cn(PAGE_GUTTER, "max-w-5xl space-y-6")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-muted-foreground text-sm">
          Requests from your account, grouped by what happens next.
        </p>
        <StatusHelp />
      </div>
      {body}
      <CancelRequestDialog
        title={cancelTarget?.title ?? ""}
        open={cancelTarget !== null}
        onOpenChange={(open) => {
          if (!open) setCancelTarget(null);
        }}
        onConfirm={() => {
          if (cancelTarget) cancelRequest.mutate(cancelTarget.id);
        }}
        isPending={cancelRequest.isPending}
      />
    </div>
  );
}

function RequestRow({
  request,
  onCancel,
  isCancelling,
}: {
  request: MediaRequest;
  /** Offered only while the request can still be withdrawn. */
  onCancel?: () => void;
  isCancelling: boolean;
}) {
  const state = requestDisplayState(request.status, request.outcome, request.state);
  const href = requestDetailHref(request.media_type, request.tmdb_id);
  const poster = tmdbImageURL(request.poster_path, "w154");
  const MediaIcon = request.media_type === "series" ? Tv : Film;
  const details = [
    formatMediaType(request.media_type),
    request.year ? String(request.year) : "",
    request.seasons?.length ? formatSeasonList(request.seasons) : "",
    state === "partially_available" && request.season_progress?.length
      ? formatSeasonProgress(request.season_progress)
      : "",
  ].filter(Boolean);
  const updated = lastChange(request);

  return (
    <li className="flex gap-3 py-3 sm:gap-4" data-request-id={request.id}>
      <ViewTransitionLink
        to={href}
        tabIndex={-1}
        aria-hidden
        className="bg-muted relative h-[72px] w-12 shrink-0 overflow-hidden rounded-md"
      >
        {poster ? (
          <img src={poster} alt="" loading="lazy" className="h-full w-full object-cover" />
        ) : (
          <span className="text-muted-foreground flex h-full items-center justify-center">
            <MediaIcon className="h-4 w-4" />
          </span>
        )}
      </ViewTransitionLink>

      <div className="flex min-w-0 flex-1 flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <ViewTransitionLink
              to={href}
              className="text-foreground min-w-0 truncate text-sm font-semibold hover:underline"
            >
              {request.title}
            </ViewTransitionLink>
            {state ? <RequestStatusBadge state={state} /> : null}
          </div>
          <p className="text-muted-foreground text-xs">{details.join(" · ")}</p>
          {request.download ? (
            <RequestDownloadProgress
              download={request.download}
              className="w-64 max-w-full py-0.5"
            />
          ) : null}
          <p className="text-muted-foreground text-xs">
            Requested{" "}
            <time dateTime={request.created_at} title={formatDate(request.created_at, "medium")}>
              {formatRequestDate(request)}
            </time>
            {updated ? (
              <>
                {" · "}Updated{" "}
                <time
                  dateTime={request.updated_at}
                  title={formatDate(request.updated_at, "medium")}
                >
                  {updated}
                </time>
              </>
            ) : null}
          </p>
          {request.last_error ? (
            <p className="text-destructive text-xs leading-snug break-words">
              {request.last_error}
            </p>
          ) : request.outcome_reason ? (
            <p className="text-muted-foreground text-xs leading-snug break-words">
              Reason: {request.outcome_reason}
            </p>
          ) : null}
        </div>

        {onCancel || request.library_content_id ? (
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {request.library_content_id ? (
              <Button asChild variant="outline" size="sm">
                <ViewTransitionLink
                  to={`/item/${encodeURIComponent(request.library_content_id)}`}
                  aria-label={`Open ${request.title} in library`}
                >
                  <Library className="h-3.5 w-3.5" aria-hidden />
                  Open in library
                </ViewTransitionLink>
              </Button>
            ) : null}
            {onCancel ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onCancel}
                disabled={isCancelling}
                aria-label={`Cancel request for ${request.title}`}
              >
                Cancel request
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    </li>
  );
}

function StatusHelp() {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" className="text-muted-foreground -ml-2 sm:-mr-2 sm:ml-0">
          <Info className="h-4 w-4" aria-hidden />
          What the statuses mean
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        collisionPadding={16}
        className="w-80 max-w-[calc(100vw-2rem)] p-4"
      >
        <dl className="space-y-2.5" aria-label="Request statuses">
          {STATUS_HELP.map(({ state, description }) => (
            <div key={state} className="grid grid-cols-[7.5rem_minmax(0,1fr)] items-start gap-3">
              <dt>
                <RequestStatusBadge state={state} className="max-w-full" />
              </dt>
              <dd className="text-muted-foreground text-xs leading-5">{description}</dd>
            </div>
          ))}
        </dl>
      </PopoverContent>
    </Popover>
  );
}

function DiscoverRowsSkeleton() {
  const { cardPresentation } = useUICustomization();
  const widthClasses = carouselCardWidthClasses(cardPresentation.poster_size);
  return (
    <div className="space-y-10" aria-hidden>
      {Array.from({ length: 3 }).map((_, row) => (
        <div key={row} className="space-y-5">
          <div className={PAGE_GUTTER}>
            <Skeleton className="h-6 w-48 rounded" />
          </div>
          <div className={cn(PAGE_GUTTER, "flex gap-4 overflow-hidden lg:gap-5")}>
            {Array.from({ length: 10 }).map((_, card) => (
              <div key={card} className={cn(widthClasses, "shrink-0")}>
                <Skeleton className="aspect-[2/3] w-full rounded-xl" />
                <Skeleton className="mt-3 h-4 w-3/4 rounded" />
                <Skeleton className="mt-1.5 h-3 w-1/2 rounded" />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function RequestRowsSkeleton() {
  return (
    <div className="space-y-3" aria-hidden>
      {Array.from({ length: 4 }).map((_, index) => (
        <div key={index} className="flex gap-4 py-3">
          <Skeleton className="h-[72px] w-12 rounded-md" />
          <div className="flex-1 space-y-2 pt-1">
            <Skeleton className="h-4 w-48 rounded" />
            <Skeleton className="h-3 w-32 rounded" />
            <Skeleton className="h-3 w-40 rounded" />
          </div>
        </div>
      ))}
    </div>
  );
}

function normalizeRequestTab(value: string | null): RequestTab {
  if (value === "mine") return "yours";
  if (REQUEST_TABS.includes(value as RequestTab)) return value as RequestTab;
  return "discover";
}

function requestGroup(request: MediaRequest): RequestGroupKey {
  switch (requestDisplayState(request.status, request.outcome, request.state)) {
    case "declined":
    case "failed":
      return "attention";
    case "cancelled":
      return "cancelled";
    // A download the library has not scanned yet is still on its way, and a
    // season request lands only when all of its seasons have.
    case "available":
      return "in_library";
    default:
      return "on_the_way";
  }
}

function groupRequests(requests: MediaRequest[]): Record<RequestGroupKey, MediaRequest[]> {
  const groups: Record<RequestGroupKey, MediaRequest[]> = {
    attention: [],
    on_the_way: [],
    in_library: [],
    cancelled: [],
  };
  // The server's order (newest request first) holds within each group, so a
  // row does not jump around when only its state changes.
  for (const request of requests) groups[requestGroup(request)].push(request);
  return groups;
}

/** When the request last changed, if that was after it was made. */
function lastChange(request: MediaRequest): string | null {
  const created = Date.parse(request.created_at);
  const updated = Date.parse(request.updated_at);
  if (!Number.isFinite(created) || !Number.isFinite(updated) || updated - created < 60_000) {
    return null;
  }
  return formatRelativeTime(request.updated_at, {
    absoluteAfterDays: 7,
    absolute: (date) => formatDate(date, "medium"),
  });
}
