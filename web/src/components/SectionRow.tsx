import { memo, useCallback, useId, useMemo } from "react";
import { CollectionsRow } from "@/components/CollectionsRow";
import { ContinueWatchingRowCard } from "@/components/ContinueWatchingCard";
import MediaCarousel, { type MediaCarouselWindowing } from "@/components/MediaCarousel";
import SectionItemCard from "@/components/SectionItemCard";
import { isProgressSectionType, sectionShowsWholeList } from "@/hooks/queries/homeRowLimit";
import { useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { buildSectionCatalogHref } from "@/pages/catalogSearchParams";
import type { ResolvedSection } from "@/api/types";
import { Pin, PinOff } from "lucide-react";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { useSectionTitle } from "@/lib/sectionTitle";
import { useT } from "@/i18n";
import {
  carouselCardWidthClasses,
  squarePosterSize,
  shortcutLabel,
  WIDE_CAROUSEL_CARD_WIDTH_CLASSES,
} from "@/lib/uiCustomization";
import { initialCarouselSlideCount, visibleCarouselSlideCount } from "@/lib/carouselWindow";

interface SectionRowProps {
  section: ResolvedSection;
  /** Library ID this section belongs to (enables pin-to-sidebar) */
  libraryId?: number;
  /**
   * The row's stored name from the layout, saved as a pin's label. The fetched
   * title can name this fetch's anchor, genre or subject, which goes stale.
   */
  pinLabel?: string;
  /**
   * The row is on screen at first paint (the first Home row): its visible
   * cards load their artwork eagerly at high fetch priority.
   */
  priority?: boolean;
  /**
   * Called with the section id when the carousel nears its last card, so a
   * row loaded in part can load the rest of itself.
   */
  onNearEnd?: (sectionId: string) => void;
}

function SectionPinButton({
  sectionId,
  label,
  rowTitle,
  libraryId,
}: {
  sectionId: string;
  /** Stored with the pin. */
  label: string;
  /** The row's shown (translated) heading, which describes the button. */
  rowTitle: string;
  libraryId: number;
}) {
  const t = useT();
  const { togglePin, isPinned, canToggle } = useToggleSidebarPin();
  const pinned = isPinned(libraryId, "section", sectionId);
  const labelId = useId();

  if (!canToggle) return null;

  return (
    <>
      <span id={labelId} hidden>
        {rowTitle}
      </span>
      <button
        onClick={() => togglePin(libraryId, { type: "section", id: sectionId, label })}
        className={`rounded-full p-1.5 transition-colors ${
          pinned
            ? "text-primary hover:text-primary/80"
            : "text-muted-foreground/40 hover:bg-accent/60 hover:text-muted-foreground opacity-0 group-hover/carousel:opacity-100"
        }`}
        title={pinned ? t("web.home.sectionRow.unpin") : t("web.home.sectionRow.pin")}
        aria-label={pinned ? t("web.home.sectionRow.unpin") : t("web.home.sectionRow.pin")}
        aria-describedby={labelId}
      >
        {pinned ? <PinOff className="h-4 w-4" /> : <Pin className="h-4 w-4" />}
      </button>
    </>
  );
}

export default memo(function SectionRow({
  section,
  libraryId,
  pinLabel,
  priority = false,
  onNearEnd,
}: SectionRowProps) {
  const sectionTitle = useSectionTitle();
  // The catalog page heads itself with the link's title, so it gets the shown
  // (translated) title. A pin stores its label, so it gets `pinLabel`.
  const shownTitle = sectionTitle(section);
  const browseHref = useMemo(
    () =>
      libraryId
        ? buildSectionCatalogHref({
            scope: "library",
            libraryId,
            sectionId: section.id,
            title: shownTitle,
          })
        : buildSectionCatalogHref({ scope: "home", sectionId: section.id, title: shownTitle }),
    [libraryId, section.id, shownTitle],
  );
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const { cardPresentation } = useUICustomization();
  // A row of audiobooks draws its square covers a size down (squarePosterSize).
  const posterSize =
    section.items.length > 0 && section.items.every((it) => it.type === "audiobook")
      ? squarePosterSize(cardPresentation.poster_size)
      : cardPresentation.poster_size;
  const posterWidthClasses = carouselCardWidthClasses(posterSize);
  const isProgressRow = isProgressSectionType(section.section_type);
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
  const sectionId = section.id;
  const handleNearEnd = useCallback(() => onNearEnd?.(sectionId), [onNearEnd, sectionId]);

  if (section.collections?.length) return <CollectionsRow section={section} />;
  if (section.items.length === 0) return null;

  const headerActions = libraryId ? (
    <SectionPinButton
      sectionId={section.id}
      label={shortcutLabel(pinLabel ?? section.title)}
      rowTitle={shownTitle}
      libraryId={libraryId}
    />
  ) : undefined;
  // The browse page of a row that shows its whole list holds the same cards.
  const rowHref = sectionShowsWholeList(section) ? undefined : browseHref;

  return (
    <MediaCarousel
      title={shownTitle}
      titleHref={rowHref}
      viewAllHref={rowHref}
      headerActions={headerActions}
      windowing={windowing}
      onNearEnd={onNearEnd ? handleNearEnd : undefined}
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
