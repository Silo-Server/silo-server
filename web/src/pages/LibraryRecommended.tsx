import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { HomeSectionItemsResponse, ResolvedSection } from "@/api/types";
import { useLibraryCollectionItems } from "@/hooks/queries/libraryCollections";
import { fetchLibrarySectionItems, useLibraryLayout } from "@/hooks/queries/sections";
import { useSidebarPins } from "@/hooks/queries/sidebarPins";
import MediaCarousel from "@/components/MediaCarousel";
import ItemCard from "@/components/ItemCard";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import HeroBanner from "@/components/HeroBanner";
import NowListeningHero from "@/components/NowListeningHero";
import SectionRow from "@/components/SectionRow";
import { Skeleton } from "@/components/ui/skeleton";
import { HERO_BANNER_SIZE_TALL } from "@/lib/design-system";
import { sectionKeys } from "@/hooks/queries/keys";
import { planNextHomeSectionBatch } from "./homeSectionQueue";
import { buildHomeSectionViewModel, type HomeSectionSlot } from "./homeSectionState";
import { collectCachedHomeSections } from "./homeSectionCache";
import { isAudiobookLibraryType } from "./libraryPageSearchParams";

interface LibraryRecommendedProps {
  libraryId: number;
  /** Library type ("movies", "series", "audiobooks", ...); drives the hero style. */
  libraryType?: string;
  /**
   * Reports whether the page is currently rendering a hero banner (i.e. a
   * featured section resolved with items). Parent uses this to decide whether
   * to apply transparent-overlay styling to the sticky marquee header.
   */
  onHeroStateChange?: (hasRenderedHero: boolean) => void;
}

const SECTION_STALE_TIME = 5 * 60 * 1000;
const MAX_CONCURRENT_SECTION_REQUESTS = 4;

