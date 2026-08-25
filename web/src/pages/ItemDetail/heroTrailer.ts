import type { ItemVideo } from "@/api/types";

/** Backdrop autoplay is desktop-only; phones keep the still Ken Burns image. */
export const HERO_TRAILER_MIN_WIDTH_PX = 768;

const HERO_TRAILER_KINDS = ["trailer", "teaser"] as const;

/**
 * Pick the YouTube clip to loop behind a movie/series hero.
 * Server order is already trailers-first / official-first; this still prefers
 * an official trailer, then any trailer, then teaser, then the first YouTube
 * row so a library that only stored clips still gets motion.
 */
export function pickHeroTrailer(videos: ItemVideo[] | undefined): ItemVideo | null {
  if (!videos?.length) {
    return null;
  }

  const youtube = videos.filter(
    (video) => video.site.toLowerCase() === "youtube" && video.site_key.trim() !== "",
  );
  if (youtube.length === 0) {
    return null;
  }

  for (const kind of HERO_TRAILER_KINDS) {
    const official = youtube.find((video) => video.kind === kind && video.is_official);
    if (official) {
      return official;
    }
    const any = youtube.find((video) => video.kind === kind);
    if (any) {
      return any;
    }
  }

  return youtube[0] ?? null;
}

/** Privacy-enhanced embed that browsers will autoplay (muted, no chrome). */
export function youtubeHeroEmbedUrl(siteKey: string): string {
  const key = encodeURIComponent(siteKey);
  const params = new URLSearchParams({
    autoplay: "1",
    mute: "1",
    controls: "0",
    playsinline: "1",
    loop: "1",
    playlist: siteKey,
    modestbranding: "1",
    rel: "0",
    iv_load_policy: "3",
    disablekb: "1",
    fs: "0",
  });
  return `https://www.youtube-nocookie.com/embed/${key}?${params.toString()}`;
}

export function shouldPlayHeroTrailer(input: {
  reducedMotion: boolean;
  viewportWideEnough: boolean;
  documentVisible: boolean;
}): boolean {
  return !input.reducedMotion && input.viewportWideEnough && input.documentVisible;
}
