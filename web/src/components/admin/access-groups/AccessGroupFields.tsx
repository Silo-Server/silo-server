import { useId } from "react";

import { StreamBitrateLimitInput } from "@/components/StreamBitrateLimitInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";

interface ToggleRowProps {
  label: string;
  description: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}

/**
 * A labeled switch in a bordered row. Below `lg` the whole row is the switch's
 * `<label>`, so a tap anywhere on it flips the switch; from `lg` up only the
 * switch takes the pointer, as before.
 */
export function ToggleRow({
  label,
  description,
  checked,
  onCheckedChange,
  disabled,
}: ToggleRowProps) {
  const descriptionId = useId();
  return (
    <label
      className={cn(
        "border-border flex items-center justify-between gap-4 rounded-lg border px-3 py-2.5 lg:pointer-events-none",
        !disabled && "max-lg:cursor-pointer",
      )}
    >
      <span className="min-w-0">
        <span className="block text-sm font-medium">{label}</span>
        <span id={descriptionId} className="text-muted-foreground block text-xs">
          {description}
        </span>
      </span>
      <Switch
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
        aria-label={label}
        aria-describedby={descriptionId}
        className="lg:pointer-events-auto"
      />
    </label>
  );
}

export function StreamBitrateLimitField({
  id,
  label,
  hint,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  value: number | null;
  onChange: (kbps: number | null) => void;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <StreamBitrateLimitInput id={id} label={label} value={value} onValueChange={onChange} />
      <p className="text-muted-foreground text-xs">{hint}</p>
    </div>
  );
}

interface LimitFieldProps {
  id: string;
  label: string;
  hint: string;
  value: number;
  onChange: (value: number) => void;
}

export function LimitField({ id, label, hint, value, onChange }: LimitFieldProps) {
  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between">
        <Label htmlFor={id}>{label}</Label>
        <span className="text-muted-foreground text-xs">{hint}</span>
      </div>
      <Input
        id={id}
        type="number"
        min={0}
        value={value}
        onChange={(event) => {
          const raw = event.target.value;
          if (raw === "") {
            if (event.target.validity.badInput) return;
            onChange(0);
            return;
          }
          if (!/^\d+$/.test(raw)) return;
          const next = Number(raw);
          if (Number.isSafeInteger(next)) onChange(next);
        }}
      />
    </div>
  );
}
