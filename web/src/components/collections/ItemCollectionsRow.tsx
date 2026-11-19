import { CollectionCardsRow } from "@/components/CollectionsRow";
import ItemCard from "@/components/ItemCard";
import MediaCarousel from "@/components/MediaCarousel";
import { useItemCollections } from "@/hooks/queries/itemCollections";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useT } from "@/i18n";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";
import { buildCollectionCatalogHref } from "@/pages/catalogSearchParams";

/**
 * The collections row on a movie or series page. When one collection holds
 * the title, the row shows up to six of that collection's other titles under
 * its name, with "Explore all" opening the collection. When several do, an
 * "Also in" row shows a card for each. Nothing shows while it loads, when no
 * collection holds the title, or when the first read fails. A failed refetch
 * keeps the last answer, as the box set row does.
 */
export default function ItemCollectionsRow({
  contentId,
  libraryId,
}: {
  contentId: string;
  libraryId?: number;
}) {
  const t = useT();
  const { data } = useItemCollections(contentId, libraryId);
  const { cardPresentation } = useUICustomization();
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  if (!data || data.cards.length === 0) return null;

  const [only] = data.cards;
  if (data.cards.length === 1 && only && data.preview.length > 0) {
    const href = buildCollectionCatalogHref(only, only.source === "user", only.library_id);
    return (
      <MediaCarousel title={only.title} edgePadding={false} viewAllHref={href}>
        {data.preview.map((item) => (
          <div
            key={item.content_id}
            className={carouselCardWidthClasses(cardPresentation.poster_size)}
          >
            <ItemCard
              item={item}
              libraryId={only.library_id}
              overlayPrefs={overlayPrefs}
              quickActionMode={quickActionMode}
            />
          </div>
        ))}
      </MediaCarousel>
    );
  }
  return (
    <CollectionCardsRow
      title={t("web.detail.sections.alsoIn")}
      cards={data.cards}
      edgePadding={false}
    />
  );
}
