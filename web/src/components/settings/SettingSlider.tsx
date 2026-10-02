import { useState } from "react";

import { Slider } from "@/components/ui/slider";
import { SETTINGS_CONTROL_WIDTH } from "@/pages/admin-settings/SettingField";
import { cn } from "@/lib/utils";

interface SettingSliderProps {
  /** The persisted value. The thumb returns here if a save is rejected. */
  value: number;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  disabled?: boolean;
  "aria-label"?: string;
  /** Fired once per gesture, when the thumb is released or a key is pressed. */
  onCommit: (value: number) => void;
  className?: string;
}

/**
 * A slider bound to a saved setting.
 *
 * Radix's Slider is controlled here, so the thumb only moves if something
 * re-renders it with a new value. Persisting on every pointer move would flood
 * the API, but rendering only the saved value makes the control inert — the
 * thumb snaps back under the pointer and onValueCommit never sees a change,
 * which is exactly the bug this component exists to prevent. While a drag is
 * in flight the draft value drives the thumb and the readout; on release the
 * draft is committed and cleared, and the saved value takes over again.
 */
export function SettingSlider({
  value,
  min,
  max,
  step,
  unit,
  disabled = false,
  "aria-label": ariaLabel,
  onCommit,
  className,
}: SettingSliderProps) {
  const [draft, setDraft] = useState<number | null>(null);
  const shown = draft ?? value;

  return (
    // Defaults to the shared settings control width so a slider row ends on the
    // same edge as the inputs and selects it is stacked against.
    <div className={className ?? cn("flex items-center gap-3", SETTINGS_CONTROL_WIDTH)}>
      <Slider
        value={[shown]}
        min={min}
        max={max}
        step={step}
        disabled={disabled}
        aria-label={ariaLabel}
        thumbLabels={ariaLabel ? [ariaLabel] : undefined}
        onValueChange={(values) => setDraft(values[0] ?? shown)}
        onValueCommit={(values) => {
          setDraft(null);
          const next = values[0];
          if (next !== undefined && next !== value) onCommit(next);
        }}
      />
      <span className="text-muted-foreground min-w-16 text-right text-xs font-medium tabular-nums">
        {shown}
        {unit ? ` ${unit}` : ""}
      </span>
    </div>
  );
}
