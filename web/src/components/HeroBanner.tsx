import { useState, useEffect, useCallback, useMemo } from "react";
import { Link } from "react-router";
import { Info, ChevronLeft, ChevronRight, Play, Pause, BookOpen } from "lucide-react";
import { decodeThumbhash } from "@/lib/thumbhash";
import { HERO_BANNER_SIZE } from "@/lib/design-system";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { cn } from "@/lib/utils";
import type { SectionItem } from "@/api/types";

interface HeroBannerProps {
  items: SectionItem[];
  maxSlides?: number;
  /**
   * Override the container height classes. Defaults to the standard hero size
   * used on the home page. Library Recommended passes the taller marquee size.
   */
  heightClassName?: string;
  /**
   * When true, the hero has no border-b and no bottom margin — it bleeds into
   * the content below via the stronger bottom gradient. Used by the Library
   * Recommended tab where the marquee header overlays the top.
   */
  bleed?: boolean;
  /**
   * When true, adds extra top padding inside the hero so the title block clears
   * a sticky marquee header overlaying the top of the frame.
   */
  reserveHeaderSpace?: boolean;
  /**
   * Library id used for building Play links. Forwarded as a `libraryId` query
   * param so the watch/item routes can scope back to the right library.
   */
  libraryId?: number;
}

/**
 * Build the target href for the hero's Play CTA. Directly-playable types
 * (movies, episodes) go to /watch; container types (series, season) have no
 * single video, so Play falls back to the detail page where the existing
 * in-context Play button handles next-episode / resume logic.
 */
function buildPlayHref(item: SectionItem, libraryId?: number): string {
  const q = libraryId ? `?libraryId=${libraryId}` : "";
  if (item.type === "ebook") {
    return `/reader/ebook/${encodeURIComponent(item.content_id)}${q}`;
  }
  if (item.type === "movie" || item.type === "episode") {
    return `/watch/${item.content_id}${q}`;
  }
  return `/item/${item.content_id}${q}`;
}

function formatRuntime(seconds: number | undefined | null): string | null {
  if (!seconds || seconds <= 0) return null;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const remaining = minutes % 60;
  return remaining === 0 ? `${hours}h` : `${hours}h ${remaining}m`;
}

