import {
  Children,
  isValidElement,
  memo,
  startTransition,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { CSSProperties, ReactNode } from "react";
import { Link } from "react-router";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselIntrinsicHeight } from "@/lib/uiCustomization";
import {
  CAROUSEL_WINDOW_BUFFER_SLIDES,
  CAROUSEL_WINDOW_IDLE_BATCH,
  scheduleCarouselIdleTask,
} from "@/lib/carouselWindow";

/**
 * Mounts only the first `initialCount` slides in the first commit. The rest
 * render as empty slides sized by `placeholderClassName`, so Embla's snaps
 * and scroll width match the full row; they fill in idle batches, as they
 * scroll near the viewport, or all at once when focus enters the row. Once
 * filled, every card is in the DOM for browser Find and assistive technology.
 */
export interface MediaCarouselWindowing {
  initialCount: number;
  /** Width classes matching each slide's own outer width. */
  placeholderClassName: string;
}

interface CarouselSlide {
  key: string;
  node: ReactNode;
}

interface MediaCarouselProps {
  title: string;
  titleHref?: string;
  loading?: boolean;
  children: ReactNode;
  skeletonCount?: number;
  skeletonAspect?: string;
  onViewAll?: () => void;
  /** Renders "Explore all" as a link to this page. Takes precedence over onViewAll. */
  viewAllHref?: string;
  /** Optional actions rendered next to the title (e.g. pin button) */
  headerActions?: ReactNode;
  /**
   * When true (default) the carousel applies its own page-edge horizontal
   * padding, so it sits correctly on full-bleed pages (Home, Library). Set to
   * false when embedding inside an already-padded container (e.g. a
   * `page-shell` page) so the header and cards align to the parent's content
   * box instead of picking up a second layer of padding.
   */
  edgePadding?: boolean;
  /**
   * Compact rows for dense hosts such as pickers and dialogs: a small
   * uppercase eyebrow title, tighter header spacing, and no page-level gap.
   */
  compact?: boolean;
  windowing?: MediaCarouselWindowing;
}

/**
 * Collects slides without cloning them (Children.toArray clones every
 * element), so an unchanged child keeps its element identity and React skips
 * it when only the carousel's own state changes. Keys come from the children
 * themselves so a slide keeps its DOM when items are inserted or reordered.
 */
function collectSlides(children: ReactNode): CarouselSlide[] {
  const slides: CarouselSlide[] = [];
  const seen = new Set<string>();
  Children.forEach(children, (child, index) => {
    if (child === null || child === undefined || typeof child === "boolean") return;
    const baseKey = isValidElement(child) && child.key !== null ? `k:${child.key}` : `i:${index}`;
    const key = seen.has(baseKey) ? `${baseKey}:${index}` : baseKey;
    seen.add(key);
    slides.push({ key, node: child });
  });
  return slides;
}

const CarouselSlideList = memo(function CarouselSlideList({
  slides,
  className,
  windowInitialCount,
  windowPlaceholderClassName,
}: {
  slides: CarouselSlide[];
  className: string;
  windowInitialCount?: number;
  windowPlaceholderClassName?: string;
}) {
  const windowed = windowInitialCount !== undefined && windowPlaceholderClassName !== undefined;
  const [mountedCount, setMountedCount] = useState(() =>
    windowed ? windowInitialCount : Number.POSITIVE_INFINITY,
  );
  const listRef = useRef<HTMLUListElement>(null);
  const total = slides.length;
  const limit = windowed ? mountedCount : total;
  const filling = limit < total;

  useEffect(() => {
    if (!filling) return;
    return scheduleCarouselIdleTask(() => {
      startTransition(() => setMountedCount((count) => count + CAROUSEL_WINDOW_IDLE_BATCH));
    });
  }, [filling, mountedCount]);

  useEffect(() => {
    const list = listRef.current;
    const viewport = list?.parentElement;
    if (!filling || !list || !viewport) return;

    // Keyboard and TV users tab through every card, so a row they enter is
    // filled before the next Tab can skip past an empty slide.
    const mountAll = () => setMountedCount(Number.POSITIVE_INFINITY);
    viewport.addEventListener("focusin", mountAll);

    let observer: IntersectionObserver | null = null;
    if (typeof IntersectionObserver !== "undefined") {
      observer = new IntersectionObserver(
        (entries) => {
          let through = 0;
          for (const entry of entries) {
            if (!entry.isIntersecting) continue;
            const index = Number((entry.target as HTMLElement).dataset.carouselSlideIndex);
            if (Number.isFinite(index)) {
              through = Math.max(through, index + 1 + CAROUSEL_WINDOW_BUFFER_SLIDES);
            }
          }
          if (through > 0) setMountedCount((count) => Math.max(count, through));
        },
        // Half a row ahead, so a drag or arrow press meets real cards.
        { root: viewport, rootMargin: "0px 50%" },
      );
      for (const slide of Array.from(list.children)) {
        if (slide instanceof HTMLElement && slide.dataset.carouselSlideIndex !== undefined) {
          observer.observe(slide);
        }
      }
    }

    return () => {
      viewport.removeEventListener("focusin", mountAll);
      observer?.disconnect();
    };
  }, [filling, total]);

  return (
    <ul ref={listRef} role="list" className={className}>
      {slides.map((slide, index) =>
        index < limit ? (
          <li key={slide.key} className="embla__slide shrink-0">
            {slide.node}
          </li>
        ) : (
          <li
            key={slide.key}
            className="embla__slide shrink-0"
            aria-hidden="true"
            data-carousel-slide-index={index}
          >
            <div className={windowPlaceholderClassName} />
          </li>
        ),
      )}
    </ul>
  );
});

