import type { Translate } from "@/i18n";

// Shown without a disclosure: the controls a household admin actually
// touches, including the HDR tone mapping switches, throttling and where
// segments are written.
export const TRANSCODING_ESSENTIAL_KEYS = [
  "playback.transcode_enabled",
  "playback.hw_accel",
  "allow_4k_transcode",
  "playback.prefer_lower_version_transcode",
  "playback.allow_hevc_encoding",
  "playback.transcode_hardware_tone_map_enabled",
  "playback.transcode_software_tone_map_enabled",
  "enable_transcode_throttle",
  "transcode_throttle_seconds",
  "playback.transcode_dir",
];

export const TRANSCODING_ADVANCED_KEYS = [
  "playback.ffmpeg_path",
  "playback.hw_device",
  "playback.segment_retention_seconds",
  "playback.copy_keyframe_playlist",
];

// Chapter thumbnails and seek previews are images cut from the video, not part
// of a transcode, so they get their own group. Width, interval and the chapter
// thumbnail worker count show without a disclosure.
const PREVIEW_SHOWN_KEYS = [
  "playback.preview_image_width",
  "playback.trickplay_interval_seconds",
  "playback.chapter_thumbnail_workers",
];

// Where the images are made is only a real choice once a transcode node could
// take the work, so these sit beside width and interval then and in Advanced
// otherwise.
export const PREVIEW_PLACEMENT_KEYS = [
  "playback.chapter_thumbnail_execution",
  "playback.trickplay_execution",
];

export const PREVIEW_ADVANCED_KEYS = [
  "playback.trickplay_workers",
  "playback.trickplay_node_capacity",
  "playback.chapter_thumbnail_hdr_policy",
  "playback.chapter_thumbnail_software_tone_map_enabled",
];

export const PREVIEW_IMAGE_KEYS = [
  ...PREVIEW_SHOWN_KEYS,
  ...PREVIEW_PLACEMENT_KEYS,
  ...PREVIEW_ADVANCED_KEYS,
];

// A new preview image width makes every chapter thumbnail and seek preview
// again; a new interval, every seek preview. The defaults are the server's,
// for a row that was never set.
export const PREVIEW_WIDTH_KEY = "playback.preview_image_width";
export const TRICKPLAY_INTERVAL_KEY = "playback.trickplay_interval_seconds";
export const REMAKE_DEFAULTS: Record<string, string> = {
  [PREVIEW_WIDTH_KEY]: "300",
  [TRICKPLAY_INTERVAL_KEY]: "10",
};

/** What saving a new width or interval makes again, for the confirmation. */
export function remakeDescription(
  t: Translate,
  widthChanged: boolean,
  previews: number | null,
): string {
  const count = previews === null ? undefined : { count: previews, count_text: String(previews) };
  if (!widthChanged) {
    return count
      ? t("web.admin.settings.playback.remake.intervalCount", count)
      : t("web.admin.settings.playback.remake.intervalExisting");
  }
  if (!count) return t("web.admin.settings.playback.remake.widthExisting");
  return count.count > 0
    ? t("web.admin.settings.playback.remake.widthCount", count)
    : t("web.admin.settings.playback.remake.widthNoPreviews");
}

export const WATCH_KEYS = ["playback.watched_threshold", "playback.min_resume_threshold"];

export const SPEED_TEST_KEYS = [
  "playback.bandwidth_probe_automatic",
  "playback.bandwidth_probe_max_age_days",
];

export const MARKER_KEYS = [
  "markers.mode",
  "markers.detect_intros",
  "markers.detect_credits",
  "markers.online_storage",
  "markers.lazy_playback",
  "markers.detection_workers",
];

// `playback.chapter_thumbnail_node_capacity` is deliberately absent from the
// UI (hidden tier): it is still saved and read through the settings API, but
// the per-node budget is derived from the node pool rather than typed in.
//
// The `download.*` family is its own page (DownloadsSettings), so it is not
// loaded or saved here.
export const KEYS = [
  ...WATCH_KEYS,
  ...SPEED_TEST_KEYS,
  ...TRANSCODING_ESSENTIAL_KEYS,
  ...TRANSCODING_ADVANCED_KEYS,
  ...PREVIEW_IMAGE_KEYS,
  ...MARKER_KEYS,
];
