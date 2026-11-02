import { useId, useRef, type ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * One card in a radio group of mutually exclusive choices, each with a title
 * and an explanation. Render several inside a `role="radiogroup"` container
 * sharing one `name`. The whole card selects its option; the radio stays the
 * keyboard and screen-reader control. A caller that nests its own controls in
 * the card should make them ignore pointer events while the option is not
 * selected, so those clicks reach the card instead.
 */
export function ChoiceOption<T extends string>({
  name,
  value,
  selected,
  disabled,
  title,
  children,
  onSelect,
}: {
  name: string;
  value: T;
  selected: boolean;
  disabled: boolean;
  title: string;
  children: ReactNode;
  onSelect: (value: T) => void;
}) {
  const id = useId();
  const radio = useRef<HTMLInputElement>(null);
  return (
    <div
      onClick={(event) => {
        if (disabled) return;
        onSelect(value);
        // Like a label click, a click on the card's text moves focus to the
        // radio so the arrow keys keep working. Clicks on the radio, its label,
        // or a nested control already place focus where it belongs.
        if (!(event.target as Element).closest("input, label, textarea, select, button, a")) {
          radio.current?.focus();
        }
      }}
      className={cn(
        "border-border/70 flex cursor-pointer items-start gap-3 rounded-xl border px-3.5 py-3",
        selected && "border-amber-500/40 bg-amber-500/5",
        disabled && "cursor-not-allowed opacity-60",
      )}
    >
      <input
        ref={radio}
        id={id}
        type="radio"
        name={name}
        value={value}
        className="mt-1 accent-amber-500"
        checked={selected}
        disabled={disabled}
        onChange={() => onSelect(value)}
      />
      <div className="min-w-0 flex-1 space-y-0.5">
        <label htmlFor={id} className="block text-sm font-medium">
          {title}
        </label>
        <div className="text-muted-foreground text-xs leading-relaxed">{children}</div>
      </div>
    </div>
  );
}