export default function LibraryRecommended({
  libraryId,
  libraryType = "",
  onHeroStateChange,
}: LibraryRecommendedProps) {
  const queryClient = useQueryClient();
  const { data, isLoading } = useLibraryLayout(libraryId);
  const [loadedSections, setLoadedSections] = useState<Map<string, ResolvedSection>>(new Map());
  const [failedIds, setFailedIds] = useState<Set<string>>(new Set());
  const [inFlightIds, setInFlightIds] = useState<Set<string>>(new Set());
  const [completedIds, setCompletedIds] = useState<Set<string>>(new Set());
  const activeSectionIdsRef = useRef<Set<string>>(new Set());

  const layout = useMemo(() => data?.sections ?? [], [data?.sections]);
  const layoutResetKey = layout
    .map(
      (section) =>
        `${section.id}:${section.section_type}:${section.title}:${section.featured ? "featured" : "row"}:${section.item_limit}:${section.is_custom ? "custom" : "default"}:${section.customized ? "customized" : "clean"}`,
    )
    .join("|");

  useEffect(() => {
    const activeIds = layout.map((section) => section.id);
    activeSectionIdsRef.current = new Set(activeIds);
    setLoadedSections(
      collectCachedHomeSections(layout, (sectionId) =>
        queryClient.getQueryData<HomeSectionItemsResponse>(
          sectionKeys.libraryItems(libraryId, sectionId),
        ),
      ),
    );
    setFailedIds(new Set());
    setInFlightIds(new Set());
    setCompletedIds(new Set());

    return () => {
      activeSectionIdsRef.current = new Set();
      activeIds.forEach((sectionId) => {
        void queryClient.cancelQueries({
          queryKey: sectionKeys.libraryItems(libraryId, sectionId),
        });
      });
    };
  }, [libraryId, layout, layoutResetKey, queryClient]);

  useEffect(() => {
    if (layout.length === 0) return;

    const nextIds = planNextHomeSectionBatch({
      layout,
      loadedIds: completedIds,
      inFlightIds,
      maxConcurrentRequests: MAX_CONCURRENT_SECTION_REQUESTS,
    });

    if (nextIds.length === 0) return;

    setInFlightIds((prev) => {
      const next = new Set(prev);
      nextIds.forEach((id) => next.add(id));
      return next;
    });

    nextIds.forEach((sectionId) => {
      void queryClient
        .fetchQuery<HomeSectionItemsResponse>({
          queryKey: sectionKeys.libraryItems(libraryId, sectionId),
          queryFn: ({ signal }) => fetchLibrarySectionItems(libraryId, sectionId, { signal }),
          staleTime: SECTION_STALE_TIME,
        })
        .then((response) => {
          if (!activeSectionIdsRef.current.has(sectionId)) return;

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
          if (!activeSectionIdsRef.current.has(sectionId)) return;

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
          if (!activeSectionIdsRef.current.has(sectionId)) return;

          setInFlightIds((prev) => {
            if (!prev.has(sectionId)) return prev;
            const next = new Set(prev);
            next.delete(sectionId);
            return next;
          });
        });
    });
  }, [completedIds, inFlightIds, layout, libraryId, queryClient]);

  const viewModel = buildHomeSectionViewModel({
    layout,
    loadedSections,
    failedIds,
  });

  // Tell the parent whether a hero is actually rendered — i.e. a featured
  // section resolved with at least one item. Error and empty states are NOT
  // considered a rendered hero, so the marquee header can drop back to its
  // glass variant (which is legible on any theme).
  const hasRenderedHero =
    viewModel.hero?.state === "ready" && (viewModel.hero.section?.items.length ?? 0) > 0;
  useEffect(() => {
    onHeroStateChange?.(hasRenderedHero);
  }, [hasRenderedHero, onHeroStateChange]);

  const retrySection = (sectionId: string) => {
    queryClient.removeQueries({ queryKey: sectionKeys.libraryItems(libraryId, sectionId) });
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

  if (isLoading && layout.length === 0) {
    return null;
  }

  return (
    <div className="space-y-10 sm:space-y-12">
      {renderHeroSlot(viewModel.hero, retrySection, libraryId, libraryType)}
      {viewModel.rows.map((slot) => {
        if (slot.state === "empty") {
          return null;
        }
        if (slot.state === "ready" && slot.section) {
          return <SectionRow key={slot.layout.id} section={slot.section} libraryId={libraryId} />;
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

      {/* Pinned Collections */}
      <PinnedCollections libraryId={libraryId} />
    </div>
  );
}

function renderHeroSlot(
  hero: HomeSectionSlot | null,
  retrySection: (sectionId: string) => void,
  libraryId: number,
  libraryType: string,
) {
  if (!hero) return null;

  if (hero.state === "ready" && hero.section) {
    // Audiobook libraries feature their continue-listening section, rendered
    // as a resume deck — square covers have no backdrop to feed the cinematic
    // carousel hero.
    if (isAudiobookLibraryType(libraryType) && hero.section.section_type === "continue_watching") {
      return <NowListeningHero section={hero.section} libraryId={libraryId} />;
    }
    return (
      <HeroBanner
        items={hero.section.items}
        maxSlides={hero.layout.item_limit}
        heightClassName={HERO_BANNER_SIZE_TALL}
        bleed
        reserveHeaderSpace
        libraryId={libraryId}
      />
    );
  }

  if (hero.state === "error") {
    return (
      <section className="px-4 pt-24 sm:px-6 lg:px-10 xl:px-12">
        <div className="surface-panel flex items-center justify-between rounded-[1.8rem] border-0 px-5 py-6">
          <p className="text-muted-foreground text-sm">
            The featured section could not be loaded right now.
          </p>
          <button
            type="button"
            onClick={() => retrySection(hero.layout.id)}
            className="border-border hover:bg-muted/40 rounded-lg border px-4 py-2 text-sm font-medium"
          >
            Retry section
          </button>
        </div>
      </section>
    );
  }

  if (hero.state === "empty") {
    return null;
  }

  return <Skeleton className={`w-full rounded-none ${HERO_BANNER_SIZE_TALL}`} />;
}

function SectionLoadingRow({ title }: { title: string }) {
  return (
    <MediaCarousel title={title} loading>
      {null}
    </MediaCarousel>
  );
}

function SectionErrorRow({ title, onRetry }: { title: string; onRetry: () => void }) {
  return (
    <section className="space-y-3 px-4 sm:px-6 lg:px-10 xl:px-12">
      <h2 className="text-foreground text-xl font-semibold tracking-tight">{title}</h2>
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

function PinnedCollections({ libraryId }: { libraryId: number }) {
  const { pins } = useSidebarPins();
  const pinnedCollections = (pins[String(libraryId)] ?? []).filter(
    (pin) => pin.type === "collection",
  );

  return (
    <>
      {pinnedCollections.map((collection) => (
        <PinnedCollectionCarousel
          key={collection.id}
          libraryId={libraryId}
          collectionId={collection.id}
          name={collection.label}
        />
      ))}
    </>
  );
}

function PinnedCollectionCarousel({
  libraryId,
  collectionId,
  name,
}: {
  libraryId: number;
  collectionId: string;
  name: string;
}) {
  const { data: items, isLoading } = useLibraryCollectionItems(libraryId, collectionId);
  const { prefs: overlayPrefs } = useOverlayPrefs();

  if (!isLoading && (!items || items.length === 0)) return null;

  return (
    <MediaCarousel title={name} loading={isLoading}>
      {(items ?? []).map((item) => (
        <div key={item.content_id} className="w-[140px] shrink-0 sm:w-[160px] lg:w-[184px]">
          <ItemCard item={item} overlayPrefs={overlayPrefs} />
        </div>
      ))}
    </MediaCarousel>
  );
}
