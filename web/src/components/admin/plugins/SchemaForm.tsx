import { useEffect, useMemo, useState } from "react";

import { Loader2 } from "lucide-react";

import type { PluginAdminForm, PluginAdminFormField, PluginAdminFormSection } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";

import {
  effectiveValue,
  evaluateShowWhen,
  validateSchemaValues,
  type SchemaOption,
} from "./schemaFormUtils";

type Props = {
  descriptor: PluginAdminForm;
  values: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  errors?: Record<string, string>;
  dynamicOptions?: Record<string, SchemaOption[]>;
  /**
   * True while the host is probing the connection for dynamic option lists
   * (root folders, quality profiles, tags). Drives per-field loading skeletons
   * on dynamic_options controls so the operator sees the form populate instead
   * of staring at empty selects.
   */
  optionsLoading?: boolean;
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

// A dynamic control is "pending" only when we're probing AND have nothing to
// show yet — so a refresh over already-loaded options never flashes a skeleton
// over the operator's current selection.
function isPending(
  field: PluginAdminFormField,
  options: SchemaOption[],
  optionsLoading: boolean | undefined,
): boolean {
  return Boolean(field.dynamic_options) && Boolean(optionsLoading) && options.length === 0;
}

// Loading placeholder for a single dynamic SELECT: a select-sized row with a
// spinner and a shimmer bar, so the field reads as "fetching from the service".
function SelectSkeleton() {
  return (
    <div
      aria-busy="true"
      className="border-input bg-muted/20 flex h-9 w-full items-center gap-2.5 rounded-md border px-3"
    >
      <Loader2 className="text-muted-foreground/70 h-3.5 w-3.5 shrink-0 animate-spin" />
      <Skeleton className="h-2.5 w-32 rounded-full" />
    </div>
  );
}

// FieldDescription is the muted helper text shown under a field or section
// label — shared so the markup can't drift between the field/switch/section
// renderers.
function FieldDescription({ text }: { text?: string }) {
  return text ? <p className="text-muted-foreground text-xs leading-relaxed">{text}</p> : null;
}

// Loading placeholder for a dynamic MULTI_SELECT (tags): a few shimmer chips.
function ChipsSkeleton() {
  return (
    <div aria-busy="true" className="flex flex-wrap gap-2">
      {["w-16", "w-12", "w-20", "w-14"].map((w) => (
        <Skeleton key={w} className={`h-7 rounded-md ${w}`} />
      ))}
    </div>
  );
}

function SchemaFormSection({
  section,
  values,
  forceOpen,
  renderFields,
}: {
  section: PluginAdminFormSection;
  values: Record<string, unknown>;
  forceOpen: boolean;
  renderFields: (keys: string[]) => React.ReactNode;
}) {
  // null = operator hasn't toggled; fall back to collapsed_default. forceOpen
  // (the section has unresolved errors) always wins so setup can't be hidden.
  const [userOpen, setUserOpen] = useState<boolean | null>(null);

  if (!evaluateShowWhen(section.show_when, values)) {
    return null;
  }

  const open = forceOpen || (userOpen ?? !section.collapsed_default);
  const showFields = section.collapsible ? open : true;

  return (
    <section className="border-border/70 bg-muted/10 space-y-3 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-2">
        <div className="space-y-0.5">
          <Label className="text-foreground text-sm font-semibold">{section.title}</Label>
          <FieldDescription text={section.description} />
        </div>
        {section.collapsible ? (
          <Button type="button" size="xs" variant="ghost" onClick={() => setUserOpen(!open)}>
            {open ? "Hide" : "Show"}
          </Button>
        ) : null}
      </div>
      {showFields ? renderFields(section.field_keys) : null}
    </section>
  );
}

export function SchemaForm({
  descriptor,
  values,
  onChange,
  errors,
  dynamicOptions,
  optionsLoading,
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

    if (field.control === "SELECT") {
      const options = optionsFor(field, dynamicOptions);
      if (isPending(field, options, optionsLoading)) {
        return <SelectSkeleton />;
      }
      return (
        <Select
          value={String(effectiveValue(field, values) ?? "")}
          onValueChange={(nextValue) => setField(field.key, nextValue)}
        >
          <SelectTrigger id={id} className="w-full">
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
      if (isPending(field, options, optionsLoading)) {
        return <ChipsSkeleton />;
      }
      const current = effectiveValue(field, values);
      const selected = Array.isArray(current) ? current.map((v) => String(v)) : [];
      if (options.length === 0) {
        return (
          <p className="text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs">
            No options available.
          </p>
        );
      }
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

  // A non-switch field: label + optional description stacked above the control.
  function renderField(field: PluginAdminFormField): React.ReactNode {
    const err = mergedErrors[field.key];
    // A field that only appears because its show_when passed reads as nested
    // under whatever toggle gates it (e.g. the anime overrides under anime_enabled).
    const nested = Boolean(field.show_when);
    return (
      <div
        key={field.key}
        data-nested={nested ? "true" : undefined}
        className={cn("space-y-2", nested && "border-border/60 ml-0.5 border-l pl-3")}
      >
        <div className="space-y-1">
          <Label htmlFor={`${idPrefix}-${field.key}`}>{field.label || field.key}</Label>
          <FieldDescription text={field.description} />
        </div>
        {renderControl(field)}
        {err ? <p className="text-destructive text-xs">{err}</p> : null}
      </div>
    );
  }

  // A switch field as a single settings row: label + description on the left,
  // the toggle on the right. No outer border — the group container owns it.
  function renderSwitchRow(field: PluginAdminFormField): React.ReactNode {
    const id = `${idPrefix}-${field.key}`;
    const err = mergedErrors[field.key];
    return (
      <div key={field.key} className="px-3.5 py-3 transition-colors">
        <div className="flex items-start gap-3">
          <Switch
            id={id}
            className="mt-0.5 shrink-0"
            checked={Boolean(effectiveValue(field, values))}
            onCheckedChange={(checked) => setField(field.key, checked)}
          />
          <div className="min-w-0 space-y-0.5">
            <Label htmlFor={id} className="cursor-pointer font-medium">
              {field.label || field.key}
            </Label>
            <FieldDescription text={field.description} />
          </div>
        </div>
        {err ? <p className="text-destructive mt-1.5 ml-11 text-xs">{err}</p> : null}
      </div>
    );
  }

  // Render an ordered field list, collapsing consecutive switches into one
  // bordered, divided container so toggles read as a cohesive group instead of
  // a column of separate boxes. Honors show_when on each field.
  function renderFieldList(fields: PluginAdminFormField[]): React.ReactNode {
    const visible = fields.filter((field) => evaluateShowWhen(field.show_when, values));
    const nodes: React.ReactNode[] = [];
    let run: PluginAdminFormField[] = [];
    // Key switch groups by their position in the list, not by their first
    // field's key — so revealing/hiding a show_when switch within a run keeps the
    // container's identity stable (no remount, no focus loss on the toggle).
    let groupIndex = 0;

    const flushSwitches = () => {
      if (run.length === 0) return;
      const group = run;
      run = [];
      nodes.push(
        <div
          key={`switch-group-${groupIndex++}`}
          className="divide-border/70 bg-card divide-y overflow-hidden rounded-lg border"
        >
          {group.map((field) => renderSwitchRow(field))}
        </div>,
      );
    };

    for (const field of visible) {
      if (field.control === "SWITCH") {
        run.push(field);
      } else {
        flushSwitches();
        nodes.push(renderField(field));
      }
    }
    flushSwitches();

    if (nodes.length === 0) return null;
    return <div className="grid gap-4">{nodes}</div>;
  }

  const sections = descriptor.sections ?? [];
  const groupedKeys = new Set<string>();
  for (const section of sections) {
    for (const key of section.field_keys) {
      groupedKeys.add(key);
    }
  }
  const ungroupedFields = descriptor.fields.filter((field) => !groupedKeys.has(field.key));

  const resolveKeys = (keys: string[]): PluginAdminFormField[] =>
    keys
      .map((key) => byKey.get(key))
      .filter((field): field is PluginAdminFormField => Boolean(field));

  return (
    <div className="grid gap-5">
      {ungroupedFields.length > 0 ? renderFieldList(ungroupedFields) : null}
      {sections.map((section) => (
        <SchemaFormSection
          key={section.key}
          section={section}
          values={values}
          forceOpen={section.field_keys.some((key) => mergedErrors[key] != null)}
          renderFields={(keys) => renderFieldList(resolveKeys(keys))}
        />
      ))}
    </div>
  );
}
