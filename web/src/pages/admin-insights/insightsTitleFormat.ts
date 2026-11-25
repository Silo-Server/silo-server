import type { Translate } from "@/i18n";

/** "Arrival (2016)", or the removed-title label when the catalog has no title for it. */
export function formatInsightsTitle(
  row: { title: string; year: number | null },
  t: Translate,
): string {
  const title = row.title.trim() || t("web.admin.insights.titles.unknownTitle");
  return row.year
    ? t("web.admin.insights.titles.titleWithYear", { title, year: String(row.year) })
    : title;
}
