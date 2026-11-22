import { useLayoutEffect, useRef, useState } from "react";
import { EyeOff, Languages } from "lucide-react";
import { useT } from "@/i18n";
import { mixesDirections } from "@/lib/direction";
import MachineTranslatedLabel from "./MachineTranslatedLabel";
import TranslatingStatus from "./TranslatingStatus";

const CLAMP_CLASSES = {
  3: "line-clamp-3",
  4: "line-clamp-4",
  5: "line-clamp-5",
} as const;

interface DetailOverviewProps {
  overview: string;
  compact?: boolean;
  /** Clamp to a few lines with a More/Less toggle instead of showing it all. */
  clamp?: boolean;
  /** Number of visible lines when clamped. Defaults to 3. */
  clampLines?: keyof typeof CLAMP_CLASSES;
  translating?: boolean;
  onTranslate?: () => void;
  /** The shown text was machine-translated by AI; label it as such. */
  machineTranslated?: boolean;
  /** Spoiler protection: show a reveal button instead of the text. */
  hidden?: boolean;
  onReveal?: () => void;
}

export default function DetailOverview({
  overview,
  compact = false,
  clamp = false,
  clampLines = 3,
  translating = false,
  onTranslate,
  machineTranslated = false,
  hidden = false,
  onReveal,
}: DetailOverviewProps) {
  const t = useT();
  const textRef = useRef<HTMLParagraphElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);
  // A caller can show the description with its line breaks (a collection's).
  const [keepsLineBreaks, setKeepsLineBreaks] = useState(false);
  // Announce "Translated by AI" only after a translation ran in this view. A
  // description that arrives already translated, such as on navigation to
  // another item, shows the label without announcing it.
  const [sawTranslation, setSawTranslation] = useState(false);
  if (translating && !sawTranslation) setSawTranslation(true);

  // Only offer "More" when the clamp actually hides text; re-check on resize
  // because the line count depends on the column width.
  useLayoutEffect(() => {
    const text = textRef.current;
    if (!clamp || expanded || !text) return;
    setKeepsLineBreaks(!/^(normal|nowrap)$/.test(getComputedStyle(text).whiteSpace));
    let active = true;
    const measure = () => {
      if (active) setOverflowing(text.scrollHeight > text.clientHeight + 1);
    };
    measure();
    // A web font that loads after the first measure can change the line count
    // while the clamped box keeps its height, so the resize observer never
    // fires. `ready` covers a load already under way (WebKit fires no
    // "loadingdone"); the event covers later ones.
    const fonts = "fonts" in document ? document.fonts : undefined;
    void fonts?.ready.then(measure);
    fonts?.addEventListener("loadingdone", measure);
    // So do the text weight preference and the interface language, which
    // changes how text such as Japanese breaks into lines (DetailTitle watches
    // both too).
    const typography = new MutationObserver(measure);
    typography.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-text-weight", "lang"],
    });
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(text);
    return () => {
      active = false;
      observer?.disconnect();
      typography.disconnect();
      fonts?.removeEventListener("loadingdone", measure);
    };
  }, [clamp, clampLines, expanded, overview, hidden]);

  if (hidden) {
    return (
      <div className="detail-hero-description max-w-2xl">
        <button
          type="button"
          onClick={onReveal}
          className="text-muted-foreground hover:text-foreground inline-flex cursor-pointer items-center gap-1.5 text-sm transition-colors"
        >
          <EyeOff className="h-3.5 w-3.5" />
          {t("web.detail.overview.revealHidden")}
        </button>
      </div>
    );
  }

  return (
    <div className="detail-hero-description max-w-2xl">
      {/* dir="auto" takes the first paragraph's direction. A collection's
          description keeps its line breaks, so when a later paragraph runs the
          other way and is clamped, Firefox and WebKit cut it on the wrong side;
          no one direction suits every paragraph of one clamped block. Nor does
          one alignment: data-clamp-cut aligns each paragraph to its own start.
          When the paragraphs run different ways, that splits the description
          between both edges and moves paragraphs that fit, so such a
          description keeps the page's alignment, and Chromium can drop its
          "…". */}
      <p
        ref={textRef}
        dir="auto"
        data-clamp-cut={
          (clamp && !expanded && overflowing && !(keepsLineBreaks && mixesDirections(overview))) ||
          undefined
        }
        className={`bidi-text text-muted-foreground leading-7 ${
          compact ? "text-sm" : "text-foreground/72 text-sm sm:text-[0.9375rem]"
        } ${clamp && !expanded ? CLAMP_CLASSES[clampLines] : ""}`}
      >
        {overview}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        {clamp && (overflowing || expanded) && (
          <button
            type="button"
            aria-expanded={expanded}
            onClick={() => setExpanded((value) => !value)}
            className="text-foreground/80 hover:text-foreground mt-1 cursor-pointer text-sm font-semibold"
          >
            {expanded ? t("web.detail.overview.less") : t("web.detail.overview.more")}
          </button>
        )}
        {/* The status and the label take the Translate button's height, so
            the row keeps its height when one replaces another. */}
        <TranslatingStatus
          translating={translating}
          label={t("web.detail.translation.translating")}
          className="mt-1.5"
        >
          {sawTranslation && machineTranslated && (
            <MachineTranslatedLabel className="border-y border-transparent py-0.5" />
          )}
        </TranslatingStatus>
        {!sawTranslation && machineTranslated && (
          <MachineTranslatedLabel className="mt-1.5 border-y border-transparent py-0.5" />
        )}
        {!translating && onTranslate && (
          <button
            type="button"
            onClick={onTranslate}
            className="text-muted-foreground hover:text-foreground border-border/60 mt-1.5 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors"
          >
            <Languages className="h-3 w-3" />
            {t("web.detail.translation.translate")}
          </button>
        )}
      </div>
    </div>
  );
}
