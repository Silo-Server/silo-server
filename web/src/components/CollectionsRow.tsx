import { useMemo } from "react";
import type { ResolvedSection, SectionCollectionCard } from "@/api/types";
import MediaCarousel, { type MediaCarouselWindowing } from "@/components/MediaCarousel";
import { CollectionPosterCard } from "@/components/collections/CollectionPosterCard";
import { useProfiles } from "@/hooks/queries/profiles";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useUICustomization } from "@/hooks/useUICustomization";
import { initialCarouselSlideCount } from "@/lib/carouselWindow";
import { ownerName } from "@/lib/collections/personalOwnership";
import { useSectionTitle } from "@/lib/sectionTitle";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";

/**
 * A row of collections on Home or a library page: one poster card per
 * collection, sized like the poster cards of other rows, each opening its
 * collection. The row has no "Explore all" and no pin: its collections are
 * the whole row, and each one has its own page.
 */
export function CollectionsRow({ section }: { section: ResolvedSection }) {
  const sectionTitle = useSectionTitle();
  return <CollectionCardsRow title={sectionTitle(section)} cards={section.collections ?? []} />;
}

/**
 * A titled row of collection poster cards, each opening its collection;
 * nothing when there are no cards. Another profile's shared collection
 * carries its owner's name.
 */
export function CollectionCardsRow({
  title,
  cards,
  edgePadding,
}: {
  title: string;
  cards: SectionCollectionCard[];
  edgePadding?: boolean;
}) {
  const { cardPresentation } = useUICustomization();
  const widthClasses = carouselCardWidthClasses(cardPresentation.poster_size);
  const windowing = useMemo<MediaCarouselWindowing>(
    () => ({
      initialCount: initialCarouselSlideCount(cardPresentation.poster_size),
      placeholderClassName: carouselCardWidthClasses(cardPresentation.poster_size),
    }),
    [cardPresentation.poster_size],
  );
  const { data: profiles = [] } = useProfiles();
  const { profile } = useCurrentProfile();
  if (cards.length === 0) return null;

  const byline = (card: SectionCollectionCard) =>
    card.source === "user" &&
    profile &&
    card.creator_profile_id &&
    card.creator_profile_id !== profile.id
      ? ownerName(profiles, card.creator_profile_id)
      : undefined;

  return (
    <MediaCarousel title={title} windowing={windowing} edgePadding={edgePadding}>
      {cards.map((card) => (
        <div key={`${card.source}:${card.id}`} className={widthClasses} role="listitem">
          <CollectionPosterCard
            collection={card}
            kind={card.source === "user" ? "user_collections" : "regular"}
            libraryId={card.library_id}
            ownerName={byline(card)}
            pinnable={false}
          />
        </div>
      ))}
    </MediaCarousel>
  );
}
