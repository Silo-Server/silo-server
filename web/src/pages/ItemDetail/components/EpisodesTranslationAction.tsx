import { Languages } from "lucide-react";

import { useT } from "@/i18n";
import TranslatingStatus from "./TranslatingStatus";

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
  const t = useT();
  // The status stays mounted beside the action so its live region exists
  // before a translation starts.
  return (
    <>
      <TranslatingStatus
        translating={translating}
        label={t("web.detail.translation.translating")}
      />
      {!translating && onTranslate && (
        <button
          type="button"
          onClick={onTranslate}
          className="text-muted-foreground hover:text-foreground border-border/60 inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors"
        >
          <Languages className="h-3 w-3" />
          {t("web.detail.translation.translateEpisodes")}
        </button>
      )}
    </>
  );
}
