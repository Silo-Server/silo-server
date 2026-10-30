import ItemCard from "@/components/ItemCard";
import MediaCarousel from "@/components/MediaCarousel";
import { useItemBoxSet } from "@/hooks/queries/boxSets";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useUICustomization } from "@/hooks/useUICustomization";
import { buildBoxSetHref } from "@/lib/mediaNavigation";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";

/**
 * The box set row on a movie's page: the box set's movies in release order,
 * starting at the movie being viewed, which is marked "This film". Nothing
 * shows while it loads, or when the movie is in no box set.
 */
export default function MovieBoxSetRow({
  contentId,
  libraryId,
}: {
  contentId: string;
  libraryId?: number;
}) {
  const { data: boxSet } = useItemBoxSet(contentId, libraryId);
  const { cardPresentation } = useUICustomization();
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  if (!boxSet || boxSet.items.length === 0) return null;

  const boxSetLibraryId = Number(boxSet.library_id) || undefined;
  const current = boxSet.items.findIndex((item) => item.content_id === contentId);

  return (
    <MediaCarousel
      title={boxSet.title}
      edgePadding={false}
      viewAllHref={buildBoxSetHref({ id: boxSet.id, libraryId: boxSet.library_id })}
      startSlide={current >= 0 ? current : undefined}
    >
      {boxSet.items.map((item) => {
        const isCurrent = item.content_id === contentId;
        return (
          <div
            key={item.content_id}
            className={carouselCardWidthClasses(cardPresentation.poster_size)}
            aria-current={isCurrent ? "page" : undefined}
          >
            <div className="relative">
              <ItemCard
                item={item}
                libraryId={boxSetLibraryId}
                overlayPrefs={overlayPrefs}
                quickActionMode={quickActionMode}
              />
              {isCurrent ? (
                <>
                  <div className="ring-foreground/80 pointer-events-none absolute inset-x-0 top-0 aspect-[2/3] rounded-lg ring-2" />
                  <span className="bg-background/85 text-foreground pointer-events-none absolute top-0 left-1/2 mt-[calc(150%-2rem)] -translate-x-1/2 rounded-full px-2.5 py-0.5 text-[0.625rem] font-semibold tracking-[0.14em] whitespace-nowrap uppercase">
                    This film
                  </span>
                </>
              ) : null}
            </div>
          </div>
        );
      })}
    </MediaCarousel>
  );
}
