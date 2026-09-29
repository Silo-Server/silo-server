import type { OverlayData } from "./types";

// Sample OverlayData used by the settings preview cards so users can see how
// the configured overlays will actually look. Two variants — movie and show —
// because some overlays (show_status, edition) only render for one or the
// other in real data.

export const SAMPLE_MOVIE_DATA: OverlayData = {
  resolution: "2160p",
  hdr: "DV HDR10",
  audio: "Atmos",
  audio_channels: "7.1",
  video_codec: "H.265",
  container: "MKV",
  aspect_ratio: "2.39:1",
  release_type: "REMUX",
  edition: "Extended",
  multi_audio: true,
  multi_sub: true,
  rating_imdb: 8.7,
  rating_tmdb: 8.5,
  rating_rt_critic: 96,
  rating_rt_audience: 92,
  content_rating: "PG-13",
  advisory_age: 13,
  year: 2024,
  runtime: 148,
  original_language: "EN",
  studio: "A24",
  network: undefined,
  show_status: undefined,
  imdb_top_250: 42,
  rt_certified_fresh: true,
};

export const SAMPLE_SHOW_DATA: OverlayData = {
  ...SAMPLE_MOVIE_DATA,
  resolution: "1080p",
  hdr: "HDR10",
  edition: undefined,
  release_type: undefined,
  runtime: 55,
  studio: undefined,
  network: "HBO",
  show_status: "returning",
  rt_certified_fresh: false,
  imdb_top_250: null,
};
