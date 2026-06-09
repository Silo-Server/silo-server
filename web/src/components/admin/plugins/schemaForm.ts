import type { PluginAdminForm, PluginAdminFormCondition, PluginAdminFormField } from "@/api/types";

export type SchemaOption = { value: string; label: string };

function stringify(value: unknown): string {
  if (typeof value === "boolean") return value ? "true" : "false";
  if (value === null || value === undefined) return "";
  return String(value);
}

export function evaluateShowWhen(
  conditions: PluginAdminFormCondition[] | undefined,
  values: Record<string, unknown>,
  fields?: PluginAdminFormField[],
): boolean {
  if (!conditions || conditions.length === 0) return true;
  return conditions.every((c) =>
    c.equals.includes(stringify(conditionValue(c.field, values, fields))),
  );
}

function conditionValue(
  key: string,
  values: Record<string, unknown>,
  fields?: PluginAdminFormField[],
): unknown {
  if (values[key] !== undefined) return values[key];
  return fields?.find((field) => field.key === key)?.default_value;
}

function isNumberControl(field: PluginAdminFormField): boolean {
  return field.control === "NUMBER";
}

function isEmpty(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === "string") return value.trim() === "";
  if (Array.isArray(value)) return value.length === 0;
  return false;
}

export function validateSchemaValues(
  descriptor: PluginAdminForm,
  values: Record<string, unknown>,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const field of descriptor.fields) {
    if (!evaluateShowWhen(field.show_when, values, descriptor.fields)) continue;
    const raw = effectiveValue(field, values);
    if (field.required && isEmpty(raw)) {
      errors[field.key] = `${field.label || field.key} is required`;
      continue;
    }
    if (isEmpty(raw)) continue;
    const v = field.validation;
    if (typeof raw === "string") {
      let patternFailed = false;
      if (v?.pattern) {
        let re: RegExp | null = null;
        try {
          re = new RegExp(v.pattern);
        } catch {
          // A malformed plugin pattern is treated as "no pattern constraint".
          re = null;
        }
        patternFailed = re !== null && !re.test(raw);
      }
      if (patternFailed) {
        errors[field.key] = `${field.label || field.key} is invalid`;
      } else if (v?.min_length && raw.length < v.min_length) {
        errors[field.key] =
          `${field.label || field.key} must be at least ${v.min_length} characters`;
      } else if (v?.max_length && raw.length > v.max_length) {
        errors[field.key] =
          `${field.label || field.key} must be at most ${v.max_length} characters`;
      }
    }
    if (isNumberControl(field) && typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      if (Number.isNaN(n)) errors[field.key] = `${field.label || field.key} must be a number`;
      else if (v?.has_min && n < (v.min ?? 0))
        errors[field.key] = `${field.label || field.key} must be ≥ ${v.min}`;
      else if (v?.has_max && n > (v.max ?? 0))
        errors[field.key] = `${field.label || field.key} must be ≤ ${v.max}`;
    }
  }
  return errors;
}

function coerceNumericString(value: unknown): unknown {
  return typeof value === "string" && /^-?\d+$/.test(value) ? Number(value) : value;
}

// coerceNumberString converts integer OR decimal numeric strings to numbers,
// leaving non-numeric strings (and non-strings) untouched. Used for array:num.
function coerceNumberString(value: unknown): unknown {
  if (typeof value !== "string") return value;
  const trimmed = value.trim();
  if (trimmed === "" || !/^-?\d+(\.\d+)?$/.test(trimmed)) return value;
  const n = Number(trimmed);
  return Number.isNaN(n) ? value : n;
}

// coerceBoolean parses booleans without the Boolean() pitfall where any
// non-empty string (including "false") becomes true.
function coerceBoolean(value: unknown): boolean {
  if (typeof value === "boolean") return value;
  if (typeof value === "string") return value.trim().toLowerCase() === "true";
  return Boolean(value);
}

/**
 * A declared field type, derived from the plugin's json_schema. For arrays the
 * suffix `:int` / `:num` records the item type so MULTI_SELECT elements coerce
 * to numbers only when the json_schema says they are numeric.
 */
export type FieldType =
  | "string"
  | "integer"
  | "number"
  | "boolean"
  | "array"
  | "array:int"
  | "array:num";

