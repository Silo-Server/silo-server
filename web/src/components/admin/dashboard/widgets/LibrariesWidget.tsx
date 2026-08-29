import { useMemo } from "react";
import { Link } from "react-router";
import { Library, ScanLine, Square } from "lucide-react";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  useAdminLibraries,
  useCancelLibraryScans,
  useScanLibrary,
} from "@/hooks/queries/admin/libraries";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { compareActiveScans } from "@/lib/scanRuns";
import { cn } from "@/lib/utils";
import type { ScanRun } from "@/api/types";
import { formatDashboardLibraryScanProgress } from "../format";
import { LibrarySkeletonRows, SectionError } from "../feedback";

export function LibrariesWidget() {
  useEventChannel("scans");
  const librariesQuery = useAdminLibraries();
  const libraries = librariesQuery.data ?? [];
  const scanLibrary = useScanLibrary();
  const cancelScans = useCancelLibraryScans();
  const { data: activeScans = [] } = useActiveScans();

  const activeScansByLibraryId = useMemo(() => {
    const scansByLibraryID = new Map<number, ScanRun[]>();
    for (const scan of activeScans) {
      if (scan.status !== "accepted" && scan.status !== "running") {
        continue;
      }
      const scans = scansByLibraryID.get(scan.library_id) ?? [];
      scans.push(scan);
      scansByLibraryID.set(scan.library_id, scans);
    }
    for (const scans of scansByLibraryID.values()) {
      scans.sort(compareActiveScans);
    }
    return scansByLibraryID;
  }, [activeScans]);

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Libraries</CardTitle>
        <Link
          to="/admin/libraries"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          Manage ›
        </Link>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 space-y-2 overflow-y-auto">
        {librariesQuery.isLoading ? (
          <LibrarySkeletonRows />
        ) : librariesQuery.error ? (
          <SectionError message="Failed to load libraries." />
        ) : libraries.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            No libraries configured.
          </div>
        ) : (
          libraries.map((lib) => {
            const activeLibraryScans = activeScansByLibraryId.get(lib.id) ?? [];
            const primaryActiveScan = activeLibraryScans[0];
            const hasActiveScan = activeLibraryScans.length > 0;
            const isScanStarting = scanLibrary.isPending && scanLibrary.variables === lib.id;
            const isCancellingScan = cancelScans.isPending && cancelScans.variables === lib.id;
            const scanProgressLabel = primaryActiveScan
              ? formatDashboardLibraryScanProgress(primaryActiveScan, activeLibraryScans.length)
              : isScanStarting
                ? "Starting scan..."
                : "";

            return (
              <div
                key={lib.id}
                className="bg-surface border-border hover:bg-surface-hover flex items-center gap-3 rounded-md border p-3 transition-colors duration-150"
              >
                {lib.poster_url ? (
                  <img
                    src={lib.poster_url}
                    alt={lib.name}
                    className="border-border h-8 w-14 flex-shrink-0 rounded border object-cover"
                  />
                ) : (
                  <div className="bg-primary/5 border-primary/10 flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg border">
                    <Library className="text-primary h-4 w-4" />
                  </div>
                )}
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-bold">{lib.name}</div>
                  <div className="text-muted-foreground flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px]">
                    <span>
                      {lib.type} · {lib.paths.length} {lib.paths.length === 1 ? "path" : "paths"}
                    </span>
                    {scanProgressLabel ? (
                      <>
                        <span className="text-border/70">·</span>
                        <span className="max-w-[22rem] truncate text-amber-300 tabular-nums">
                          {scanProgressLabel}
                        </span>
                      </>
                    ) : null}
                  </div>
                </div>
                <div className="flex flex-shrink-0 items-center gap-2">
                  <Button
                    variant={hasActiveScan ? "destructive" : "ghost"}
                    size="icon"
                    className="h-7 w-7 cursor-pointer"
                    onClick={(e) => {
                      e.stopPropagation();
                      if (hasActiveScan) {
                        cancelScans.mutate(lib.id);
                        return;
                      }
                      scanLibrary.mutate(lib.id);
                    }}
                    disabled={hasActiveScan ? isCancellingScan : isScanStarting}
                    title={hasActiveScan ? "Stop Library Scans" : "Scan Library"}
                    aria-label={hasActiveScan ? `Stop scans for ${lib.name}` : `Scan ${lib.name}`}
                  >
                    {hasActiveScan ? (
                      <Square className="h-3 w-3 fill-current" />
                    ) : (
                      <ScanLine className={cn("h-3 w-3", isScanStarting && "animate-pulse")} />
                    )}
                  </Button>
                  <div
                    className={cn(
                      "h-2 w-2 rounded-full",
                      hasActiveScan || isScanStarting
                        ? "bg-amber-400 shadow-[0_0_6px_rgba(251,191,36,0.65)]"
                        : lib.enabled
                          ? "bg-green-500"
                          : "bg-muted-foreground/30",
                      hasActiveScan && "animate-pulse",
                    )}
                    title={
                      hasActiveScan
                        ? "Scan in progress"
                        : lib.enabled
                          ? "Library enabled"
                          : "Library disabled"
                    }
                  />
                </div>
              </div>
            );
          })
        )}
      </CardContent>
    </Card>
  );
}
