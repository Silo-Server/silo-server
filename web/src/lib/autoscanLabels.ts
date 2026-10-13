import type { AutoscanAvailableSource, AutoscanSource } from "@/api/types";

/** Stable key for the (plugin, capability) -> manifest display_name map. */
export function pluginDisplayNameKey(pluginId: string, capabilityId: string): string {
  return `${pluginId}:${capabilityId}`;
}

/** Build the (plugin, capability) -> display_name lookup from the picker list. */
export function buildPluginDisplayNames(available: AutoscanAvailableSource[]): Map<string, string> {
  const map = new Map<string, string>();
  for (const a of available) {
    map.set(pluginDisplayNameKey(a.plugin_id, a.capability_id), a.display_name);
  }
  return map;
}

/** Maps the Activity panel uses to resolve an event/scan's source label. */
export interface SourceLabelLookups {
  sourceByID: Map<string, AutoscanSource>;
  connectionByID: Map<string, string>;
  displayNames: Map<string, string>;
}
