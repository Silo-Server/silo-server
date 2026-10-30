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
import { Info, ChevronLeft, ChevronRight, Play, Pause, BookOpen, Volume2, VolumeX } from "lucide-react";
import { decodeThumbhash } from "@/lib/thumbhash";
import { HERO_BANNER_SIZE } from "@/lib/design-system";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useHeroTrailersEnabled } from "@/hooks/useHeroTrailersEnabled";
import { useShowTitleArt } from "@/hooks/useTitleArt";
import { HeroOverviewText } from "./HeroOverviewText";
import { heroClipUrl } from "./heroClips";
import HeroTrailerLayer from "./HeroTrailerLayer";
import {
  HERO_TRAILER_BLACK_MS,
  HERO_TRAILER_STILL_HOLD_MS,
  heroTrailerEnabled,
  heroTrailerStage,
} from "./heroTrailer";
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
import { formatHeroMetadata } from "./heroMetadata";

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

/**
 * Alternating slow pan/zoom for consecutive hero slides.
 *
 * These must be written out literally. Tailwind only keeps a theme variable
 * whose name it can find in the scanned source, and assembling the name from a
 * template literal hid both from it — so the real values were tree-shaken out
 * of the bundle and `var(--animate-ken-burns-a)` resolved to nothing at
 * runtime. app.css still overrides both to `none` under
 * `prefers-reduced-motion`, which is what keeps this accessible.
 */
const KEN_BURNS_ANIMATIONS = ["var(--animate-ken-burns-a)", "var(--animate-ken-burns-b)"] as const;
const HERO_BACKDROP_FADE_MS = 1000;

function shouldKeepOutgoingBackdropMotion(): boolean {
  return !window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

const HeroBackdropSlide = memo(function HeroBackdropSlide({
  slide,
  index,
  isActive,
  keepsMotion,
  shouldLoad,
  hideImage,
  faded,
}: {
  slide: SectionItem;
  index: number;
  isActive: boolean;
  keepsMotion: boolean;
  shouldLoad: boolean;
  hideImage: boolean;
  /**
   * The slide's clip is taking over, so its still steps aside for the fade to
   * black. Kept mounted rather than unmounted, so it can come straight back if
   * the clip fails.
   */
  faded?: boolean;
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
      className={`bg-muted absolute inset-0 transition-opacity duration-1000 ease-in-out ${isActive && !faded ? "opacity-100" : "opacity-0"}`}
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
            isActive && "will-change-transform",
            loadedUrl ? "opacity-100" : "opacity-0",
          )}
          style={{
            animation: keepsMotion
              ? KEN_BURNS_ANIMATIONS[index % KEN_BURNS_ANIMATIONS.length]
              : "none",
            filter: `brightness(var(--hero-backdrop-brightness, 0.78)) saturate(var(--hero-backdrop-saturate, 0.95))${hideImage ? " blur(24px)" : ""}`,
          }}
          onLoad={() => setLoadedUrl(imageUrl)}
        />
      )}
    </div>
  );
});

