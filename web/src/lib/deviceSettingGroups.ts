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
  "playback.intro_skip_mode": "episodes",
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
  "nav.primary_menu",
  "ui.card_presentation",
]);

/**
 * Deprecated keys and the key that takes over from them.
 *
 * A deprecated control is hidden only where its replacement is actually on
 * offer. Against a server that predates the replacement's revision the old
 * control is the only way to express the preference at all, and hiding it
 * unconditionally would additionally strand an existing per-device override
 * with nothing on screen to see or clear it with. Where both exist, only the
 * replacement is shown: the server keeps the pair in step, so two controls
 * would let one visible choice silently rewrite the other.
 */
const SUPERSEDED_BY: Partial<Record<SettingKey, SettingKey>> = {
  "playback.auto_skip_intro": "playback.intro_skip_mode",
};

/**
 * Whether [key] is deprecated *and* the server offers what replaces it.
 *
 * Gated on the manifest's own `deprecated` flag so the rule is contract-driven:
 * a key that stops being deprecated stops being hidden without an edit here.
 */
function isSupersededOnServer(key: SettingKey, supportedKeys: readonly SettingKey[]): boolean {
  if (!SETTING_DEFINITIONS[key]?.deprecated) return false;
  const replacement = SUPERSEDED_BY[key];
  return replacement !== undefined && supportedKeys.includes(replacement);
}

/**
 * The manifest's platform identifiers, as `platforms` uses them.
 *
 * Devices self-report a free-form platform string ("iOS Web", "tvOS",
 * "android-tv"), so editing another device means mapping that string onto
 * these before the advisory `platforms` tags can be applied.
 */
export type ManifestPlatform = "web" | "ios" | "tvos" | "macos" | "android" | "android_tv";

/**
 * Maps a device's self-reported platform string to a manifest platform, or
 * null when the string is unrecognized. Order matters: every browser platform
 * string ends in "Web" ("iOS Web", "Android Web"), so the web check runs
 * before the OS checks — an iPhone browser is a web device, not an iOS app.
 */
export function manifestPlatformFor(devicePlatform: string | undefined): ManifestPlatform | null {
  if (!devicePlatform) return null;
  const p = devicePlatform.toLowerCase();
  if (p.includes("web") || p.includes("browser")) return "web";
  if (p.includes("android-tv") || p.includes("android_tv") || p.includes("android tv")) {
    return "android_tv";
  }
  if (p.includes("tvos") || p.includes("apple tv")) return "tvos";
  if (p.includes("ios") || p.includes("iphone") || p.includes("ipad")) return "ios";
  if (p.includes("macos") || p.includes("mac os")) return "macos";
  if (p.includes("android")) return "android";
  return null;
}

/**
 * Whether a setting is expected to do anything on the given platform.
 *
 * The manifest's `platforms` field is advisory UI metadata: absent means
 * "expected everywhere". An unrecognized device platform shows everything —
 * offering a control that may be inert beats hiding one that is not.
 */
export function settingAppliesToPlatform(
  key: SettingKey,
  platform: ManifestPlatform | null,
): boolean {
  const platforms = SETTING_DEFINITIONS[key]?.platforms;
  if (!platforms?.length || !platform) return true;
  return platforms.includes(platform);
}

/**
 * [supportedKeys] is what the connected server can store, which decides whether
 * a deprecated key still earns a control. It defaults to this client's own
 * contract — the newest server this build knows about — so callers that are
 * not editing a specific server's settings keep the current-contract answer.
 */
export function groupForDeviceSetting(
  key: SettingKey,
  supportedKeys: readonly SettingKey[] = ALL_DEVICE_SETTING_KEYS,
): DeviceSettingGroupId | null {
  if (HIDDEN_KEYS.has(key) || isSupersededOnServer(key, supportedKeys)) return null;
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

/**
 * The groups to render, in reading order, with empty groups omitted.
 *
 * `devicePlatform` is the target device's self-reported platform string; when
 * given, settings whose `platforms` tag excludes that device are dropped — a
 * browser gets no "Screen orientation", a phone no TV-only toggles. A key the
 * device stores a value for stays visible regardless, so a stale override can
 * always be seen and cleared.
 */
export function groupDeviceSettings(
  keys: readonly SettingKey[] = ALL_DEVICE_SETTING_KEYS,
  options?: { devicePlatform?: string; keysWithStoredValues?: ReadonlySet<string> },
): DeviceSettingGroup[] {
  const platform = manifestPlatformFor(options?.devicePlatform);
  const byGroup = new Map<DeviceSettingGroupId, SettingKey[]>();
  for (const key of keys) {
    if (
      options?.devicePlatform !== undefined &&
      !settingAppliesToPlatform(key, platform) &&
      !options?.keysWithStoredValues?.has(key)
    ) {
      continue;
    }
    // The key list this screen was given is exactly the set the connected
    // server supports, so it is also the answer to "is the replacement here".
    const group = groupForDeviceSetting(key, keys);
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

/**
 * Every device-scoped key this screen deliberately does not show against a
 * server supporting [supportedKeys]. A deprecated key is only among them while
 * its replacement is available, which is the same rule the grouping applies.
 */
export function hiddenDeviceSettingKeys(
  supportedKeys: readonly SettingKey[] = ALL_DEVICE_SETTING_KEYS,
): SettingKey[] {
  return ALL_DEVICE_SETTING_KEYS.filter(
    (key) => HIDDEN_KEYS.has(key) || isSupersededOnServer(key, supportedKeys),
  );
}
