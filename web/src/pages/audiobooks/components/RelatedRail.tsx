import type { ReactNode } from "react";
import MediaCardArtwork from "@/components/MediaCardArtwork";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import WatchProgressBar from "@/components/WatchProgressBar";
import { useUICustomization } from "@/hooks/useUICustomization";
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
  return (
    <section>
      <div className="mb-4 flex items-baseline justify-between gap-3">
        <h2 className="text-xl font-semibold tracking-tight">{heading}</h2>
        {meta && <span className="text-muted-foreground text-sm">{meta}</span>}
      </div>
      <div className="-mx-2 -mt-1 flex gap-4 overflow-x-auto px-2 pt-1 pb-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {items.map((item) => (
          <ViewTransitionLink
            key={item.content_id}
            to={`/item/${encodeURIComponent(item.content_id)}`}
            aria-current={item.highlight ? "page" : undefined}
            className={`group/related block ${widthClasses}`}
          >
            <div
              className={`overflow-hidden rounded-lg transition-[filter] group-hover/related:brightness-110 ${
                item.highlight ? "ring-primary ring-offset-background ring-2 ring-offset-2" : ""
              }`}
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
                {item.progress != null && item.progress > 0 && (
                  <WatchProgressBar percent={item.progress * 100} />
                )}
              </MediaCardArtwork>
            </div>
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
      </div>
    </section>
  );
}
