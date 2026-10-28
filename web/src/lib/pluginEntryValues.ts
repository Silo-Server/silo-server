/**
 * Reading and writing a plugin's per-account settings.
 *
 * Values are keyed by SCHEMA ENTRY, not by that entry's fields: one entry, one
 * value, and the value is the entry's object as a JSON string.
 *
 *   schema: key "report", json_schema {properties: {api_key, show_report}}
 *   stored: {"report": "{\"api_key\":\"…\",\"show_report\":true}"}
 *
 * So a field is not addressable on its own, and writing one as its own key is
 * rejected ("plugin user config key is not declared in schema"). The inner value
 * keeps the schema's own type: show_report is a JSON boolean, not "true".
 */

/** The object a plugin's schema entry currently holds, or an empty object. */
export function readPluginEntryObject(
  values: Record<string, string> | undefined,
  entryKey: string,
): Record<string, unknown> {
  const raw = values?.[entryKey];
  if (typeof raw !== "string" || raw.trim() === "") return {};

  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
    return {};
  } catch {
    // A value this server or an older client left unparseable is treated as
    // unset rather than crashing a settings screen.
    return {};
  }
}

/**
 * The whole values map with one entry's object updated.
 *
 * The write endpoint REPLACES the stored set, so every other entry is carried
 * through untouched; sending only the changed entry would clear the rest.
 */
export function updatePluginEntryObject(
  values: Record<string, string> | undefined,
  entryKey: string,
  patch: Record<string, unknown>,
): Record<string, string> {
  return {
    ...(values ?? {}),
    [entryKey]: JSON.stringify({ ...readPluginEntryObject(values, entryKey), ...patch }),
  };
}

/** A boolean field, or false when absent or of another type. */
export function entryBoolean(value: unknown): boolean {
  return value === true;
}
