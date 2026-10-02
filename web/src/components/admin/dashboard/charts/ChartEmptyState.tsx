import { Skeleton } from "@/components/ui/skeleton";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";

/**
 * Placeholder for a chart with nothing to draw yet.
 *
 * The metrics sampler only collects while the server runs, so a fresh install
 * has no history — say that plainly ("Collecting data — samples since …")
 * instead of drawing an empty axis that reads as broken.
 */
export function ChartEmptyState({
  height = 160,
  fill = false,
  message = "No data yet",
  since,
  detail,
  className,
}: {
  height?: number;
  /** Fill the parent instead of reserving `height`, matching a fill-height chart. */
  fill?: boolean;
  message?: string;
  /** ISO timestamp of the oldest sample; switches the copy to the collecting state. */
  since?: string | null;
  detail?: string;
  className?: string;
}) {
  const collectingSince = since ? formatRelativeTime(since) : null;
  const headline = collectingSince ? "Collecting data" : message;
  const sub = collectingSince ? `Samples since ${collectingSince}` : detail;

  return (
    <div
      className={cn(
        "text-muted-foreground flex flex-col items-center justify-center gap-1 text-center",
        fill && "min-h-0 flex-1",
        className,
      )}
      style={fill ? undefined : { minHeight: height }}
    >
      <div className="text-sm">{headline}</div>
      {sub ? <div className="text-[11px] opacity-80">{sub}</div> : null}
    </div>
  );
}

/** Loading placeholder for a plotted chart. */
export function ChartSkeleton({ height = 160, fill = false }: { height?: number; fill?: boolean }) {
  return (
    <Skeleton
      className={cn("w-full rounded-md", fill && "min-h-0 flex-1")}
      style={fill ? undefined : { height }}
    />
  );
}

/** Loading placeholder for a bar list. */
export function BarListSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-7 rounded-md" />
      ))}
    </div>
  );
}
