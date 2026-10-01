import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * A selectable card used by the Add-source flow. Cards rather than a dropdown
 * because each option needs a sentence explaining what it does — the choice
 * between "Sonarr pushes to Silo" and "Silo polls Sonarr" is meaningless
 * without it, and a <select> has nowhere to put that.
 */
export function ChoiceCard({
  title,
  description,
  icon,
  badge,
  selected,
  onSelect,
  disabled,
  describedBy,
}: {
  title: string;
  description?: string;
  icon?: ReactNode;
  /** Short qualifier shown beside the title, e.g. "Recommended". */
  badge?: string;
  selected: boolean;
  onSelect: () => void;
  /**
   * Unavailable: stays focusable, so a screen reader still reaches it and the
   * reason `describedBy` names, but does nothing.
   */
  disabled?: boolean;
  describedBy?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      aria-disabled={disabled || undefined}
      aria-describedby={describedBy}
      onClick={disabled ? undefined : onSelect}
      className={cn(
        "rounded-lg border p-3 text-left transition-colors aria-disabled:cursor-not-allowed aria-disabled:opacity-60",
        selected
          ? "border-primary bg-accent"
          : "border-border [&:not([aria-disabled])]:hover:bg-accent/50",
      )}
    >
      <span className="flex flex-wrap items-center gap-1.5 text-sm font-medium">
        {icon}
        {title}
        {badge && (
          <span className="bg-success/15 text-success rounded-full px-2 py-0.5 text-[10px] font-semibold tracking-wide uppercase">
            {badge}
          </span>
        )}
      </span>
      {description && (
        <span className="text-muted-foreground mt-1 block text-xs leading-relaxed">
          {description}
        </span>
      )}
    </button>
  );
}

/**
 * Numbered progress indicator for the Add-source flow. Steps are supplied by
 * the caller because their number varies per source: a webhook-only watcher
 * needing no credentials genuinely has fewer questions than a pollable arr.
 */
export function StepTrail({ steps, currentIndex }: { steps: string[]; currentIndex: number }) {
  if (steps.length < 2) return null;

  return (
    <ol className="flex flex-wrap items-center gap-x-2 gap-y-1">
      {steps.map((step, index) => {
        const done = index < currentIndex;
        const active = index === currentIndex;
        return (
          <li key={step} className="flex items-center gap-2">
            {index > 0 && <span className="bg-border hidden h-px w-6 sm:block" aria-hidden />}
            <span
              className={cn(
                "flex items-center gap-1.5 text-xs",
                active ? "text-foreground font-medium" : "text-muted-foreground",
              )}
              aria-current={active ? "step" : undefined}
            >
              <span
                className={cn(
                  "grid size-5 shrink-0 place-items-center rounded-full border text-[10px] font-semibold",
                  active && "bg-foreground text-background border-transparent",
                  done && "border-success/40 bg-success/15 text-success",
                )}
              >
                {done ? "✓" : index + 1}
              </span>
              {step}
            </span>
          </li>
        );
      })}
    </ol>
  );
}
