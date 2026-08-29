import { useId, type ReactNode } from "react";
import { Check, TriangleAlert } from "lucide-react";

import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { RestartBadge } from "@/components/settings/RestartBadge";
import { cn } from "@/lib/utils";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useGroupRestartAll } from "./FieldGroup";
import "@/styles/admin-settings.css";

interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

/**
 * Width of a text, password, or select control inside a settings row. Pages
 * that hand `SettingFieldRow` a control of their own use this instead of
 * picking a width, so every row on a page ends on the same edge.
 */
export const SETTINGS_CONTROL_WIDTH = "w-full sm:w-[var(--settings-control-w)]";

/** Width of a number control inside a settings row. See {@link SETTINGS_CONTROL_WIDTH}. */
export const SETTINGS_NUMBER_WIDTH = "w-full sm:w-[var(--settings-control-w-num)]";

/**
 * One-line probe result rendered under a field description, e.g.
 * "Detected VA-API on renderD128". Pass any node to `status` instead when the
 * copy needs richer markup.
 */
export function SettingFieldStatus({
  tone = "ok",
  children,
}: {
  tone?: "ok" | "warn" | "muted";
  children: ReactNode;
}) {
  const Icon = tone === "warn" ? TriangleAlert : tone === "ok" ? Check : null;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-[12.5px] leading-snug",
        tone === "ok" && "text-green-600 dark:text-green-400",
        tone === "warn" && "text-amber-600 dark:text-amber-400",
        tone === "muted" && "text-muted-foreground",
      )}
    >
      {Icon ? <Icon className="size-3.5 shrink-0" aria-hidden="true" /> : null}
      {children}
    </span>
  );
}

export interface SettingFieldRowProps {
  label: ReactNode;
  /** Ties the label to the control; omit for rows whose control has no id. */
  htmlFor?: string;
  /**
   * The `server_settings` key behind the row, e.g. `branding.server_name`.
   * Rendered as a small mono caption under the label so an admin can match
   * what they see to the API, environment overrides, and support answers.
   */
  settingKey?: string;
  /** The row has an unsaved edit; shows the violet dot before the label. */
  dirty?: boolean;
  /** Description under the label. One short sentence, or nothing at all. */
  description?: ReactNode;
  descriptionId?: string;
  /** Extra line under the description — probe results, quota notes. */
  status?: ReactNode;
  /** Amber "Restart" chip after the label; drive it from `useRestartKeys`. */
  restartRequired?: boolean;
  /**
   * Trailing unit for the control, e.g. "days" or "Mbps". It renders in the
   * row's own unit slot rather than beside the control on purpose: the slot is
   * reserved on every row, so a row that has a unit and a row that does not
   * still end their controls on the same edge.
   */
  unit?: ReactNode;
  className?: string;
  /** The control itself. */
  children: ReactNode;
}

/**
 * The row shell every admin setting sits in: label and description on the
 * left, control right-aligned, unit slot after it, hairline underneath.
 * Exported so the credential and limit variants line up with plain fields
 * instead of re-deriving spacing.
 */
export function SettingFieldRow({
  label,
  htmlFor,
  settingKey,
  dirty,
  description,
  descriptionId,
  status,
  restartRequired,
  unit,
  className,
  children,
}: SettingFieldRowProps) {
  // A group that already says "Changes apply after a restart" does not need the
  // same fact repeated on every row inside it.
  const groupSaysRestart = useGroupRestartAll();

  return (
    <div
      className={cn(
        "settings-field-row border-border/60 relative flex flex-col gap-3 border-b py-3.5 last:border-b-0",
        "sm:flex-row sm:items-start sm:gap-6",
        className,
      )}
    >
      <div className="min-w-0 flex-1 sm:max-w-[520px]">
        <div className="flex flex-wrap items-center gap-2">
          {dirty ? (
            <span
              className="size-1.5 shrink-0 rounded-full bg-[var(--settings-accent)]"
              title="Unsaved change"
            />
          ) : null}
          <Label htmlFor={htmlFor} className="text-sm font-medium">
            {label}
          </Label>
          {restartRequired && !groupSaysRestart && <RestartBadge />}
        </div>
        {settingKey ? (
          <span className="text-muted-foreground/70 mt-0.5 block font-mono text-[10px] tracking-[0.02em]">
            {settingKey}
          </span>
        ) : null}
        {description ? (
          <p id={descriptionId} className="text-muted-foreground mt-1 text-xs leading-relaxed">
            {description}
          </p>
        ) : null}
        {status ? <div className="mt-1.5">{status}</div> : null}
      </div>
      <div className="flex w-full items-center gap-2 sm:w-auto sm:shrink-0 sm:justify-end">
        {/* Grows to fill the row on a stacked phone layout; content-sized and
            right-aligned from `sm` up, which is what puts every control on the
            shared edge. */}
        <div className="flex min-w-0 flex-1 items-center gap-2 sm:flex-none sm:justify-end">
          {children}
        </div>
        {unit ? (
          <span className="text-muted-foreground shrink-0 text-xs whitespace-nowrap sm:w-[var(--settings-unit-w)]">
            {unit}
          </span>
        ) : (
          // The reserved half of the contract: an empty slot on a unit-less row
          // so its control does not slide right past the rows that have one.
          <span
            aria-hidden="true"
            className="hidden shrink-0 sm:block sm:w-[var(--settings-unit-w)]"
          />
        )}
      </div>
    </div>
  );
}

