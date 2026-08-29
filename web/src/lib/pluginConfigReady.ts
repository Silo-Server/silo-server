import type { PluginInstallation } from "@/api/types";

/**
 * Whether a plugin installation has the global configuration it asks for.
 *
 * Silo never sees a plugin's secrets — they live in the plugin's own runtime
 * config — but the installation response says which config keys the plugin
 * declares and which have a value or configured secret saved. That is enough
 * to stop a provider with no API key from reading as connected, which is the
 * shared rule for every provider tile backed by a plugin: "Connected" means
 * the plugin could actually serve a configured request.
 */
export function installationConfigReady(installation: PluginInstallation): boolean {
  const schema = installation.global_config_schema ?? [];
  if (schema.length === 0) return true;

  const saved = new Map((installation.global_configs ?? []).map((config) => [config.key, config]));
  const filled = (key: string) => {
    const config = saved.get(key);
    if (!config) return false;
    if ((config.configured_secrets ?? []).length > 0) return true;
    // Any saved non-null value counts — `false` and `0` are values an admin
    // deliberately chose (a required boolean saved as false is configured).
    // Only a blank string is excluded: an empty text box is not a value, and
    // counting it would let a keyless plugin read as connected.
    return Object.values(config.value ?? {}).some((value) =>
      typeof value === "string" ? value.trim() !== "" : value != null,
    );
  };

  // A plugin that takes configuration and has none saved is not set up, whether
  // or not it marked a field required — several plugins declare everything
  // optional and then fail every lookup without a key.
  if (!schema.some((entry) => filled(entry.key))) return false;
  return schema.filter((entry) => entry.required).every((entry) => filled(entry.key));
}
