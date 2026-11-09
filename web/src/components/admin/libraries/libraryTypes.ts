import { BookHeadphones, BookMarked, BookOpen, Film, Layers, Podcast, Tv } from "lucide-react";

import { t } from "@/i18n";

// Each type's name is read when shown, in the interface language.
export const LIBRARY_TYPES = [
  {
    value: "movies",
    icon: Film,
    get label() {
      return t("web.admin.common.libraryEditor.type.movies");
    },
  },
  {
    value: "series",
    icon: Tv,
    get label() {
      return t("web.admin.common.libraryEditor.type.series");
    },
  },
  {
    value: "mixed",
    icon: Layers,
    get label() {
      return t("web.admin.common.libraryEditor.type.mixed");
    },
  },
  {
    value: "audiobooks",
    icon: BookHeadphones,
    get label() {
      return t("web.admin.common.libraryEditor.type.audiobooks");
    },
  },
  {
    value: "ebooks",
    icon: BookOpen,
    get label() {
      return t("web.admin.common.libraryEditor.type.ebooks");
    },
  },
  {
    value: "manga",
    icon: BookMarked,
    get label() {
      return t("web.admin.common.libraryEditor.type.manga");
    },
  },
  {
    value: "podcasts",
    icon: Podcast,
    get label() {
      return t("web.admin.common.libraryEditor.type.podcasts");
    },
  },
] as const;

// Other spellings internal/librarykind accepts for each library type; the
// server stores the type as free text.
const LIBRARY_TYPE_ALIASES: Readonly<Record<string, string>> = {
  movie: "movies",
  tv: "series",
  show: "series",
  tvshows: "series",
  audiobook: "audiobooks",
  ebook: "ebooks",
  podcast: "podcasts",
};

/** The library type a stored type string means, or undefined for an unknown type. */
export function findLibraryType(type: string) {
  const kind = type.trim().toLowerCase();
  const value = LIBRARY_TYPE_ALIASES[kind] ?? kind;
  return LIBRARY_TYPES.find((entry) => entry.value === value);
}

export function libraryTypeMeta(type: string) {
  return findLibraryType(type) ?? LIBRARY_TYPES[0];
}

// Keep these aligned with the video scanner and intro-marker library filters.
/** The box set size a library starts with, as the server defaults it. */
export const DEFAULT_BOX_SET_MIN_MOVIES = 2;
/** The minimum box set sizes the form offers; the server accepts 2 to 10. */
export const BOX_SET_MIN_MOVIE_OPTIONS = [2, 3, 4, 5] as const;

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
    trickplay: ["movie", "movies", "series", "tv", "show", "tvshows", "mixed"].includes(kind),
    introDetection: kind === "series" || kind === "mixed" || kind === "movies",
    // Movies get local credits only, on a best-effort basis, so new movie
    // libraries leave detection off. Mixed libraries also hold episodes,
    // which get intros too, so they keep detection on.
    creditsOnlyDetection: kind === "movies",
    movieCreditsDetection: kind === "movies" || kind === "mixed",
    // Box sets group movies, so only libraries that hold movies offer them.
    boxSets: ["movie", "movies", "mixed"].includes(kind),
  };
}