/**
 * Parse the declared property types out of a plugin's (object) json_schema.
 * Tolerant: an invalid/empty/non-object schema yields `{}` so callers fall back
 * to the existing control-based coercion heuristic.
 */
export function parseFieldTypes(jsonSchema: string | undefined | null): Record<string, FieldType> {
  if (!jsonSchema) return {};
  let parsed: unknown;
  try {
    parsed = JSON.parse(jsonSchema);
  } catch {
    return {};
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
  const props = (parsed as { properties?: unknown }).properties;
  if (!props || typeof props !== "object" || Array.isArray(props)) return {};

  const out: Record<string, FieldType> = {};
  for (const [key, value] of Object.entries(props as Record<string, unknown>)) {
    if (!value || typeof value !== "object") continue;
    const type = (value as { type?: unknown }).type;
    if (type === "array") {
      const items = (value as { items?: unknown }).items;
      const itemType =
        items && typeof items === "object" ? (items as { type?: unknown }).type : undefined;
      if (itemType === "integer") out[key] = "array:int";
      else if (itemType === "number") out[key] = "array:num";
      else out[key] = "array";
    } else if (type === "string" || type === "integer" || type === "number" || type === "boolean") {
      out[key] = type;
    }
  }
  return out;
}

/**
 * Coerce a single field's raw form value into the value persisted/sent to the
 * plugin. When `fieldType` (the declared json_schema type) is supplied,
 * coercion is type-driven; otherwise it falls back to the legacy control-based
 * heuristic so plugins without a json_schema keep working.
 */
export function coerceFieldValue(
  field: PluginAdminFormField,
  raw: unknown,
  fieldType?: FieldType,
): unknown {
  if (fieldType !== undefined) {
    switch (fieldType) {
      case "boolean":
        return coerceBoolean(raw);
      case "integer":
      case "number": {
        if (typeof raw === "number") return raw;
        if (typeof raw === "string") {
          if (raw.trim() === "") return undefined;
          const n = Number(raw);
          return Number.isNaN(n) ? raw : n;
        }
        return raw;
      }
      case "string": {
        // NEVER coerce a declared string to a number, even if all-digits.
        if (typeof raw === "string") {
          return raw.trim() === "" ? undefined : raw;
        }
        return raw;
      }
      case "array":
      case "array:int":
      case "array:num": {
        const arr = Array.isArray(raw) ? raw : [];
        if (fieldType === "array") return arr;
        if (fieldType === "array:int") return arr.map((v) => coerceNumericString(v));
        return arr.map((v) => coerceNumberString(v));
      }
    }
  }

  // Legacy control-based heuristic (no declared type for this field).
  if (field.control === "SWITCH") return coerceBoolean(raw);
  if (field.control === "MULTI_SELECT") {
    const arr = Array.isArray(raw) ? raw : [];
    return arr.map((v) => coerceNumericString(v));
  }
  if (field.control === "SELECT" && field.dynamic_options) {
    if (typeof raw === "string" && raw.trim() === "") return undefined;
    return coerceNumericString(raw);
  }
  if (field.control === "NUMBER") {
    if (typeof raw === "number") return raw;
    if (typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      return Number.isNaN(n) ? raw : n;
    }
    return undefined;
  }
  if (typeof raw === "string") {
    const t = raw.trim();
    return t === "" ? undefined : raw;
  }
  return raw;
}

/**
 * The value to display/persist for a field: the explicit form value when
 * present, otherwise the field's declared `default_value`.
 */
export function effectiveValue(
  field: PluginAdminFormField,
  values: Record<string, unknown>,
): unknown {
  return values[field.key] !== undefined ? values[field.key] : field.default_value;
}

export function buildSchemaValues(
  descriptor: PluginAdminForm,
  draft: Record<string, unknown>,
  fieldTypes?: Record<string, FieldType>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of descriptor.fields) {
    if (!evaluateShowWhen(field.show_when, draft, descriptor.fields)) continue; // don't persist hidden fields' stale values
    // Fall back to the declared default for untouched fields so an unmodified
    // default persists exactly as it is displayed.
    const rawSource = draft[field.key] !== undefined ? draft[field.key] : field.default_value;
    const coerced = coerceFieldValue(field, rawSource, fieldTypes?.[field.key]);
    if (coerced === undefined) continue;
    out[field.key] = coerced;
  }
  return out;
}
