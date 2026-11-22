import { Languages } from "lucide-react";
import type { SectionItem } from "@/api/types";
import { useOnViewTranslation } from "@/hooks/useOnViewTranslation";
import MachineTranslatedLabel from "@/pages/ItemDetail/components/MachineTranslatedLabel";
import TranslatingStatus from "@/pages/ItemDetail/components/TranslatingStatus";
import { useT } from "@/i18n";
import { HeroOverviewText } from "./HeroOverviewText";

/**
 * The Featured hero's overview with on-view translation. Only the visible
 * slide renders it, so off-screen slides wait until they rotate in:
 * auto mode translates it, button mode offers Translate, and an AI-translated
 * overview is labeled. onRefreshItems refetches the hero's own section while a
 * translation runs.
 *
 * The hero keeps this mounted for every slide, passing no item while the
 * slide's overview is hidden, so a translation started on an earlier slide
 * keeps polling instead of starting over when that slide returns.
 */
export default function HeroOverview({
  item,
  pendingContentIds,
  onRefreshItems,
}: {
  item: SectionItem | undefined;
  pendingContentIds: ReadonlySet<string>;
  onRefreshItems?: () => void;
}) {
  const t = useT();
  const { translating, onTranslate } = useOnViewTranslation(item, {
    onPoll: onRefreshItems,
    pendingContentIds,
  });
  if (!item) return null;
  // The hero shows the overview without the tagline, so an AI tagline alone
  // must not label a provider or human overview.
  const machineTranslated = item.machine_translated_fields?.includes("overview") ?? false;

  return (
    <div className="mb-7 max-w-2xl">
      <HeroOverviewText overview={item.overview} />
      {/* Mounted while idle too, so its live region exists before a
        translation starts. The flex row keeps the status at the Translate
        button's height instead of the paragraph's taller line. */}
      <div className="flex">
        <TranslatingStatus
          translating={translating}
          label={t("web.home.hero.translating")}
          className="mt-1.5"
        />
      </div>
      {!translating && (machineTranslated || onTranslate) && (
        <div className="mt-1.5 flex flex-wrap items-center gap-2">
          {machineTranslated && (
            <MachineTranslatedLabel className="border-y border-transparent py-0.5" />
          )}
          {onTranslate && (
            <button
              type="button"
              onClick={onTranslate}
              className="text-muted-foreground hover:text-foreground border-border/60 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors"
            >
              <Languages className="h-3 w-3" />
              {t("web.home.hero.translate")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
