import { useId } from "react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";

/** A row of mutually exclusive choices, as in Show: Movies / TV shows / Both. */
export function Segmented<Value extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: Value;
  options: ReadonlyArray<{ value: Value; label: string; disabled?: boolean }>;
  onChange: (value: Value) => void;
}) {
  const id = useId();
  return (
    <div className="grid content-start gap-2">
      <span id={id} className="text-[14px] font-semibold">
        {label}
      </span>
      <RadioGroupPrimitive.Root
        aria-labelledby={id}
        value={value}
        onValueChange={(next) => onChange(next as Value)}
        orientation="horizontal"
        className="bg-muted/50 inline-flex w-fit gap-1 rounded-xl p-1"
      >
        {options.map((option) => (
          <RadioGroupPrimitive.Item
            key={option.value}
            value={option.value}
            disabled={option.disabled}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 data-[state=checked]:bg-background data-[state=checked]:text-foreground h-9 rounded-lg px-3.5 text-[13.5px] font-medium outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-45"
          >
            {option.label}
          </RadioGroupPrimitive.Item>
        ))}
      </RadioGroupPrimitive.Root>
    </div>
  );
}
