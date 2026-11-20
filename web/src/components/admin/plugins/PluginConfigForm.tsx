import { useEffect, useMemo, useState } from "react";

import type {
  ConnectionCheckResponse,
  PluginAdminForm,
  PluginAdminFormField,
  PluginConfigSchema,
} from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { useT } from "@/i18n";

import {
  adminFormForConfigSchema,
  formValuesFromConfig,
  humanizeConfigKey,
} from "./configSchemaAdminForm";
import { SchemaForm } from "./SchemaForm";
import {
  buildSchemaValues,
  parseFieldTypes,
  type BuildSchemaValuesOptions,
} from "./schemaFormUtils";

type PluginConfigValue = Record<string, unknown>;

const EMPTY_FIELDS: PluginAdminFormField[] = [];
const NO_MANAGED_FIELDS: readonly string[] = [];
// Global config saves merge into the stored value, so an emptied field must
// be sent as an explicit clear or the stored value stays.
const SAVE_OPTIONS: BuildSchemaValuesOptions = { explicitClears: true };

type Props = {
  schema: PluginConfigSchema;
  value?: PluginConfigValue;
  configuredSecrets?: string[];
  managedFields?: readonly string[];
  onSave: (key: string, value: PluginConfigValue, clearSecrets: string[]) => void;
  onTest?: (
    key: string,
    value: PluginConfigValue,
    clearSecrets: string[],
  ) => Promise<ConnectionCheckResponse>;
  isSaving?: boolean;
  isTesting?: boolean;
  /**
   * Reports each edit as the entry it would save, so a page can test staged
   * entries together (the Sign-in page's connection test).
   */
  onDraftChange?: (key: string, value: PluginConfigValue, clearSecrets: string[]) => void;
  /**
   * Leave out the form's own title, description, and border, for a page panel
   * that already shows them.
   */
  bare?: boolean;
  /**
   * Prefix for the field ids, schema.key by default. A page that shows the
   * same schema key for several plugins must pass one that tells them apart,
   * or the labels of one form point at another's inputs.
   */
  idPrefix?: string;
};

