import { SETTING_DEFINITIONS, type SettingDefinition, type SettingKey } from "./settingsContract";

/**
 * Client-side settings resolution, mirroring the server's
 * internal/settingsresolve semantics exactly: the definition's declared
 * resolution order decides which stored value wins, an identity absent from the
 * context drops the scopes that need it, and policy constraints narrow the
 * answer without destroying what the user authored.
 *
 * The server remains the authority for online resolution (the
 * /settings/values/effective endpoint); this module exists so the web client
 * can resolve from rows it already holds — and so the cross-platform
 * conformance fixture in contracts/settings/v1/conformance.json has a web
 * implementation to run against. Every behavioral choice here is pinned by
 * settingsConformance.test.ts; do not change one without the fixture agreeing.
 */

/** The remote storage scopes the server resolves. */
export type RemoteSettingScope =
  | "account"
  | "profile"
  | "profile_device"
  | "profile_client"
  | "profile_library"
  | "profile_series";

/** Where a resolved value came from, or "default" when nothing was stored. */
export type ResolvedSettingSource = RemoteSettingScope | "default";

export type SettingConstraintKind = "ceiling" | "floor" | "allowlist" | "locked";

/** One stored row, as the server's values API reports it. */
export interface StoredSettingRow {
  key: string;
  scope: RemoteSettingScope;
  profileId?: string;
  deviceId?: string;
  clientFamily?: string;
  libraryId?: number;
  seriesId?: string;
  value: unknown;
}

/** The identity a resolution happens against. Absent fields drop their scopes. */
export interface SettingResolutionContext {
  profileId?: string;
  deviceId?: string;
  clientFamily?: string;
  libraryIds?: readonly number[];
  seriesIds?: readonly string[];
}

/** A constraint attached to one key, overriding what the manifest binds. */
export interface SettingConstraintBinding {
  policyInput: string;
  constraint: SettingConstraintKind;
}

export interface ResolvedSetting {
  key: SettingKey;
  value: unknown;
  source: ResolvedSettingSource;
  /** True when a policy constraint narrowed value away from what was stored. */
  constrained: boolean;
  /** What the user authored (may be null); present only when constrained. */
  storedValue?: unknown;
  constraintKind?: SettingConstraintKind;
}

/**
 * Resolve the effective value for each requested key against stored rows.
 *
 * Unknown and client_local keys are omitted rather than erroring, matching the
 * server: they have no server-resolved answer. `constraintBindings` lets a
 * caller (in practice, the conformance runner) attach a constraint to a key the
 * shipped manifest does not bind; a key without an entry uses the manifest's
 * own binding.
 */
export function resolveSettingValues(
  keys: readonly string[],
  stored: readonly StoredSettingRow[],
  context: SettingResolutionContext,
  constraints?: Readonly<Record<string, unknown>>,
  constraintBindings?: Readonly<Record<string, SettingConstraintBinding>>,
): ResolvedSetting[] {
  const out: ResolvedSetting[] = [];
  const seen = new Set<string>();
  for (const key of keys) {
    if (seen.has(key)) continue;
    seen.add(key);
    if (!(key in SETTING_DEFINITIONS)) continue;
    const def = SETTING_DEFINITIONS[key as SettingKey];
    if (def.persistence !== "remote") continue;
    out.push(resolveOne(def, stored, context, constraints, constraintBindings?.[key]));
  }
  return out;
}

function resolveOne(
  def: SettingDefinition,
  stored: readonly StoredSettingRow[],
  context: SettingResolutionContext,
  constraints: Readonly<Record<string, unknown>> | undefined,
  bindingOverride: SettingConstraintBinding | undefined,
): ResolvedSetting {
  const candidates = stored.filter((row) => row.key === def.key);

  let value: unknown = def.defaultValue;
  let source: ResolvedSettingSource = "default";
  for (const scope of def.resolutionOrder) {
    if (scope === "default") break;
    const row = pickForScope(scope, candidates, context);
    if (!row) continue;
    value = row.value;
    source = scope as RemoteSettingScope;
    break;
  }

  return applyConstraint(
    def,
    { key: def.key, value, source, constrained: false },
    constraints,
    bindingOverride,
  );
}

/**
 * pickForScope returns the candidate row for one scope, mirroring the server:
 * an identity missing from the context matches nothing, and a tie between
 * several content rows breaks deterministically by (libraryId, seriesId).
 */
