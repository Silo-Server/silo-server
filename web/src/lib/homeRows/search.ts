import type { PickerGroup } from "./catalog";
import { variantFamily } from "./variants";

/** Words people search for that a card's own text doesn't contain. */
const ALIASES: Record<string, readonly string[]> = {
  continue_watching: ["resume", "in progress", "audiobook"],
  next_up: ["next episode", "up next"],
  recently_added: ["new", "latest"],
  recently_released: ["new", "release"],
  new_to_library: ["new", "added"],
  trending_on_server: ["popular", "hot"],
  most_watched: ["popular", "trending", "top"],
  trending_discover: ["tmdb", "popular", "worldwide"],
  recommended_for_you: ["recommendation", "for you"],
  seasonal_themed: [
    "christmas",
    "halloween",
    "holiday",
    "valentine",
    "thanksgiving",
    "summer",
    "family",
  ],
  editorial_spotlight: ["director", "actor", "studio", "80s", "era", "decade"],
  format_showcase: ["4k", "uhd", "hdr", "dolby vision", "quality"],
  random: ["random", "shuffle"],
  collection: ["collection", "ghibli", "franchise", "box set", "list", "smart"],
  custom_filter: ["rules", "filter", "genre", "decade", "year", "rating", "smart"],
};

function haystack(card: PickerGroup["cards"][number]): string {
  const family = variantFamily(card.type);
  return [
    card.label,
    card.sentence,
    ...card.def.presets.map((preset) => preset.display_name),
    ...(family?.options.flatMap((option) => [option.chip, option.label]) ?? []),
    ...(ALIASES[card.type] ?? []),
  ]
    .join("\n")
    .toLowerCase();
}

/**
 * The picker's groups narrowed to cards whose name, sentence, presets,
 * variants or common aliases contain every word of the query. Groups with no
 * match drop out; an empty query keeps everything.
 */
export function searchPickerGroups(groups: PickerGroup[], query: string): PickerGroup[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return groups;
  return groups
    .map((group) => ({
      ...group,
      cards: group.cards.filter((card) => {
        const text = haystack(card);
        return words.every((word) => text.includes(word));
      }),
    }))
    .filter((group) => group.cards.length > 0);
}
