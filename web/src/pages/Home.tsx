/* eslint-disable react-hooks/set-state-in-effect */
import { startTransition, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { LayoutDashboard } from "lucide-react";
import HeroBanner from "@/components/HeroBanner";
import SectionRow from "@/components/SectionRow";
import TasteSeedBanner from "@/components/TasteSeedBanner";
import { Skeleton } from "@/components/ui/skeleton";
import type { HomeSectionItemsResponse, ResolvedSection, ResolvedSectionLayout } from "@/api/types";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { HERO_BANNER_SIZE } from "@/lib/design-system";
import { sectionKeys } from "@/hooks/queries/keys";
import {
  fetchHomeSectionItems,
  HOME_SECTION_GC_TIME,
  HOME_SECTION_STALE_TIME,
  useHomeLayout,
} from "@/hooks/queries/sections";
import { planNextHomeSectionBatch } from "./homeSectionQueue";
import { buildHomeSectionViewModel, type HomeSectionSlot } from "./homeSectionState";
import { collectCachedHomeSections } from "./homeSectionCache";
import { useSectionRefreshSignal } from "./homeSurfaceRefresh";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselIntrinsicHeight } from "@/lib/uiCustomization";
import {
  isFirefoxEngine,
  isWebKitEngine,
  prefersReducedMotion,
} from "@/hooks/useImmediateSidebarCollapse";
import { SIDEBAR_DETAILS_REVEAL_DEADLINE_MS } from "@/components/sidebarItemNavigation";

const MAX_CONCURRENT_SECTION_REQUESTS = 5;
const SKELETON_CARD_COUNT = 7;
const EAGER_HOME_ROW_COUNT = 2;
const HOME_ROW_RENDER_MARGIN = "100% 0px";
const HOME_ROW_RESTORE_DELAY_MS = 360;
const HOME_ROW_RESTORE_STAGGER_MS = 70;
const HOME_ROW_RESTORE_MAX_DELAY_MS = 900;
const DESKTOP_SIDEBAR_QUERY = "(min-width: 64rem)";