function pickForScope(
  scope: string,
  candidates: readonly StoredSettingRow[],
  context: SettingResolutionContext,
): StoredSettingRow | undefined {
  const profileId = context.profileId ?? "";
  const deviceId = context.deviceId ?? "";
  const clientFamily = context.clientFamily ?? "";
  const matches = candidates.filter((row) => {
    if (row.scope !== scope) return false;
    switch (scope) {
      case "account":
        return true;
      case "profile":
        return (row.profileId ?? "") === profileId;
      case "profile_device":
        return (
          (row.profileId ?? "") === profileId &&
          (row.deviceId ?? "") === deviceId &&
          deviceId !== ""
        );
      case "profile_client":
        return (
          (row.profileId ?? "") === profileId &&
          (row.clientFamily ?? "") === clientFamily &&
          clientFamily !== ""
        );
      case "profile_library":
        return (
          (row.profileId ?? "") === profileId &&
          (context.libraryIds ?? []).includes(row.libraryId ?? 0)
        );
      case "profile_series":
        return (
          (row.profileId ?? "") === profileId &&
          (context.seriesIds ?? []).includes(row.seriesId ?? "")
        );
      default:
        return false;
    }
  });
  if (matches.length > 1) {
    matches.sort((a, b) => {
      const byLibrary = (a.libraryId ?? 0) - (b.libraryId ?? 0);
      if (byLibrary !== 0) return byLibrary;
      return (a.seriesId ?? "").localeCompare(b.seriesId ?? "");
    });
  }
  return matches[0];
}

/**
 * applyConstraint narrows an effective value to what policy permits without
 * destroying the authored value: a preference capped today must take effect
 * the day the cap lifts.
 */
function applyConstraint(
  def: SettingDefinition,
  resolved: ResolvedSetting,
  constraints: Readonly<Record<string, unknown>> | undefined,
  bindingOverride: SettingConstraintBinding | undefined,
): ResolvedSetting {
  const binding = bindingOverride ?? def.constrainedBy;
  if (!binding || !constraints || !(binding.policyInput in constraints)) {
    return resolved;
  }
  const limit = constraints[binding.policyInput];

  const narrowed = narrowValue(def, binding.constraint, resolved.value, limit);
  if (!narrowed.changed) return resolved;
  return {
    ...resolved,
    value: narrowed.value,
    storedValue: resolved.value,
    constrained: true,
    constraintKind: binding.constraint,
  };
}

function narrowValue(
  def: SettingDefinition,
  kind: SettingConstraintKind,
  value: unknown,
  limit: unknown,
): { value: unknown; changed: boolean } {
  switch (kind) {
    case "locked":
      // The policy value replaces the user's outright.
      if (jsonEquals(value, limit)) return { value, changed: false };
      return { value: limit, changed: true };

    case "ceiling":
      // null on a nullable numeric means "no cap of my own" — unbounded
      // above, which is exactly what a ceiling exists to bring down. It has
      // no rank, so a plain comparison would let it slip past the cap.
      if (value === null && isNumeric(def)) return { value: limit, changed: true };
      if (compareValues(def, value, limit) <= 0) return { value, changed: false };
      return { value: limit, changed: true };

    case "floor":
      // The mirror: unbounded above already satisfies any floor.
      if (value === null && isNumeric(def)) return { value, changed: false };
      if (compareValues(def, value, limit) >= 0) return { value, changed: false };
      return { value: limit, changed: true };

    case "allowlist": {
      if (!Array.isArray(limit) || limit.length === 0) return { value, changed: false };
      if (limit.some((entry) => jsonEquals(entry, value))) return { value, changed: false };
      // Falling back to the first allowed member rather than the default:
      // the default may itself be outside the allowlist, and an effective
      // value the policy forbids is the one thing this must never return.
      return { value: limit[0], changed: true };
    }
  }
}

/**
 * compareValues ranks two values through the definition's own schema: numbers
 * numerically, ordered enums by member position. Anything unrankable compares
 * equal, matching the server.
 */
function compareValues(def: SettingDefinition, a: unknown, b: unknown): number {
  if (isNumeric(def)) {
    if (typeof a !== "number" || typeof b !== "number") return 0;
    return a < b ? -1 : a > b ? 1 : 0;
  }
  if (def.type === "enum" && def.ordered && def.values) {
    const left = def.values.findIndex((member) => jsonEquals(member.value, a));
    const right = def.values.findIndex((member) => jsonEquals(member.value, b));
    if (left < 0 || right < 0) return 0;
    return left < right ? -1 : left > right ? 1 : 0;
  }
  return 0;
}

function isNumeric(def: SettingDefinition): boolean {
  return def.type === "integer" || def.type === "number";
}

/** Structural equality over JSON values, ignoring object key order. */
export function jsonEquals(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a === null || b === null) return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((entry, index) => jsonEquals(entry, b[index]));
  }
  if (typeof a === "object" && typeof b === "object") {
    const left = a as Record<string, unknown>;
    const right = b as Record<string, unknown>;
    const keys = Object.keys(left);
    if (keys.length !== Object.keys(right).length) return false;
    return keys.every((key) => key in right && jsonEquals(left[key], right[key]));
  }
  return false;
}
