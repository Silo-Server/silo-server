import { ChevronLeft, ChevronRight } from "lucide-react";
import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import BrandCard from "@/components/BrandCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { useT } from "@/i18n";
import { readingArrowKey } from "@/lib/direction";

interface BrandCarouselProps {
  kind: DiscoverBrowseKind;
  title: string;
  cards: DiscoverBrandCard[] | undefined;
  isLoading: boolean;
  isError: boolean;
  onRetry?: () => void;
}

export default function BrandCarousel({
  kind,
  title,
  cards,
  isLoading,
  isError,
  onRetry,
}: BrandCarouselProps) {
  const t = useT();
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();

  const slides = isLoading
    ? Array.from({ length: 8 }).map((_, idx) => (
        <Skeleton key={idx} className="h-28 w-52 flex-none rounded-xl sm:h-32 sm:w-64" />
      ))
    : (cards ?? []).map((card) => <BrandCard key={card.slug} kind={kind} card={card} />);

  return (
    <section className="group/carousel relative isolate">
      {/* The same header as MediaCarousel, so brand rows read as rows of the page. */}
      <div className="mb-3 flex items-end justify-between gap-4 px-4 sm:px-6 lg:px-10 xl:px-12">
        <h2 className="text-foreground text-xl font-semibold tracking-tight">{title}</h2>
        {isError && onRetry ? (
          <button
            type="button"
            onClick={onRetry}
            className="text-muted-foreground hover:text-foreground text-xs underline-offset-2 hover:underline"
          >
            {t("common.retry")}
          </button>
        ) : null}
      </div>

      {isError && !isLoading ? (
        <div className="text-muted-foreground flex h-28 items-center px-4 text-xs sm:px-6 lg:px-10 xl:px-12">
          {t("web.common.ui.carousel.loadFailed", { title_lower: title.toLowerCase() })}
        </div>
      ) : (
        <div className="relative">
          {canScrollPrev && (
            <div className="from-background/80 pointer-events-none absolute start-0 top-0 bottom-0 z-[5] w-10 bg-gradient-to-r to-transparent rtl:bg-gradient-to-l" />
          )}
          {canScrollPrev && (
            <button
              type="button"
              onClick={scrollPrev}
              className="from-background/80 absolute start-0 top-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-(--duration-fast) group-hover/carousel:opacity-100 focus-visible:opacity-100 rtl:bg-gradient-to-l"
              aria-label={t("web.common.ui.carousel.scrollLeft")}
            >
              <ChevronLeft className="text-foreground h-6 w-6" />
            </button>
          )}

          <div
            ref={emblaRef}
            className="embla__viewport overflow-hidden pe-4 sm:pe-6 lg:pe-10 xl:pe-12"
            tabIndex={0}
            aria-label={t("web.common.ui.carousel.label", { title })}
            onKeyDown={(e) => {
              const key = readingArrowKey(e);
              if (key === "ArrowLeft") {
                scrollPrev();
              } else if (key === "ArrowRight") {
                scrollNext();
              }
            }}
          >
            <ul
              role="list"
              className="embla__container flex cursor-grab list-none gap-4 py-2 ps-4 sm:ps-6 lg:ps-10 xl:ps-12"
            >
              {slides.map((slide, index) => (
                <li key={index} className="embla__slide shrink-0">
                  {slide}
                </li>
              ))}
            </ul>
          </div>

          {canScrollNext && (
            <button
              type="button"
              onClick={scrollNext}
              className="from-background/80 absolute end-0 top-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-(--duration-fast) group-hover/carousel:opacity-100 focus-visible:opacity-100 rtl:bg-gradient-to-r"
              aria-label={t("web.common.ui.carousel.scrollRight")}
            >
              <ChevronRight className="text-foreground h-6 w-6" />
            </button>
          )}
        </div>
      )}
    </section>
  );
}
