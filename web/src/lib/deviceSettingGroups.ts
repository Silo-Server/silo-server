import { SETTING_DEFINITIONS, type SettingKey } from "@/lib/settingsContract";
import { ALL_DEVICE_SETTING_KEYS } from "@/lib/settingsDisplay";

/**
 * Device settings, grouped by what they affect rather than by manifest order.
 *
 * The manifest's own `category` is close but not sufficient: `player.*` holds
 * both picture and sound keys, and `playback.*` spans subtitles and episode
 * behaviour. Someone looking for "why does the sound lag" reads down a Sound
 * heading, not down an alphabetical list of 30 keys.
 *
 * The small override table below is the only per-key knowledge in the settings
 * UI, and it is checked by a test that every device-scoped key lands in exactly
 * one group — so a key added to the manifest cannot silently disappear from
 * this screen.
 */
export type DeviceSettingGroupId = "picture" | "sound" | "subtitles" | "episodes";

export interface DeviceSettingGroup {
  id: DeviceSettingGroupId;
  title: string;
  /** Shown beside the title; names the device so the scope stays concrete. */
  description: string;
  keys: SettingKey[];
}

const GROUP_META: Record<DeviceSettingGroupId, { title: string; description: string }> = {
  picture: { title: "Picture", description: "How video looks on this device" },
  sound: { title: "Sound", description: "Audio on this device" },
  subtitles: { title: "Subtitles", description: "On this device" },
  episodes: { title: "Episodes", description: "What happens between episodes" },
};

const GROUP_ORDER: DeviceSettingGroupId[] = ["picture", "sound", "subtitles", "episodes"];

/** Keys whose group is not implied by their manifest category. */
const EXPLICIT_GROUPS: Partial<Record<string, DeviceSettingGroupId>> = {
  "playback.audio_language": "sound",
  "playback.subtitle_language": "subtitles",
  "playback.subtitle_mode": "subtitles",
  "playback.show_forced_subtitles": "subtitles",
  "playback.subtitle_appearance": "subtitles",
  "playback.preferred_quality": "picture",
  "playback.max_bitrate_kbps": "picture",
  "playback.auto_skip_intro": "episodes",
  "playback.auto_skip_credits": "episodes",
  "playback.auto_skip_recap": "episodes",
  "playback.auto_play_next": "episodes",
  "playback.auto_play_next_preview": "episodes",
  "playback.next_up_prompt_seconds": "episodes",
  "player.hdr_enabled": "picture",
  "player.dolby_vision_enabled": "picture",
  "player.dv_profile7_hdr10_fallback": "picture",
  "player.match_frame_rate": "picture",
  "player.video_gravity": "picture",
  "player.orientation_mode": "picture",
  "player.seek_cache_enabled": "picture",
  "player.audio_sync_ms": "sound",
  "player.playback_speed": "sound",
  "player.subtitle_sync_ms": "subtitles",
  "player.sleep_timer_default_minutes": "episodes",
};

/**
 * Keys deliberately kept off this screen.
 *
 * `ui.*` device overrides exist in the contract but belong to the Appearance
 * screen, which already edits them at profile scope; showing them here would
 * give one setting two homes. `ui.library_page_state` is remembered browse
 * state rather than a preference — it has no control in the manifest at all.
 */
const HIDDEN_KEYS = new Set<string>([
  "ui.theme",
  "ui.text_scale",
  "ui.text_weight",
  "ui.high_contrast",
  "ui.library_page_state",
  "ui.remember_library_page_state",
]);

export function groupForDeviceSetting(key: SettingKey): DeviceSettingGroupId | null {
  if (HIDDEN_KEYS.has(key)) return null;
  const explicit = EXPLICIT_GROUPS[key];
  if (explicit) return explicit;

  // Anything new falls back to its manifest category, so an added key shows up
  // somewhere sensible rather than vanishing.
  const definition = SETTING_DEFINITIONS[key];
  switch (definition?.category) {
    case "player":
    case "playback":
      return "picture";
    default:
      return null;
  }
}

/** The groups to render, in reading order, with empty groups omitted. */
export function groupDeviceSettings(
  keys: readonly SettingKey[] = ALL_DEVICE_SETTING_KEYS,
): DeviceSettingGroup[] {
  const byGroup = new Map<DeviceSettingGroupId, SettingKey[]>();
  for (const key of keys) {
    const group = groupForDeviceSetting(key);
    if (!group) continue;
    const existing = byGroup.get(group);
    if (existing) {
      existing.push(key);
    } else {
      byGroup.set(group, [key]);
    }
  }

  return GROUP_ORDER.filter((id) => (byGroup.get(id)?.length ?? 0) > 0).map((id) => ({
    id,
    title: GROUP_META[id].title,
    description: GROUP_META[id].description,
    keys: byGroup.get(id) ?? [],
  }));
}

/** Every device-scoped key this screen deliberately does not show. */
export function hiddenDeviceSettingKeys(): SettingKey[] {
  return ALL_DEVICE_SETTING_KEYS.filter((key) => HIDDEN_KEYS.has(key));
}
