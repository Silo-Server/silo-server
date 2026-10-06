import type {
  AutoscanPathRewrite,
  AutoscanScanSourceDescriptor,
  AutoscanSource,
  AutoscanSourceInput,
  AutoscanWebhookProvider,
} from "@/api/types";

import { parseConfigValues } from "./sourceDescriptor";
import { newMapping, usableMappings, type MappingDraft } from "./webhookSetup";

/**
 * Draft state and request bodies for the source dialog. Pure functions only, so
 * the "a PUT replaces the whole source" contract can be tested without a DOM.
 */

export const WEBHOOK_PROVIDER_KEY = "webhook_provider";

export function pluginKey(pluginId: string, capabilityId: string): string {
  return `${pluginId}:${capabilityId}`;
}

export function isWebhookSource(source: Pick<AutoscanSource, "delivery_mode">): boolean {
  return source.delivery_mode === "webhook";
}

/**
 * Which arr a webhook source expects, for the setup instructions. An explicit
 * choice in the source's own config wins; otherwise a descriptor naming exactly
 * one connection kind tells us. Anything else stays "auto", which shows the
 * union of both services' triggers.
 */
export function webhookProviderOf(
  descriptor: AutoscanScanSourceDescriptor,
  sourceConfig?: Record<string, unknown>,
): AutoscanWebhookProvider | "auto" {
  const chosen = sourceConfig?.[WEBHOOK_PROVIDER_KEY];
  if (chosen === "sonarr" || chosen === "radarr") return chosen;

  const kinds = descriptor.connection_kinds ?? [];
  if (kinds.length === 1 && (kinds[0] === "sonarr" || kinds[0] === "radarr")) {
    return kinds[0];
  }
  return "auto";
}

/**
 * Legacy source_config keys that were superseded by a newer key. Rows written
 * before the rename still carry the old key, so its lines are merged into the
 * new one when the editor loads and the old key is dropped — the first save
 * from the editor migrates the row forward rather than carrying both.
 */
const LEGACY_CONFIG_KEY_ALIASES: Record<string, string> = {
  movie_nested_paths: "movie_flat_paths",
  tv_nested_paths: "tv_flat_paths",
};

/**
 * The plugin whose keys the aliases above belong to. Applying them globally
 * would silently rename an unrelated plugin's identically-named key on the
 * first full-state save, losing its configuration.
 */
const CEPHFS_PLUGIN_ID = "silo.autoscan.cephfs";
const CEPHFS_CAPABILITY_ID = "cephfs";

function ownsLegacyAliases(source: AutoscanSource): boolean {
  return source.plugin_id === CEPHFS_PLUGIN_ID && source.capability_id === CEPHFS_CAPABILITY_ID;
}

/** Merge newline-separated values, de-duplicating and dropping blanks. */
function mergeLines(...values: Array<string | undefined>): string {
  const lines = new Set<string>();
  for (const value of values) {
    (value ?? "")
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)
      .forEach((line) => lines.add(line));
  }
  return Array.from(lines).join("\n");
}

/**
 * Prepare a source's stored config for editing: pass values through as-is,
 * folding any superseded key into its replacement so nothing an operator
 * previously configured silently disappears from the form.
 */
export function sourceConfigForEdit(source: AutoscanSource): Record<string, string> {
  const config = { ...(source.source_config ?? {}) };
  if (!ownsLegacyAliases(source)) return config;

  for (const [legacyKey, currentKey] of Object.entries(LEGACY_CONFIG_KEY_ALIASES)) {
    if (config[legacyKey] === undefined) continue;
    config[currentKey] = mergeLines(config[currentKey], config[legacyKey]);
    delete config[legacyKey];
  }
  return config;
}

export function normalizeSourceConfig(config: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(config)) {
    const trimmedKey = key.trim();
    if (!trimmedKey) continue;
    out[trimmedKey] = value.trim();
  }
  return out;
}

/** The largest poll interval the server stores (a 32-bit signed integer). */
export const MAX_POLL_INTERVAL_SECONDS = 2_147_483_647;

/**
 * Parse the poll-interval text field. Blank means "use the global default".
 * Anything that is not an integer the server accepts (1 to
 * MAX_POLL_INTERVAL_SECONDS) is invalid and blocks saving.
 */
export function parseIntervalInput(
  value: string,
): { valid: true; seconds: number | null } | { valid: false } {
  const trimmed = value.trim();
  if (trimmed === "") return { valid: true, seconds: null };
  const n = Number(trimmed);
  if (!Number.isInteger(n) || n < 1 || n > MAX_POLL_INTERVAL_SECONDS) return { valid: false };
  return { valid: true, seconds: n };
}

