import { useLayoutEffect, useRef, useState } from "react";
import { Languages } from "lucide-react";

interface DetailOverviewProps {
  overview: string;
  compact?: boolean;
  /** Clamp to a few lines with a More/Less toggle instead of showing it all. */
  clamp?: boolean;
  translating?: boolean;
  onTranslate?: () => void;
}

export default function DetailOverview({
  overview,
  compact = false,
  clamp = false,
  translating = false,
  onTranslate,
}: DetailOverviewProps) {
  const textRef = useRef<HTMLParagraphElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  // Only offer "More" when the clamp actually hides text; re-check on resize
  // because the line count depends on the column width.
  useLayoutEffect(() => {
    const text = textRef.current;
    if (!clamp || expanded || !text) return;
    const measure = () => setOverflowing(text.scrollHeight > text.clientHeight + 1);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(text);
    return () => observer.disconnect();
  }, [clamp, expanded, overview]);

  return (
    <div className="detail-hero-description max-w-2xl">
      <p
        ref={textRef}
        className={`text-muted-foreground leading-7 ${
          compact ? "text-sm" : "text-foreground/72 text-sm sm:text-[15px]"
        } ${clamp && !expanded ? "line-clamp-3" : ""} ${
          translating ? "animate-pulse opacity-50" : ""
        }`}
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
            {expanded ? "Less" : "More"}
          </button>
        )}
        {translating && (
          <span className="text-muted-foreground/70 mt-1 inline-flex items-center gap-1.5 text-xs">
            <Languages className="h-3 w-3 animate-pulse" />
            Translating…
          </span>
        )}
        {!translating && onTranslate && (
          <button
            type="button"
            onClick={onTranslate}
            className="text-muted-foreground hover:text-foreground border-border/60 mt-1.5 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors"
          >
            <Languages className="h-3 w-3" />
            Translate
          </button>
        )}
      </div>
    </div>
  );
}
