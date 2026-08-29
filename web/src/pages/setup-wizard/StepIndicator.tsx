import type { StepDef } from "./useWizardSteps";

export function StepIndicator({ steps }: { steps: StepDef[] }) {
  const activeIndex = steps.findIndex((s) => s.active);

  return (
    <div className="space-y-3">
      {/* Segmented progress track */}
      <div
        className="flex gap-1"
        role="progressbar"
        aria-valuenow={activeIndex + 1}
        aria-valuemax={steps.length}
      >
        {steps.map((step) => (
          <div
            key={step.id}
            className={`h-1 flex-1 rounded-full transition-all duration-(--duration-slow) ${
              step.complete
                ? "bg-foreground/25"
                : step.active
                  ? "bg-foreground shadow-[0_0_8px_rgba(232,232,236,0.15)]"
                  : "bg-foreground/[0.06]"
            }`}
          />
        ))}
      </div>

      {/* Step context */}
      <p className="text-muted-foreground text-xs tracking-wide">
        <span className="uppercase">Setup</span>
        <span className="text-foreground/15 mx-2">/</span>
        <span>
          Step {activeIndex + 1} of {steps.length}
        </span>
      </p>
    </div>
  );
}
