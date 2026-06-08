type PlayableMediaType = "movie" | "series" | "season" | "episode" | "audiobook";

interface MediaHrefInput {
  contentId: string;
  type: PlayableMediaType;
  libraryId?: number;
  restart?: boolean;
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

export function buildItemHref({ contentId, libraryId }: Pick<MediaHrefInput, "contentId" | "libraryId">) {
  return appendQuery(`/item/${contentId}`, { libraryId });
}

export function buildMediaPlayHref({ contentId, type, libraryId, restart }: MediaHrefInput) {
  if (type === "movie" || type === "episode") {
    return appendQuery(`/watch/${contentId}`, { libraryId, restart });
  }
  if (type === "audiobook") {
    return appendQuery(`/item/${contentId}`, { libraryId, play: true, restart });
  }
  return buildItemHref({ contentId, libraryId });
}

export function isVideoWatchHref(href: string) {
  return href.startsWith("/watch/");
}