export default function Home() {
  const queryClient = useQueryClient();
  const { data, isLoading, isError, refetch } = useHomeLayout();
  const { data: homeRefreshSignal = 0 } = useSectionRefreshSignal();
  const { cardPresentation } = useUICustomization();
  const [rowRestorationReady, setRowRestorationReady] = useState(
    () => !shouldWaitForSidebarReturn(),
  );

  useDocumentTitle("Home");

  const layout = useMemo(() => data?.sections ?? [], [data?.sections]);
  const layoutResetKey = layout
    .map(
      (section) =>
        `${section.id}:${section.section_type}:${section.title}:${section.featured ? "featured" : "row"}:${section.item_limit}:${section.is_custom ? "custom" : "default"}:${section.customized ? "customized" : "clean"}`,
    )
    .join("|");
  const cacheResetKey = `${homeRefreshSignal}:${layoutResetKey}`;
  const [initialCachedSections] = useState(() => readCachedHomeSections(queryClient, layout));
  const cacheResetKeyRef = useRef(cacheResetKey);

  const [loadedSections, setLoadedSections] = useState(initialCachedSections);
  const [failedIds, setFailedIds] = useState<Set<string>>(new Set());
  const [inFlightIds, setInFlightIds] = useState<Set<string>>(new Set());
  const [completedIds, setCompletedIds] = useState<Set<string>>(() =>
    freshCachedHomeSectionIds(queryClient, layout),
  );
  // Every reset below starts a new load generation. A request only reports
  // back into the generation that started it.
  const loadGenerationRef = useRef(0);

  useEffect(() => {
    if (rowRestorationReady) return;
    const stage = document.querySelector<HTMLElement>(".sidebar-main-stage");
    if (!stage) {
      setRowRestorationReady(true);
      return;
    }

    let revealed = false;
    const reveal = (event?: Event) => {
      if (revealed) return;
      if (
        event instanceof TransitionEvent &&
        (event.target !== stage || event.propertyName !== "transform")
      ) {
        return;
      }
      revealed = true;
      stage.removeEventListener("transitionend", reveal);
      stage.removeEventListener("transitioncancel", reveal);
      window.clearTimeout(deadline);
      startTransition(() => setRowRestorationReady(true));
    };

    stage.addEventListener("transitionend", reveal);
    stage.addEventListener("transitioncancel", reveal);
    const deadline = window.setTimeout(reveal, SIDEBAR_DETAILS_REVEAL_DEADLINE_MS);
    return () => {
      revealed = true;
      stage.removeEventListener("transitionend", reveal);
      stage.removeEventListener("transitioncancel", reveal);
      window.clearTimeout(deadline);
    };
  }, [rowRestorationReady]);

  useEffect(() => {
    const activeIds = layout.map((section) => section.id);
    const cachedSections = readCachedHomeSections(queryClient, layout);
    const cacheReset = cacheResetKeyRef.current !== cacheResetKey;
    cacheResetKeyRef.current = cacheResetKey;
    setLoadedSections((current) =>
      mapsHaveSameEntries(current, cachedSections) ? current : cachedSections,
    );
    setFailedIds((current) => (current.size === 0 ? current : new Set()));
    setInFlightIds((current) => (current.size === 0 ? current : new Set()));
    const nextCompletedIds = cacheReset
      ? new Set<string>()
      : freshCachedHomeSectionIds(queryClient, layout);
    setCompletedIds((current) =>
      setsHaveSameEntries(current, nextCompletedIds) ? current : nextCompletedIds,
    );

    return () => {
      // Cancelling settles each in-flight request with its pre-fetch data, or
      // a CancelledError when there was none. Neither answers the next
      // generation, which re-requests these sections itself.
      loadGenerationRef.current += 1;
      activeIds.forEach((sectionId) => {
        void queryClient.cancelQueries({ queryKey: sectionKeys.homeItems(sectionId) });
      });
    };
  }, [cacheResetKey, layout, queryClient]);

  useEffect(() => {
    if (layout.length === 0) return;

    const nextIds = planNextHomeSectionBatch({
      layout,
      loadedIds: completedIds,
      inFlightIds,
      maxConcurrentRequests: MAX_CONCURRENT_SECTION_REQUESTS,
    });

    if (nextIds.length === 0) return;

    const generation = loadGenerationRef.current;
    setInFlightIds((prev) => {
      const next = new Set(prev);
      nextIds.forEach((id) => next.add(id));
      return next;
    });

    nextIds.forEach((sectionId) => {
      void queryClient
        .fetchQuery<HomeSectionItemsResponse>({
          queryKey: sectionKeys.homeItems(sectionId),
          queryFn: ({ signal }) => fetchHomeSectionItems(sectionId, { signal }),
          staleTime: HOME_SECTION_STALE_TIME,
          gcTime: HOME_SECTION_GC_TIME,
        })
        .then((response) => {
          if (loadGenerationRef.current !== generation) return;

          setLoadedSections((prev) => {
            const next = new Map(prev);
            next.set(sectionId, response.section);
            return next;
          });
          setFailedIds((prev) => {
            if (!prev.has(sectionId)) return prev;
            const next = new Set(prev);
            next.delete(sectionId);
            return next;
          });
          setCompletedIds((prev) => {
            const next = new Set(prev);
            next.add(sectionId);
            return next;
          });
        })
        .catch(() => {
          if (loadGenerationRef.current !== generation) return;

          setFailedIds((prev) => {
            const next = new Set(prev);
            next.add(sectionId);
            return next;
          });
          setCompletedIds((prev) => {
            const next = new Set(prev);
            next.add(sectionId);
            return next;
          });
        })
        .finally(() => {
          if (loadGenerationRef.current !== generation) return;

          setInFlightIds((prev) => {
            if (!prev.has(sectionId)) return prev;
            const next = new Set(prev);
            next.delete(sectionId);
            return next;
          });
        });
    });
  }, [completedIds, inFlightIds, layout, queryClient]);

  const viewModel = buildHomeSectionViewModel({
    layout,
    loadedSections,
    failedIds,
  });

  const retrySection = (sectionId: string) => {
    queryClient.removeQueries({ queryKey: sectionKeys.homeItems(sectionId) });
    setFailedIds((prev) => {
      if (!prev.has(sectionId)) return prev;
      const next = new Set(prev);
      next.delete(sectionId);
      return next;
    });
    setCompletedIds((prev) => {
      if (!prev.has(sectionId)) return prev;
      const next = new Set(prev);
      next.delete(sectionId);
      return next;
    });
  };
  const heroSlot = renderHeroSlot(viewModel.hero, retrySection);
  const hasHeroSlot = heroSlot !== null;
  const rowPlaceholderHeight = carouselIntrinsicHeight(cardPresentation.poster_size);
  let readyRowIndex = 0;

  if (isLoading && !data) {
    return <HomePageSkeleton />;
  }

  if (isError && !data) {
    return (
      <div className="page-shell flex min-h-[50vh] items-center justify-center py-12">
        <div className="surface-panel max-w-md rounded-[1.8rem] border-0 p-6 text-center shadow-none">
          <p className="text-lg font-semibold">Unable to load the homepage</p>
          <p className="text-muted-foreground mt-2 text-sm">
            We couldn&apos;t load your section layout right now.
          </p>
          <button
            type="button"
            onClick={() => void refetch()}
            className="border-border hover:bg-muted/40 mt-4 rounded-lg border px-4 py-2 text-sm font-medium"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  return (
    <>
      <h1 className="sr-only">Home</h1>
      <div className={`space-y-10 ${hasHeroSlot ? "pb-2" : "pt-6 pb-2"}`}>
        {heroSlot}
        <TasteSeedBanner />

        {viewModel.rows.map((slot) => {
          if (slot.state === "empty") {
            return null;
          }
          if (slot.state === "ready" && slot.section) {
            const rowIndex = readyRowIndex++;
            return (
              <DeferredHomeSection
                key={slot.layout.id}
                section={slot.section}
                eager={rowIndex < EAGER_HOME_ROW_COUNT}
                restorationReady={rowRestorationReady}
                placeholderHeight={rowPlaceholderHeight}
                restoreDelayMs={Math.min(
                  HOME_ROW_RESTORE_DELAY_MS +
                    Math.max(0, rowIndex - EAGER_HOME_ROW_COUNT) * HOME_ROW_RESTORE_STAGGER_MS,
                  HOME_ROW_RESTORE_MAX_DELAY_MS,
                )}
              />
            );
          }
          if (slot.state === "error") {
            return (
              <SectionErrorRow
                key={slot.layout.id}
                title={slot.layout.title}
                onRetry={() => retrySection(slot.layout.id)}
              />
            );
          }
          return <SectionLoadingRow key={slot.layout.id} title={slot.layout.title} />;
        })}

        {layout.length === 0 && !isLoading && (
          <div className="surface-panel flex h-64 flex-col items-center justify-center gap-3 rounded-[1.8rem] border-0 px-6 text-center">
            <LayoutDashboard className="text-muted-foreground h-10 w-10" />
            <p className="text-muted-foreground text-sm">
              Your home screen is empty. Customize it by adding sections to display your media.
            </p>
            <Link to="/settings/home" className="text-primary text-sm font-medium hover:underline">
              Customize Home Screen
            </Link>
          </div>
        )}
      </div>
    </>
  );
}

function DeferredHomeSection({
  section,
  eager,
  restorationReady,
  placeholderHeight,
  restoreDelayMs,
}: {
  section: ResolvedSection;
  eager: boolean;
  restorationReady: boolean;
  placeholderHeight: string;
  restoreDelayMs: number;
}) {
  // Even visible rows start as fixed-size opaque space for the first commit.
  // This keeps a cold Home return small enough for the sidebar handoff to
  // begin before card trees and cached poster load events are mounted.
  const [rendered, setRendered] = useState(prefersReducedMotion);
  const placeholderRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // Restore the bounded above-fold budget as soon as the lightweight Home
    // commit has painted. WebKit and Firefox keep lower rows behind the sidebar
    // return gate, but the visible Home surface must travel with that motion
    // instead of waiting for transitionend (or its 760 ms safety deadline).
    if (rendered || (!eager && !restorationReady)) return;
    if (eager) {
      startTransition(() => setRendered(true));
      return;
    }
    const placeholder = placeholderRef.current;
    if (!placeholder || typeof IntersectionObserver === "undefined") {
      setRendered(true);
      return;
    }

    let revealed = false;
    const reveal = () => {
      if (revealed) return;
      revealed = true;
      observer.disconnect();
      window.clearTimeout(restoreTimer);
      startTransition(() => setRendered(true));
    };
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        reveal();
      },
      { rootMargin: HOME_ROW_RENDER_MARGIN },
    );
    observer.observe(placeholder);
    // IntersectionObserver keeps the first Home commit and near-viewport work
    // small. It is not permanent virtualization: every row is restored just
    // after the sidebar carry so browser Find and assistive technology
    // can reach the complete page without requiring visual scrolling.
    const restoreTimer = window.setTimeout(reveal, restoreDelayMs);
    return () => {
      revealed = true;
      observer.disconnect();
      window.clearTimeout(restoreTimer);
    };
  }, [eager, rendered, restorationReady, restoreDelayMs]);

  if (rendered) return <SectionRow section={section} />;

  return (
    <div
      ref={placeholderRef}
      data-home-section-placeholder={section.id}
      aria-hidden="true"
      style={{ minHeight: placeholderHeight }}
    />
  );
}

