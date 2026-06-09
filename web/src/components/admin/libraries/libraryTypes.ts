import { BookHeadphones, Film, Layers, Podcast, Tv } from "lucide-react";

export const LIBRARY_TYPES = [
  { value: "movies", label: "Movies", icon: Film },
  { value: "series", label: "Series", icon: Tv },
  { value: "mixed", label: "Mixed", icon: Layers },
  { value: "audiobooks", label: "Audiobooks", icon: BookHeadphones },
  { value: "podcasts", label: "Podcasts", icon: Podcast },
] as const;

export function libraryTypeMeta(type: string) {
  return LIBRARY_TYPES.find((t) => t.value === type) ?? LIBRARY_TYPES[0];
}
