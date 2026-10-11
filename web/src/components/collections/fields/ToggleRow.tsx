import { useId, type ReactNode } from "react";

import { Switch } from "@/components/ui/switch";

/**
 * A calm switch row: the label names what turning it on does, the help line
 * says who it reaches. `children` sits under the row (a warning, say).
 */
export function ToggleRow({
  label,
  help,
  checked,
  onCheckedChange,
  disabled = false,
  switchLabel,
  children,
}: {
  label: string;
  help: string;
  /** The switch's own name, when the visible label alone doesn't say what it acts on. */
  switchLabel?: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
  children?: ReactNode;
}) {
  const id = useId();
  return (
    <div className="grid gap-3">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <label htmlFor={id} className="text-[14.5px] font-semibold">
            {label}
          </label>
          <p id={`${id}-help`} className="text-muted-foreground mt-0.5 text-[13px] leading-snug">
            {help}
          </p>
        </div>
        <Switch
          id={id}
          aria-label={switchLabel}
          aria-describedby={`${id}-help`}
          checked={checked}
          disabled={disabled}
          // A 44px touch target under 1024px, without changing the layout.
          className="mt-0.5 max-lg:relative max-lg:after:absolute max-lg:after:-inset-[13px] max-lg:after:content-['']"
          onCheckedChange={(next) => onCheckedChange(next === true)}
        />
      </div>
      {children}
    </div>
  );
}