interface SettingFieldProps {
  label: string;
  /** The `server_settings` key, shown as a mono caption under the label. */
  settingKey?: string;
  /** The field has an unsaved edit; drive it from `form.isDirty(key)`. */
  dirty?: boolean;
  type?: "text" | "number" | "password" | "toggle" | "duration" | "select";
  /**
   * Placeholder for `text`, description for every other type. Prefer
   * `description` for new call sites.
   */
  hint?: string;
  /** Always rendered under the label, whatever the type. */
  description?: ReactNode;
  /** Extra line under the description, e.g. a detection result. */
  status?: ReactNode;
  /** Rendered in the row's trailing unit slot, e.g. "%" or "Mbps". */
  unit?: string;
  value: string;
  onChange: (value: string) => void;
  options?: SelectOption[];
  sensitiveConfigured?: boolean;
  disabled?: boolean;
  /** Marks the field with a restart badge; drive it from `useRestartKeys`. */
  restartRequired?: boolean;
  className?: string;
}

export function SettingField({
  label,
  settingKey,
  dirty,
  type = "text",
  hint,
  description,
  status,
  unit,
  value,
  onChange,
  options,
  sensitiveConfigured,
  disabled,
  restartRequired,
  className,
}: SettingFieldProps) {
  const controlId = useId();
  const hintId = useId();

  // `text` keeps treating `hint` as a placeholder (its long-standing
  // behaviour); every other type shows it under the label.
  const hintAsDescription = type === "text" ? undefined : hint;
  const rowDescription = description ?? hintAsDescription;
  const describedBy = rowDescription ? hintId : undefined;

  const row = (control: ReactNode) => (
    <SettingFieldRow
      label={label}
      htmlFor={controlId}
      settingKey={settingKey}
      dirty={dirty}
      description={rowDescription}
      descriptionId={hintId}
      status={status}
      restartRequired={restartRequired}
      unit={unit}
      className={className}
    >
      {control}
    </SettingFieldRow>
  );

  if (type === "toggle") {
    return row(
      <Switch
        id={controlId}
        checked={value === "true"}
        onCheckedChange={(val) => onChange(val ? "true" : "false")}
        disabled={disabled}
        aria-describedby={describedBy}
      />,
    );
  }

  if (type === "select" && options) {
    const currentVal = value || options[0]?.value || "";
    return row(
      <Select value={currentVal} onValueChange={onChange} disabled={disabled}>
        <SelectTrigger
          id={controlId}
          className={cn("border-muted-foreground/25", SETTINGS_CONTROL_WIDTH)}
          aria-describedby={describedBy}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((opt) => (
            <SelectItem key={opt.value} value={opt.value} disabled={opt.disabled}>
              {opt.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>,
    );
  }

  if (type === "password") {
    return row(
      <Input
        id={controlId}
        type="password"
        placeholder={sensitiveConfigured ? "configured" : (hint ?? "Not configured")}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        className={cn("border-muted-foreground/25", SETTINGS_CONTROL_WIDTH)}
        aria-describedby={describedBy}
      />,
    );
  }

  if (type === "number") {
    return row(
      <Input
        id={controlId}
        type="number"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        className={cn("border-muted-foreground/25", SETTINGS_NUMBER_WIDTH)}
        aria-describedby={describedBy}
      />,
    );
  }

  // text and duration
  return row(
    <Input
      id={controlId}
      type="text"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      className={cn("border-muted-foreground/25", SETTINGS_CONTROL_WIDTH)}
      placeholder={type === "text" ? hint : undefined}
      aria-describedby={describedBy}
    />,
  );
}
