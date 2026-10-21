import { Languages } from "lucide-react";

/**
 * The on-view translation status or action beside an Episodes heading. A
 * season's job covers its episodes, so this is where a page offers it when
 * the episode list, not the page's own description, is what is missing.
 */
export default function EpisodesTranslationAction({
  translating,
  onTranslate,
}: {
  translating: boolean;
  onTranslate?: () => void;
}) {
  if (translating) {
    return (
      <span className="text-muted-foreground/70 inline-flex items-center gap-1.5 text-xs">
        <Languages className="h-3 w-3 animate-pulse" />
        Translating…
      </span>
    );
  }
  if (!onTranslate) return null;
  return (
    <button
      type="button"
      onClick={onTranslate}
      className="text-muted-foreground hover:text-foreground border-border/60 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors"
    >
      <Languages className="h-3 w-3" />
      Translate episodes
    </button>
  );
}
