import { cn } from "@/lib/utils";

/**
 * A load bar, and the fixed-height slot it sits in so rows stay aligned whether
 * or not they have one.
 *
 * A null fill draws no bar at all. `ResourceMetric.fill` is null both when
 * nothing measured the reading and when it has no ceiling to measure against,
 * and a bar at zero for either would read as "measured and idle" — the mistake
 * every dash on the admin resource surfaces exists to avoid. A measured zero
 * does still draw, as a stub, so that it stays distinguishable from no
 * measurement.
 */
export function LoadMeter({ fill, warning }: { fill: number | null; warning?: boolean }) {
  return (
    <div className="h-[3px]" aria-hidden="true">
      {fill !== null && (
        <div className="bg-border/70 h-full overflow-hidden rounded-full">
          <div
            className={cn(
              // Readings poll on an interval, so the width change is the only
              // thing on the page that shows load moving rather than just being
              // different. motion-safe, because that is also the only reason
              // anyone would want it off.
              "h-full min-w-[3px] rounded-full motion-safe:transition-[width]",
              warning ? "bg-warning" : "bg-foreground/55",
            )}
            style={{ width: `${Math.min(100, Math.max(0, fill))}%` }}
          />
        </div>
      )}
    </div>
  );
}
