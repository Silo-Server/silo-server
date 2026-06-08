import * as React from "react";

import { cn } from "@/lib/utils";

interface ProgressProps extends Omit<React.ComponentProps<"div">, "role"> {
  /** Completion percentage, clamped to the 0–100 range. */
  value: number;
}

function Progress({ value, className, ...props }: ProgressProps) {
  const clamped = Math.min(100, Math.max(0, value));
  return (
    <div
      data-slot="progress"
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={clamped}
      className={cn("bg-muted h-1.5 overflow-hidden rounded-sm", className)}
      {...props}
    >
      <div className="bg-primary h-full transition-[width]" style={{ width: `${clamped}%` }} />
    </div>
  );
}

export { Progress };
