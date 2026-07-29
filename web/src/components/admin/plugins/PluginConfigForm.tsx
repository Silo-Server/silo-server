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

import { SchemaForm } from "./SchemaForm";
import { buildSchemaValues } from "./schemaFormUtils";

type PluginConfigValue = Record<string, unknown>;

type Props = {
  schema: PluginConfigSchema;
  value?: PluginConfigValue;
  configuredSecrets?: string[];
  onSave: (key: string, value: PluginConfigValue, clearSecrets: string[]) => void;
  onTest?: (
    key: string,
    value: PluginConfigValue,
    clearSecrets: string[],
  ) => Promise<ConnectionCheckResponse>;
  isSaving?: boolean;
  isTesting?: boolean;
};

type SupportedField = PluginAdminFormField & {
  inferredType?: "string" | "number" | "integer" | "boolean";
};

type ParsedObjectSchema = {
  supported: boolean;
  fields: SupportedField[];
};

function humanizeKey(value: string) {
  return value
    .split("_")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function parseJSONSchema(schema: PluginConfigSchema): ParsedObjectSchema {
  try {
    const parsed = JSON.parse(schema.json_schema) as {
      type?: string;
      required?: string[];
      properties?: Record<
        string,
        {
          type?: string;
          title?: string;
          description?: string;
          writeOnly?: boolean;
          format?: string;
        }
      >;
    };
    if (parsed.type !== "object" || !parsed.properties) {
      return { supported: false, fields: [] };
    }

    const fields = Object.entries(parsed.properties).map(([key, property]) => {
      const propertyType = property.type;
      if (!propertyType || !["string", "number", "integer", "boolean"].includes(propertyType)) {
        return null;
      }
      const isSensitive = property.writeOnly === true || property.format === "password";
      const control =
        propertyType === "boolean"
          ? "SWITCH"
          : propertyType === "number" || propertyType === "integer"
            ? "NUMBER"
            : isSensitive
              ? "PASSWORD"
              : "TEXT";
      return {
        key,
        label: property.title || humanizeKey(key),
        description: property.description,
        control,
        placeholder: "",
        required: parsed.required?.includes(key) ?? false,
        secret: isSensitive,
        multiline: false,
        options: [],
        rows: 0,
        inferredType: propertyType as "string" | "number" | "integer" | "boolean",
      } satisfies SupportedField;
    });

    if (fields.some((field) => field == null)) {
      return { supported: false, fields: [] };
    }
    return { supported: true, fields: fields.filter(Boolean) as SupportedField[] };
  } catch {
    return { supported: false, fields: [] };
  }
}

function defaultValueForField(field: SupportedField): string | boolean {
  if (field.default_value !== undefined) {
    if (typeof field.default_value === "boolean") {
      return field.default_value;
    }
    if (typeof field.default_value === "number") {
      return String(field.default_value);
    }
    if (typeof field.default_value === "string") {
      return field.default_value;
    }
  }
  if (field.control === "SWITCH") {
    return false;
  }
  return "";
}

function valueForField(field: SupportedField, configValue?: PluginConfigValue): string | boolean {
  const raw = configValue?.[field.key];
  if (typeof raw === "boolean") {
    return raw;
  }
  if (typeof raw === "number") {
    return String(raw);
  }
  if (typeof raw === "string") {
    return raw;
  }
  return defaultValueForField(field);
}

export function PluginConfigForm({
  schema,
  value,
  configuredSecrets = [],
  onSave,
  onTest,
  isSaving = false,
  isTesting = false,
}: Props) {
  const parsedFallback = useMemo(() => parseJSONSchema(schema), [schema]);
  const fields = useMemo<SupportedField[]>(() => {
    if (schema.admin_form?.fields?.length) {
      return schema.admin_form.fields;
    }
    return parsedFallback.fields;
  }, [parsedFallback.fields, schema.admin_form?.fields]);

  const supported =
    fields.length > 0 && (schema.admin_form?.fields?.length ? true : parsedFallback.supported);

  const descriptor = useMemo<PluginAdminForm>(() => {
    const base = schema.admin_form ?? { fields };
    const configured = new Set(configuredSecrets);
    return {
      ...base,
      fields: base.fields.map((field) =>
        configured.has(field.key) && (field.secret || field.control === "PASSWORD")
          ? { ...field, placeholder: "Saved secret — leave blank to keep" }
          : field,
      ),
    };
  }, [configuredSecrets, fields, schema.admin_form]);

  const [values, setValues] = useState<PluginConfigValue>(() =>
    Object.fromEntries(fields.map((field) => [field.key, valueForField(field, value)])),
  );
  const [testResult, setTestResult] = useState<ConnectionCheckResponse | null>(null);
  const [clearSecrets, setClearSecrets] = useState<Set<string>>(new Set());

  useEffect(() => {
    setValues(Object.fromEntries(fields.map((field) => [field.key, valueForField(field, value)])));
    setClearSecrets(new Set());
  }, [fields, value]);

  function handleChange(next: PluginConfigValue) {
    setTestResult(null);
    setValues(next);
    setClearSecrets((current) => {
      const updated = new Set(current);
      for (const key of configuredSecrets) {
        const replacement = next[key];
        if (typeof replacement === "string" && replacement.trim() !== "") {
          updated.delete(key);
        }
      }
      return updated;
    });
  }

  async function handleTest() {
    if (!onTest) {
      return;
    }

    try {
      setTestResult(
        await onTest(schema.key, buildSchemaValues(descriptor, values), Array.from(clearSecrets)),
      );
    } catch (error) {
      setTestResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  if (!supported) {
    return (
      <div className="space-y-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-3">
        <Label>{schema.title || schema.key}</Label>
        <p className="text-muted-foreground text-sm">
          This plugin uses a configuration schema shape that the admin form does not support yet.
        </p>
      </div>
    );
  }

  return (
    <fieldset disabled={isSaving || isTesting} className="space-y-3 rounded-md border p-3">
      <div className="space-y-1">
        <Label>{schema.title || schema.key}</Label>
        {schema.description ? (
          <p className="text-muted-foreground text-xs">{schema.description}</p>
        ) : null}
      </div>

      <SchemaForm
        descriptor={descriptor}
        values={values}
        onChange={handleChange}
        idPrefix={schema.key}
      />

      {configuredSecrets.length > 0 ? (
        <div className="space-y-2 rounded-md border border-dashed p-2.5">
          {configuredSecrets.map((key) => {
            const field = fields.find((candidate) => candidate.key === key);
            const clearing = clearSecrets.has(key);
            const required = field?.required === true;
            return (
              <div key={key} className="flex items-center justify-between gap-3 text-xs">
                <span className={clearing ? "text-destructive" : "text-muted-foreground"}>
                  {field?.label || humanizeKey(key)}: {clearing ? "will be cleared" : "saved"}
                  {required ? " (required)" : ""}
                </span>
                {!required ? (
                  <Button
                    type="button"
                    size="xs"
                    variant="ghost"
                    onClick={() =>
                      setClearSecrets((current) => {
                        const updated = new Set(current);
                        if (updated.has(key)) updated.delete(key);
                        else updated.add(key);
                        return updated;
                      })
                    }
                  >
                    {clearing ? "Keep saved secret" : "Clear saved secret"}
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
          disabled={isSaving || isTesting}
          onClick={() =>
            onSave(schema.key, buildSchemaValues(descriptor, values), Array.from(clearSecrets))
          }
        >
          {schema.admin_form?.submit_label || "Save config"}
        </Button>
      </div>
    </fieldset>
  );
}
