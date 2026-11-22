import type { MessageKey, Translate } from "@/i18n";

function itemTypeLabelKey(type: string): MessageKey | null {
  switch (type) {
    case "movie":
      return "web.search.typeMovie";
    case "series":
      return "web.search.typeSeries";
    case "season":
      return "web.search.typeSeason";
    case "episode":
      return "web.search.typeEpisode";
    case "ebook":
      return "web.search.typeEbook";
    case "audiobook":
      return "web.search.typeAudiobook";
    case "manga":
      return "web.search.typeManga";
    default:
      return null;
  }
}

/**
 * The label for a catalog item's kind (`movie`, `season`, ...), which the API
 * sends as English data. A kind the client has no label for shows as the
 * server sends it.
 */
export function itemTypeLabel(t: Translate, type: string): string {
  const key = itemTypeLabelKey(type);
  return key ? t(key) : type;
}