function shouldWaitForSidebarReturn(): boolean {
  const userAgent = typeof navigator === "undefined" ? "" : navigator.userAgent;
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    (isWebKitEngine(userAgent) || isFirefoxEngine(userAgent)) &&
    window.matchMedia(DESKTOP_SIDEBAR_QUERY).matches &&
    !prefersReducedMotion() &&
    document.documentElement.dataset.sidebarVisualCollapsed === "true"
  );
}

function readCachedHomeSections(queryClient: QueryClient, layout: ResolvedSectionLayout[]) {
  return collectCachedHomeSections(layout, (sectionId) =>
    queryClient.getQueryData<HomeSectionItemsResponse>(sectionKeys.homeItems(sectionId)),
  );
}

function freshCachedHomeSectionIds(
  queryClient: QueryClient,
  layout: ResolvedSectionLayout[],
): Set<string> {
  const freshAfter = Date.now() - HOME_SECTION_STALE_TIME;
  return new Set(
    layout
      .filter((section) => {
        const state = queryClient.getQueryState<HomeSectionItemsResponse>(
          sectionKeys.homeItems(section.id),
        );
        return Boolean(
          state?.data !== undefined && !state.isInvalidated && state.dataUpdatedAt > freshAfter,
        );
      })
      .map((section) => section.id),
  );
}

