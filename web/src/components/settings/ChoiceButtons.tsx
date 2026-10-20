import { useId } from "react";

import { Button } from "@/components/ui/button";

interface ChoiceButtonsProps<T extends string> {
  label: string;
  /** Hide the visible label when the section title already says it. */
  hideLabel?: boolean;
  value: T;
  options: readonly { value: T; label: string }[];
  disabled?: boolean;
  onChange: (next: T) => void;
}

/** A row of buttons for picking one of a few choices, as on Accessibility. */
export function ChoiceButtons<T extends string>({
  label,
  hideLabel = false,
  value,
  options,
  disabled,
  onChange,
}: ChoiceButtonsProps<T>) {
  const labelId = useId();
  return (
    <div className="space-y-2">
      <p id={labelId} className={hideLabel ? "sr-only" : "text-sm font-medium"}>
        {label}
      </p>
      <div role="radiogroup" aria-labelledby={labelId} className="flex flex-wrap gap-2">
        {options.map((option) => (
          <Button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={value === option.value}
            size="sm"
            variant={value === option.value ? "default" : "outline"}
            disabled={disabled}
            onClick={() => {
              if (option.value !== value) onChange(option.value);
            }}
          >
            {option.label}
          </Button>
        ))}
      </div>
    </div>
  );
}
