import { useId, type ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * One card in a radio group of mutually exclusive choices, each with a title
 * and an explanation. Render several inside a `role="radiogroup"` container
 * sharing one `name`.
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
  return (
    <div
      className={cn(
        "border-border/70 flex items-start gap-3 rounded-xl border px-3.5 py-3",
        selected && "border-amber-500/40 bg-amber-500/5",
        disabled && "opacity-60",
      )}
    >
      <input
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
