import type { ReactNode } from "react";
import MediaCardArtwork from "@/components/MediaCardArtwork";
import MediaCarousel from "@/components/MediaCarousel";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import WatchProgressBar from "@/components/WatchProgressBar";
import { useUICustomization } from "@/hooks/useUICustomization";
import { buildItemHref } from "@/lib/mediaNavigation";
import { carouselCardWidthClasses, squareCarouselCardWidthClasses } from "@/lib/uiCustomization";

interface RelatedRailItem {
  content_id: string;
  title: string;
  poster_url?: string;
  poster_thumbhash?: string;
  /** A line above the title, such as the book's place in its series. */
  kicker?: string;
  subtitle?: ReactNode;
  highlight?: boolean;
  /** 0-1 while the profile is partway through it; draws a bar on the cover. */
  progress?: number;
}

interface RelatedRailProps {
  heading: string;
  /** Shown beside the heading, such as a series' book count. */
  meta?: string;
  items: RelatedRailItem[];
  coverAspect?: "square" | "poster";
}

export function RelatedRail({ heading, meta, items, coverAspect = "square" }: RelatedRailProps) {
  const { cardPresentation } = useUICustomization();
  if (items.length === 0) return null;
  const square = coverAspect === "square";
  const widthClasses = square
    ? squareCarouselCardWidthClasses(cardPresentation.poster_size)
    : carouselCardWidthClasses(cardPresentation.poster_size);
  const currentIndex = items.findIndex((item) => item.highlight);

  return (
    <MediaCarousel
      title={heading}
      edgePadding={false}
      startSlide={currentIndex >= 0 ? currentIndex : undefined}
      headerActions={
        meta ? <span className="text-muted-foreground text-sm">{meta}</span> : undefined
      }
    >
      {items.map((item) => (
        <ViewTransitionLink
          key={item.content_id}
          to={buildItemHref({ contentId: item.content_id })}
          aria-current={item.highlight ? "page" : undefined}
          className={`media-card group/related block ${widthClasses}`}
        >
          <MediaCardArtwork
            src={item.poster_url}
            alt={item.title}
            mediaType={square ? "audiobook" : "ebook"}
            thumbhash={item.poster_thumbhash}
            square={square}
            scrim="none"
            lazy
          >
            {item.highlight && (
              <div
                aria-hidden="true"
                className="ring-primary pointer-events-none absolute inset-0 rounded-xl ring-2 ring-inset"
              />
            )}
            {item.progress != null && item.progress > 0 && (
              <WatchProgressBar percent={item.progress * 100} />
            )}
          </MediaCardArtwork>
          {item.kicker && (
            <div className="text-muted-foreground mt-2 truncate text-xs">{item.kicker}</div>
          )}
          <div
            className={`line-clamp-2 text-sm leading-snug font-semibold ${item.kicker ? "mt-0.5" : "mt-2"}`}
          >
            {item.title}
          </div>
          {item.subtitle ? (
            <div className="text-muted-foreground mt-0.5 truncate text-xs">{item.subtitle}</div>
          ) : null}
        </ViewTransitionLink>
      ))}
    </MediaCarousel>
  );
}