/** Trimmed, complete rewrites only — the shape the API stores. */
export function cleanRewrites(rewrites: readonly AutoscanPathRewrite[]): AutoscanPathRewrite[] {
  return rewrites
    .map((rewrite) => ({ from: rewrite.from.trim(), to: rewrite.to.trim() }))
    .filter((rewrite) => rewrite.from.length > 0 && rewrite.to.length > 0);
}

/** Rows with exactly one side filled in: an unfinished mapping, not an empty one. */
export function incompleteMappings(drafts: readonly MappingDraft[]): MappingDraft[] {
  return drafts.filter((draft) => (draft.from.trim() === "") !== (draft.to.trim() === ""));
}

export function mappingsFromRewrites(rewrites: readonly AutoscanPathRewrite[]): MappingDraft[] {
  return rewrites.map((rewrite) => newMapping(rewrite.to, rewrite.from));
}

/**
 * The complete PUT body for a stored source, with `overrides` applied.
 *
 * The update replaces the whole source: a field left out is cleared, not kept.
 * Quick actions on the list (the enabled switch) build their body here so they
 * resend exactly what is stored.
 */
export function sourceUpdateBody(
  source: AutoscanSource,
  overrides: Partial<AutoscanSourceInput> = {},
): AutoscanSourceInput {
  return {
    connection_id: source.connection_id ?? null,
    enabled: source.enabled,
    delivery_mode: source.delivery_mode,
    poll_interval_seconds: source.poll_interval_seconds ?? null,
    path_rewrites: cleanRewrites(source.path_rewrites ?? []),
    source_config: { ...(source.source_config ?? {}) },
    label: source.label ?? "",
    ...overrides,
  };
}

/** Everything the source dialog edits, in the shape its controls want. */
export interface SourceDraft {
  /** "plugin_id:capability_id" composite key of the chosen plugin. */
  pluginKey: string;
  deliveryMode: AutoscanSource["delivery_mode"] | "";
  /** "" means no connection. */
  connectionId: string;
  /** "" means use the global default. */
  intervalStr: string;
  /** Typed while the renderer owns them; serialized only on save. */
  sourceConfig: Record<string, unknown>;
  /** False while a required or validated config field is unsatisfied. */
  configValid: boolean;
  /** Set once the operator edits config, so a late re-parse cannot clobber it. */
  configDirty: boolean;
  mappings: MappingDraft[];
  /**
   * Set once the operator edits the mapping rows. Until then the rows are the
   * dialog's own suggestion and may be re-seeded, e.g. when the provider changes.
   */
  mappingsDirty: boolean;
  label: string;
}

export const BLANK_SOURCE_DRAFT: SourceDraft = {
  pluginKey: "",
  deliveryMode: "",
  connectionId: "",
  intervalStr: "",
  sourceConfig: {},
  configValid: true,
  configDirty: false,
  mappings: [],
  mappingsDirty: false,
  label: "",
};

export function draftFromSource(
  source: AutoscanSource,
  descriptor: AutoscanScanSourceDescriptor,
): SourceDraft {
  return {
    pluginKey: pluginKey(source.plugin_id, source.capability_id),
    deliveryMode: source.delivery_mode,
    connectionId: source.connection_id ?? "",
    intervalStr: source.poll_interval_seconds != null ? String(source.poll_interval_seconds) : "",
    sourceConfig: parseConfigValues(descriptor, sourceConfigForEdit(source)),
    configValid: true,
    configDirty: false,
    mappings: mappingsFromRewrites(source.path_rewrites ?? []),
    mappingsDirty: false,
    label: source.label ?? "",
  };
}

/** Normalize the picker's value: "" and the picker's none-sentinel both mean none. */
export function draftConnectionId(draft: Pick<SourceDraft, "connectionId">): string | null {
  return draft.connectionId && draft.connectionId !== "__none__" ? draft.connectionId : null;
}

/**
 * The complete PUT body for an edited source. Every field is sent: the edit
 * dialog owns all of them, and the list's enabled switch owns `enabled`, which
 * is read from the live source rather than the dialog's snapshot.
 */
export function editedSourceBody(
  source: AutoscanSource,
  draft: SourceDraft,
  serializedConfig: Record<string, string>,
): AutoscanSourceInput {
  const interval = parseIntervalInput(draft.intervalStr);
  return {
    connection_id: draftConnectionId(draft),
    enabled: source.enabled,
    delivery_mode: source.delivery_mode,
    // Callers block saving on an invalid interval; keep the stored value
    // rather than clearing it if one slips through.
    poll_interval_seconds: interval.valid ? interval.seconds : source.poll_interval_seconds,
    path_rewrites: usableMappings(draft.mappings),
    source_config: normalizeSourceConfig(serializedConfig),
    label: draft.label.trim(),
  };
}