export function PluginConfigForm({
  schema,
  value,
  configuredSecrets = [],
  managedFields = NO_MANAGED_FIELDS,
  onSave,
  onTest,
  isSaving = false,
  isTesting = false,
  onDraftChange,
  bare = false,
  idPrefix,
}: Props) {
  const t = useT();
  const inferredDescriptor = useMemo(() => adminFormForConfigSchema(schema), [schema]);
  const fields = inferredDescriptor?.fields ?? EMPTY_FIELDS;
  const supported = inferredDescriptor != null;
  const lockedKeys = Object.fromEntries(
    managedFields.map((key) => [key, t("web.admin.plugins.provisioning.managed")]),
  );
  const allFieldsManaged =
    fields.length > 0 && fields.every((field) => managedFields.includes(field.key));

  const descriptor = useMemo<PluginAdminForm>(() => {
    const base = inferredDescriptor ?? { fields };
    const configured = new Set(configuredSecrets);
    return {
      ...base,
      fields: base.fields.map((field) =>
        configured.has(field.key) && (field.secret || field.control === "PASSWORD")
          ? { ...field, placeholder: t("web.admin.plugins.configForm.savedSecretPlaceholder") }
          : field,
      ),
    };
  }, [configuredSecrets, fields, inferredDescriptor, t]);
  const fieldTypes = useMemo(() => parseFieldTypes(schema.json_schema), [schema.json_schema]);
  function stagedValue(draft: PluginConfigValue) {
    const result = buildSchemaValues(descriptor, draft, fieldTypes, SAVE_OPTIONS);
    for (const key of managedFields) delete result[key];
    return result;
  }
  const editableClears = (keys: Set<string>) =>
    Array.from(keys).filter((key) => !managedFields.includes(key));

  const savedValues = useMemo(() => formValuesFromConfig(fields, value), [fields, value]);
  const [values, setValues] = useState<PluginConfigValue>(savedValues);
  const [testResult, setTestResult] = useState<ConnectionCheckResponse | null>(null);
  const [clearSecrets, setClearSecrets] = useState<Set<string>>(new Set());

  useEffect(() => {
    setValues(savedValues);
    setClearSecrets(new Set());
  }, [savedValues]);

  // A later release of ownership must not restore an edit or a secret clear.
  if (managedFields.some((key) => values[key] !== savedValues[key])) {
    setValues({
      ...values,
      ...Object.fromEntries(managedFields.map((key) => [key, savedValues[key]])),
    });
  }
  if (managedFields.some((key) => clearSecrets.has(key))) {
    setClearSecrets(new Set(editableClears(clearSecrets)));
  }

  function handleChange(next: PluginConfigValue) {
    setTestResult(null);
    setValues(next);
    const updated = new Set(clearSecrets);
    for (const key of configuredSecrets) {
      const replacement = next[key];
      if (typeof replacement === "string" && replacement.trim() !== "") {
        updated.delete(key);
      }
    }
    setClearSecrets(updated);
    onDraftChange?.(schema.key, stagedValue(next), editableClears(updated));
  }

  function toggleClearSecret(key: string) {
    const updated = new Set(clearSecrets);
    if (updated.has(key)) updated.delete(key);
    else updated.add(key);
    setClearSecrets(updated);
    onDraftChange?.(schema.key, stagedValue(values), editableClears(updated));
  }

  async function handleTest() {
    if (!onTest) {
      return;
    }

    try {
      setTestResult(await onTest(schema.key, stagedValue(values), editableClears(clearSecrets)));
    } catch (error) {
      setTestResult({
        success: false,
        message:
          error instanceof Error ? error.message : t("web.admin.plugins.configForm.checkFailed"),
      });
    }
  }

  if (!supported) {
    return (
      <div className="space-y-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-3">
        <Label>{schema.title || schema.key}</Label>
        <p className="text-muted-foreground text-sm">
          {t("web.admin.plugins.configForm.unsupportedSchema")}
        </p>
      </div>
    );
  }

  return (
    <fieldset
      disabled={isSaving || isTesting}
      className={bare ? "space-y-3" : "space-y-3 rounded-md border p-3"}
    >
      {bare ? null : (
        <div className="space-y-1">
          <Label>{schema.title || schema.key}</Label>
          {schema.description ? (
            <p className="text-muted-foreground text-xs">{schema.description}</p>
          ) : null}
        </div>
      )}

      <SchemaForm
        descriptor={descriptor}
        values={values}
        onChange={handleChange}
        idPrefix={idPrefix ?? schema.key}
        lockedKeys={lockedKeys}
      />

      {configuredSecrets.length > 0 ? (
        <div className="space-y-2 rounded-md border border-dashed p-2.5">
          {configuredSecrets.map((key) => {
            const field = fields.find((candidate) => candidate.key === key);
            const clearing = !managedFields.includes(key) && clearSecrets.has(key);
            const required = field?.required === true;
            return (
              <div key={key} className="flex items-center justify-between gap-3 text-xs">
                <span className={clearing ? "text-destructive" : "text-muted-foreground"}>
                  {t("web.admin.plugins.configForm.savedSecretStatus", {
                    label: field?.label || humanizeConfigKey(key),
                    state: clearing ? "cleared" : "saved",
                    required: required ? "yes" : "no",
                  })}
                </span>
                {!required && !managedFields.includes(key) ? (
                  <Button
                    type="button"
                    size="xs"
                    variant="ghost"
                    onClick={() => toggleClearSecret(key)}
                  >
                    {clearing
                      ? t("web.admin.plugins.configForm.keepSecret")
                      : t("web.admin.plugins.configForm.clearSecret")}
                  </Button>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-3">
        {onTest ? (
          <ConnectionCheckAction
            onClick={handleTest}
            result={testResult}
            isPending={isTesting}
            disabled={isSaving}
          />
        ) : null}
        <Button
          size="sm"
          variant="outline"
          disabled={isSaving || isTesting || allFieldsManaged}
          onClick={() => onSave(schema.key, stagedValue(values), editableClears(clearSecrets))}
        >
          {schema.admin_form?.submit_label || t("web.admin.plugins.configForm.save")}
        </Button>
      </div>
    </fieldset>
  );
}
