import { useMemo } from "react";
import { Link } from "react-router";
import { AlertTriangle, CheckCircle2, CircleSlash, Clock, Loader2 } from "lucide-react";

import type { AutoscanScanStatus } from "@/api/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAutoscanScans } from "@/hooks/queries/useAutoscan";
import { formatActiveScanMode, formatActiveScanTrigger } from "@/lib/scanRuns";
import { cn } from "@/lib/utils";
import { SectionError, UserSkeletonRows } from "../feedback";
import { formatScanDuration } from "../format";

const ROW_LIMIT = 8;

/**
 * Recently finished (and in-flight) scan runs.
 *
 * Duration is derived here rather than read from the row: the scans endpoint
 * reports the two timestamps and nothing else, and a scan that is still
 * running has no end to subtract from.
 */
export function ScanActivityWidget() {
  const scansQuery = useAutoscanScans({ limit: ROW_LIMIT });
  const librariesQuery = useAdminLibraries();
  const scans = scansQuery.data?.rows ?? [];

  const libraryNames = useMemo(() => {
    const names = new Map<number, string>();
    for (const library of librariesQuery.data ?? []) {
      names.set(library.id, library.name);
    }
    return names;
  }, [librariesQuery.data]);

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Scan activity</CardTitle>
        <Link
          to="/admin/autoscan"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          All scans ›
        </Link>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {scansQuery.isLoading ? (
          <UserSkeletonRows />
        ) : scansQuery.error ? (
          <SectionError message="Failed to load scan activity." />
        ) : scans.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">No scans yet.</div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Library</TableHead>
                <TableHead className="hidden sm:table-cell">Mode</TableHead>
                <TableHead className="hidden md:table-cell">Trigger</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-right">Duration</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {scans.slice(0, ROW_LIMIT).map((scan) => (
                <TableRow key={scan.id}>
                  <TableCell className="max-w-[14rem] truncate text-[13px] font-semibold">
                    {libraryNames.get(scan.library_id) ?? `Library #${scan.library_id}`}
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden text-[11px] sm:table-cell">
                    {formatActiveScanMode(scan)}
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden text-[11px] md:table-cell">
                    {formatActiveScanTrigger(scan.trigger)}
                  </TableCell>
                  <TableCell>
                    <ScanStatus status={scan.status} />
                  </TableCell>
                  <TableCell className="text-muted-foreground text-right text-[11px] tabular-nums">
                    {formatScanDuration(scan)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function ScanStatus({ status }: { status: AutoscanScanStatus }) {
  const tone = scanStatusTone(status);
  const Icon = tone.icon;
  return (
    <span className={cn("flex items-center gap-1 text-[11px] font-medium", tone.className)}>
      <Icon
        className={cn("h-3.5 w-3.5", status === "running" && "animate-spin")}
        aria-hidden="true"
      />
      {tone.label}
    </span>
  );
}

/** Icon and word together — the tint is a second signal, never the only one. */
function scanStatusTone(status: AutoscanScanStatus) {
  switch (status) {
    case "completed":
      return { label: "Completed", icon: CheckCircle2, className: "text-emerald-500" };
    case "failed":
      return { label: "Failed", icon: AlertTriangle, className: "text-destructive" };
    case "running":
      return { label: "Running", icon: Loader2, className: "text-sky-500" };
    case "cancelled":
      return { label: "Cancelled", icon: CircleSlash, className: "text-amber-500" };
    case "accepted":
      return { label: "Queued", icon: Clock, className: "text-muted-foreground" };
  }
}
