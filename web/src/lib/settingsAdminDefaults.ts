import { SETTING_DEFINITIONS, type SettingKey } from "@/lib/settingsContract";

/**
 * Administrator defaults: a definition marked adminDefault can have its
 * default replaced for the whole server by the server setting
 * `defaults.<key>`, which holds the value as JSON text.
 */
const ADMIN_DEFAULT_PREFIX = "defaults.";

/** The server setting holding the administrator's default for key. */
export function adminDefaultServerKey(key: SettingKey): string {
  return ADMIN_DEFAULT_PREFIX + key;
}

/** The setting whose administrator default serverSettingKey holds, if any. */
export function adminDefaultSettingFor(serverSettingKey: string): SettingKey | undefined {
  if (!serverSettingKey.startsWith(ADMIN_DEFAULT_PREFIX)) return undefined;
  const key = serverSettingKey.slice(ADMIN_DEFAULT_PREFIX.length) as SettingKey;
  return SETTING_DEFINITIONS[key]?.adminDefault ? key : undefined;
}

/**
 * The stored administrator default, or the contract default when none is set
 * or the stored text is not JSON, which is also how the server resolves it.
 */
export function storedAdminDefault(key: SettingKey, raw: string | undefined): unknown {
  const fallback = SETTING_DEFINITIONS[key].defaultValue;
  if (!raw) return fallback;
  try {
    return JSON.parse(raw);
  } catch {
    return fallback;
  }
}