export default function HeroBanner({
  items,
  maxSlides = 10,
  heightClassName = HERO_BANNER_SIZE,
  bleed = false,
  reserveHeaderSpace = false,
  libraryId,
}: HeroBannerProps) {
  const slides = useMemo(() => items.slice(0, maxSlides), [items, maxSlides]);
  const [activeIndex, setActiveIndex] = useState(0);
  const [loaded, setLoaded] = useState<Record<number, boolean>>({});
  const [paused, setPaused] = useState(false);
  // Bumped whenever auto-advance restarts a fresh 8s cycle — after the slide
  // changes, or after the user unpauses. Used as part of the progress-rail key
  // so the CSS animation restarts in lockstep with the setInterval timer
  // (otherwise, resuming via animation-play-state desyncs: rail finishes
  // before the next slide advance, or vice-versa, depending on when the user
  // paused).
  const [playCycle, setPlayCycle] = useState(0);

  const next = useCallback(() => {
    setActiveIndex((i) => (i + 1) % slides.length);
  }, [slides.length]);

  const prev = useCallback(() => {
    setActiveIndex((i) => (i - 1 + slides.length) % slides.length);
  }, [slides.length]);

  // Pause/play helpers. Unpausing bumps playCycle so the rail animation
  // restarts at scaleX(0) in sync with the fresh 8s interval started below.
  const pause = useCallback(() => setPaused(true), []);
  const resume = useCallback(() => {
    setPaused((prev) => {
      if (prev) setPlayCycle((c) => c + 1);
      return false;
    });
  }, []);
  const togglePause = useCallback(() => {
    setPaused((prev) => {
      if (prev) setPlayCycle((c) => c + 1);
      return !prev;
    });
  }, []);

  // `activeIndex` is in the dep list so that manual navigation (prev/next
  // chevrons) also resets the 8s countdown — otherwise after clicking next at
  // t=5s the auto-advance would still fire at t=8 instead of t=13, desyncing
  // with the progress rail that restarts on slide change.
  useEffect(() => {
    if (slides.length <= 1 || paused) return;
    const prefersReducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (prefersReducedMotion) return;
    const timer = setInterval(next, 8000);
    return () => clearInterval(timer);
  }, [next, slides.length, paused, activeIndex]);

  const current = slides[activeIndex] ?? slides[0];
  useAmbientColor(current?.backdrop_thumbhash);
  if (slides.length === 0) return null;
  if (!current) return null;

  const metaParts: string[] = [];
  if (current.year > 0) metaParts.push(String(current.year));
  if (current.rating_imdb != null) metaParts.push(`IMDb ${current.rating_imdb.toFixed(1)}`);
  (current.genres ?? []).slice(0, 3).forEach((g) => metaParts.push(g));
  const runtime = formatRuntime(current.duration_seconds);
  if (runtime) metaParts.push(runtime);

  const slideCount = slides.length;
  const padded = (n: number) => String(n).padStart(2, "0");
  const primaryActionLabel = current.type === "ebook" ? "Read" : "Play";
  const PrimaryActionIcon = current.type === "ebook" ? BookOpen : Play;

  return (
    <section
      className={cn(
        "home-hero group relative w-full overflow-hidden",
        heightClassName,
        bleed ? "-mt-[96px] mb-0 sm:-mt-[104px]" : "border-border/60 mb-10 border-b",
      )}
      aria-roledescription="carousel"
      aria-label="Featured content"
      onMouseEnter={pause}
      onMouseLeave={resume}
      onFocus={pause}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget)) resume();
      }}
    >
      {/* Backdrop layers – all stacked, crossfade via opacity */}
      {slides.map((slide, i) => {
        const thumbhash = slide.backdrop_thumbhash ? decodeThumbhash(slide.backdrop_thumbhash) : "";
        const isActive = i === activeIndex;
        return (
          <div
            key={slide.content_id ?? i}
            aria-roledescription="slide"
            className={`bg-muted absolute inset-0 transition-opacity duration-1000 ease-in-out ${isActive ? "opacity-100" : "opacity-0"}`}
            style={
              thumbhash
                ? {
                    backgroundImage: `url(${thumbhash})`,
                    backgroundSize: "cover",
                    backgroundPosition: "center 20%",
                  }
                : undefined
            }
          >
            {slide.backdrop_url && (
              <img
                src={slide.backdrop_url}
                alt=""
                className={`h-full w-full object-cover object-[center_20%] transition-opacity duration-[--duration-slow] will-change-transform ${loaded[i] ? "opacity-100" : "opacity-0"}`}
                style={{
                  animation: `var(--animate-ken-burns-${i % 2 === 0 ? "a" : "b"})`,
                  filter: `brightness(var(--hero-backdrop-brightness, 0.78)) saturate(var(--hero-backdrop-saturate, 0.95))`,
                }}
                onLoad={() => setLoaded((prev) => ({ ...prev, [i]: true }))}
              />
            )}
          </div>
        );
      })}

      {/* Gradient overlays */}
      {bleed && <div className="hero-top-scrim" />}
      <div className={bleed ? "hero-gradient-strong" : "hero-gradient"} />
      <div className="ambient-glow" />
      <div className="hero-gradient-left" />
      {!bleed && <div className="hero-vignette" />}

      {/* Content */}
      <div
        className={cn(
          "relative z-10 flex h-full min-h-0 items-end px-4 pb-10 sm:px-6 sm:pb-12 lg:px-10 lg:pb-16 xl:px-12",
          reserveHeaderSpace && "pt-24 sm:pt-28 lg:pt-32",
        )}
      >
        <div className="w-full max-w-[1380px]">
          <div className="max-w-3xl">
            <p className="hero-eyebrow mb-4 hidden sm:block">
              <span className="hero-eyebrow-strong">Featured</span>
              <span className="hero-eyebrow-divider">—</span>
              <span>No. {padded(activeIndex + 1)}</span>
            </p>
            <h1
              key={current.content_id}
              className="font-display mb-4 max-w-4xl text-4xl font-extrabold tracking-[-0.04em] text-balance sm:text-5xl lg:text-6xl xl:text-7xl"
              style={{ textShadow: "var(--hero-text-shadow, none)" }}
            >
              {current.title}
            </h1>
            {metaParts.length > 0 && (
              <div className="hero-meta-track mb-5 text-white/85">
                {metaParts.map((part) => (
                  <span key={part}>{part}</span>
                ))}
              </div>
            )}
            {current.overview && (
              <p className="text-foreground/72 mb-7 line-clamp-2 max-w-2xl text-sm leading-7 sm:line-clamp-none sm:text-base">
                {current.overview}
              </p>
            )}
            <div className="flex flex-wrap items-center gap-3">
              <Link
                to={buildPlayHref(current, libraryId)}
                className="pill pill-primary transition-colors duration-[--duration-fast]"
              >
                <PrimaryActionIcon className="h-4 w-4 fill-current" />
                {primaryActionLabel}
              </Link>
              <Link
                to={`/item/${encodeURIComponent(current.content_id)}${
                  libraryId ? `?libraryId=${libraryId}` : ""
                }`}
                className="pill pill-glass transition-colors duration-[--duration-fast]"
              >
                <Info className="h-4 w-4" />
                More Info
              </Link>
            </div>
          </div>
        </div>
      </div>

      {/* Live region for screen readers */}
      <div aria-live="polite" className="sr-only">
        {slides[activeIndex]?.title}
      </div>

      {/* Navigation arrows. Visible on every breakpoint so users can always
          advance slides manually — important when `prefers-reduced-motion`
          disables autoplay, and for accessibility generally. On mobile they
          render smaller and slightly more transparent so they don't crowd
          narrow viewports. On lg+ they fade in on hover. */}
      {slides.length > 1 && (
        <>
          <button
            type="button"
            onClick={prev}
            className="glass-subtle absolute top-1/2 left-3 z-20 -translate-y-1/2 rounded-full p-1.5 opacity-60 transition-opacity duration-[--duration-fast] hover:opacity-100 sm:left-5 sm:p-2 sm:opacity-80 lg:opacity-0 lg:group-hover:opacity-90"
            aria-label="Previous slide"
          >
            <ChevronLeft className="size-4 sm:size-5" />
          </button>
          <button
            type="button"
            onClick={next}
            className="glass-subtle absolute top-1/2 right-3 z-20 -translate-y-1/2 rounded-full p-1.5 opacity-60 transition-opacity duration-[--duration-fast] hover:opacity-100 sm:right-5 sm:p-2 sm:opacity-80 lg:opacity-0 lg:group-hover:opacity-90"
            aria-label="Next slide"
          >
            <ChevronRight className="size-4 sm:size-5" />
          </button>

          {/* Editorial slide counter. On mobile we render a compact counter +
              pause toggle in the top-right corner so it never fights the CTA
              buttons at the bottom, while still letting users pause autoplay.
              On sm+ the full rail + pause control sits bottom-right.
              When `reserveHeaderSpace` is set, the hero bleeds under a sticky
              marquee header on top, so the counter matches the same `pt-24`
              offset the content uses to clear that header. */}
          <div
            className={cn(
              "absolute right-4 z-20 flex items-center gap-2 sm:hidden",
              reserveHeaderSpace ? "top-24" : "top-4",
            )}
          >
            <div className="hero-slide-counter" aria-hidden="true">
              <span className="num">{padded(activeIndex + 1)}</span>
              <span>/</span>
              <span>{padded(slideCount)}</span>
            </div>
            <button
              type="button"
              onClick={togglePause}
              aria-label={paused ? "Play slideshow" : "Pause slideshow"}
              className="glass-subtle flex h-7 w-7 items-center justify-center rounded-full text-white/80 transition-colors hover:text-white"
            >
              {paused ? <Play className="size-3" /> : <Pause className="size-3" />}
            </button>
          </div>
          <div className="absolute right-6 bottom-6 z-20 hidden items-center gap-3 sm:flex lg:right-10 lg:bottom-8 xl:right-12">
            <div className="hero-slide-counter" aria-hidden="true">
              <span className="num">{padded(activeIndex + 1)}</span>
              <span className="sep" />
              <span>{padded(slideCount)}</span>
            </div>
            <div
              className="hero-progress-rail"
              data-paused={paused ? "true" : "false"}
              aria-hidden="true"
            >
              {/* The key restarts the animation in lockstep with the
                  setInterval timer: on slide change, and on unpause (which
                  starts a fresh 8s cycle). Pausing mid-cycle freezes the bar
                  via `animation-play-state: paused` without remounting. */}
              <span key={`${activeIndex}-${playCycle}`} />
            </div>
            <button
              type="button"
              onClick={togglePause}
              aria-label={paused ? "Play slideshow" : "Pause slideshow"}
              className="flex h-7 w-7 items-center justify-center rounded-full text-white/70 transition-colors hover:text-white"
            >
              {paused ? <Play className="size-3.5" /> : <Pause className="size-3.5" />}
            </button>
          </div>
        </>
      )}
    </section>
  );
}
