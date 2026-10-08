import { Fragment } from "react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export interface PillOption {
  /** Unique among the options; what `value` and `onChange` carry. */
  value: string;
  label: string;
  icon?: LucideIcon;
  /** A thin divider is drawn before this pill. */
  separated?: boolean;
  /** How many things the pill holds, shown quietly after its label. */
  count?: number;
  /** This pill alone can't be chosen right now. */
  disabled?: boolean;
}

/**
 * A row of pills, one pressed, with a summary (a count, say) on the right.
 * On phones the pills take the whole width and scroll, and the summary drops
 * to the line below.
 */
export function PillSwitcher({
  label,
  options,
  value,
  onChange,
  disabled,
  summary,
}: {
  /** The group's accessible name. */
  label: string;
  options: PillOption[];
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  summary?: string;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <div
        role="group"
        aria-label={label}
        className="-mx-1 flex min-w-0 flex-1 items-center gap-2 overflow-x-auto px-1 py-1 max-sm:basis-full"
      >
        {options.map((option) => {
          const pressed = option.value === value;
          return (
            <Fragment key={option.value}>
              {option.separated ? (
                <span aria-hidden className="bg-border mx-1.5 h-5 w-px shrink-0" />
              ) : null}
              <button
                type="button"
                aria-pressed={pressed}
                disabled={disabled || option.disabled}
                onClick={() => {
                  if (!pressed) onChange(option.value);
                }}
                className={cn(
                  "focus-visible:ring-ring/50 inline-flex h-[34px] shrink-0 items-center gap-2 rounded-full border px-3.5 text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-60",
                  pressed
                    ? "bg-primary text-primary-foreground border-transparent shadow-sm"
                    : "border-border text-foreground/80 hover:bg-accent",
                )}
              >
                {option.icon ? (
                  <option.icon aria-hidden className="size-[15px] opacity-80" />
                ) : null}
                {option.label}
                {option.count !== undefined ? (
                  // The space keeps "Movies 14" two words for a screen reader; flex hides it.
                  <>
                    {" "}
                    <span className="text-[12.5px] opacity-60">{option.count}</span>
                  </>
                ) : null}
              </button>
            </Fragment>
          );
        })}
      </div>
      {summary ? (
        <p className="text-muted-foreground shrink-0 text-[13px] whitespace-nowrap">{summary}</p>
      ) : null}
    </div>
  );
}