function mapsHaveSameEntries<K, V>(left: Map<K, V>, right: Map<K, V>): boolean {
  if (left.size !== right.size) return false;
  for (const [key, value] of left) {
    if (right.get(key) !== value) return false;
  }
  return true;
}

function setsHaveSameEntries<T>(left: Set<T>, right: Set<T>): boolean {
  if (left.size !== right.size) return false;
  for (const value of left) {
    if (!right.has(value)) return false;
  }
  return true;
}

function renderHeroSlot(hero: HomeSectionSlot | null, retrySection: (sectionId: string) => void) {
  if (!hero) return null;

  if (hero.state === "ready" && hero.section) {
    return <HeroBanner items={hero.section.items} maxSlides={hero.layout.item_limit} />;
  }

  if (hero.state === "error") {
    return (
      <div
        className={`bg-muted relative ${HERO_BANNER_SIZE} w-full overflow-hidden rounded-[1.8rem]`}
      >
        <div className="from-background via-background/70 absolute inset-0 bg-gradient-to-t to-transparent" />
        <div className="relative flex h-full items-end px-4 pb-10 sm:px-6 sm:pb-12 lg:px-12 lg:pb-16">
          <div className="max-w-xl space-y-3">
            <p className="text-3xl font-bold">{hero.layout.title}</p>
            <p className="text-muted-foreground text-sm">
              This featured section could not be loaded right now.
            </p>
            <button
              type="button"
              onClick={() => retrySection(hero.layout.id)}
              className="border-border hover:bg-muted/40 rounded-lg border px-4 py-2 text-sm font-medium"
            >
              Retry section
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (hero.state === "empty") {
    return null;
  }

  return <Skeleton className={`${HERO_BANNER_SIZE} w-full rounded-[1.8rem]`} />;
}

function HomePageSkeleton() {
  return (
    <div className="space-y-8 py-4">
      <Skeleton className={`${HERO_BANNER_SIZE} w-full rounded-[1.8rem]`} />
      {Array.from({ length: 3 }).map((_, i) => (
        <div key={i} className="space-y-3 px-4 sm:px-6 lg:px-12">
          <Skeleton className="h-6 w-48" />
          <div className="flex gap-4">
            {Array.from({ length: SKELETON_CARD_COUNT }).map((_, j) => (
              <Skeleton
                key={j}
                className="aspect-[2/3] w-[130px] shrink-0 rounded-lg sm:w-[150px] lg:w-[178px]"
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function SectionLoadingRow({ title }: { title: string }) {
  return (
    <section className="space-y-3 px-4 sm:px-6 lg:px-12">
      <h2 className="text-foreground h-6 text-sm font-semibold">{title}</h2>
      <div className="flex gap-4 overflow-hidden">
        {Array.from({ length: SKELETON_CARD_COUNT }).map((_, index) => (
          <Skeleton
            key={index}
            className="aspect-[2/3] w-[130px] shrink-0 rounded-lg sm:w-[150px] lg:w-[178px]"
          />
        ))}
      </div>
    </section>
  );
}

function SectionErrorRow({ title, onRetry }: { title: string; onRetry: () => void }) {
  return (
    <section className="space-y-3 px-4 sm:px-6 lg:px-12">
      <h2 className="text-foreground h-6 text-sm font-semibold">{title}</h2>
      <div className="surface-panel flex items-center justify-between rounded-[1.4rem] border-0 px-5 py-4">
        <p className="text-muted-foreground text-sm">This section could not be loaded right now.</p>
        <button
          type="button"
          onClick={onRetry}
          className="border-border hover:bg-muted/40 rounded-lg border px-4 py-2 text-sm font-medium"
        >
          Retry
        </button>
      </div>
    </section>
  );
}
