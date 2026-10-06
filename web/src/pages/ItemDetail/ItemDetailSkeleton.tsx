import { Skeleton } from "@/components/ui/skeleton";
import {
  CastSkeleton,
  CrewSkeleton,
  RecommendationGridSkeleton,
} from "./components/SectionSkeletons";

/** Holds the detail page layout while an item or title loads. */
export default function ItemDetailSkeleton() {
  return (
    <div>
      {/* Hero skeleton */}
      <section className="border-border/10 relative isolate overflow-hidden border-b">
        <div className="absolute inset-0 bg-gradient-to-r from-[var(--background)] via-[var(--background)]/70 to-transparent" />
        <div className="absolute inset-0 bg-gradient-to-t from-[var(--background)] via-[var(--background)]/40 to-transparent" />

        <div className="page-shell-wide relative flex min-h-[60dvh] flex-col justify-end pt-28 pb-8 lg:min-h-[72dvh]">
          <div className="flex flex-col gap-6 lg:flex-row lg:items-end">
            <Skeleton className="aspect-[2/3] w-[170px] flex-shrink-0 rounded-lg sm:w-[220px]" />

            <div className="max-w-3xl flex-1 space-y-4">
              <Skeleton className="h-4 w-16" />
              <Skeleton className="h-3 w-24" />
              <Skeleton className="h-10 w-80 max-w-full" />
              <div className="flex gap-2">
                <Skeleton className="h-6 w-14 rounded-md" />
                <Skeleton className="h-6 w-16 rounded-md" />
                <Skeleton className="h-6 w-20 rounded-md" />
              </div>
              <div className="flex gap-3">
                <Skeleton className="h-5 w-16" />
                <Skeleton className="h-5 w-16" />
              </div>
              <div className="space-y-2">
                <Skeleton className="h-4 w-full max-w-2xl" />
                <Skeleton className="h-4 w-5/6 max-w-xl" />
                <Skeleton className="h-4 w-3/4 max-w-lg" />
              </div>
              <Skeleton className="h-4 w-64" />
              <div className="flex gap-3 pt-2">
                <Skeleton className="h-10 w-28 rounded-full" />
                <Skeleton className="h-10 w-10 rounded-lg" />
                <Skeleton className="h-10 w-10 rounded-lg" />
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* Below-fold section skeletons */}
      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        <CastSkeleton />
        <CrewSkeleton />
        <RecommendationGridSkeleton />
      </div>
    </div>
  );
}
