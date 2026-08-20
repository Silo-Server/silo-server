import { useDiscover, useTasteProfile } from "@/hooks/queries/recommendations";
import type { DiscoverRow } from "@/api/types";
import MediaCarousel from "@/components/MediaCarousel";
import SectionItemCard from "@/components/SectionItemCard";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { Skeleton } from "@/components/ui/skeleton";
import { Sparkles, RefreshCw } from "lucide-react";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";

function buildSectionHref(row: DiscoverRow): string | undefined {
  if (!row.section_kind) return undefined;
  const base = `/recommendations/section/${encodeURIComponent(row.section_kind)}`;
  return row.section_key ? `${base}/${encodeURIComponent(row.section_key)}` : base;
}

function TasteProfileCard({
  profile,
  isLoading,
}: {
  profile:
    | {
        top_genres: string[];
        favorite_directors: string[];
        signal_counts: Record<string, number>;
      }
    | undefined;
  isLoading: boolean;
}) {
  if (isLoading) {
    return (
      <div className="glass-subtle space-y-3 rounded-xl p-5">
        <Skeleton className="h-5 w-32 rounded" />
        <Skeleton className="h-4 w-48 rounded" />
      </div>
    );
  }

  if (!profile || (profile.top_genres.length === 0 && profile.favorite_directors.length === 0)) {
    return null;
  }

  const totalSignals = Object.values(profile.signal_counts).reduce((a, b) => a + b, 0);

  return (
    <div className="glass-subtle space-y-4 rounded-xl p-5">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Your Taste Profile</h2>
        {totalSignals > 0 && (
          <span className="text-muted-foreground text-xs">
            {totalSignals} signal{totalSignals !== 1 ? "s" : ""}
          </span>
        )}
      </div>

      {profile.top_genres.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            Top Genres
          </p>
          <div className="flex flex-wrap gap-1.5">
            {profile.top_genres.map((genre) => (
              <span
                key={genre}
                className="bg-accent text-foreground rounded-full px-2.5 py-0.5 text-xs font-medium"
              >
                {genre}
              </span>
            ))}
          </div>
        </div>
      )}

      {profile.favorite_directors.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            Favorite Directors
          </p>
          <div className="flex flex-wrap gap-1.5">
            {profile.favorite_directors.map((director) => (
              <span
                key={director}
                className="border-border text-foreground rounded-full border px-2.5 py-0.5 text-xs"
              >
                {director}
              </span>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function DiscoverEmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-24 text-center">
      <Sparkles className="text-muted-foreground/50 h-10 w-10" />
      <div className="space-y-1">
        <p className="text-sm font-medium">Not enough data yet</p>
        <p className="text-muted-foreground max-w-sm text-xs">
          Watch and rate more content to unlock personalized recommendations.
        </p>
      </div>
    </div>
  );
}

function DiscoverErrorState({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 py-24 text-center">
      <p className="text-muted-foreground text-sm">Failed to load recommendations.</p>
      <button
        onClick={onRetry}
        className="text-primary hover:text-primary/80 inline-flex items-center gap-2 text-sm font-medium"
      >
        <RefreshCw className="h-4 w-4" />
        Retry
      </button>
    </div>
  );
}

function DiscoverSkeletons({ posterWidthClasses }: { posterWidthClasses: string }) {
  return (
    <div className="space-y-10 pt-2">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="space-y-5">
          <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
            <Skeleton className="h-6 w-48 rounded" />
          </div>
          <div className="flex gap-4 overflow-hidden px-4 sm:px-6 lg:gap-5 lg:px-10 xl:px-12">
            {Array.from({ length: 12 }).map((_, j) => (
              <div key={j} className={posterWidthClasses}>
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

export default function Recommendations() {
  useDocumentTitle("Recommendations");

  const tasteProfileQuery = useTasteProfile();
  const { data, isLoading, isError, refetch } = useDiscover();
  const { cardPresentation } = useUICustomization();
  const posterWidthClasses = carouselCardWidthClasses(cardPresentation.poster_size);

  const rows = data?.rows ?? [];

  return (
    <div className="space-y-2 pb-10">
      {/* Header */}
      <div className="px-4 pt-6 pb-4 sm:px-6 lg:px-10 xl:px-12">
        <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <h1 className="text-foreground text-2xl font-bold tracking-tight sm:text-3xl">
              Recommendations
            </h1>
            <p className="text-muted-foreground mt-1 text-sm">
              Personalized picks based on your viewing history and ratings.
            </p>
          </div>
          <TasteProfileCard
            profile={tasteProfileQuery.data}
            isLoading={tasteProfileQuery.isLoading}
          />
        </div>
      </div>

      {/* Content */}
      {isLoading ? (
        <DiscoverSkeletons posterWidthClasses={posterWidthClasses} />
      ) : isError ? (
        <DiscoverErrorState onRetry={() => refetch()} />
      ) : rows.length === 0 ? (
        <DiscoverEmptyState />
      ) : (
        rows.map((row, i) => (
          <MediaCarousel
            key={`${row.type}-${i}`}
            title={row.label}
            titleHref={buildSectionHref(row)}
          >
            {row.items.map((item) => (
              <div key={item.content_id} className={posterWidthClasses} role="listitem">
                <SectionItemCard item={item} />
              </div>
            ))}
          </MediaCarousel>
        ))
      )}
    </div>
  );
}
