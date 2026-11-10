import { useParams } from "react-router";
import { RefreshCw, Sparkles } from "lucide-react";

import PageBack from "@/components/PageBack";
import SectionItemCard from "@/components/SectionItemCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useRecommendationSection } from "@/hooks/queries/recommendations";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useUICustomization } from "@/hooks/useUICustomization";
import { cardGridClasses } from "@/lib/uiCustomization";
import { useT, type Translate } from "@/i18n";

const KIND_FALLBACK_LABEL: Record<string, (t: Translate, key?: string) => string> = {
  "for-you-main": (t) => t("web.recommendations.title"),
  cluster: (t, key) =>
    key
      ? t("web.recommendations.fallbackClusterKey", { key })
      : t("web.recommendations.fallbackCluster"),
  "similar-users": (t) => t("web.recommendations.fallbackSimilarUsers"),
  popular: (t) => t("web.recommendations.fallbackPopular"),
  "recently-added": (t) => t("web.recommendations.fallbackRecentlyAdded"),
  "top-rated": (t) => t("web.recommendations.fallbackTopRated"),
  genre: (t, key) =>
    key
      ? t("web.recommendations.fallbackGenre", { genre: key })
      : t("web.recommendations.fallbackGenrePicks"),
};

function fallbackTitle(t: Translate, kind: string, key?: string) {
  const fn = KIND_FALLBACK_LABEL[kind];
  return fn ? fn(t, key) : t("web.recommendations.fallbackDefault");
}

function GridSkeleton() {
  const { cardPresentation } = useUICustomization();
  return (
    <div className={cardGridClasses(cardPresentation.poster_size)}>
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
  const t = useT();
  return (
    <div className="flex flex-col items-center justify-center gap-4 py-24 text-center">
      <p className="text-muted-foreground text-sm">{t("web.recommendations.sectionLoadFailed")}</p>
      <button
        onClick={onRetry}
        className="text-primary hover:text-primary/80 inline-flex items-center gap-2 text-sm font-medium"
      >
        <RefreshCw className="h-4 w-4" />
        {t("common.retry")}
      </button>
    </div>
  );
}

function EmptyState() {
  const t = useT();
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-24 text-center">
      <Sparkles className="text-muted-foreground/50 h-10 w-10" />
      <div className="space-y-1">
        <p className="text-sm font-medium">{t("web.recommendations.sectionEmptyTitle")}</p>
        <p className="text-muted-foreground max-w-sm text-xs">
          {t("web.recommendations.sectionEmptyBody")}
        </p>
      </div>
    </div>
  );
}

export default function RecommendationsSection() {
  const t = useT();
  const params = useParams<{ kind: string; key?: string }>();
  const kind = params.kind ?? "";
  const key = params.key;

  const { data, isLoading, isError, refetch } = useRecommendationSection(kind, key);
  const title = data?.label || fallbackTitle(t, kind, key);
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const { cardPresentation } = useUICustomization();

  useDocumentTitle(title);

  return (
    <div className="relative space-y-6 px-4 pt-6 pb-12 sm:px-6 lg:px-10 xl:px-12">
      <PageBack to="/recommendations" up />
      <div className="mt-10 flex flex-col gap-1.5 sm:mt-12">
        <h1 className="text-foreground text-2xl font-bold tracking-tight sm:text-3xl">{title}</h1>
        {data && data.items.length > 0 && (
          <p className="text-muted-foreground text-sm">
            {t("web.recommendations.sectionCount", {
              count: data.items.length,
              count_text: String(data.items.length),
            })}
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
        <div className={cardGridClasses(cardPresentation.poster_size)}>
          {data.items.map((item) => (
            <SectionItemCard
              key={item.content_id}
              item={item}
              overlayPrefs={overlayPrefs}
              quickActionMode={quickActionMode}
            />
          ))}
        </div>
      )}
    </div>
  );
}
