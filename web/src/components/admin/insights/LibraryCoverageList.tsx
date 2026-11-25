import { BarList, type BarListItem } from "@/components/admin/dashboard/charts";
import type { AdminInsightsLibrary } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

/**
 * Each movie and TV library with the share of its titles played in the
 * window. The bar is the played share, so a library nobody watches reads
 * as a short bar however large it is.
 */
export function LibraryCoverageList({
  libraries,
  linkFor,
}: {
  libraries: AdminInsightsLibrary["libraries"];
  linkFor?: (libraryId: string) => string;
}) {
  const t = useT();
  const items: BarListItem[] = libraries.map((library) => ({
    id: library.library_id,
    label: library.name,
    value: Math.round(library.played_share * 1000) / 10,
    secondary: t("web.admin.insights.library.coverage.detail", {
      played: library.played_titles.toLocaleString(formatLocale()),
      titles: library.titles.toLocaleString(formatLocale()),
      titles_count: library.titles,
      never: library.never_started.toLocaleString(formatLocale()),
    }),
    to: linkFor?.(library.library_id),
  }));
  return (
    <BarList
      items={items}
      max={100}
      seriesIndex={2}
      formatValue={(value) =>
        t("web.admin.insights.library.coverage.playedShare", {
          value: value.toLocaleString(formatLocale(), { maximumFractionDigits: 1 }),
        })
      }
      emptyLabel={t("web.admin.insights.library.coverage.empty")}
    />
  );
}
