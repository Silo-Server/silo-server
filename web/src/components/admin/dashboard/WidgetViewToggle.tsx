import { cn } from "@/lib/utils";

export interface WidgetViewOption<T extends string> {
  value: T;
  /** Short button text. */
  label: string;
  /** Accessible name: what pressing the button shows. */
  ariaLabel: string;
}

/**
 * A widget's presentation switch (Egress's Total/Nodes, Concurrent streams'
 * Lines/Stacked/Total), styled like the range picker beside it. Toggle buttons
 * rather than one control: each says which view it shows, and the pressed
 * state tells a screen reader which one is on.
 */
export function WidgetViewToggle<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: readonly WidgetViewOption<T>[];
  onChange: (value: T) => void;
}) {
  return (
    <div className="border-border/60 bg-muted/20 flex shrink-0 items-center gap-0.5 rounded-full border p-0.5">
      {options.map((option) => {
        const active = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            aria-label={option.ariaLabel}
            className={cn(
              "cursor-pointer rounded-full px-1.5 py-0.5 text-[0.625rem] leading-none font-semibold transition-colors",
              "focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none",
              active
                ? "border-primary/40 bg-primary/15 text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-muted/60",
            )}
            onClick={() => onChange(option.value)}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