export default function MediaCarousel({
  title,
  titleHref,
  loading = false,
  children,
  skeletonCount = 7,
  skeletonAspect = "aspect-[2/3]",
  onViewAll,
  viewAllHref,
  headerActions,
  edgePadding = true,
  compact = false,
  windowing,
}: MediaCarouselProps) {
  const viewAllClass =
    "text-muted-foreground hover:text-primary text-[0.75rem] font-semibold tracking-[0.16em] uppercase transition-all active:scale-[0.98]";
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();
  const { cardPresentation } = useUICustomization();
  // Page-edge padding is opt-out so the carousel can also be embedded in an
  // already-padded container without double-padding the header and cards.
  const headerPadX = edgePadding ? " px-4 sm:px-6 lg:px-10 xl:px-12" : "";
  const viewportPadX = edgePadding ? " pr-4 sm:pr-6 lg:pr-10 xl:pr-12" : "";
  const containerPadX = edgePadding ? " pl-4 sm:pl-6 lg:pl-10 xl:pl-12" : "";
  const titleClass = compact
    ? "text-muted-foreground text-[0.6875rem] font-semibold tracking-[0.18em] uppercase"
    : "text-foreground text-xl font-semibold tracking-tight";
  const headerGap = compact ? "mb-2" : "mb-5";
  const slideGap = compact ? "gap-3" : "gap-4 lg:gap-5";
  const listClassName = `embla__container flex cursor-grab list-none ${slideGap}${containerPadX}`;
  // Memoized on the children so the carousel's own state (the scroll arrows
  // flipping after Embla initializes) does not re-render every slide.
  const slides = useMemo(
    () =>
      loading
        ? Array.from({ length: skeletonCount }, (_, i) => ({
            key: `skeleton:${i}`,
            node: (
              <div className="w-[130px] sm:w-[150px] lg:w-[178px]">
                <Skeleton className={`w-full ${skeletonAspect} rounded-lg`} />
                <Skeleton className="mt-2 h-4 w-3/4 rounded" />
                <Skeleton className="mt-1 h-3 w-1/2 rounded" />
              </div>
            ),
          }))
        : collectSlides(children),
    [children, loading, skeletonAspect, skeletonCount],
  );

  return (
    <section
      className="section-row media-carousel group/carousel relative isolate"
      style={
        {
          "--carousel-intrinsic-h": carouselIntrinsicHeight(cardPresentation.poster_size),
        } as CSSProperties
      }
    >
      <div className={`${headerGap} flex items-end justify-between gap-4${headerPadX}`}>
        <div className="flex items-center gap-2">
          {titleHref ? (
            <Link to={titleHref} className="group/title hover:text-primary transition-colors">
              <h2 className={titleClass}>
                {title}
                <span className="text-muted-foreground group-hover/title:text-primary ml-2 text-sm transition-colors">
                  View
                </span>
              </h2>
            </Link>
          ) : (
            <h2 className={titleClass}>{title}</h2>
          )}
          {headerActions}
        </div>
        {viewAllHref ? (
          <Link
            to={viewAllHref}
            aria-label={`Explore all ${title}`}
            className={`${viewAllClass} shrink-0`}
          >
            Explore all
          </Link>
        ) : onViewAll ? (
          <button onClick={onViewAll} className={viewAllClass}>
            Explore all
          </button>
        ) : null}
      </div>

      <div className="relative">
        {/* Left edge gradient */}
        {canScrollPrev && (
          <div className="from-background/80 pointer-events-none absolute top-0 bottom-0 left-0 z-[5] w-10 bg-gradient-to-r to-transparent" />
        )}

        {/* Left arrow */}
        {canScrollPrev && (
          <button
            type="button"
            onClick={scrollPrev}
            className="from-background/80 absolute top-0 bottom-0 left-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-(--duration-fast) group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll left"
          >
            <ChevronLeft className="text-foreground h-6 w-6" />
          </button>
        )}

        <div
          ref={emblaRef}
          className={`embla__viewport -mt-1 overflow-hidden pt-1${viewportPadX}`}
          tabIndex={0}
          aria-label="Media carousel"
          onKeyDown={(e) => {
            if (e.key === "ArrowLeft") {
              scrollPrev();
            } else if (e.key === "ArrowRight") {
              scrollNext();
            }
          }}
        >
          <CarouselSlideList
            slides={slides}
            className={listClassName}
            windowInitialCount={windowing?.initialCount}
            windowPlaceholderClassName={windowing?.placeholderClassName}
          />
        </div>

        {/* Right arrow */}
        {canScrollNext && (
          <button
            type="button"
            onClick={scrollNext}
            className="from-background/80 absolute top-0 right-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-(--duration-fast) group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll right"
          >
            <ChevronRight className="text-foreground h-6 w-6" />
          </button>
        )}
      </div>
    </section>
  );
}
