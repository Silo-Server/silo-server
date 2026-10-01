import { useId, useState } from "react";

import { Input } from "@/components/ui/input";
import { SETTINGS_NUMBER_WIDTH, SettingFieldRow } from "@/pages/admin-settings/SettingField";

export interface LimitFieldProps {
  label: string;
  /** Stored value; equal to `unlimitedValue` when the limit is off. */
  value: string;
  onChange: (value: string) => void;
  /** Sentinel the backend reads as "no limit". */
  unlimitedValue?: string;
  /** Fallback used when a limit is re-enabled and nothing was typed before. */
  fallbackValue?: string;
  unlimitedLabel?: string;
  /** Rendered in the row's trailing unit slot, e.g. "Mbps". */
  unit?: string;
  /**
   * Stored units per displayed unit, e.g. `1e9` to type a byte budget in GB.
   * The stored value and the unlimited sentinel are unchanged — only what the
   * admin reads and types is scaled.
   */
  scale?: number;
  hint?: string;
  min?: number;
  disabled?: boolean;
  restartRequired?: boolean;
}

/**
 * Decimal places kept when a stored value is converted for display. Three is
 * MB granularity on a GB budget, which is finer than any storage decision an
 * admin makes here, and it keeps float division from surfacing as 53.68709…1.
 */
const SCALED_DECIMALS = 3;

function toDisplayUnits(stored: string, scale: number): string {
  if (scale === 1) return stored;
  const trimmed = stored.trim();
  if (trimmed === "") return "";
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed)) return stored;
  return String(Number((parsed / scale).toFixed(SCALED_DECIMALS)));
}

function toStoredUnits(displayed: string, scale: number): string {
  if (scale === 1) return displayed;
  const trimmed = displayed.trim();
  if (trimmed === "") return "";
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed)) return displayed;
  return String(Math.round(parsed * scale));
}

/**
 * Number input paired with an "Unlimited" checkbox, replacing the
 * "0 = unlimited" hint convention. The sentinel never reaches the admin's
 * eyes, but the saved value is unchanged.
 */
export function LimitField({
  label,
  value,
  onChange,
  unlimitedValue = "0",
  fallbackValue = "",
  unlimitedLabel = "Unlimited",
  unit,
  scale = 1,
  hint,
  min = 0,
  disabled = false,
  restartRequired = false,
}: LimitFieldProps) {
  const controlId = useId();
  const checkboxId = useId();
  const hintId = useId();
  const unlimited = value.trim() === unlimitedValue;
  // Remembers the limit that Unlimited replaced so unchecking restores it
  // instead of dumping the admin back onto an empty box.
  const [lastLimit, setLastLimit] = useState(fallbackValue);
  // Keeps what was typed while it still round-trips to the stored value, so a
  // scaled field does not eat the "." of "1." or rewrite "1.50" mid-keystroke.
  const [draft, setDraft] = useState<string | null>(null);
  const displayed =
    draft !== null && toStoredUnits(draft, scale) === value ? draft : toDisplayUnits(value, scale);

  function changeLimit(next: string) {
    setDraft(next);
    onChange(toStoredUnits(next, scale));
  }

  function toggleUnlimited(checked: boolean) {
    setDraft(null);
    if (checked) {
      setLastLimit(unlimited ? fallbackValue : value);
      onChange(unlimitedValue);
      return;
    }
    onChange(lastLimit.trim() === unlimitedValue ? fallbackValue : lastLimit);
  }

  return (
    <SettingFieldRow
      label={label}
      htmlFor={controlId}
      description={hint}
      descriptionId={hintId}
      restartRequired={restartRequired}
      unit={unit}
    >
      <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
        {/* Ahead of the input, not after it: the row's unit slot sits to the
            right of the control edge, so anything between the input and that
            edge would knock this field's box out of line with the plain number
            fields it is stacked against. */}
        <label htmlFor={checkboxId} className="flex shrink-0 items-center gap-2 text-sm">
          <input
            id={checkboxId}
            type="checkbox"
            checked={unlimited}
            onChange={(e) => toggleUnlimited(e.target.checked)}
            disabled={disabled}
          />
          {unlimitedLabel}
        </label>
        <Input
          id={controlId}
          type="number"
          min={min}
          // A scaled field is fractional by nature (0.5 GB), and the default
          // step of 1 would mark those values invalid.
          step={scale === 1 ? undefined : "any"}
          value={unlimited ? "" : displayed}
          placeholder={unlimited ? unlimitedLabel : undefined}
          onChange={(e) => changeLimit(e.target.value)}
          disabled={disabled || unlimited}
          className={SETTINGS_NUMBER_WIDTH}
          aria-describedby={hint ? hintId : undefined}
        />
      </div>
    </SettingFieldRow>
  );
}
