import { useEffect, useState } from "react";
import { Tabs as TabsPrimitive } from "radix-ui";
import { Shuffle } from "lucide-react";
import { useT, type MessageKey } from "@/i18n";
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

/** Scroll distance (in px) at which an overlay header switches to glass. */
const GLASS_THRESHOLD_PX = 160;

export default function LibraryHeader({
  libraryName,
  libraryType = "",
  overlay = false,
  availableTabs = DEFAULT_TABS,
  onShuffle,
  shuffleDisabled = false,
}: LibraryHeaderProps) {
  const t = useT();
  const tabLabels = isAudiobookLibraryType(libraryType) ? AUDIOBOOK_TAB_LABELS : TAB_LABELS;
  const [pastThreshold, setPastThreshold] = useState(false);

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
      className={cn("library-marquee-header", overlay && "is-overlay")}
      data-scrolled={overlay ? (scrolled ? "true" : "false") : undefined}
    >
      {/* Eyebrow is hidden on mobile — the top nav already identifies the
          current page, and at narrow widths it would either crowd out the
          tab pills or truncate to "LIBRAR…". */}
      <div className="hidden min-w-0 items-center gap-4 sm:flex">
        <p className="hero-eyebrow truncate">
          <span>{t("web.library.header.eyebrow")}</span>
          {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- a divider glyph, not text */}
          <span className="hero-eyebrow-divider">/</span>
          <span className="hero-eyebrow-strong">{libraryName}</span>
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
        <TabsPrimitive.List
          className="marquee-tab-bar"
          aria-label={t("web.library.header.tabsLabel")}
        >
          {availableTabs.map((tab) => (
            <TabsPrimitive.Trigger key={tab} value={tab} className="marquee-tab-trigger">
              {t(tabLabels[tab])}
            </TabsPrimitive.Trigger>
          ))}
        </TabsPrimitive.List>
      </div>
    </header>
  );
}
