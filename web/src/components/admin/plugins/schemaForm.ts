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
): boolean {
  if (!conditions || conditions.length === 0) return true;
  return conditions.every((c) => c.equals.includes(stringify(values[c.field])));
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
    if (!evaluateShowWhen(field.show_when, values)) continue;
    const raw = values[field.key];
    if (field.required && isEmpty(raw)) {
      errors[field.key] = `${field.label || field.key} is required`;
      continue;
    }
    if (isEmpty(raw)) continue;
    const v = field.validation;
    if (typeof raw === "string") {
      if (v?.pattern && !new RegExp(v.pattern).test(raw)) {
        errors[field.key] = `${field.label || field.key} is invalid`;
      } else if (v?.min_length && raw.length < v.min_length) {
        errors[field.key] = `${field.label || field.key} must be at least ${v.min_length} characters`;
      } else if (v?.max_length && raw.length > v.max_length) {
        errors[field.key] = `${field.label || field.key} must be at most ${v.max_length} characters`;
      }
    }
    if (isNumberControl(field) && typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      if (Number.isNaN(n)) errors[field.key] = `${field.label || field.key} must be a number`;
      else if (v?.has_min && n < (v.min ?? 0)) errors[field.key] = `${field.label || field.key} must be ≥ ${v.min}`;
      else if (v?.has_max && n > (v.max ?? 0)) errors[field.key] = `${field.label || field.key} must be ≤ ${v.max}`;
    }
  }
  return errors;
}

function coerceNumericString(value: unknown): unknown {
  return typeof value === "string" && /^-?\d+$/.test(value) ? Number(value) : value;
}

export function coerceFieldValue(field: PluginAdminFormField, raw: unknown): unknown {
  if (field.control === "SWITCH") return Boolean(raw);
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

export function buildSchemaValues(
  descriptor: PluginAdminForm,
  draft: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of descriptor.fields) {
    if (!evaluateShowWhen(field.show_when, draft)) continue; // don't persist hidden fields' stale values
    const coerced = coerceFieldValue(field, draft[field.key]);
    if (coerced === undefined) continue;
    out[field.key] = coerced;
  }
  return out;
}
