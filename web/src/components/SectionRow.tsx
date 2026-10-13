import { memo, useMemo } from "react";
import { ContinueWatchingRowCard } from "@/components/ContinueWatchingCard";
import MediaCarousel, { type MediaCarouselWindowing } from "@/components/MediaCarousel";
import SectionItemCard from "@/components/SectionItemCard";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { buildSectionCatalogHref, isSectionBrowseSupported } from "@/pages/catalogSearchParams";
import type { ResolvedSection } from "@/api/types";
import { Pin, PinOff } from "lucide-react";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { carouselCardWidthClasses, WIDE_CAROUSEL_CARD_WIDTH_CLASSES } from "@/lib/uiCustomization";
import { initialCarouselSlideCount, visibleCarouselSlideCount } from "@/lib/carouselWindow";

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
  const navigate = useViewTransitionNavigate();
  const browseSupported = isSectionBrowseSupported(section.section_type);
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const { cardPresentation } = useUICustomization();
  const posterSize = cardPresentation.poster_size;
  const posterWidthClasses = carouselCardWidthClasses(posterSize);
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
  const windowing = useMemo<MediaCarouselWindowing>(
    () => ({
      initialCount: initialCarouselSlideCount(slideSize),
      placeholderClassName:
        slideSize === "wide"
          ? WIDE_CAROUSEL_CARD_WIDTH_CLASSES
          : carouselCardWidthClasses(slideSize),
    }),
    [slideSize],
  );
  const priorityCount = priority ? visibleCarouselSlideCount(slideSize) : 0;

  const handleViewAll = () => {
    if (!browseSupported) {
      return;
    }

    navigate(
      libraryId
        ? buildSectionCatalogHref({
            scope: "library",
            libraryId: libraryId,
            sectionId: section.id,
            title: section.title,
          })
        : buildSectionCatalogHref({
            scope: "home",
            sectionId: section.id,
            title: section.title,
          }),
    );
  };

  if (section.items.length === 0) return null;

  const headerActions =
    libraryId && browseSupported ? (
      <SectionPinButton sectionId={section.id} sectionTitle={section.title} libraryId={libraryId} />
    ) : undefined;

  return (
    <MediaCarousel
      title={section.title}
      onViewAll={
        browseSupported && section.total_count > section.item_limit ? handleViewAll : undefined
      }
      headerActions={headerActions}
      windowing={windowing}
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
            <div key={`${item.content_id}-${index}`} className={posterWidthClasses} role="listitem">
              <SectionItemCard
                item={item}
                libraryId={libraryId}
                overlayPrefs={overlayPrefs}
                quickActionMode={quickActionMode}
                priority={index < priorityCount}
              />
            </div>
          ))}
    </MediaCarousel>
  );
});