function heroPlayLabel(item: SectionItem, activeAudiobookPlaying?: boolean | null): string {
  if (item.type === "ebook") {
    return "Read";
  }
  if (item.type !== "audiobook") {
    return "Play";
  }
  if (activeAudiobookPlaying === true) {
    return "Pause";
  }
  if (activeAudiobookPlaying === false) {
    return "Resume";
  }
  const position = item.position_seconds ?? 0;
  const duration = item.duration_seconds ?? 0;
  if (position > 0 && (duration <= 0 || position < duration)) {
    return "Resume";
  }
  if (item.user_state?.played) {
    return "Listen Again";
  }
  return "Listen";
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
  const [{ activeIndex, outgoingIndex }, setBackdropState] = useState({
    activeIndex: 0,
    outgoingIndex: null as number | null,
  });
  const [paused, setPaused] = useState(false);
  const audiobookPlayback = useAudiobookPlaybackController();
  // Bumped whenever auto-advance restarts a fresh 8s cycle — after the slide
  // changes, or after the user unpauses. Used as part of the progress-rail key
  // so the CSS animation restarts in lockstep with the setInterval timer
  // (otherwise, resuming via animation-play-state desyncs: rail finishes
  // before the next slide advance, or vice-versa, depending on when the user
  // paused).
  const [playCycle, setPlayCycle] = useState(0);

  const next = useCallback(() => {
    setBackdropState(({ activeIndex: current }) => ({
      activeIndex: (current + 1) % slides.length,
      outgoingIndex: shouldKeepOutgoingBackdropMotion() ? current : null,
    }));
  }, [slides.length]);

  const prev = useCallback(() => {
    setBackdropState(({ activeIndex: current }) => ({
      activeIndex: (current - 1 + slides.length) % slides.length,
      outgoingIndex: shouldKeepOutgoingBackdropMotion() ? current : null,
    }));
  }, [slides.length]);

  useEffect(() => {
    if (outgoingIndex === null) return;
    const timer = window.setTimeout(() => {
      setBackdropState((current) =>
        current.outgoingIndex === outgoingIndex ? { ...current, outgoingIndex: null } : current,
      );
    }, HERO_BACKDROP_FADE_MS);
    return () => window.clearTimeout(timer);
  }, [outgoingIndex]);

  /*
   * Pointer and keyboard focus hold the carousel where it is, so a slide cannot
   * change under someone who is reading it. That is deliberately separate from
   * the pause control: a hover that stopped a two-minute clip would look like a
   * frozen hero, so the clip ignores it and only the control stops it.
   */
  const [hoverPaused, setHoverPaused] = useState(false);
  const holdSlideshow = useCallback(() => setHoverPaused(true), []);
  const releaseSlideshow = useCallback(() => {
    setHoverPaused((prev) => {
      if (prev) setPlayCycle((c) => c + 1);
      return false;
    });
  }, []);

  // The pause control. Unpausing bumps playCycle so the rail animation restarts
  // at scaleX(0) in sync with the fresh 8s interval started below.
  const togglePause = useCallback(() => {
    setPaused((prev) => {
      if (prev) setPlayCycle((c) => c + 1);
      return !prev;
    });
  }, []);

  const current = slides[activeIndex] ?? slides[0];

  /*
   * Title art. The hero names a title the way the rest of the app does, on the
   * same `ui.title_art` choice the title pages read. Until the profile's answer
   * is known the logo's box is reserved rather than flashing text, and a logo
   * that fails to load falls back to the text for that slide.
   */
  const showTitleArt = useShowTitleArt();
  const [logoFailed, setLogoFailed] = useState(false);
  /* ── Hero trailer ─────────────────────────────────────────────────────────
   * The active slide's clip, when it has one: the still holds, fades out to
   * black, and the muted clip plays at its own length, handing the carousel on
   * when it ends. `./heroTrailer` owns the gates and which layer is on screen;
   * this only runs the timers and the element.
   *
   * Every step is opt-outable on purpose. A clip that is slow, blocked or
   * missing never leaves the backdrop still, so the hero is never a black
   * screen and never a spinner.
   */
  const heroRef = useRef<HTMLElement | null>(null);
  const [clipHeld, setClipHeld] = useState(false);
  const [clipReady, setClipReady] = useState(false);
  const [clipFadeDone, setClipFadeDone] = useState(false);
  const [clipFailed, setClipFailed] = useState(false);
  const [clipProgress, setClipProgress] = useState(0);
  /** The slide the sequence above belongs to; a new slide starts it over. */
  const [clipSeededId, setClipSeededId] = useState<string | null>(null);
  const [heroOnScreen, setHeroOnScreen] = useState(true);
  const [tabVisible, setTabVisible] = useState(true);
  /*
   * Both media settings start as "backdrop only" and are corrected on the
   * client, so neither the server's markup nor the first paint can be a clip.
   */
  const [reducedMotion, setReducedMotion] = useState(true);
  const [saveData, setSaveData] = useState(true);
  /** Muted unless the viewer says otherwise; see `handleClipMuteToggle`. */
  const [clipMuted, setClipMuted] = useState(true);
  const clipVideoRef = useRef<HTMLVideoElement | null>(null);

  const heroTrailersEnabled = useHeroTrailersEnabled();
  const clipUrl = current ? heroClipUrl(current) : undefined;
  // Two gates decide whether a slide plays its clip: this profile asked for
  // hero trailers, and the slide and device allow one. The setting's first read
  // is undefined, so the hero holds its backdrop until the answer arrives.
  const clipEnabled =
    heroTrailersEnabled === true &&
    heroTrailerEnabled({
      hasClip: Boolean(clipUrl) && !clipFailed,
      reducedMotion,
      saveData,
    });
  // Which layer is on screen. Derived, so a clip that cannot play can never
  // leave the still and the fade cannot start before the clip is ready.
  const clipStage = heroTrailerStage({
    enabled: clipEnabled,
    held: clipHeld,
    ready: clipReady,
    faded: clipFadeDone,
  });
  // The pause control stops the clip too; a pointer hovering the hero does not.
  const clipPlaying = clipEnabled && clipStage === "video" && heroOnScreen && tabVisible && !paused;

  /*
   * A new slide starts over — the still again, nothing carried in from the last
   * one. React's "adjust state when a prop changes" pattern: one render instead
   * of two, and no flash of the previous slide's clip.
   */
  if (current && clipSeededId !== current.content_id) {
    setClipSeededId(current.content_id);
    setClipFadeDone(false);
    setClipHeld(false);
    setClipReady(false);
    setClipFailed(false);
    setClipProgress(0);
    // Sound is a per-slide choice: the next slide starts muted, which is also
    // the only state a clip is allowed to autoplay in.
    setClipMuted(true);
    // A logo that failed on the last slide says nothing about this one.
    setLogoFailed(false);
  }

  /* Read once on the client, and again whenever the media query changes. */
  useEffect(() => {
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const connection = (navigator as Navigator & { connection?: { saveData?: boolean } })
      .connection;

    const sync = () => {
      setReducedMotion(motion.matches);
      setSaveData(Boolean(connection?.saveData));
    };

    sync();
    motion.addEventListener("change", sync);

    return () => motion.removeEventListener("change", sync);
  }, []);

  /* Hold the still before the clip may be revealed. */
  useEffect(() => {
    if (!clipEnabled || clipHeld) {
      return;
    }

    const timer = window.setTimeout(() => setClipHeld(true), HERO_TRAILER_STILL_HOLD_MS);

    return () => window.clearTimeout(timer);
  }, [clipEnabled, clipHeld]);

  /*
   * The hold is over and the clip is ready, so the backdrop fades out to black
   * and the clip fades in behind it. Both are timer callbacks — nothing on the
   * render path sets state, so there is no cascading render to pay for.
   */
  useEffect(() => {
    if (!clipEnabled || !clipHeld || !clipReady || clipFadeDone) {
      return;
    }

    const timer = window.setTimeout(() => setClipFadeDone(true), HERO_TRAILER_BLACK_MS);

    return () => window.clearTimeout(timer);
  }, [clipEnabled, clipHeld, clipReady, clipFadeDone]);

  /* Nothing plays off screen: pause when the tab is hidden or the hero leaves view. */
  useEffect(() => {
    const onVisibilityChange = () => setTabVisible(!document.hidden);

    onVisibilityChange();
    document.addEventListener("visibilitychange", onVisibilityChange);

    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, []);

  useEffect(() => {
    const hero = heroRef.current;

    // Older browsers — and jsdom — have no observer: the hero simply keeps playing.
    if (!hero || typeof IntersectionObserver === "undefined") {
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          setHeroOnScreen(entry.isIntersecting);
        }
      },
      { threshold: 0.15 },
    );

    observer.observe(hero);

    return () => observer.disconnect();
  }, []);

  const handleClipReady = useCallback(() => setClipReady(true), []);
  const handleClipUnavailable = useCallback(() => setClipFailed(true), []);
  const handleClipProgress = useCallback((fraction: number) => setClipProgress(fraction), []);

  /*
   * The sound control. Unmuting is a gesture, so it happens here rather than in
   * an effect, and the element is asked rather than the state — a slide change
   * mounts a new element, and the two must never be allowed to disagree.
   */
  const handleClipMuteToggle = useCallback(() => {
    const video = clipVideoRef.current;
    const next = video ? !video.muted : !clipMuted;

    if (video) {
      video.muted = next;

      // Safari will not make a clip audible that began muted unless the element
      // is played again inside the gesture that asked for sound.
      if (!next) {
        const started = video.play();

        if (started && typeof started.catch === "function") {
          void started.catch(() => undefined);
        }
      }
    }

    setClipMuted(next);
  }, [clipMuted]);

  /*
   * The clip ends: its own length is the slide's length. With another slide to
   * show, the carousel moves on; on its own it starts the sequence again, which
   * `HeroTrailerLayer` rewinds the element for.
   */
  const handleClipEnded = useCallback(() => {
    setClipFadeDone(false);
    setClipHeld(false);
    setClipProgress(0);

    if (slides.length > 1) {
      next();
    }
  }, [next, slides.length]);

  // `activeIndex` is in the dep list so that manual navigation (prev/next
  // chevrons) also resets the 8s countdown — otherwise after clicking next at
  // t=5s the auto-advance would still fire at t=8 instead of t=13, desyncing
  // with the progress rail that restarts on slide change. A slide with a clip
  // sets its own pace: the clip's end moves the carousel on, not a timer.
  useEffect(() => {
    if (slides.length <= 1 || paused || hoverPaused || clipEnabled) return;
    const prefersReducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (prefersReducedMotion) return;
    const timer = setInterval(next, 8000);
    return () => clearInterval(timer);
  }, [next, slides.length, paused, hoverPaused, activeIndex, clipEnabled]);

  useAmbientColor(current?.backdrop_thumbhash);
  if (slides.length === 0) return null;
  if (!current) return null;
  /*
   * The logo is resolved here, past the guard above, because it reads the slide:
   * the art when the profile's `ui.title_art` allows it, the box held while that
   * answer is still unknown, and the text when the art is off or failed to load.
   */
  const logoUrl = showTitleArt && current.logo_url && !logoFailed ? current.logo_url : undefined;
  const holdTitle = showTitleArt === undefined && Boolean(current.logo_url) && !logoFailed;
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

  const metadata = formatHeroMetadata(current);

  const slideCount = slides.length;
  const padded = (n: number) => String(n).padStart(2, "0");
  const activeAudiobookPlaying =
    current.type === "audiobook" && audiobookPlayback?.active?.contentId === current.content_id
      ? audiobookPlayback.active.playing
      : null;
  const playLabel = heroPlayLabel(current, activeAudiobookPlaying);
  const playHref = buildMediaPlayHref({
    contentId: current.content_id,
    type: current.type,
    libraryId,
  });

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
      ref={heroRef}
      className={cn(
        "home-hero group relative w-full overflow-hidden",
        heightClassName,
        bleed ? "-mt-[96px] mb-0 sm:-mt-[104px]" : "border-border/60 mb-10 border-b",
      )}
      aria-roledescription="carousel"
      aria-label="Featured content"
      onMouseEnter={holdSlideshow}
      onMouseLeave={releaseSlideshow}
      onFocus={holdSlideshow}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget)) releaseSlideshow();
      }}
    >
      {/* Preload neighbors in both directions; retain loaded backdrops for crossfades. */}
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
          index={i}
          isActive={i === activeIndex}
          faded={i === activeIndex && clipEnabled && clipStage !== "still"}
          keepsMotion={i === activeIndex || i === outgoingIndex}
          shouldLoad={
            i === activeIndex ||
            i === outgoingIndex ||
            i === (activeIndex + 1) % slideCount ||
            i === (activeIndex - 1 + slideCount) % slideCount
          }
        />
      ))}

      {/* Hero trailer: the black the still fades through, then the clip. Both
          sit under the scrims, so the title block keeps its contrast. */}
      {clipEnabled && (
        <div
          className="hero-trailer-black"
          data-visible={clipStage === "still" ? "false" : "true"}
          style={{ transitionDuration: `${HERO_TRAILER_BLACK_MS}ms` }}
        />
      )}
      {clipEnabled && clipUrl && (
        <HeroTrailerLayer
          key={current.content_id}
          videoRef={clipVideoRef}
          clipUrl={clipUrl}
          stage={clipStage}
          muted={clipMuted}
          playing={clipPlaying}
          onReady={handleClipReady}
          onProgress={handleClipProgress}
          onEnded={handleClipEnded}
          onUnavailable={handleClipUnavailable}
        />
      )}

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
            {logoUrl ? (
              <>
                <h1 className="sr-only">{current.title}</h1>
                <img
                  src={logoUrl}
                  alt=""
                  decoding="async"
                  onError={() => setLogoFailed(true)}
                  className="mb-4 h-16 w-full max-w-[300px] object-contain object-left sm:h-20 sm:max-w-[360px] lg:h-24 lg:max-w-[420px] xl:h-28 xl:max-w-[480px]"
                />
              </>
            ) : holdTitle ? (
              <>
                <h1 className="sr-only">{current.title}</h1>
                <div
                  aria-hidden="true"
                  data-testid="hero-title-pending"
                  className="mb-4 h-16 w-full max-w-[300px] sm:h-20 sm:max-w-[360px] lg:h-24 lg:max-w-[420px] xl:h-28 xl:max-w-[480px]"
                />
              </>
            ) : (
              <h1
                key={current.content_id}
                className="font-display mb-4 max-w-4xl text-4xl font-extrabold tracking-[-0.04em] text-balance sm:text-5xl lg:text-6xl xl:text-7xl"
                style={{ textShadow: "var(--hero-text-shadow, none)" }}
              >
                {current.title}
              </h1>
            )}
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
            <Suspense
              fallback={
                overviewShown ? (
                  <div className="mb-7 max-w-2xl">
                    <HeroOverviewText overview={current.overview} />
                  </div>
                ) : null
              }
            >
              <HeroOverview
                item={overviewShown ? current : undefined}
                pendingContentIds={pendingSlideIds}
                onRefreshItems={onRefreshItems}
              />
            </Suspense>
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
                More Info
              </ViewTransitionLink>
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
      {(slides.length > 1 || clipEnabled) && (
        <>
          <button
            type="button"
            onClick={prev}
            className="glass-subtle absolute top-1/2 left-3 z-20 -translate-y-1/2 rounded-full p-1.5 opacity-60 transition-opacity duration-(--duration-fast) hover:opacity-100 sm:left-5 sm:p-2 sm:opacity-80 lg:opacity-0 lg:group-hover:opacity-90"
            aria-label="Previous slide"
          >
            <ChevronLeft className="size-4 sm:size-5" />
          </button>
          <button
            type="button"
            onClick={next}
            className="glass-subtle absolute top-1/2 right-3 z-20 -translate-y-1/2 rounded-full p-1.5 opacity-60 transition-opacity duration-(--duration-fast) hover:opacity-100 sm:right-5 sm:p-2 sm:opacity-80 lg:opacity-0 lg:group-hover:opacity-90"
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
              onClick={handleClipMuteToggle}
              aria-label={clipMuted ? "Unmute trailer" : "Mute trailer"}
              className="glass-subtle flex h-7 w-7 items-center justify-center rounded-full text-white/80 transition-colors hover:text-white"
            >
              {clipMuted ? <VolumeX className="size-3" /> : <Volume2 className="size-3" />}
            </button>
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
              data-paused={paused || hoverPaused ? "true" : "false"}
              data-clip={clipEnabled ? "true" : "false"}
              aria-hidden="true"
            >
              {/* The key restarts the animation in lockstep with the
                  setInterval timer: on slide change, and on unpause (which
                  starts a fresh 8s cycle). Pausing mid-cycle freezes the bar
                  via `animation-play-state: paused` without remounting. A
                  slide with a clip has no timer to restart: the bar reports
                  the clip's own position instead (see data-clip in app.css). */}
              {clipEnabled ? (
                <span style={{ width: `${Math.round(clipProgress * 100)}%` }} />
              ) : (
                <span key={`${activeIndex}-${playCycle}`} />
              )}
            </div>
            <button
              type="button"
              onClick={handleClipMuteToggle}
              aria-label={clipMuted ? "Unmute trailer" : "Mute trailer"}
              className="flex h-7 w-7 items-center justify-center rounded-full text-white/70 transition-colors hover:text-white"
            >
              {clipMuted ? <VolumeX className="size-3.5" /> : <Volume2 className="size-3.5" />}
            </button>
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
