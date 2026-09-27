import { useEffect, useRef } from "react";
import { Check } from "lucide-react";

import { cn } from "@/lib/utils";
import type { SkippableStep } from "./setupStorage";
import type { StepDef, WizardStepId } from "./useWizardSteps";

interface StepRailProps {
  steps: StepDef[];
  summaries: Partial<Record<WizardStepId, string>>;
  /** Reopen a finished step. Only skippable steps are ever reachable. */
  onVisit: (id: SkippableStep) => void;
}

/**
 * The wizard's spine: every step in order, the current one marked, finished
 * ones recording what was chosen. On wide screens it is a vertical ladder
 * beside the content; on narrow ones the same list collapses to a compact
 * horizontal strip that shows only the current rung's neighbours.
 */
export function StepRail({ steps, summaries, onVisit }: StepRailProps) {
  const activeIndex = steps.findIndex((step) => step.active);
  const listRef = useRef<HTMLOListElement>(null);

  // On the narrow horizontal strip the current rung can sit past the edge;
  // bring it into view whenever the step changes. A no-op on the vertical rail.
  useEffect(() => {
    const active = listRef.current?.querySelector<HTMLElement>('[aria-current="step"]');
    active?.scrollIntoView?.({ block: "nearest", inline: "center" });
  }, [activeIndex]);

  return (
    <nav aria-label="Setup steps" className="setup-rail">
      <ol ref={listRef} className="setup-rail-list">
        {steps.map((step, index) => {
          const state = step.active ? "active" : step.complete ? "done" : "todo";
          const summary = summaries[step.id];
          const content = (
            <>
              <span className="setup-rail-marker" aria-hidden="true">
                {state === "done" ? <Check className="size-3" strokeWidth={3} /> : null}
              </span>
              <span className="setup-rail-text">
                <span
                  className={cn(
                    "setup-rail-label",
                    // On the narrow strip only the current rung and its
                    // neighbours keep a visible label.
                    Math.abs(index - activeIndex) > 1 && "max-[52rem]:sr-only",
                  )}
                >
                  {step.label}
                </span>
                {summary ? <span className="setup-rail-summary">{summary}</span> : null}
              </span>
            </>
          );
          return (
            <li
              key={step.id}
              data-state={state}
              className="setup-rail-item"
              aria-current={step.active ? "step" : undefined}
            >
              {step.reachable && !step.active ? (
                <button
                  type="button"
                  className="setup-rail-row setup-rail-row-button"
                  onClick={() => onVisit(step.id as SkippableStep)}
                >
                  {content}
                </button>
              ) : (
                <span className="setup-rail-row">{content}</span>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
