import {
  lazy,
  memo,
  Suspense,
  useState,
  useEffect,
  useCallback,
  useMemo,
  useRef,
  type MouseEvent,
} from "react";
import { Link } from "react-router";
import { Info, ChevronLeft, ChevronRight, Play, Pause, BookOpen } from "lucide-react";
import { decodeThumbhash } from "@/lib/thumbhash";
import { HERO_BANNER_SIZE } from "@/lib/design-system";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { usePageActivity } from "@/hooks/usePageActivity";
import { usePrefersReducedMotion } from "@/hooks/usePrefersReducedMotion";
import { HeroOverviewText } from "./HeroOverviewText";
import { LocalErrorBoundary } from "./LocalErrorBoundary";
import { cn } from "@/lib/utils";
import type { SectionItem } from "@/api/types";
import { buildItemHref, buildMediaPlayHref } from "@/lib/mediaNavigation";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";
import { parseWatchHref } from "@/pages/watchRouteHelpers";
import { markPlaybackIntent } from "@/player/first-frame";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { isEpisodeStill, isEpisodeUnwatched } from "@/lib/episodeSpoilers";
import { RatingEntry } from "@/components/ratings/RatingEntry";
import MachineTranslatedLabel from "@/pages/ItemDetail/components/MachineTranslatedLabel";
import { formatHeroMetadata } from "./heroMetadata";
import { numberText, useAppLanguage, useT, type Translate } from "@/i18n";

// On-view translation for the visible slide loads after the hero itself, so
// it stays out of the launch bundle; the plain overview shows meanwhile.
const HeroOverview = lazy(() => import("./HeroOverview"));

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
  /**
   * Refetches the items while an on-view translation of the shown slide runs,
   * so the translated description replaces the original in place.
   */
  onRefreshItems?: () => void;
}

const HeroBackdropSlide = memo(function HeroBackdropSlide({
  slide,
  isActive,
  shouldLoad,
  hideImage,
}: {
  slide: SectionItem;
  isActive: boolean;
  shouldLoad: boolean;
  hideImage: boolean;
}) {
  const [loadedUrl, setLoadedUrl] = useState<string | null>(null);
  // Keep the browser's loaded image visible while a refreshed URL is pending.
  // Distant slides retain their loaded source until they become a neighbor.
  const imageUrl = shouldLoad ? slide.backdrop_url : loadedUrl;
  const thumbhash =
    !hideImage && slide.backdrop_thumbhash ? decodeThumbhash(slide.backdrop_thumbhash) : "";

  return (
    <div
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
      {slide.backdrop_url && imageUrl && (
        <img
          src={imageUrl}
          alt=""
          fetchPriority={isActive ? "high" : "low"}
          className={cn(
            "h-full w-full object-cover object-[center_20%] transition-opacity duration-(--duration-slow)",
            loadedUrl ? "opacity-100" : "opacity-0",
          )}
          style={{
            filter: `brightness(var(--hero-backdrop-brightness, 0.78)) saturate(var(--hero-backdrop-saturate, 0.95))${hideImage ? " blur(24px)" : ""}`,
          }}
          onLoad={() => setLoadedUrl(imageUrl)}
        />
      )}
    </div>
  );
});

function heroPlayLabel(
  t: Translate,
  item: SectionItem,
  activeAudiobookPlaying?: boolean | null,
): string {
  if (item.type === "ebook") {
    return t("web.home.hero.read");
  }
  if (item.type !== "audiobook") {
    return t("web.home.hero.play");
  }
  if (activeAudiobookPlaying === true) {
    return t("web.home.hero.pause");
  }
  if (activeAudiobookPlaying === false) {
    return t("web.home.hero.resume");
  }
  const position = item.position_seconds ?? 0;
  const duration = item.duration_seconds ?? 0;
  if (position > 0 && (duration <= 0 || position < duration)) {
    return t("web.home.hero.resume");
  }
  if (item.user_state?.played) {
    return t("web.home.hero.listenAgain");
  }
  return t("web.home.hero.listen");
}

