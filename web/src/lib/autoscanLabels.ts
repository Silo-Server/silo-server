import type { AutoscanAvailableSource, AutoscanSource } from "@/api/types";

export interface SourceLabelParts {
  operatorLabel?: string | null;
  connectionName?: string | null;
  displayName?: string | null;
  capabilityId: string;
  pluginId: string;
}

export interface SourceLabel {
  name: string;
  detail: string;
}

/**
 * Resolve a scan source's display label through four rungs, most-specific first:
 *   operator label -> connection name -> manifest display_name -> capability_id.
 * The winning rung is `name`; the remaining plugin identity is `detail`.
 */
export function composeSourceLabel(parts: SourceLabelParts): SourceLabel {
  const operator = parts.operatorLabel?.trim() ?? "";
  const connection = parts.connectionName?.trim() ?? "";
  const display = parts.displayName?.trim() ?? "";
  const pluginIdentity = display || parts.pluginId || parts.capabilityId;
  const pluginDetail = parts.pluginId || parts.capabilityId;
  const detailWithPlugin = (detail: string) =>
    detail === pluginDetail ? detail : `${detail} · ${pluginDetail}`;

  if (operator) {
    return { name: operator, detail: detailWithPlugin(connection || pluginIdentity) };
  }
  if (connection) {
    return { name: connection, detail: detailWithPlugin(pluginIdentity) };
  }
  if (display) {
    return { name: display, detail: pluginDetail };
  }
  return { name: parts.capabilityId, detail: pluginDetail };
}

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

/**
 * Resolve the `name` rung for an Activity event/scan that references a source by
 * id. Returns "" when the reference carries no capability (caller supplies its
 * own fallback, e.g. "Autoscan").
 */
export function resolveEventSourceName(
  ref: { source_id?: string | null; capability_id?: string; plugin_id?: string | null },
  lookups: SourceLabelLookups,
): string {
  if (!ref.capability_id || !ref.plugin_id) return "";
  const source = ref.source_id ? lookups.sourceByID.get(ref.source_id) : undefined;
  const connectionName = source?.connection_id
    ? lookups.connectionByID.get(source.connection_id)
    : undefined;
  const displayName = lookups.displayNames.get(
    pluginDisplayNameKey(ref.plugin_id, ref.capability_id),
  );
  return composeSourceLabel({
    operatorLabel: source?.label,
    connectionName,
    displayName,
    capabilityId: ref.capability_id,
    pluginId: ref.plugin_id,
  }).name;
}
