import * as React from "react";

import { cn } from "@/lib/utils";

/** A range drawn on the track behind the value fill, such as buffered media. */
export interface ProgressSegment {
  /** Start of the range as a percentage of the track, clamped to 0–100. */
  from: number;
  /** End of the range as a percentage of the track, clamped to 0–100. */
  to: number;
  className: string;
}

interface ProgressProps extends Omit<React.ComponentProps<"div">, "role"> {
  /** Completion percentage, clamped to the 0–100 range. */
  value: number;
  /** Ranges drawn behind the value fill; empty ranges are skipped. */
  segments?: ProgressSegment[];
}

const clampPercent = (value: number) => Math.min(100, Math.max(0, value));

function Progress({ value, segments, className, ...props }: ProgressProps) {
  const clamped = clampPercent(value);
  const layered = segments !== undefined && segments.length > 0;
  return (
    <div
      data-slot="progress"
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={clamped}
      className={cn("bg-muted h-1.5 overflow-hidden rounded-sm", layered && "relative", className)}
      {...props}
    >
      {segments?.map((segment, index) => {
        const from = clampPercent(segment.from);
        const to = clampPercent(segment.to);
        if (to <= from) return null;
        return (
          <div
            key={index}
            className={cn("absolute inset-y-0 transition-[left,width]", segment.className)}
            style={{ left: `${from}%`, width: `${to - from}%` }}
          />
        );
      })}
      <div
        className={cn("bg-primary h-full transition-[width]", layered && "relative")}
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}

export { Progress };