export default function HeroBanner({
  items,
  maxSlides = 10,
  heightClassName = HERO_BANNER_SIZE,
  bleed = false,
  reserveHeaderSpace = false,
  libraryId,
  onRefreshItems,
}: HeroBannerProps) {
  const t = useT();
  // The details line comes from a plain helper; follow language changes.
  useAppLanguage();
  const spoilerPrefs = useEpisodeSpoilerPrefs();
  const slides = useMemo(() => items.slice(0, maxSlides), [items, maxSlides]);
  const pendingSlideIds = useMemo(
    () =>
      new Set(
        slides
          .filter((slide) => slide.pending_translation_language)
          .map((slide) => slide.content_id),
      ),
    [slides],
  );
  const [activeIndex, setActiveIndex] = useState(0);
  const { isVisible: pageVisible } = usePageActivity();
  const sectionRef = useRef<HTMLElement>(null);
  // Unknown until the observer first reports, so a hero mounted below the
  // fold preloads nothing; without IntersectionObserver it counts as shown.
  const [onScreen, setOnScreen] = useState(() => typeof IntersectionObserver === "undefined");
  // A hero scrolled out of view or in a background tab neither advances nor
  // downloads the next slide's backdrop.
  const shown = pageVisible && onScreen;
  const audiobookPlayback = useAudiobookPlaybackController();
  // Bumped whenever auto-advance restarts a fresh 8s cycle — after the slide
  // changes, or after the user unpauses. Used as part of the progress-rail key
  // so the CSS animation restarts in lockstep with the setInterval timer
  // (otherwise, resuming via animation-play-state desyncs: rail finishes
  // before the next slide advance, or vice-versa, depending on when the user
  // paused).
  const [playCycle, setPlayCycle] = useState(0);

  const next = useCallback(() => {
    setActiveIndex((current) => (current + 1) % slides.length);
  }, [slides.length]);

  const prev = useCallback(() => {
    setActiveIndex((current) => (current - 1 + slides.length) % slides.length);
  }, [slides.length]);

  // Two separate reasons stop the slideshow. Hovering or focusing the hero
  // holds it only while the pointer or focus stays inside; the Pause/Play
  // button records the user's choice, which hover and focus never change.
  // Under reduced motion the slideshow starts stopped and runs only after the
  // user presses Play.
  const reduceMotion = usePrefersReducedMotion();
  const [held, setHeld] = useState(false);
  const [choice, setChoice] = useState<"play" | "pause" | null>(null);
  const wantsPlay = choice === "play" || (choice === null && !reduceMotion);
  // What actually happens: drives the timer and the progress rail.
  const autoplaying = wantsPlay && !held;
  const hold = useCallback(() => setHeld(true), []);
  // Starting to play bumps playCycle so the rail animation restarts at
  // scaleX(0) in sync with the fresh 8s interval started below.
  const release = useCallback(() => {
    if (held && wantsPlay) setPlayCycle((c) => c + 1);
    setHeld(false);
  }, [held, wantsPlay]);
  const togglePause = useCallback(() => {
    if (wantsPlay) {
      setChoice("pause");
      return;
    }
    // Pressing Play starts the slideshow now, even under the pointer.
    setChoice("play");
    setHeld(false);
    setPlayCycle((c) => c + 1);
  }, [wantsPlay]);

  // `activeIndex` is in the dep list so that manual navigation (prev/next
  // chevrons) also resets the 8s countdown — otherwise after clicking next at
  // t=5s the auto-advance would still fire at t=8 instead of t=13, desyncing
  // with the progress rail that restarts on slide change.
  useEffect(() => {
    if (slides.length <= 1 || !autoplaying || !shown) return;
    const timer = setInterval(next, 8000);
    return () => clearInterval(timer);
  }, [next, slides.length, autoplaying, shown, activeIndex]);

  useEffect(() => {
    const section = sectionRef.current;
    if (!section || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(([entry]) => {
      if (entry) setOnScreen(entry.isIntersecting);
    });
    observer.observe(section);
    return () => observer.disconnect();
  }, [slides.length]);

  const current = slides[activeIndex] ?? slides[0];
  useAmbientColor(current?.backdrop_thumbhash);
  if (slides.length === 0) return null;
  if (!current) return null;
  const overviewShown =
    Boolean(current.overview) &&
    !(
      spoilerPrefs.hideOverviews &&
      current.type === "episode" &&
      isEpisodeUnwatched({
        played: current.user_state?.played,
        position_seconds: current.position_seconds,
      })
    );
  const overviewFallback = overviewShown ? (
    <div className="mb-7 max-w-2xl">
      <HeroOverviewText overview={current.overview} />
      {current.machine_translated_fields?.includes("overview") && (
        <MachineTranslatedLabel className="mt-1.5" />
      )}
    </div>
  ) : null;

  const metadata = formatHeroMetadata(current);

  const slideCount = slides.length;
  const padded = (n: number) => numberText(String(n).padStart(2, "0"));
  const activeAudiobookPlaying =
    current.type === "audiobook" && audiobookPlayback?.active?.contentId === current.content_id
      ? audiobookPlayback.active.playing
      : null;
  const playLabel = heroPlayLabel(t, current, activeAudiobookPlaying);
  const playHref = buildMediaPlayHref({
    contentId: current.content_id,
    type: current.type,
    libraryId,
  });

  // Previous, play/pause and next share one dark pill, in the order media
  // players use. Each button is 44px wide and its hit area 44px tall. Below
  // lg, the scroll margin brings a focused button out from under the sticky
  // header.
  const slideButtonClass =
    "relative flex h-8 w-11 scroll-mt-24 items-center justify-center rounded-full text-white/85 transition-colors after:absolute after:inset-x-0 after:-inset-y-1.5 after:content-[''] hover:text-white lg:scroll-mt-0";

  const handlePlayClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (activeAudiobookPlaying == null) {
      // This Play link navigates without the playback controller, so it
      // starts the first-frame clock itself. A modified or non-primary click
      // opens another tab, which this tab's clock cannot time.
      const opensHere =
        event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
      const request = opensHere ? parseWatchHref(playHref) : null;
      if (request) markPlaybackIntent(request.requestKey);
      return;
    }
    event.preventDefault();
    audiobookPlayback?.toggleActivePlayback();
  };

  return (
    <section
      ref={sectionRef}
      className={cn(
        "home-hero relative flex w-full flex-col overflow-hidden",
        heightClassName,
        bleed ? "-mt-[96px] mb-0 sm:-mt-[104px]" : "border-border/60 mb-10 border-b",
      )}
      aria-roledescription="carousel"
      aria-label={t("web.home.hero.carouselLabel")}
      onMouseEnter={hold}
      onMouseLeave={release}
      onFocus={hold}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget)) release();
      }}
    >
      {/* Preload the slide that comes next while the hero is shown; retain
          loaded backdrops for crossfades. Going back fades in from the
          thumbhash. */}
      {slides.map((slide, i) => (
        <HeroBackdropSlide
          key={slide.content_id ?? i}
          slide={slide}
          hideImage={
            spoilerPrefs.hideImages &&
            slide.type === "episode" &&
            isEpisodeStill(slide.backdrop_is_episode_still) &&
            isEpisodeUnwatched({
              played: slide.user_state?.played,
              position_seconds: slide.position_seconds,
            })
          }
          isActive={i === activeIndex}
          shouldLoad={i === activeIndex || (shown && i === (activeIndex + 1) % slideCount)}
        />
      ))}

      {/* Gradient overlays */}
      {bleed && <div className="hero-top-scrim" />}
      <div className={bleed ? "hero-gradient-strong" : "hero-gradient"} />
      <div className="ambient-glow" />
      <div className="hero-gradient-left" />
      {!bleed && <div className="hero-vignette" />}

      {/* Content */}
      <div
        className={cn(
          "relative z-10 flex flex-1 items-end px-4 pb-10 sm:px-6 sm:pb-12 lg:px-10 lg:pb-16 xl:px-12",
          // A tall title block grows the hero rather than overflowing it;
          // the top padding keeps the title off the top edge (and, below
          // sm, clear of the slide controls) when that happens.
          reserveHeaderSpace
            ? cn(slides.length > 1 ? "pt-36" : "pt-24", "sm:pt-28 lg:pt-32")
            : "pt-16",
        )}
      >
        <div className="w-full max-w-[1380px]">
          <div className="max-w-3xl">
            <h1
              key={current.content_id}
              className="bidi-text font-display mb-4 max-w-4xl text-4xl font-extrabold tracking-[-0.04em] text-balance sm:text-5xl lg:text-6xl xl:text-7xl"
              style={{ textShadow: "var(--hero-text-shadow, none)" }}
            >
              {current.title}
            </h1>
            {metadata.length > 0 && (
              <div className="hero-meta-track mb-5 text-white/85">
                {metadata.map((entry) =>
                  entry.rating ? (
                    <RatingEntry key={entry.key} rating={entry.rating} size="sm" />
                  ) : (
                    <span key={entry.key}>{entry.label}</span>
                  ),
                )}
              </div>
            )}
            {/* The plain overview stands in while the chunk loads, and for good
              if it fails to load. */}
            <LocalErrorBoundary fallback={overviewFallback}>
              <Suspense fallback={overviewFallback}>
                <HeroOverview
                  item={overviewShown ? current : undefined}
                  pendingContentIds={pendingSlideIds}
                  onRefreshItems={onRefreshItems}
                />
              </Suspense>
            </LocalErrorBoundary>
            <div className="flex flex-wrap items-center gap-3">
              <Link
                to={playHref}
                onClick={handlePlayClick}
                className="pill pill-primary transition-colors duration-(--duration-fast)"
              >
                {current.type === "ebook" ? (
                  <BookOpen className="h-4 w-4" />
                ) : activeAudiobookPlaying ? (
                  <Pause className="h-4 w-4 fill-current" />
                ) : (
                  <Play className="h-4 w-4 fill-current" />
                )}
                {playLabel}
              </Link>
              <ViewTransitionLink
                to={buildItemHref({ contentId: current.content_id, libraryId })}
                className="pill pill-glass transition-colors duration-(--duration-fast)"
              >
                <Info className="h-4 w-4" />
                {t("web.home.hero.moreInfo")}
              </ViewTransitionLink>
            </div>
          </div>
        </div>
      </div>

      {/* Live region for screen readers */}
      <div aria-live="polite" className="sr-only">
        {slides[activeIndex]?.title}
      </div>

      {/* Slide controls. They sit in a band the content's padding keeps
          empty, so the title, details and buttons never run under them:
          below sm in the top corner, inside the top padding (lower when
          `reserveHeaderSpace` leaves room for the marquee header); on sm+
          in the bottom corner, inside the bottom padding. One set of
          buttons serves every width, so focus survives a resize. */}
      {slides.length > 1 && (
        <div
          className={cn(
            "absolute end-4 z-20 flex items-center gap-3 sm:end-6 sm:top-auto sm:bottom-2 lg:end-10 lg:bottom-6 xl:end-12",
            reserveHeaderSpace ? "top-24" : "top-4",
          )}
        >
          <div className="hero-slide-counter hidden sm:inline-flex" aria-hidden="true">
            <span className="num">{padded(activeIndex + 1)}</span>
            <span className="sep" />
            <span>{padded(slideCount)}</span>
          </div>
          <div
            className="hero-progress-rail hidden w-20 sm:block md:w-36"
            data-paused={autoplaying ? "false" : "true"}
            aria-hidden="true"
          >
            {/* The key restarts the animation in lockstep with the
                  setInterval timer: on slide change, and on unpause (which
                  starts a fresh 8s cycle). Pausing mid-cycle freezes the bar
                  via `animation-play-state: paused` without remounting. */}
            <span key={`${activeIndex}-${playCycle}`} />
          </div>
          {/* On phones the counter rides in the pill, which backs it over
                bright artwork. */}
          <div className="flex items-center rounded-full border border-white/10 bg-black/60 backdrop-blur-md">
            <div
              className="hero-slide-counter ps-3.5 pe-1 text-white/85 sm:hidden"
              aria-hidden="true"
            >
              <span className="num">{padded(activeIndex + 1)}</span>
              {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- a separator glyph, not text */}
              <span>/</span>
              <span>{padded(slideCount)}</span>
            </div>
            <button
              type="button"
              onClick={prev}
              aria-label={t("web.home.hero.previousSlide")}
              className={slideButtonClass}
            >
              <ChevronLeft className="size-4" />
            </button>
            <button
              type="button"
              onClick={togglePause}
              aria-label={
                wantsPlay ? t("web.home.hero.pauseSlideshow") : t("web.home.hero.playSlideshow")
              }
              className={slideButtonClass}
            >
              {wantsPlay ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
            </button>
            <button
              type="button"
              onClick={next}
              aria-label={t("web.home.hero.nextSlide")}
              className={slideButtonClass}
            >
              <ChevronRight className="size-4" />
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
