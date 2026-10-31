import { memo, useMemo, useState } from "react";
import { ContinueWatchingRowCard } from "@/components/ContinueWatchingCard";
import MediaCarousel, { type MediaCarouselWindowing } from "@/components/MediaCarousel";
import SectionItemCard from "@/components/SectionItemCard";
import { useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { buildSectionCatalogHref } from "@/pages/catalogSearchParams";
import type { ResolvedSection } from "@/api/types";
import { Pin, PinOff } from "lucide-react";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import {
  carouselCardWidthClasses,
  featuredCarouselCardWidthClasses,
  rankedCarouselCardWidthClasses,
  rankedCarouselGutterClasses,
  rankedCarouselNumeralClasses,
  WIDE_CAROUSEL_CARD_WIDTH_CLASSES,
  type CarouselSlideWidth,
} from "@/lib/uiCustomization";
import { initialCarouselSlideCount, visibleCarouselSlideCount } from "@/lib/carouselWindow";
import { MAX_RANKED_CARDS } from "@/lib/rankNumerals";
import {
  EXPANDING_SECTION_TYPES,
  RANKED_SECTION_TYPES,
  expandedCardWidthClasses,
} from "@/lib/featuredCard";
import { useHeroTrailersEnabled } from "@/hooks/useHeroTrailersEnabled";

interface SectionRowProps {
  section: ResolvedSection;
  /** Library ID this section belongs to (enables pin-to-sidebar) */
  libraryId?: number;
  /**
   * The row is on screen at first paint (the first Home row): its visible
   * cards load their artwork eagerly at high fetch priority.
   */
  priority?: boolean;
}

function SectionPinButton({
  sectionId,
  sectionTitle,
  libraryId,
}: {
  sectionId: string;
  sectionTitle: string;
  libraryId: number;
}) {
  const { togglePin, isPinned, canToggle } = useToggleSidebarPin();
  const pinned = isPinned(libraryId, "section", sectionId);

  if (!canToggle) return null;

  return (
    <button
      onClick={() => togglePin(libraryId, { type: "section", id: sectionId, label: sectionTitle })}
      className={`rounded-full p-1.5 transition-colors ${
        pinned
          ? "text-primary hover:text-primary/80"
          : "text-muted-foreground/40 hover:bg-accent/60 hover:text-muted-foreground opacity-0 group-hover/carousel:opacity-100"
      }`}
      title={pinned ? "Unpin from sidebar" : "Pin to sidebar"}
    >
      {pinned ? <PinOff className="h-4 w-4" /> : <Pin className="h-4 w-4" />}
    </button>
  );
}

export default memo(function SectionRow({ section, libraryId, priority = false }: SectionRowProps) {
  const browseHref = useMemo(
    () =>
      libraryId
        ? buildSectionCatalogHref({
            scope: "library",
            libraryId,
            sectionId: section.id,
            title: section.title,
          })
        : buildSectionCatalogHref({ scope: "home", sectionId: section.id, title: section.title }),
    [libraryId, section.id, section.title],
  );
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const { cardPresentation } = useUICustomization();
  const posterSize = cardPresentation.poster_size;
  /*
   * Which card is open, and the only state an opening row keeps. The row owns it
   * rather than each card, so two tiles can never be open at the same time.
   */
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const isRanked = RANKED_SECTION_TYPES.has(section.section_type);
  const isExpanding = EXPANDING_SECTION_TYPES.has(section.section_type);
  /*
   * The Featured row lays its cards out wider than an ordinary row does, so this
   * is the width its slides take — the same width carouselWindow measures its
   * first commit against, or the row mounts too few of them.
   */
  const posterWidthClasses = isRanked
    ? rankedCarouselCardWidthClasses(posterSize)
    : isExpanding
      ? featuredCarouselCardWidthClasses(posterSize)
      : carouselCardWidthClasses(posterSize);
  const rankedGutterClasses = isRanked ? rankedCarouselGutterClasses(posterSize) : "";
  const rankedNumeralClasses = isRanked ? rankedCarouselNumeralClasses(posterSize) : "";
  const expandedWidthClasses = isExpanding ? expandedCardWidthClasses(posterSize) : "";
  // Read once for the row rather than once per card. A clip is bandwidth a
  // profile opts into, and this is the same gate the hero reads.
  const trailersEnabled = useHeroTrailersEnabled() === true;
  const isProgressRow =
    section.section_type === "continue_watching" || section.section_type === "next_up";
  // One spoiler lookup per row rather than one per card.
  const { hideImages, hideOverviews } = useEpisodeSpoilerPrefs({ enabled: isProgressRow });
  const spoilerPrefs = useMemo(() => ({ hideImages, hideOverviews }), [hideImages, hideOverviews]);
  // Progress rows stay wide (16:9) whenever they hold any video, matching the
  // native apps, so Continue Watching keeps its shape as movies and episodes
  // come and go. Only a Continue Watching row made up entirely of books uses
  // the poster variant, where audiobook covers render square and ebook covers
  // upright 2:3.
  const progressCardVariant: "wide" | "poster" =
    section.section_type === "continue_watching" &&
    section.items.length > 0 &&
    section.items.every((it) => it.type === "audiobook" || it.type === "ebook")
      ? "poster"
      : "wide";
  const slideSize = isProgressRow && progressCardVariant === "wide" ? "wide" : posterSize;
  // Which width table this row's slides are measured against.
  const slideWidth: CarouselSlideWidth = isRanked ? "ranked" : isExpanding ? "featured" : "poster";
  const windowing = useMemo<MediaCarouselWindowing>(
    () => ({
      initialCount: initialCarouselSlideCount(slideSize, undefined, slideWidth),
      placeholderClassName:
        slideSize === "wide" ? WIDE_CAROUSEL_CARD_WIDTH_CLASSES : posterWidthClasses,
    }),
    [slideSize, slideWidth, posterWidthClasses],
  );
  const priorityCount = priority ? visibleCarouselSlideCount(slideSize, undefined, slideWidth) : 0;

  if (section.items.length === 0) return null;

  const headerActions = libraryId ? (
    <SectionPinButton sectionId={section.id} sectionTitle={section.title} libraryId={libraryId} />
  ) : undefined;

  return (
    <MediaCarousel
      title={section.title}
      titleHref={browseHref}
      viewAllHref={browseHref}
      headerActions={headerActions}
      windowing={windowing}
      reflowKey={expandedKey}
    >
      {isProgressRow
        ? section.items.map((item, index) => (
            <ContinueWatchingRowCard
              key={item.content_id}
              sectionItem={item}
              libraryId={libraryId}
              overlayPrefs={overlayPrefs}
              quickActionMode={quickActionMode}
              variant={progressCardVariant}
              spoilerPrefs={spoilerPrefs}
              priority={index < priorityCount}
            />
          ))
        : section.items.map((item, index) => (
            /*
             * The card widens *in the row* when it opens — its width is swapped,
             * not layered over its neighbours — so the row reflows and the cards
             * to its right move along. Only one width class may be present at a
             * time: with two on one element, which wins is decided by stylesheet
             * order rather than by the order they are written here.
             */
            <div
              key={`${item.content_id}-${index}`}
              className={`transition-[width] duration-300 ease-out ${
                isExpanding && expandedKey === item.content_id
                  ? expandedWidthClasses
                  : posterWidthClasses
              }`}
              role="listitem"
            >
              <SectionItemCard
                item={item}
                libraryId={libraryId}
                overlayPrefs={overlayPrefs}
                quickActionMode={quickActionMode}
                priority={index < priorityCount}
                rank={isRanked && index < MAX_RANKED_CARDS ? index + 1 : undefined}
                rankedGutterClasses={rankedGutterClasses}
                rankedNumeralClasses={rankedNumeralClasses}
                expandable={isExpanding}
                expanded={isExpanding && expandedKey === item.content_id}
                onExpand={() => setExpandedKey(item.content_id)}
                onCollapse={() =>
                  setExpandedKey((current) => (current === item.content_id ? null : current))
                }
                restingWidthClasses={posterWidthClasses}
                trailersEnabled={trailersEnabled}
              />
            </div>
          ))}
    </MediaCarousel>
  );
});
