import type { Translate } from "@/i18n";

// Provider names are brands, the same in every language.
const PROVIDER_LABELS: Record<string, string | undefined> = { tvdb: "TVDB", tmdb: "TMDB" };

/** A provider's brand name: TVDB, TMDB. */
export function providerLabel(provider: string): string {
  return PROVIDER_LABELS[provider] ?? provider.toUpperCase();
}

/**
 * Names an episode order: TVDB aired order, TMDB default order, or a named
 * order such as TMDB “Story Arcs”.
 */
export function episodeOrderLabel(
  t: Translate,
  provider: string,
  orderId: string,
  orderName?: string,
): string {
  const label = providerLabel(provider);
  if (orderId) {
    return t("web.item.episodeOrder.namedOrder", { provider: label, order: orderName || orderId });
  }
  return t("web.item.episodeOrder.providerOrder", {
    provider: label,
    order: t(
      provider === "tvdb"
        ? "web.item.episodeOrder.airedOrder"
        : "web.item.episodeOrder.defaultOrder",
    ),
  });
}

/** Whether a season title only repeats its number ("Season 3"), in English or the viewer's language. */
export function isNumberedSeasonTitle(t: Translate, title: string, number: number): boolean {
  const trimmed = title.trim();
  return (
    trimmed === `Season ${number}` || trimmed === t("web.item.arrangeSeasons.season", { number })
  );
}
