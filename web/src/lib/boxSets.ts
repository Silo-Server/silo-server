import type { BoxSetSummary, BrowseItem } from "@/api/types";
import { numberText, t } from "@/i18n";

/** "2001 – 2011", or one year when the box set spans one. */
export function boxSetYears(boxSet: Pick<BoxSetSummary, "first_year" | "last_year">) {
  const first = boxSet.first_year || undefined;
  const last = boxSet.last_year || undefined;
  if (first && last && first !== last) return `${numberText(first)} – ${numberText(last)}`;
  return numberText(first ?? last ?? "");
}

export function boxSetFilmCount(count: number) {
  return t("web.detail.boxSet.filmCount", { count });
}

/**
 * The movie a box set's play button starts: the first one in collection
 * order the profile hasn't played, or the first one once all are played.
 */
export function boxSetNextMovie<T extends Pick<BrowseItem, "user_state">>(items: T[]) {
  return items.find((item) => !item.user_state?.played) ?? items[0];
}
