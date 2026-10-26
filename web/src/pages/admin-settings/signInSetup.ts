// How the Sign-in page lays out a sign-in plugin's configuration as guided
// setup steps, and how it stages edits to it. The plugin decides the steps
// through its manifest's admin_form sections; the convention is described in
// docs/architecture/external-sign-in.md#sign-in-settings-page.
import type {
  PluginAdminForm,
  PluginAdminFormField,
  PluginAdminFormSection,
  PluginConfigSchema,
  PluginInstallation,
} from "@/api/types";
import {
  adminFormForConfigSchema,
  formValuesFromConfig,
  humanizeConfigKey,
} from "@/components/admin/plugins/configSchemaAdminForm";
import { buildSchemaValues, fieldIsVisible } from "@/components/admin/plugins/schemaFormUtils";
import { authCapabilityOf } from "@/lib/externalSignInAdmin";

/**
 * The config entry whose `value` field names the provider on the login page.
 * The server reads it for the login button, so the page gives it its own step.
 */
export const LOGIN_NAME_CONFIG_KEY = "display_name";

/**
 * Capability metadata naming a field, as "config_key.field_key", that the
 * connection test reads as the person to look up. The page asks for it in
 * the test step and never saves it.
 */
export const TEST_USERNAME_METADATA = "connection_test_username_field";

/** One saved global config entry and the form that edits it. */
export interface SignInConfigEntry {
  schema: PluginConfigSchema;
  descriptor: PluginAdminForm;
  saved: Record<string, unknown> | undefined;
  configuredSecrets: string[];
}

/** A run of fields from one config entry, shown as a step or an Advanced section. */
export interface SignInFieldGroup {
  id: string;
  schemaKey: string;
  title: string;
  description?: string;
  fields: PluginAdminFormField[];
  section?: PluginAdminFormSection;
}

export interface SignInFieldRef {
  schemaKey: string;
  field: PluginAdminFormField;
}

export interface SignInSetupLayout {
  entries: Map<string, SignInConfigEntry>;
  /** Groups shown as numbered steps, in manifest order. */
  steps: SignInFieldGroup[];
  /** Groups the plugin marked collapsible, shown under Advanced. */
  advanced: SignInFieldGroup[];
  /** The login page name, shown in the page's own Login button step. */
  loginName?: SignInFieldRef;
  /** The connection test's look-up field, shown in the test step. */
  testUsername?: SignInFieldRef;
  /** Entries whose schema the admin form can't render; edited on the plugin page. */
  unsupported: { schema: PluginConfigSchema; saved: boolean }[];
}

function parseFieldRef(value: unknown): { schemaKey: string; fieldKey: string } | null {
  if (typeof value !== "string") return null;
  const dot = value.indexOf(".");
  if (dot <= 0 || dot === value.length - 1) return null;
  return { schemaKey: value.slice(0, dot), fieldKey: value.slice(dot + 1) };
}

function schemaTitle(schema: PluginConfigSchema): string {
  return schema.title?.trim() || humanizeConfigKey(schema.key);
}

/** Splits a sign-in plugin's configuration into setup steps and Advanced sections. */
export function signInSetupLayout(installation: PluginInstallation): SignInSetupLayout {
  const layout: SignInSetupLayout = {
    entries: new Map(),
    steps: [],
    advanced: [],
    unsupported: [],
  };
  const testRef = parseFieldRef(authCapabilityOf(installation)?.metadata?.[TEST_USERNAME_METADATA]);

  for (const schema of installation.global_config_schema ?? []) {
    const descriptor = adminFormForConfigSchema(schema);
    const saved = installation.global_configs?.find((entry) => entry.key === schema.key);
    if (!descriptor) {
      layout.unsupported.push({ schema, saved: saved !== undefined });
      continue;
    }
    layout.entries.set(schema.key, {
      schema,
      descriptor,
      saved: saved?.value,
      configuredSecrets: saved?.configured_secrets ?? [],
    });

    const nameField =
      schema.key === LOGIN_NAME_CONFIG_KEY
        ? descriptor.fields.find((field) => field.key === "value")
        : undefined;
    if (nameField && descriptor.fields.length === 1) {
      layout.loginName = { schemaKey: schema.key, field: nameField };
      continue;
    }

    let hiddenKey: string | null = null;
    if (testRef?.schemaKey === schema.key) {
      const field = descriptor.fields.find((candidate) => candidate.key === testRef.fieldKey);
      if (field) {
        layout.testUsername = { schemaKey: schema.key, field };
        hiddenKey = field.key;
      }
    }

    const byKey = new Map(descriptor.fields.map((field) => [field.key, field]));
    const sections = descriptor.sections ?? [];
    const sectioned = new Set(sections.flatMap((section) => section.field_keys));
    const loose = descriptor.fields.filter(
      (field) => !sectioned.has(field.key) && field.key !== hiddenKey,
    );
    let describedSchema = false;
    if (loose.length > 0) {
      layout.steps.push({
        id: schema.key,
        schemaKey: schema.key,
        title: schemaTitle(schema),
        description: schema.description,
        fields: loose,
      });
      describedSchema = true;
    }
    for (const section of sections) {
      const fields = section.field_keys
        .filter((key) => key !== hiddenKey)
        .map((key) => byKey.get(key))
        .filter((field): field is PluginAdminFormField => field !== undefined);
      if (fields.length === 0) continue;
      const group: SignInFieldGroup = {
        id: `${schema.key}.${section.key}`,
        schemaKey: schema.key,
        title: section.title?.trim() || schemaTitle(schema),
        // The entry's own description belongs to its first group.
        description: section.description || (describedSchema ? undefined : schema.description),
        fields,
        section,
      };
      describedSchema = true;
      (section.collapsible ? layout.advanced : layout.steps).push(group);
    }
  }
  return layout;
}

