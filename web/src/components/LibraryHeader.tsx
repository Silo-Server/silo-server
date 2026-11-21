import { useEffect, useState } from "react";
import { Tabs as TabsPrimitive } from "radix-ui";
import { Shuffle } from "lucide-react";
import { edgeMask, useScrollStrip } from "@/hooks/useScrollStrip";
import { useT, type MessageKey } from "@/i18n";
import { hasArabicScript } from "@/lib/direction";
import { cn } from "@/lib/utils";
import { isAudiobookLibraryType } from "@/pages/libraryPageSearchParams";

type LibraryTab = "recommended" | "library" | "collections";

interface LibraryHeaderProps {
  libraryName: string;
  /** Library type ("movies", "series", "audiobooks", ...); drives tab labels. */
  libraryType?: string;
  /**
   * When true, the header renders transparently to sit over a hero backdrop,
   * and switches to a glass surface once the user scrolls past a threshold.
   */
  overlay?: boolean;
  /** The selected tab, kept in view when the tabs scroll sideways. */
  activeTab: LibraryTab;
  availableTabs?: readonly LibraryTab[];
  /** Starts a shuffle of the whole library; omitted where nothing can shuffle. */
  onShuffle?: () => void;
  shuffleDisabled?: boolean;
}

const DEFAULT_TABS: readonly LibraryTab[] = ["recommended", "library", "collections"];

const TAB_LABELS: Record<LibraryTab, MessageKey> = {
  recommended: "web.library.header.recommendedTab",
  library: "web.library.header.libraryTab",
  collections: "web.library.header.collectionsTab",
};

// Audiobook libraries open on a resume-first deck rather than a discovery
// feed, so "Recommended" would mislabel what the tab actually shows.
const AUDIOBOOK_TAB_LABELS: Record<LibraryTab, MessageKey> = {
  ...TAB_LABELS,
  recommended: "web.library.header.homeTab",
};

const SELECTED_TAB = '[role="tab"][data-state="active"]';

/** Scroll distance (in px) at which an overlay header switches to glass. */
const GLASS_THRESHOLD_PX = 160;

export default function LibraryHeader({
  libraryName,
  libraryType = "",
  overlay = false,
  activeTab,
  availableTabs = DEFAULT_TABS,
  onShuffle,
  shuffleDisabled = false,
}: LibraryHeaderProps) {
  const t = useT();
  const tabLabels = isAudiobookLibraryType(libraryType) ? AUDIOBOOK_TAB_LABELS : TAB_LABELS;
  const [pastThreshold, setPastThreshold] = useState(false);
  // The active tab stops clear of the edge fade, which the strip pins to 24px
  // so the two match at any text size.
  const {
    ref: tabStrip,
    edges,
    measure,
    reveal,
  } = useScrollStrip<HTMLDivElement>(SELECTED_TAB, activeTab, 24);

  // A selection that changes while focus sits on another tab (browser Back,
  // say) takes focus along, so focus never stays on a tab scrolled out of view.
  useEffect(() => {
    const strip = tabStrip.current;
    const focused = document.activeElement;
    if (!strip || !(focused instanceof HTMLElement) || !strip.contains(focused)) return;
    const selected = strip.querySelector<HTMLElement>(SELECTED_TAB);
    if (selected && selected !== focused) selected.focus({ preventScroll: true });
  }, [activeTab, tabStrip]);

  useEffect(() => {
    if (!overlay) return;
    const update = () => {
      setPastThreshold(window.scrollY > GLASS_THRESHOLD_PX);
    };
    update();
    window.addEventListener("scroll", update, { passive: true });
    return () => window.removeEventListener("scroll", update);
  }, [overlay]);

  const scrolled = overlay && pastThreshold;

  return (
    <header
      data-sticky-header
      // The eyebrow carries its own spacing (me-6), so when it hides it leaves
      // no gap behind for the tabs to lose.
      className={cn("library-marquee-header gap-0", overlay && "is-overlay")}
      data-scrolled={overlay ? (scrolled ? "true" : "false") : undefined}
    >
      {/* Eyebrow is hidden on mobile — the top nav already identifies the
          current page, and at narrow widths it would either crowd out the
          tab pills or truncate to "LIBRAR…". From sm up it takes the room the
          controls leave, so it gives way before the tabs have to scroll: first
          the "Library /" prefix goes, so the library name stays readable, then
          the whole eyebrow. */}
      <div className="@container hidden min-w-0 flex-1 items-center gap-4 sm:flex">
        <p className="hero-eyebrow me-6 truncate @max-[7.5rem]:hidden">
          <span className="@max-[15.5rem]:hidden">{t("web.library.header.eyebrow")}</span>
          {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- a divider glyph, not text */}
          <span className="hero-eyebrow-divider @max-[15.5rem]:hidden">/</span>
          <span
            className="hero-eyebrow-strong"
            data-cursive={hasArabicScript(libraryName) || undefined}
          >
            {libraryName}
          </span>
        </p>
      </div>
      <div className="flex min-w-0 items-center gap-2">
        {onShuffle && (
          // Drawn as a one-button tab bar so it matches the tabs beside it.
          <div className="marquee-tab-bar shrink-0">
            <button
              type="button"
              className="marquee-tab-trigger gap-1.5 disabled:cursor-default disabled:opacity-60"
              onClick={onShuffle}
              disabled={shuffleDisabled}
              title={t("web.library.header.shuffle")}
              aria-label={t("web.library.header.shuffle")}
            >
              <Shuffle className="size-4" aria-hidden="true" />
              <span className="hidden sm:inline">{t("web.library.header.shuffle")}</span>
            </button>
          </div>
        )}
        {/* Tabs that don't fit (phones, longer translations) scroll sideways
            inside the pill instead of running off the screen. The fade sits on
            the inner strip rather than around the pill: a masked ancestor would
            stop the pill's backdrop blur from reaching the hero behind it. The
            tab list sits inside the strip so it holds its tabs directly. */}
        <div className="marquee-tab-bar min-w-0 p-0">
          <div
            ref={tabStrip}
            // Firefox would make the scrolling strip its own Tab stop; the
            // arrow keys already move between the tabs.
            tabIndex={-1}
            onScroll={measure}
            onFocus={(event) => {
              // Tabbing back in lands on the selected tab, which a hand scroll
              // may have left under the fade. Arrow keys and taps select the
              // tab they focus, so the selection change reveals those.
              if (event.target.matches(SELECTED_TAB)) reveal();
            }}
            className={cn(
              "min-w-0 overflow-x-auto overflow-y-hidden rounded-full p-1 [--strip-fade:24px] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
              edgeMask(edges),
            )}
          >
            <TabsPrimitive.List
              className="flex w-max items-center gap-[0.15rem]"
              aria-label={t("web.library.header.tabsLabel")}
            >
              {availableTabs.map((tab) => (
                <TabsPrimitive.Trigger key={tab} value={tab} className="marquee-tab-trigger">
                  {t(tabLabels[tab])}
                </TabsPrimitive.Trigger>
              ))}
            </TabsPrimitive.List>
          </div>
        </div>
      </div>
    </header>
  );
}
