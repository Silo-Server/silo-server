import { Skeleton } from "@/components/ui/skeleton";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";

/** Skeleton for a horizontal cast carousel (portrait cards + name/role text) */
export function CastSkeleton({ count = 8 }: { count?: number }) {
  return (
    <div>
      <Skeleton className="mb-5 h-6 w-16" />
      <div className="flex gap-3 overflow-hidden">
        {Array.from({ length: count }, (_, i) => (
          <div key={i} className="w-[110px] shrink-0">
            <Skeleton className="mb-2.5 aspect-[2/3] w-full rounded-lg" />
            <Skeleton className="h-3.5 w-[80%]" />
            <Skeleton className="mt-1 h-3 w-[60%]" />
          </div>
        ))}
      </div>
    </div>
  );
}

/** Skeleton for the crew definition list (glass panel with label/value rows) */
export function CrewSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div>
      <Skeleton className="mb-4 h-6 w-16" />
      <div className="glass-subtle grid grid-cols-[auto_1fr] gap-x-6 gap-y-3 rounded-xl px-5 py-4">
        {Array.from({ length: rows }, (_, i) => (
          <div key={i} className="contents">
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 w-48" />
          </div>
        ))}
      </div>
    </div>
  );
}

/** Skeleton for the season poster carousel (portrait cards + title) */
export function SeasonCarouselSkeleton({ count = 6 }: { count?: number }) {
  return (
    <section>
      <div className="mb-5 flex items-end justify-between gap-4">
        <Skeleton className="h-6 w-24" />
        <Skeleton className="h-4 w-14" />
      </div>
      <div className="relative">
        <div className="overflow-hidden pb-5">
          <div className="flex gap-4">
            {Array.from({ length: count }, (_, i) => (
              <div key={i} className="w-[160px] shrink-0 sm:w-[170px]">
                <Skeleton className="aspect-[2/3] w-full rounded-xl" />
                <div className="px-0.5 pt-2.5">
                  <Skeleton className="h-3.5 w-[70%]" />
                  <Skeleton className="mt-1 h-3 w-[50%]" />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

/** Skeleton for the "More Like This" poster carousel. */
export function RecommendationGridSkeleton({ count = 6 }: { count?: number }) {
  const { cardPresentation } = useUICustomization();
  const posterWidthClasses = carouselCardWidthClasses(cardPresentation.poster_size);
  return (
    <div>
      <Skeleton className="mb-5 h-6 w-36" />
      <div className="flex gap-4 overflow-hidden lg:gap-5">
        {Array.from({ length: count }, (_, i) => (
          <div key={i} className={posterWidthClasses}>
            <Skeleton className="aspect-[2/3] w-full rounded-lg" />
            {cardPresentation.caption !== "artwork" ? (
              <Skeleton className="mt-1.5 h-4 w-3/4" />
            ) : null}
          </div>
        ))}
      </div>
    </div>
  );
}

/** Skeleton for the episode grid on a season page (landscape cards + text) */
export function EpisodeGridSkeleton({ count = 10 }: { count?: number }) {
  return (
    <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5">
      {Array.from({ length: count }, (_, i) => (
        <div key={i}>
          <Skeleton className="aspect-video w-full rounded-lg" />
          <Skeleton className="mt-2 h-3 w-16" />
          <Skeleton className="mt-1 h-4 w-24" />
          <Skeleton className="mt-1.5 h-3 w-20" />
        </div>
      ))}
    </div>
  );
}

/** Skeleton for a horizontal episode carousel (landscape cards + text) */
export function EpisodeCarouselSkeleton({ count = 5 }: { count?: number }) {
  return (
    <div>
      <Skeleton className="mb-5 h-6 w-36" />
      <div className="flex gap-3 overflow-hidden">
        {Array.from({ length: count }, (_, i) => (
          <div key={i} className="w-[240px] shrink-0">
            <Skeleton className="aspect-video w-full rounded-[1.1rem]" />
            <Skeleton className="mt-2 h-3 w-16" />
            <Skeleton className="mt-1 h-4 w-32" />
            <Skeleton className="mt-1 h-3 w-10" />
          </div>
        ))}
      </div>
    </div>
  );
}
