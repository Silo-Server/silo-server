import { buildSchemaValues } from "@/components/admin/plugins/schemaFormUtils";
import type {
  AutoscanAvailableSource,
  AutoscanDeliveryMode,
  AutoscanScanSourceDescriptor,
  Library,
  PluginAdminFormField,
} from "@/api/types";

/**
 * Host defaults for a source whose descriptor is missing or malformed. These
 * mirror `autoscan.DefaultScanSourceDescriptor` on the server: poll delivery
 * with an optional connection, which is how every source behaved before
 * descriptors existed.
 */
export const DEFAULT_DESCRIPTOR: AutoscanScanSourceDescriptor = {
  delivery_modes: ["poll"],
  connection: "optional",
};

/**
 * Resolve the descriptor for an available source. The server always sends one,
 * but a client may be talking to an older server — falling back keeps the
 * Add-source flow working rather than rendering an empty dialog.
 */
export function descriptorFor(
  source: AutoscanAvailableSource | undefined,
): AutoscanScanSourceDescriptor {
  const descriptor = source?.descriptor;
  if (!descriptor || !descriptor.delivery_modes?.length) {
    return DEFAULT_DESCRIPTOR;
  }
  return descriptor;
}

/** Delivery mode to use when the operator is not asked to choose. */
export function defaultDeliveryMode(
  descriptor: AutoscanScanSourceDescriptor,
): AutoscanDeliveryMode {
  if (descriptor.delivery_modes.includes("poll")) return "poll";
  return descriptor.delivery_modes[0] ?? "poll";
}

/**
 * Whether the operator must be asked how changes arrive. A source supporting a
 * single mode answers the question by itself, so the step is skipped.
 */
export function needsDeliveryChoice(descriptor: AutoscanScanSourceDescriptor): boolean {
  return descriptor.delivery_modes.length > 1;
}

/**
 * Whether the connection step applies. A `none` source reaches its provider
 * without host-held credentials (local watcher, or the provider pushes to us).
 * Poll-specific: a webhook delivery never uses a bound connection, so the step
 * is hidden when the chosen mode is webhook regardless of the requirement.
 */
export function needsConnectionStep(
  descriptor: AutoscanScanSourceDescriptor,
  deliveryMode: AutoscanDeliveryMode,
): boolean {
  if (deliveryMode === "webhook") return false;
  return descriptor.connection !== "none";
}

/** A `required` connection blocks creation until one is bound. */
export function connectionIsMandatory(
  descriptor: AutoscanScanSourceDescriptor,
  deliveryMode: AutoscanDeliveryMode,
): boolean {
  return needsConnectionStep(descriptor, deliveryMode) && descriptor.connection === "required";
}

/**
 * Restrict the connection picker to the kinds a source can actually talk to.
 * An empty `connection_kinds` means "any", which is also the fallback when a
 * connection's kind is unknown to us.
 */
export function connectionMatchesKinds(
  descriptor: AutoscanScanSourceDescriptor,
  connectionKind: string,
): boolean {
  const kinds = descriptor.connection_kinds ?? [];
  if (kinds.length === 0) return true;
  return kinds.includes(connectionKind);
}

/** Config form fields, or an empty list when the source declares none. */
export function configFields(descriptor: AutoscanScanSourceDescriptor): PluginAdminFormField[] {
  return descriptor.config_form?.fields ?? [];
}

/**
 * Seed values for a source's config form, honouring each field's declared
 * default. Values are stored as strings because `source_config` is a string map
 * on the wire.
 */
export function initialConfigValues(
  descriptor: AutoscanScanSourceDescriptor,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of configFields(descriptor)) {
    if (field.default_value === undefined || field.default_value === null) continue;
    out[field.key] = field.default_value;
  }
  return out;
}

/**
 * Serialize a typed form draft into the string map `source_config` stores.
 *
 * Done once at submit rather than per keystroke: booleans and multi-selects
 * must stay typed while the renderer owns them, or `false` round-trips as the
 * truthy string "false" and an array collapses into an unreadable join.
 */
export function serializeConfigValues(
  values: Record<string, unknown>,
  descriptor?: AutoscanScanSourceDescriptor,
): Record<string, string> {
  // Delegate field selection to the shared plugin-config helper so this path
  // behaves identically: it drops fields hidden by an unsatisfied show_when
  // (whose stale values the UI presents as absent) and substitutes each
  // untouched field's declared default, so what is stored matches what the
  // operator was shown.
  const resolved = descriptor?.config_form
    ? buildSchemaValues(descriptor.config_form, values)
    : values;

  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(resolved)) {
    if (value === undefined || value === null) continue;
    out[key] = Array.isArray(value) ? value.map(String).join(",") : String(value);
  }
  return out;
}

/** Values as the renderer wants them, from the string map the API returns. */
export function parseConfigValues(
  descriptor: AutoscanScanSourceDescriptor,
  stored: Record<string, string>,
): Record<string, unknown> {
  const byKey = new Map(configFields(descriptor).map((field) => [field.key, field]));
  const out: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(stored)) {
    switch (byKey.get(key)?.control) {
      case "SWITCH":
        out[key] = value === "true";
        break;
      case "MULTI_SELECT":
        out[key] = value ? value.split(",").filter(Boolean) : [];
        break;
      case "NUMBER": {
        const parsed = Number(value);
        out[key] = value !== "" && Number.isFinite(parsed) ? parsed : value;
        break;
      }
      default:
        out[key] = value;
    }
  }
  return out;
}

// --- Fill-from sources -----------------------------------------------------

/**
 * Host-known values a config field can be populated from. Kept in sync with the
 * `FillFrom*` constants in internal/autoscan/adminform.go.
 */
export const FILL_FROM_MOVIE_LIBRARY_PATHS = "library_paths_movie";
export const FILL_FROM_TV_LIBRARY_PATHS = "library_paths_tv";

function libraryKind(type: string): "movie" | "tv" | "mixed" | null {
  switch (type.trim().toLowerCase()) {
    case "movie":
    case "movies":
      return "movie";
    case "series":
    case "show":
    case "shows":
    case "tv":
    case "tvshows":
      return "tv";
    case "mixed":
      return "mixed";
    default:
      return null;
  }
}

/**
 * Collect enabled library paths for a fill source. A mixed library contributes
 * to both movie and TV fills, since its paths can hold either.
 *
 * Returns null when the fill source is unknown, so the UI can distinguish
 * "nothing to offer" from "not offerable".
 */
export function fillValueFromLibraries(
  fillFrom: string | undefined,
  libraries: readonly Library[],
): string | null {
  if (fillFrom !== FILL_FROM_MOVIE_LIBRARY_PATHS && fillFrom !== FILL_FROM_TV_LIBRARY_PATHS) {
    return null;
  }
  const want = fillFrom === FILL_FROM_MOVIE_LIBRARY_PATHS ? "movie" : "tv";

  const paths = new Set<string>();
  for (const library of libraries) {
    if (!library.enabled) continue;
    const kind = libraryKind(library.type);
    if (kind !== want && kind !== "mixed") continue;
    for (const path of library.paths ?? []) {
      const trimmed = path.trim();
      if (trimmed) paths.add(trimmed);
    }
  }
  return Array.from(paths).join("\n");
}
