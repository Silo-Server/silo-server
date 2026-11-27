import type { Translate } from "@/i18n";

/** A provider that numbers a series' episodes. */
export type Provider = "tvdb" | "tmdb";

/** Reads a stored provider value; anything but "tmdb" is TVDB. */
export const asProvider = (v: string | undefined): Provider => (v === "tmdb" ? "tmdb" : "tvdb");

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

/** Names the order a series' files are named in: TVDB numbering, TMDB numbering. */
export function fileOrderLabel(t: Translate, provider: Provider): string {
  return t(
    provider === "tvdb" ? "web.item.episodeOrder.files.tvdb" : "web.item.episodeOrder.files.tmdb",
  );
}
