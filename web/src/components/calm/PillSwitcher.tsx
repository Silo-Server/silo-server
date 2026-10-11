import { Fragment } from "react";
import { ChevronLeft, ChevronRight, type LucideIcon } from "lucide-react";
import { edgeMask, useScrollStrip } from "@/hooks/useScrollStrip";
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
 * Pills that don't fit scroll sideways without a scrollbar: the edge with more
 * past it fades, and with a mouse a chevron there pages the strip. The pressed
 * pill stays in view. On phones the pills take the whole width and the summary
 * drops to the line below.
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
  // The pressed pill stops clear of the fade (4rem).
  const { ref, edges, measure, page } = useScrollStrip<HTMLDivElement>(
    `[aria-pressed="true"]`,
    value,
    64,
  );
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <div className="relative min-w-0 flex-1 max-sm:basis-full">
        <div
          ref={ref}
          role="group"
          aria-label={label}
          onScroll={measure}
          className={cn(
            "-mx-1 overflow-x-auto px-1 py-1 [--strip-fade:4rem] [--strip-hold:1.75rem] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
            edgeMask(edges),
          )}
        >
          <div className="flex w-max items-center gap-2">
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
        </div>
        {edges.left ? <PageButton side="left" onClick={() => page("left")} /> : null}
        {edges.right ? <PageButton side="right" onClick={() => page("right")} /> : null}
      </div>
      {summary ? (
        <p className="text-muted-foreground shrink-0 text-[13px] whitespace-nowrap">{summary}</p>
      ) : null}
    </div>
  );
}

/**
 * Pages the strip toward `side`. Only for a mouse or trackpad: touch swipes
 * the strip, and the keyboard reaches every pill with Tab, which scrolls it
 * into view, so the button stays out of the tab order and the accessibility tree.
 */
function PageButton({ side, onClick }: { side: "left" | "right"; onClick: () => void }) {
  const Icon = side === "left" ? ChevronLeft : ChevronRight;
  return (
    <button
      type="button"
      tabIndex={-1}
      aria-hidden
      onClick={onClick}
      className={cn(
        "bg-background/90 text-foreground/80 hover:text-foreground hover:bg-accent border-border absolute top-1/2 hidden size-7 -translate-y-1/2 items-center justify-center rounded-full border shadow-sm backdrop-blur-sm transition-colors [:root[data-fine-pointer=true]_&]:inline-flex",
        side === "left" ? "left-0" : "right-0",
      )}
    >
      <Icon className="size-4" />
    </button>
  );
}