function isSecret(field: PluginAdminFormField) {
  return field.secret || field.control === "PASSWORD";
}

function hasSavedValue(entry: SignInConfigEntry, field: PluginAdminFormField): boolean {
  if (isSecret(field)) {
    return entry.configuredSecrets.includes(field.key);
  }
  const value = entry.saved?.[field.key];
  if (typeof value === "string") return value.trim() !== "";
  return value !== undefined && value !== null;
}

/** A required field with nothing saved yet. */
export function isFieldMissing(entry: SignInConfigEntry, field: PluginAdminFormField): boolean {
  return field.required && !hasSavedValue(entry, field);
}

/**
 * What the saved configuration still lacks before the provider can be turned
 * on: every required field with no saved value, and every required entry
 * that has never been saved and has no required fields of its own.
 */
export function missingSignInSetup(layout: SignInSetupLayout): string[] {
  const missing: string[] = [];
  for (const { schema, saved } of layout.unsupported) {
    if (schema.required && !saved) missing.push(schemaTitle(schema));
  }
  for (const entry of layout.entries.values()) {
    const savedValues = formValuesFromConfig(entry.descriptor.fields, entry.saved);
    const required = entry.descriptor.fields.filter(
      (field) => field.required && fieldIsVisible(entry.descriptor, field, savedValues),
    );
    if (required.length === 0) {
      if (entry.schema.required && entry.saved === undefined)
        missing.push(schemaTitle(entry.schema));
      continue;
    }
    for (const field of required) {
      if (!hasSavedValue(entry, field)) missing.push(field.label || humanizeConfigKey(field.key));
    }
  }
  return missing;
}

/** Unsaved edits to one config entry. */
export interface ConfigDraft {
  values: Record<string, unknown>;
  clearSecrets: string[];
}

// Global config saves merge into the stored value, so an emptied field must
// be sent as an explicit clear or the stored value stays.
const SAVE_OPTIONS = { explicitClears: true };

export function savedFormValues(entry: SignInConfigEntry): Record<string, unknown> {
  return formValuesFromConfig(entry.descriptor.fields, entry.saved);
}

/** Whether one field of a draft differs from what is saved. */
export function fieldChanged(
  entry: SignInConfigEntry,
  draft: ConfigDraft | undefined,
  field: PluginAdminFormField,
): boolean {
  if (!draft) return false;
  if (isSecret(field)) {
    const typed = draft.values[field.key];
    return (typeof typed === "string" && typed !== "") || draft.clearSecrets.includes(field.key);
  }
  return draft.values[field.key] !== savedFormValues(entry)[field.key];
}

/** How many fields of a draft differ from what is saved. */
export function changedFieldCount(entry: SignInConfigEntry, draft: ConfigDraft | undefined) {
  if (!draft) return 0;
  return entry.descriptor.fields.filter((field) => fieldChanged(entry, draft, field)).length;
}

/** The entry a draft would save, as the config and connection-test endpoints take it. */
export function stagedEntry(entry: SignInConfigEntry, values: Record<string, unknown>) {
  return buildSchemaValues(entry.descriptor, values, undefined, SAVE_OPTIONS);
}
