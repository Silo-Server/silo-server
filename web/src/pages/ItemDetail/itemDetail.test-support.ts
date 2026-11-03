import type { FileVersion, ItemDetail } from "@/api/types";

/** A 1080p H.264/AAC MKV with no size, duration or bitrate; tests set what they read. */
export function makeFileVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: 1,
    resolution: "1080p",
    codec_video: "h264",
    codec_audio: "aac",
    hdr: false,
    container: "mkv",
    file_size: 0,
    duration: 0,
    bitrate: 0,
    ...overrides,
  };
}

/** An item of `type` (content id `<type>-1`) whose required catalog fields are all empty. */
export function makeItemDetail<T extends ItemDetail["type"]>(
  type: T,
  overrides: Partial<ItemDetail> = {},
): ItemDetail & { type: T } {
  return {
    content_id: `${type}-1`,
    title: "",
    year: 2024,
    overview: "",
    runtime: 0,
    content_rating: "",
    genres: [],
    rating_imdb: null,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    ratings: [],
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    release_date: null,
    first_air_date: null,
    last_air_date: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    season_count: null,
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
    type,
  };
}
