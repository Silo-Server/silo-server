import { useId } from "react";

import type { PluginAdminFormField } from "@/api/types";
import { effectiveValue } from "@/components/admin/plugins/schemaFormUtils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";

import {
  SETTINGS_CONTROL_WIDTH,
  SETTINGS_NUMBER_WIDTH,
  SettingFieldRow,
  SettingFieldStatus,
} from "./SettingField";

// Radix reserves the empty string for "no selection", so an option whose
// value is "" (a plugin's "Provider default") travels as a stand-in.
const EMPTY_OPTION_VALUE = "\u0000empty";

interface SignInConfigFieldProps {
  field: PluginAdminFormField;
  values: Record<string, unknown>;
  idPrefix: string;
  dirty: boolean;
  /** A secret saved on the server; the input stays blank to keep it. */
  secretSaved?: boolean;
  /** The saved secret is staged to be cleared. */
  clearing?: boolean;
  onChange: (value: unknown) => void;
  onToggleClear?: () => void;
  /** Overrides the plugin's label and description (the page's own steps). */
  label?: string;
  description?: string;
}

/**
 * One field of a sign-in plugin's configuration as a settings row, so plugin
 * fields line up with the page's own settings.
 */
export function SignInConfigField({
  field,
  values,
  idPrefix,
  dirty,
  secretSaved = false,
  clearing = false,
  onChange,
  onToggleClear,
  label,
  description,
}: SignInConfigFieldProps) {
  const id = `${idPrefix}-${field.key}`;
  const descriptionId = useId();
  const value = effectiveValue(field, values);
  const secret = field.secret || field.control === "PASSWORD";
  const describedBy = (description ?? field.description) ? descriptionId : undefined;

  let control: React.ReactNode;
  if (field.control === "SWITCH") {
    control = (
      <Switch
        id={id}
        checked={Boolean(value)}
        onCheckedChange={onChange}
        aria-describedby={describedBy}
      />
    );
  } else if (field.control === "SELECT") {
    const options = field.options ?? [];
    const current = String(value ?? "");
    const toItem = (item: string) => (item === "" ? EMPTY_OPTION_VALUE : item);
    control = (
      <Select
        value={toItem(current)}
        onValueChange={(next) => onChange(next === EMPTY_OPTION_VALUE ? "" : next)}
      >
        <SelectTrigger id={id} className={SETTINGS_CONTROL_WIDTH} aria-describedby={describedBy}>
          <SelectValue placeholder={field.placeholder || "Select"} />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option.value} value={toItem(option.value)}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    );
  } else if (field.control === "TEXTAREA" || field.multiline) {
    control = (
      <textarea
        id={id}
        rows={field.rows && field.rows > 0 ? field.rows : 3}
        value={String(value ?? "")}
        placeholder={field.placeholder}
        onChange={(event) => onChange(event.target.value)}
        aria-describedby={describedBy}
        className={cn(
          SETTINGS_CONTROL_WIDTH,
          "border-border bg-background placeholder:text-muted-foreground min-h-20 rounded-md border px-3 py-2 font-mono text-xs shadow-xs outline-none",
          "focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]",
        )}
      />
    );
  } else {
    control = (
      <Input
        id={id}
        type={secret ? "password" : field.control === "NUMBER" ? "number" : "text"}
        autoComplete={secret ? "new-password" : "off"}
        value={String(value ?? "")}
        placeholder={
          secret && secretSaved && !clearing ? "Saved. Type to replace." : field.placeholder
        }
        onChange={(event) => onChange(event.target.value)}
        aria-describedby={describedBy}
        className={field.control === "NUMBER" ? SETTINGS_NUMBER_WIDTH : SETTINGS_CONTROL_WIDTH}
      />
    );
  }

  return (
    <SettingFieldRow
      label={label ?? (field.label || field.key)}
      htmlFor={id}
      dirty={dirty}
      description={description ?? field.description}
      descriptionId={descriptionId}
      status={
        secret && secretSaved && onToggleClear && !field.required ? (
          <span className="inline-flex items-center gap-2">
            <SettingFieldStatus tone={clearing ? "warn" : "muted"}>
              {clearing ? "Will be cleared when you save." : "Saved."}
            </SettingFieldStatus>
            <Button type="button" size="xs" variant="ghost" onClick={onToggleClear}>
              {clearing ? "Keep" : "Clear"}
            </Button>
          </span>
        ) : null
      }
    >
      {control}
    </SettingFieldRow>
  );
}
