import { useState } from "react";
import { Link } from "react-router";
import { RefreshCw } from "lucide-react";
import type { MarkerEditAuditEntry, MarkerSegment } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAllMarkerEditHistory } from "@/hooks/queries/admin/markers";
import { MARKER_LABELS, formatClock } from "@/lib/markers";

const LIMIT_OPTIONS = ["25", "50", "100"] as const;

export default function AdminMarkerHistory() {
  const [limit, setLimit] = useState("50");
  const history = useAllMarkerEditHistory(Number(limit));
  const rows = history.data ?? [];

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-2">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Marker History</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Recent manual intro, recap, credits, and preview marker edits across the server.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Select value={limit} onValueChange={setLimit}>
            <SelectTrigger className="w-[8rem]" aria-label="History limit">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LIMIT_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {option} rows
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              void history.refetch();
            }}
            disabled={history.isFetching}
          >
            <RefreshCw className={`size-4 ${history.isFetching ? "animate-spin" : ""}`} />
            Refresh
          </Button>
        </div>
      </div>

      <div className="border-border bg-surface overflow-hidden rounded-lg border">
        {history.isLoading ? (
          <MarkerHistorySkeleton />
        ) : history.isError ? (
          <div className="text-destructive px-4 py-8 text-center text-sm">
            Failed to load marker history.
          </div>
        ) : rows.length === 0 ? (
          <div className="text-muted-foreground px-4 py-8 text-center text-sm">
            No marker edits recorded.
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Item</TableHead>
                <TableHead>Segment</TableHead>
                <TableHead>Change</TableHead>
                <TableHead>Actor</TableHead>
                <TableHead>Request</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <MarkerHistoryRow key={row.id} row={row} />
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}

function MarkerHistoryRow({ row }: { row: MarkerEditAuditEntry }) {
  const title = row.media_title || fileName(row.file_path) || `File ${row.media_file_id}`;
  const itemLabel = row.item_type ? `${title} (${row.item_type})` : title;

  return (
    <TableRow>
      <TableCell className="text-muted-foreground text-xs">
        {formatHistoryDate(row.created_at)}
      </TableCell>
      <TableCell>
        <div className="max-w-[18rem] truncate font-medium">
          {row.item_id ? (
            <Link
              to={`/item/${encodeURIComponent(row.item_id)}`}
              className="hover:text-primary hover:underline"
              title={itemLabel}
            >
              {itemLabel}
            </Link>
          ) : (
            <span title={itemLabel}>{itemLabel}</span>
          )}
        </div>
        <div className="text-muted-foreground max-w-[18rem] truncate text-xs">
          {row.file_path || `Media file ${row.media_file_id}`}
        </div>
      </TableCell>
      <TableCell>{MARKER_LABELS[row.segment]}</TableCell>
      <TableCell>
        <div className="flex items-center gap-2">
          <Badge variant={row.action === "clear" ? "outline" : "secondary"}>
            {row.action === "clear" ? "Cleared" : "Set"}
          </Badge>
          <span className="text-muted-foreground font-mono text-xs">
            {formatHistoryRange(row.before)}
            {" -> "}
            {formatHistoryRange(row.after)}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <div className="font-medium">{row.username ?? "Unknown user"}</div>
        {row.impersonator_username && (
          <div className="text-muted-foreground text-xs">via {row.impersonator_username}</div>
        )}
      </TableCell>
      <TableCell>
        <div className="text-muted-foreground max-w-[13rem] truncate text-xs">
          {row.request_id ?? "No request id"}
        </div>
        <div className="text-muted-foreground max-w-[13rem] truncate text-xs">
          {row.client_ip ?? row.user_agent ?? ""}
        </div>
      </TableCell>
    </TableRow>
  );
}

function MarkerHistorySkeleton() {
  return (
    <div className="space-y-3 p-4">
      {Array.from({ length: 6 }).map((_, index) => (
        <Skeleton key={index} className="h-10 w-full" />
      ))}
    </div>
  );
}

function formatHistoryRange(marker: MarkerSegment | null): string {
  if (!marker || marker.start == null || marker.end == null) return "none";
  return `${formatClock(marker.start)}-${formatClock(marker.end)}`;
}

function formatHistoryDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function fileName(path: string | undefined): string {
  if (!path) return "";
  return path.split(/[\\/]/).pop() ?? path;
}
