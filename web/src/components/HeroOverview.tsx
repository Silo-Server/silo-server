import { Languages } from "lucide-react";
import type { SectionItem } from "@/api/types";
import { useOnViewTranslation } from "@/hooks/useOnViewTranslation";
import MachineTranslatedLabel from "@/pages/ItemDetail/components/MachineTranslatedLabel";
import { HeroOverviewText } from "./HeroOverviewText";

/**
 * The Featured hero's overview with on-view translation. Only the visible
 * slide renders it, so off-screen slides wait until they rotate in:
 * auto mode translates it, button mode offers Translate, and an AI-translated
 * overview is labeled. onRefreshItems refetches the hero's own section while a
 * translation runs.
 */
export default function HeroOverview({
  item,
  onRefreshItems,
}: {
  item: SectionItem;
  onRefreshItems?: () => void;
}) {
  const { translating, onTranslate } = useOnViewTranslation(item, { onPoll: onRefreshItems });
  // The hero shows the overview without the tagline, so an AI tagline alone
  // must not label a provider or human overview.
  const machineTranslated = item.machine_translated_fields?.includes("overview") ?? false;

  return (
    <div className="mb-7 max-w-2xl">
      <HeroOverviewText overview={item.overview} translating={translating} />
      {translating ? (
        <span className="text-muted-foreground/70 mt-1 inline-flex items-center gap-1.5 text-xs">
          <Languages className="h-3 w-3 animate-pulse" />
          Translating…
        </span>
      ) : (
        (machineTranslated || onTranslate) && (
          <div className="mt-1.5 flex flex-wrap items-center gap-2">
            {machineTranslated && <MachineTranslatedLabel />}
            {onTranslate && (
              <button
                type="button"
                onClick={onTranslate}
                className="text-muted-foreground hover:text-foreground border-border/60 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors"
              >
                <Languages className="h-3 w-3" />
                Translate
              </button>
            )}
          </div>
        )
      )}
    </div>
  );
}
