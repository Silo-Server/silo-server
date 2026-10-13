import type { ReactNode } from "react";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

import { KeyValueRow, SourceTag } from "../ui";
import { rowChanged, type RowDraft, type ValueSource } from "./policySources";

/**
 * A read-only policy value with its source tag. A custom value shows the
 * inherited value it replaces underneath ("Default: unlimited").
 */
export function PolicyValueRow({
  label,
  description,
  value,
  source,
  base,
  changed,
}: {
  label: ReactNode;
  description?: ReactNode;
  value: ReactNode;
  source?: ValueSource;
  base?: string;
  changed?: boolean;
}) {
  return (
    <KeyValueRow
      label={label}
      description={description}
      value={value}
      below={source === "custom" ? base : undefined}
      tag={source ? <SourceTag source={source} /> : undefined}
      changed={changed}
    />
  );
}

/**
 * "Default · {value}" or "Custom" for one policy row. The custom control
 * (children) shows only while Custom is picked.
 */
export function DefaultCustomSegment({
  label,
  defaultText,
  custom,
  onCustomChange,
  disabled,
  children,
}: {
  label: string;
  /** What Default resolves to; undefined while unknown. */
  defaultText: string | undefined;
  custom: boolean;
  onCustomChange: (custom: boolean) => void;
  disabled?: boolean;
  children?: ReactNode;
}) {
  const option = (pressed: boolean, text: string, next: boolean) => (
    <button
      type="button"
      aria-pressed={pressed}
      disabled={disabled}
      onClick={() => {
        if (!pressed) onCustomChange(next);
      }}
      className={cn(
        "rounded-md px-2.5 py-1 text-xs font-medium whitespace-nowrap transition-colors disabled:opacity-50",
        !pressed && "text-muted-foreground hover:text-foreground",
        pressed && (next ? "bg-amber-500/15 text-amber-300" : "bg-accent text-foreground"),
      )}
    >
      {text}
    </button>
  );
  return (
    <div className="flex max-w-full flex-wrap items-center justify-end gap-2">
      <div
        role="group"
        aria-label={label}
        className="border-border/70 inline-flex shrink-0 rounded-lg border p-0.5"
      >
        {option(!custom, defaultText ? `Default · ${defaultText}` : "Default", false)}
        {option(custom, "Custom", true)}
      </div>
      {custom ? children : null}
    </div>
  );
}

/**
 * A Default/Custom row whose custom value is one of a few choices. Custom
 * starts from the inherited value; with none known it waits for a pick.
 */
export function ChoicePolicyEdit<T extends string | boolean>({
  label,
  description,
  row,
  saved,
  inherited,
  options,
  optionKey: key = String,
  onChange,
  disabled,
}: {
  label: string;
  description?: ReactNode;
  row: RowDraft<T>;
  /** The saved override, to mark the row changed. */
  saved: T | null;
  /** The value Default resolves to; undefined while unknown. */
  inherited: T | undefined;
  options: { value: T; label: string }[];
  /** The Select key of an option; it must not be empty (Radix reserves ""). */
  optionKey?: (value: T) => string;
  onChange: (row: RowDraft<T>) => void;
  disabled?: boolean;
}) {
  const labelOf = (value: T | undefined) =>
    value === undefined ? undefined : options.find((option) => option.value === value)?.label;
  return (
    <KeyValueRow
      label={label}
      description={description}
      changed={rowChanged(row, saved)}
      value={
        <DefaultCustomSegment
          label={label}
          defaultText={labelOf(inherited)}
          custom={row.custom}
          disabled={disabled}
          onCustomChange={(custom) =>
            onChange(custom ? { custom, value: inherited ?? null } : { custom, value: null })
          }
        >
          <Select
            value={row.value === null ? "" : key(row.value)}
            onValueChange={(picked) => {
              const option = options.find((candidate) => key(candidate.value) === picked);
              if (option) onChange({ custom: true, value: option.value });
            }}
            disabled={disabled}
          >
            <SelectTrigger aria-label={label} className="w-36">
              <SelectValue placeholder="Choose" />
            </SelectTrigger>
            <SelectContent>
              {options.map((option) => (
                <SelectItem key={key(option.value)} value={key(option.value)}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </DefaultCustomSegment>
      }
    />
  );
}
