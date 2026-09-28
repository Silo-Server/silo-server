import { useEffect, useState } from "react";
import type { RequestDownload } from "@/api/types";
import { Progress } from "@/components/ui/progress";
import { formatRequestDownload, requestDownloadPercent } from "@/lib/requestDownload";
import { cn } from "@/lib/utils";

/** How often a shown estimate is checked against the clock again. */
const ESTIMATE_CLOCK_INTERVAL_MS = 30_000;

/**
 * Renders again every 30 seconds while enabled. The estimate is worked out
 * against the clock, and a poll that returns the same figures does not
 * render its readers again, so without this an estimate would neither count
 * down nor disappear once it has passed or its figures have gone stale.
 */
function useEstimateClock(enabled: boolean) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const id = window.setInterval(() => setTick((tick) => tick + 1), ESTIMATE_CLOCK_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [enabled]);
}

/**
 * How far a request's or one server's downloads are: a bar once the size is
 * known, over the phase with its percentage and estimate. Admin views name a
 * blocked import as such.
 */
export function RequestDownloadProgress({
  download,
  admin = false,
  className,
}: {
  download: RequestDownload;
  admin?: boolean;
  className?: string;
}) {
  useEstimateClock(Boolean(download.estimated_completion_at));
  const percent = requestDownloadPercent(download);
  return (
    <div className={cn("min-w-0 space-y-1", className)} data-download-phase={download.phase}>
      {percent !== undefined ? <Progress value={percent} aria-label="Download progress" /> : null}
      <p className="text-muted-foreground text-xs leading-snug">
        {formatRequestDownload(download, { admin })}
      </p>
    </div>
  );
}
