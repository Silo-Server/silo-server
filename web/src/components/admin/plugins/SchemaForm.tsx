import { useEffect, useMemo, useState } from "react";

import type {
  PluginAdminForm,
  PluginAdminFormField,
  PluginAdminFormSection,
} from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

import {
  effectiveValue,
  evaluateShowWhen,
  validateSchemaValues,
  type SchemaOption,
} from "./schemaForm";

type Props = {
  descriptor: PluginAdminForm;
  values: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  errors?: Record<string, string>;
  dynamicOptions?: Record<string, SchemaOption[]>;
  idPrefix?: string;
  onValidityChange?: (valid: boolean) => void;
};

function optionsFor(
  field: PluginAdminFormField,
  dynamicOptions: Record<string, SchemaOption[]> | undefined,
): SchemaOption[] {
  if (field.dynamic_options) {
    return dynamicOptions?.[field.key] ?? field.options ?? [];
  }
  return field.options ?? [];
}

function SchemaFormSection({
  section,
  values,
  renderField,
}: {
  section: PluginAdminFormSection;
  values: Record<string, unknown>;
  renderField: (key: string) => React.ReactNode;
}) {
  const [open, setOpen] = useState(!section.collapsed_default);

  if (!evaluateShowWhen(section.show_when, values)) {
    return null;
  }

  const showFields = section.collapsible ? open : true;

  return (
    <div className="space-y-3 rounded-md border p-3">
      <div className="flex items-center justify-between gap-2">
        <div className="space-y-1">
          <Label>{section.title}</Label>
          {section.description ? (
            <p className="text-muted-foreground text-xs">{section.description}</p>
          ) : null}
        </div>
        {section.collapsible ? (
          <Button
            type="button"
            size="xs"
            variant="ghost"
            onClick={() => setOpen((current) => !current)}
          >
            {open ? "Hide" : "Show"}
          </Button>
        ) : null}
      </div>
      {showFields ? (
        <div className="grid gap-4">{section.field_keys.map((key) => renderField(key))}</div>
      ) : null}
    </div>
  );
}

export function SchemaForm({
  descriptor,
  values,
  onChange,
  errors,
  dynamicOptions,
  idPrefix = "schema",
  onValidityChange,
}: Props) {
  const byKey = useMemo(() => {
    const map = new Map<string, PluginAdminFormField>();
    for (const field of descriptor.fields) {
      map.set(field.key, field);
    }
    return map;
  }, [descriptor.fields]);

  const clientErrors = useMemo(
    () => validateSchemaValues(descriptor, values),
    [descriptor, values],
  );

  const mergedErrors = useMemo(() => {
    return { ...clientErrors, ...(errors ?? {}) };
  }, [clientErrors, errors]);

  const valid = Object.keys(clientErrors).length === 0;
  useEffect(() => {
    onValidityChange?.(valid);
  }, [valid, onValidityChange]);

  function setField(key: string, value: unknown) {
    onChange({ ...values, [key]: value });
  }

  function renderControl(field: PluginAdminFormField): React.ReactNode {
    const id = `${idPrefix}-${field.key}`;

    if (field.control === "SWITCH") {
      return (
        <div className="flex items-center gap-3 rounded-md border px-3 py-2">
          <Switch
            id={id}
            checked={Boolean(effectiveValue(field, values))}
            onCheckedChange={(checked) => setField(field.key, checked)}
          />
          <span className="text-muted-foreground text-sm">{field.placeholder || ""}</span>
        </div>
      );
    }

    if (field.control === "SELECT") {
      const options = optionsFor(field, dynamicOptions);
      return (
        <Select
          value={String(effectiveValue(field, values) ?? "")}
          onValueChange={(nextValue) => setField(field.key, nextValue)}
        >
          <SelectTrigger id={id}>
            <SelectValue placeholder={field.placeholder || "Select"} />
          </SelectTrigger>
          <SelectContent>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      );
    }

    if (field.control === "MULTI_SELECT") {
      const options = optionsFor(field, dynamicOptions);
      const current = effectiveValue(field, values);
      const selected = Array.isArray(current) ? current.map((v) => String(v)) : [];
      return (
        <div className="flex flex-wrap gap-2">
          {options.map((option) => {
            const isSelected = selected.includes(option.value);
            return (
              <Button
                key={option.value}
                type="button"
                size="xs"
                variant={isSelected ? "default" : "outline"}
                onClick={() => {
                  const next = isSelected
                    ? selected.filter((value) => value !== option.value)
                    : [...selected, option.value];
                  setField(field.key, next);
                }}
              >
                {option.label}
              </Button>
            );
          })}
        </div>
      );
    }

    if (field.control === "TEXTAREA" || field.multiline) {
      return (
        <textarea
          id={id}
          className="border-border bg-background min-h-24 w-full rounded-md border px-3 py-2 text-sm"
          rows={field.rows && field.rows > 0 ? field.rows : 4}
          value={String(effectiveValue(field, values) ?? "")}
          placeholder={field.placeholder}
          onChange={(event) => setField(field.key, event.target.value)}
        />
      );
    }

    return (
      <Input
        id={id}
        type={
          field.control === "PASSWORD" || field.secret
            ? "password"
            : field.control === "NUMBER"
              ? "number"
              : "text"
        }
        value={String(effectiveValue(field, values) ?? "")}
        placeholder={field.placeholder}
        onChange={(event) => setField(field.key, event.target.value)}
      />
    );
  }

  function renderField(field: PluginAdminFormField): React.ReactNode {
    if (!evaluateShowWhen(field.show_when, values)) {
      return null;
    }
    const err = mergedErrors[field.key];
    return (
      <div key={field.key} className="space-y-2">
        <div className="space-y-1">
          <Label htmlFor={`${idPrefix}-${field.key}`}>{field.label || field.key}</Label>
          {field.description ? (
            <p className="text-muted-foreground text-xs">{field.description}</p>
          ) : null}
        </div>
        {renderControl(field)}
        {err ? <p className="text-destructive text-xs">{err}</p> : null}
      </div>
    );
  }

  function renderFieldByKey(key: string): React.ReactNode {
    const field = byKey.get(key);
    if (!field) {
      return null;
    }
    return renderField(field);
  }

  const sections = descriptor.sections ?? [];
  const groupedKeys = new Set<string>();
  for (const section of sections) {
    for (const key of section.field_keys) {
      groupedKeys.add(key);
    }
  }
  const ungroupedFields = descriptor.fields.filter((field) => !groupedKeys.has(field.key));

  return (
    <div className="grid gap-4">
      {ungroupedFields.length > 0 ? (
        <div className="grid gap-4">{ungroupedFields.map((field) => renderField(field))}</div>
      ) : null}
      {sections.map((section) => (
        <SchemaFormSection
          key={section.key}
          section={section}
          values={values}
          renderField={renderFieldByKey}
        />
      ))}
    </div>
  );
}
