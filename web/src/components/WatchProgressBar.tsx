import type { HTMLAttributes } from "react";

import { cn } from "@/lib/utils";

interface WatchProgressBarProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** Watched share, 0–100. Values outside the range are clamped. */
  percent: number;
  /** Fill colour; defaults to the theme's primary. */
  fillColor?: string;
  /** Animate width changes (cards whose progress updates in place). */
  animate?: boolean;
  /** Sit lower, under a bottom-corner label such as a box set's film count. */
  belowCornerLabel?: boolean;
}

/**
 * Watch-progress pill drawn over card artwork. The dark track carries a
 * keyline (outline plus soft shadow) and the fill a hairline edge, so the
 * bar stays legible on bright backdrops where a light fill on a translucent
 * track would vanish. The native apps draw the same design; keep them in step.
 *
 * It sits at the card's bottom edge, inset like the other card overlays, or
 * lower with `belowCornerLabel`.
 */
export default function WatchProgressBar({
  percent,
  fillColor = "var(--primary)",
  animate = false,
  belowCornerLabel = false,
  className,
  ...rest
}: WatchProgressBarProps) {
  const clamped = Math.min(100, Math.max(0, percent));
  return (
    <div
      data-slot="watch-progress"
      className={cn(
        "pointer-events-none absolute inset-x-2.5 h-[3px] rounded-full bg-black/55",
        belowCornerLabel ? "bottom-1" : "bottom-2",
        "shadow-[0_0_0_1px_rgba(0,0,0,0.35),0_1px_3px_rgba(0,0,0,0.45)]",
        className,
      )}
      {...rest}
    >
      <div
        className={cn(
          "h-full rounded-full shadow-[0_0_0_0.5px_rgba(0,0,0,0.4)]",
          animate && "transition-[width] duration-300",
        )}
        style={{ width: `${clamped}%`, background: fillColor }}
      />
    </div>
  );
}
