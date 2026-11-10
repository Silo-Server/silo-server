import type { Library } from "@/api/types";
import { t } from "@/i18n";

/** One line for the rail and the recap: the library's name, or a count. */
export function librarySummary(libraries: Library[]): string {
  if (libraries.length === 0) return t("web.setup.library.summaryNone");
  if (libraries.length === 1) return libraries[0]!.name;
  return t("web.setup.library.summaryCount", {
    count: libraries.length,
    shown: String(libraries.length),
  });
}
