import { BookHeadphones, BookMarked, BookOpen, Film, Layers, Podcast, Tv } from "lucide-react";

export const LIBRARY_TYPES = [
  { value: "movies", label: "Movies", icon: Film },
  { value: "series", label: "Series", icon: Tv },
  { value: "mixed", label: "Mixed", icon: Layers },
  { value: "audiobooks", label: "Audiobooks", icon: BookHeadphones },
  { value: "ebooks", label: "Ebooks", icon: BookOpen },
  { value: "manga", label: "Manga", icon: BookMarked },
  { value: "podcasts", label: "Podcasts", icon: Podcast },
] as const;

export function libraryTypeMeta(type: string) {
  return LIBRARY_TYPES.find((t) => t.value === type) ?? LIBRARY_TYPES[0];
}

// Keep these aligned with the video scanner and intro-marker library filters.
export function librarySettingSupport(type: string) {
  const kind = type.trim().toLowerCase();
  // The scanner routes these types to dedicated pipelines; everything else
  // follows the video pipeline, including custom library types.
  const video = ![
    "audiobook",
    "audiobooks",
    "ebook",
    "ebooks",
    "manga",
    "podcast",
    "podcasts",
  ].includes(kind);
  return {
    trailers: video,
    chapterThumbnails: video,
    introDetection: kind === "series" || kind === "mixed",
  };
}
