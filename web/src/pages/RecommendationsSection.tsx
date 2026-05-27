import { useParams } from "react-router";
import { RefreshCw, Sparkles } from "lucide-react";

import PageBack from "@/components/PageBack";
import SectionItemCard from "@/components/SectionItemCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useRecommendationSection } from "@/hooks/queries/recommendations";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";

const KIND_FALLBACK_LABEL: Record<string, (key?: string) => string> = {
  "for-you-main": () => "For You",
  cluster: (key) => (key ? `Personalized cluster ${key}` : "Personalized cluster"),
  "similar-users": () => "Users Like You Also Enjoyed",
  popular: () => "Popular on This Server",
  "recently-added": () => "Recently Added",
  "top-rated": () => "Top Rated",
  genre: (key) => (key ? `Popular in ${key}` : "Genre picks"),
};

function fallbackTitle(kind: string, key?: string) {
  const fn = KIND_FALLBACK_LABEL[kind];
  return fn ? fn(key) : "Recommendations";
}

function GridSkeleton() {
  return (
    <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 lg:gap-5 xl:grid-cols-7">
      {Array.from({ length: 24 }).map((_, i) => (
        <div key={i}>
          <Skeleton className="aspect-[2/3] w-full rounded-xl" />
          <Skeleton className="mt-3 h-4 w-3/4 rounded" />
          <Skeleton className="mt-1.5 h-3 w-1/2 rounded" />
        </div>
      ))}
    </div>
  );
}

function ErrorState({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 py-24 text-center">
      <p className="text-muted-foreground text-sm">Failed to load this section.</p>
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

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-24 text-center">
      <Sparkles className="text-muted-foreground/50 h-10 w-10" />
      <div className="space-y-1">
        <p className="text-sm font-medium">Nothing here yet</p>
        <p className="text-muted-foreground max-w-sm text-xs">
          Watch and rate more content to surface picks for this section.
        </p>
      </div>
    </div>
  );
}

export default function RecommendationsSection() {
  const params = useParams<{ kind: string; key?: string }>();
  const kind = params.kind ?? "";
  const key = params.key;

  const { data, isLoading, isError, refetch } = useRecommendationSection(kind, key);
  const title = data?.label || fallbackTitle(kind, key);

  useDocumentTitle(title);

  return (
    <div className="relative space-y-6 px-4 pt-6 pb-12 sm:px-6 lg:px-10 xl:px-12">
      <PageBack to="/recommendations" preferHistory={false} />
      <div className="mt-10 flex flex-col gap-1.5 sm:mt-12">
        <h1 className="text-foreground text-2xl font-bold tracking-tight sm:text-3xl">{title}</h1>
        {data && data.items.length > 0 && (
          <p className="text-muted-foreground text-sm">
            {data.items.length} {data.items.length === 1 ? "title" : "titles"}
          </p>
        )}
      </div>

      {isLoading ? (
        <GridSkeleton />
      ) : isError ? (
        <ErrorState onRetry={() => refetch()} />
      ) : !data || data.items.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 lg:gap-5 xl:grid-cols-7">
          {data.items.map((item) => (
            <SectionItemCard key={item.content_id} item={item} />
          ))}
        </div>
      )}
    </div>
  );
}
