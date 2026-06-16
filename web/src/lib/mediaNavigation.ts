type PlayableMediaType =
  | "movie"
  | "series"
  | "season"
  | "episode"
  | "audiobook"
  | "ebook"
  | "manga"
  | "podcast";

interface MediaHrefInput {
  contentId: string;
  type: PlayableMediaType;
  libraryId?: number;
  restart?: boolean;
  // In-app path to return to after the reader (manga chapters pass their series
  // page to break the chapter→reader→chapter loop). Routed through the query
  // helper so it is always a proper query param, even when libraryId is absent.
  backTo?: string;
}

function appendQuery(base: string, params: Record<string, string | number | boolean | undefined>) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value == null || value === false) {
      continue;
    }
    search.set(key, value === true ? "1" : String(value));
  }
  const suffix = search.toString();
  return suffix ? `${base}?${suffix}` : base;
}

export function buildItemHref({
  contentId,
  libraryId,
}: Pick<MediaHrefInput, "contentId" | "libraryId">) {
  return appendQuery(`/item/${encodeURIComponent(contentId)}`, { libraryId });
}

export function buildMediaPlayHref({
  contentId,
  type,
  libraryId,
  restart,
  backTo,
}: MediaHrefInput) {
  if (type === "movie" || type === "episode") {
    return appendQuery(`/watch/${encodeURIComponent(contentId)}`, { libraryId, restart });
  }
  if (type === "audiobook") {
    return appendQuery(`/item/${encodeURIComponent(contentId)}`, {
      libraryId,
      play: true,
      restart,
    });
  }
  if (type === "ebook") {
    return appendQuery(`/reader/ebook/${encodeURIComponent(contentId)}`, { libraryId, backTo });
  }
  // Manga series (and series/season) are not directly playable: you open the
  // detail page and read an individual chapter (itself an ebook item) from
  // there. Fall through to the item href.
  return buildItemHref({ contentId, libraryId });
}

export function isVideoWatchHref(href: string) {
  return href.startsWith("/watch/");
}
