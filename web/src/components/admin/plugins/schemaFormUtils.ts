import type { PluginAdminForm, PluginAdminFormCondition, PluginAdminFormField } from "@/api/types";
import { t as appT, type MessageKey, type TextValues } from "@/i18n";

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

/**
 * Client-side checks of a plugin form. `t` formats the messages; a component
 * passes its `useT()` so the messages follow a language change.
 */
export function validateSchemaValues(
  descriptor: PluginAdminForm,
  values: Record<string, unknown>,
  t: (id: MessageKey, values?: TextValues) => string = appT,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const field of descriptor.fields) {
    if (!fieldIsVisible(descriptor, field, values)) continue;
    const raw = effectiveValue(field, values);
    const label = field.label || field.key;
    if (field.required && isEmpty(raw)) {
      errors[field.key] = t("web.common.schemaForm.required", { label });
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
        errors[field.key] = t("web.common.schemaForm.invalid", { label });
      } else if (v?.min_length && raw.length < v.min_length) {
        errors[field.key] = t("web.common.schemaForm.minLength", {
          label,
          count: v.min_length,
          count_text: v.min_length,
        });
      } else if (v?.max_length && raw.length > v.max_length) {
        errors[field.key] = t("web.common.schemaForm.maxLength", {
          label,
          count: v.max_length,
          count_text: v.max_length,
        });
      }
    }
    if (isNumberControl(field) && typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      if (Number.isNaN(n)) errors[field.key] = t("web.common.schemaForm.notNumber", { label });
      else if (v?.has_min && n < (v.min ?? 0))
        errors[field.key] = t("web.common.schemaForm.min", { label, min: v.min ?? 0 });
      else if (v?.has_max && n > (v.max ?? 0))
        errors[field.key] = t("web.common.schemaForm.max", { label, max: v.max ?? 0 });
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
 * suffix `:int` / `:num` / `:bool` records the item type; plain `array` holds
 * string items or items of unknown type, and passes them through unchanged.
 */
export type FieldType =
  | "string"
  | "integer"
  | "number"
  | "boolean"
  | "array"
  | "array:bool"
  | "array:int"
  | "array:num";

type ItemType = "string" | "integer" | "number" | "boolean";

/**
 * The schema a local `#/$defs/<name>` or `#/definitions/<name>` reference
 * points at, or the schema itself when it isn't one.
 */
function resolveLocalRef(schema: unknown, root: object): unknown {
  const ref = (schema as { $ref?: unknown } | null)?.$ref;
  if (typeof ref !== "string") return schema;
  const [, section, name] = /^#\/(\$defs|definitions)\/([^/]+)$/.exec(ref) ?? [];
  if (!section || !name) return undefined;
  const defs = (root as Record<string, unknown>)[section];
  if (!defs || typeof defs !== "object") return undefined;
  // JSON Pointer escapes "/" as "~1" and "~" as "~0" (RFC 6901).
  return (defs as Record<string, unknown>)[name.replaceAll("~1", "/").replaceAll("~0", "~")];
}

/**
 * The type of an array's items: a plain `type`, a nullable type list such as
 * `["string", "null"]`, or the type every `enum` value shares, following a
 * local `$ref` first.
 */
function arrayItemType(itemSchema: unknown, root: object): ItemType | undefined {
  const items = resolveLocalRef(itemSchema, root);
  if (!items || typeof items !== "object") return undefined;
  const { type, enum: values } = items as { type?: unknown; enum?: unknown };
  const types = Array.isArray(type) ? type.filter((t) => t !== "null") : [type];
  if (types.length === 1 && ["string", "integer", "number", "boolean"].includes(types[0])) {
    return types[0] as ItemType;
  }
  if (!Array.isArray(values) || values.length === 0) return undefined;
  if (values.every((v) => typeof v === "string")) return "string";
  if (values.every((v) => typeof v === "boolean")) return "boolean";
  if (values.every((v) => Number.isInteger(v))) return "integer";
  if (values.every((v) => typeof v === "number")) return "number";
  return undefined;
}

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
      const itemType = arrayItemType((value as { items?: unknown }).items, parsed);
      if (itemType === "integer") out[key] = "array:int";
      else if (itemType === "number") out[key] = "array:num";
      else if (itemType === "boolean") out[key] = "array:bool";
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
      case "array:bool":
      case "array:int":
      case "array:num": {
        const arr = Array.isArray(raw) ? raw : [];
        if (fieldType === "array") return arr;
        // A saved list loads as is, so keep a null item a nullable type allows.
        if (fieldType === "array:bool") return arr.map((v) => (v === null ? v : coerceBoolean(v)));
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

export function fieldIsVisible(
  descriptor: PluginAdminForm,
  field: PluginAdminFormField,
  values: Record<string, unknown>,
): boolean {
  if (!evaluateShowWhen(field.show_when, values, descriptor.fields)) return false;
  const containingSections = (descriptor.sections ?? []).filter((section) =>
    section.field_keys.includes(field.key),
  );
  return (
    containingSections.length === 0 ||
    containingSections.some((section) =>
      evaluateShowWhen(section.show_when, values, descriptor.fields),
    )
  );
}

export type BuildSchemaValuesOptions = {
  /**
   * Send an emptied non-secret field as an explicit clear ("" for a string
   * field, null otherwise) instead of leaving it out. Plugin global config
   * saves merge into the stored value, so an omitted field keeps what was
   * saved; the server treats an explicit empty or null as clearing it.
   * Secret fields stay omitted: a blank secret keeps the stored one, and
   * clearing it is the separate clear_secrets action.
   */
  explicitClears?: boolean;
};

/**
 * The explicit clear sent for an emptied field: "" for a declared (or
 * text-control) string, null for numbers and dynamic selects.
 */
export function clearedFieldValue(field: PluginAdminFormField, fieldType?: FieldType): "" | null {
  if (fieldType !== undefined) return fieldType === "string" ? "" : null;
  return field.control === "NUMBER" || field.dynamic_options ? null : "";
}

export function buildSchemaValues(
  descriptor: PluginAdminForm,
  draft: Record<string, unknown>,
  fieldTypes?: Record<string, FieldType>,
  options?: BuildSchemaValuesOptions,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of descriptor.fields) {
    if (!fieldIsVisible(descriptor, field, draft)) continue; // don't persist hidden fields' stale values
    // Fall back to the declared default for untouched fields so an unmodified
    // default persists exactly as it is displayed.
    const rawSource = draft[field.key] !== undefined ? draft[field.key] : field.default_value;
    const coerced = coerceFieldValue(field, rawSource, fieldTypes?.[field.key]);
    if (coerced === undefined) {
      if (options?.explicitClears && !field.secret && field.control !== "PASSWORD") {
        out[field.key] = clearedFieldValue(field, fieldTypes?.[field.key]);
      }
      continue;
    }
    out[field.key] = coerced;
  }
  return out;
}
