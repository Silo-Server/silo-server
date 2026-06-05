import type { AutoscanAvailableSource, AutoscanSource } from "@/api/types";

export interface SourceLabelParts {
  operatorLabel?: string | null;
  connectionName?: string | null;
  displayName?: string | null;
  capabilityId: string;
  installationId: number;
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
  const pluginSuffix = `plugin #${parts.installationId}`;
  const pluginIdentity = display || parts.capabilityId;

  if (operator) {
    return { name: operator, detail: `${connection || pluginIdentity} · ${pluginSuffix}` };
  }
  if (connection) {
    return { name: connection, detail: `${pluginIdentity} · ${pluginSuffix}` };
  }
  if (display) {
    return { name: display, detail: pluginSuffix };
  }
  return { name: parts.capabilityId, detail: pluginSuffix };
}

/** Stable key for the (installation, capability) -> manifest display_name map. */
export function pluginDisplayNameKey(installationId: number, capabilityId: string): string {
  return `${installationId}:${capabilityId}`;
}

/** Build the (installation, capability) -> display_name lookup from the picker list. */
export function buildPluginDisplayNames(available: AutoscanAvailableSource[]): Map<string, string> {
  const map = new Map<string, string>();
  for (const a of available) {
    map.set(pluginDisplayNameKey(a.installation_id, a.capability_id), a.display_name);
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
  ref: { source_id?: string | null; capability_id?: string; installation_id?: number | null },
  lookups: SourceLabelLookups,
): string {
  if (!ref.capability_id || ref.installation_id == null) return "";
  const source = ref.source_id ? lookups.sourceByID.get(ref.source_id) : undefined;
  const connectionName = source?.connection_id
    ? lookups.connectionByID.get(source.connection_id)
    : undefined;
  const displayName = lookups.displayNames.get(
    pluginDisplayNameKey(ref.installation_id, ref.capability_id),
  );
  return composeSourceLabel({
    operatorLabel: source?.label,
    connectionName,
    displayName,
    capabilityId: ref.capability_id,
    installationId: ref.installation_id,
  }).name;
}
