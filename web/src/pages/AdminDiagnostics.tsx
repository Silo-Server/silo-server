import { useEffect, useMemo, useState } from "react";
import type { FormEvent, KeyboardEvent, ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { Bug, Download, ExternalLink, FilterX, Trash2, TriangleAlert } from "lucide-react";
import { toast } from "sonner";

import type { DiagnosticReport, DiagnosticReportState, DiagnosticReportSummary } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { DateTimePicker } from "@/components/DateTimePicker";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { useDateTimeFormat } from "@/hooks/useDateTimeFormat";
import {
  downloadDiagnosticReport,
  useDeleteDiagnosticReport,
  useDiagnosticReport,
  useDiagnosticReports,
  useDiagnosticsStatus,
  useUpdateDiagnosticsUploadsEnabled,
} from "@/hooks/queries/admin/diagnostics";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";

const PAGE_SIZE = 25;
const FILTER_KEYS = ["user_id", "platform", "report_type", "from", "to", "short_id"];

interface FilterDraft {
  userID: string;
  platform: string;
  reportType: string;
  from: string;
  to: string;
  shortID: string;
}

export default function AdminDiagnostics() {
  useDateTimeFormat();
  const [searchParams, setSearchParams] = useSearchParams();
  const appliedFilterKey = FILTER_KEYS.map((key) => searchParams.get(key) ?? "").join("\u0000");
  const [filters, setFilters] = useState<FilterDraft>(() => draftFromSearchParams(searchParams));
  const [pagination, setPagination] = useState<{
    filterKey: string;
    cursor?: string;
    cursorStack: string[];
  }>(() => ({ filterKey: appliedFilterKey, cursorStack: [] }));
  const [selectedID, setSelectedID] = useState<string>();
  const [deleteConfirmationOpen, setDeleteConfirmationOpen] = useState(false);
  const [downloading, setDownloading] = useState(false);

  useEffect(() => {
    setFilters(draftFromSearchParams(searchParams));
    setPagination((current) =>
      current.filterKey === appliedFilterKey
        ? current
        : { filterKey: appliedFilterKey, cursorStack: [] },
    );
    setSelectedID(undefined);
  }, [appliedFilterKey, searchParams]);

  const activeCursor = pagination.filterKey === appliedFilterKey ? pagination.cursor : undefined;
  const activeCursorStack = pagination.filterKey === appliedFilterKey ? pagination.cursorStack : [];

  const query = useMemo(() => {
    return {
      user_id: searchParams.get("user_id") || undefined,
      platform: searchParams.get("platform") || undefined,
      report_type: searchParams.get("report_type") || undefined,
      from: searchParams.get("from") || undefined,
      to: searchParams.get("to") || undefined,
      short_id: searchParams.get("short_id") || undefined,
      limit: PAGE_SIZE,
      cursor: activeCursor,
    };
  }, [activeCursor, searchParams]);

  const status = useDiagnosticsStatus();
  const updateUploadsEnabled = useUpdateDiagnosticsUploadsEnabled();
  const reports = useDiagnosticReports(query);
  const selectedReport = useDiagnosticReport(selectedID);
  const deleteReport = useDeleteDiagnosticReport();
  const hasAppliedFilters = FILTER_KEYS.some((key) => searchParams.has(key));
  const uploadsEnabled = status.data !== undefined && status.data.status !== "disabled";

  function setFilter<Key extends keyof FilterDraft>(key: Key, value: FilterDraft[Key]) {
    setFilters((current) => ({ ...current, [key]: value }));
  }

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    const userID = normalizeUserID(filters.userID);
    if (filters.userID.trim() && !userID) {
      toast.error("User ID must be a positive whole number.");
      return;
    }
    const next = new URLSearchParams(searchParams);
    setOrDelete(next, "user_id", userID);
    setOrDelete(next, "platform", filters.platform === "all" ? "" : filters.platform);
    setOrDelete(next, "report_type", filters.reportType === "all" ? "" : filters.reportType);
    setOrDelete(next, "from", toRFC3339(filters.from));
    setOrDelete(next, "to", toRFC3339(filters.to));
    setOrDelete(next, "short_id", filters.shortID);
    setSearchParams(next, { replace: true });
    setSelectedID(undefined);
  }

  function clearFilters() {
    const next = new URLSearchParams(searchParams);
    FILTER_KEYS.forEach((key) => next.delete(key));
    setSearchParams(next, { replace: true });
    setSelectedID(undefined);
  }

  function goNext() {
    if (!reports.data?.next_cursor) return;
    setPagination({
      filterKey: appliedFilterKey,
      cursor: reports.data.next_cursor,
      cursorStack: [...activeCursorStack, activeCursor ?? ""],
    });
    setSelectedID(undefined);
  }

  function goPrevious() {
    const next = [...activeCursorStack];
    const previous = next.pop();
    setPagination({
      filterKey: appliedFilterKey,
      cursor: previous || undefined,
      cursorStack: next,
    });
    setSelectedID(undefined);
  }

  async function handleDownload(report: DiagnosticReport) {
    setDownloading(true);
    try {
      await downloadDiagnosticReport(report);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to download diagnostic report");
    } finally {
      setDownloading(false);
    }
  }

  return (
    <div className="space-y-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Client Diagnostics</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Review client crash reports, device context, and correlated playback sessions.
          </p>
        </div>
        <div className="flex items-center gap-5 sm:gap-6">
          <div className="flex items-center gap-2">
            <Label htmlFor="diagnostics-uploads-enabled" className="text-muted-foreground text-sm">
              Client uploads
            </Label>
            <Switch
              id="diagnostics-uploads-enabled"
              checked={uploadsEnabled}
              onCheckedChange={(enabled) => updateUploadsEnabled.mutate(enabled)}
              disabled={!status.data || status.isError || updateUploadsEnabled.isPending}
            />
          </div>
          <div className="text-right">
            <div className="text-muted-foreground text-xs font-medium tracking-[0.2em] uppercase">
              Retention
            </div>
            <div className="text-sm">
              {status.data ? `${status.data.retention_days} days` : "Loading..."}
            </div>
          </div>
        </div>
      </div>

      {status.data?.status !== undefined && status.data.status !== "available" && (
        <FeatureStatusBanner status={status.data.status} />
      )}

      <form
        className="surface-panel-subtle flex flex-wrap items-end gap-x-3 gap-y-4 rounded-2xl p-4"
        onSubmit={applyFilters}
      >
        <FilterField label="User" htmlFor="diagnostics-user" className="w-24">
          <Input
            id="diagnostics-user"
            type="number"
            min={1}
            step={1}
            inputMode="numeric"
            placeholder="Any"
            value={filters.userID}
            onChange={(event) => setFilter("userID", event.target.value)}
          />
        </FilterField>
        <FilterField label="Platform" htmlFor="diagnostics-platform" className="min-w-36 flex-1">
          <Select value={filters.platform} onValueChange={(value) => setFilter("platform", value)}>
            <SelectTrigger id="diagnostics-platform" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All platforms</SelectItem>
              <SelectItem value="android">Android</SelectItem>
              <SelectItem value="android-tv">Android TV</SelectItem>
              <SelectItem value="ios">iOS</SelectItem>
              <SelectItem value="tvos">tvOS</SelectItem>
            </SelectContent>
          </Select>
        </FilterField>
        <FilterField label="Report type" htmlFor="diagnostics-type" className="min-w-36 flex-1">
          <Select
            value={filters.reportType}
            onValueChange={(value) => setFilter("reportType", value)}
          >
            <SelectTrigger id="diagnostics-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All types</SelectItem>
              <SelectItem value="crash">Crash</SelectItem>
              <SelectItem value="anr">ANR</SelectItem>
              <SelectItem value="native_crash">Native crash</SelectItem>
              <SelectItem value="hang">Hang</SelectItem>
              <SelectItem value="abnormal_exit">Abnormal exit</SelectItem>
              <SelectItem value="manual">Manual</SelectItem>
            </SelectContent>
          </Select>
        </FilterField>
        <FilterField label="From" htmlFor="diagnostics-from" className="min-w-44 flex-1">
          <DateTimePicker
            id="diagnostics-from"
            value={filters.from}
            onChange={(value) => setFilter("from", value)}
          />
        </FilterField>
        <FilterField label="To" htmlFor="diagnostics-to" className="min-w-44 flex-1">
          <DateTimePicker
            id="diagnostics-to"
            value={filters.to}
            onChange={(value) => setFilter("to", value)}
          />
        </FilterField>
        <FilterField label="Short ID" htmlFor="diagnostics-short-id" className="min-w-32 flex-1">
          <Input
            id="diagnostics-short-id"
            className="font-mono uppercase placeholder:normal-case"
            placeholder="Exact ID"
            value={filters.shortID}
            onChange={(event) => setFilter("shortID", event.target.value)}
          />
        </FilterField>
        <div className="ml-auto flex gap-2">
          <Button type="submit">Apply</Button>
          <Button
            type="button"
            variant="outline"
            onClick={clearFilters}
            disabled={!hasAppliedFilters}
            aria-label="Clear diagnostic report filters"
          >
            <FilterX />
            Clear
          </Button>
        </div>
      </form>

      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Received</TableHead>
              <TableHead>Short ID</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Platform</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Summary</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="text-right">Bundle</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {reports.isLoading && (
              <TableRow>
                <TableCell colSpan={8} className="text-muted-foreground py-10 text-center">
                  Loading diagnostic reports...
                </TableCell>
              </TableRow>
            )}
            {reports.isError && (
              <TableRow>
                <TableCell colSpan={8} className="text-destructive py-10 text-center">
                  {reports.error instanceof Error
                    ? reports.error.message
                    : "Failed to load diagnostic reports."}
                </TableCell>
              </TableRow>
            )}
            {!reports.isLoading && !reports.isError && reports.data?.reports.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="py-14 text-center">
                  <EmptyState filtered={hasAppliedFilters} />
                </TableCell>
              </TableRow>
            )}
            {reports.data?.reports.map((report) => (
              <DiagnosticReportRow key={report.id} report={report} onSelect={setSelectedID} />
            ))}
          </TableBody>
        </Table>
      </div>

      <div className="flex items-center justify-between gap-3">
        <Button
          type="button"
          variant="outline"
          disabled={activeCursorStack.length === 0 || reports.isFetching}
          onClick={goPrevious}
        >
          Previous
        </Button>
        <div className="text-muted-foreground text-xs">
          {reports.isFetching
            ? "Refreshing..."
            : reports.data?.next_cursor
              ? `Page ${activeCursorStack.length + 1} · More reports`
              : reports.data?.reports.length
                ? `Page ${activeCursorStack.length + 1}`
                : ""}
        </div>
        <Button
          type="button"
          variant="outline"
          disabled={!reports.data?.next_cursor || reports.isFetching}
          onClick={goNext}
        >
          Next
        </Button>
      </div>

      <Sheet
        open={selectedID !== undefined}
        onOpenChange={(open) => !open && setSelectedID(undefined)}
      >
        <SheetContent className="w-full sm:max-w-3xl">
          <SheetHeader>
            <SheetTitle className="font-mono">
              {selectedReport.data?.short_id ?? "Report"}
            </SheetTitle>
            <SheetDescription>
              {selectedReport.data
                ? `${formatToken(selectedReport.data.report_type)} · ${formatDateTime(selectedReport.data.received_at)}`
                : "Client diagnostic report details"}
            </SheetDescription>
          </SheetHeader>
          <div className="overflow-y-auto px-4 pb-8">
            {selectedReport.isLoading && (
              <p className="text-muted-foreground py-8 text-sm">Loading report details...</p>
            )}
            {selectedReport.isError && (
              <p className="text-destructive py-8 text-sm">
                {selectedReport.error instanceof Error
                  ? selectedReport.error.message
                  : "Failed to load report details."}
              </p>
            )}
            {selectedReport.data && (
              <DiagnosticReportDetail
                report={selectedReport.data}
                downloading={downloading}
                deleting={deleteReport.isPending}
                onDownload={() => void handleDownload(selectedReport.data!)}
                onDelete={() => setDeleteConfirmationOpen(true)}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={deleteConfirmationOpen}
        onOpenChange={setDeleteConfirmationOpen}
        title="Delete diagnostic report?"
        description={`This permanently deletes report ${selectedReport.data?.short_id ?? ""} and its uploaded bundle.`}
        confirmLabel="Delete report"
        variant="destructive"
        isPending={deleteReport.isPending}
        onConfirm={() => {
          if (!selectedID) return;
          deleteReport.mutate(selectedID, {
            onSuccess: () => {
              setDeleteConfirmationOpen(false);
              setSelectedID(undefined);
            },
          });
        }}
      />
    </div>
  );
}

function FilterField({
  label,
  htmlFor,
  className,
  children,
}: {
  label: string;
  htmlFor: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <Label
        htmlFor={htmlFor}
        className="text-muted-foreground text-[11px] font-medium tracking-wide uppercase"
      >
        {label}
      </Label>
      {children}
    </div>
  );
}

function DiagnosticReportRow({
  report,
  onSelect,
}: {
  report: DiagnosticReportSummary;
  onSelect: (id: string) => void;
}) {
  function handleKeyDown(event: KeyboardEvent<HTMLTableRowElement>) {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onSelect(report.id);
    }
  }

  return (
    <TableRow
      className="cursor-pointer"
      tabIndex={0}
      onClick={() => onSelect(report.id)}
      onKeyDown={handleKeyDown}
      aria-label={`Open diagnostic report ${report.short_id}`}
    >
      <TableCell className="whitespace-nowrap">{formatDateTime(report.received_at)}</TableCell>
      <TableCell className="font-mono text-xs font-medium">{report.short_id}</TableCell>
      <TableCell>
        <div>#{report.user_id}</div>
        {report.profile_id && (
          <div className="text-muted-foreground max-w-[120px] truncate text-xs">
            {report.profile_id}
          </div>
        )}
      </TableCell>
      <TableCell>
        <div>{formatPlatform(report.platform)}</div>
        <div className="text-muted-foreground text-xs">
          v{report.app_version}
          {report.app_build ? ` (${report.app_build})` : ""}
        </div>
      </TableCell>
      <TableCell>
        <Badge variant="outline">{formatToken(report.report_type)}</Badge>
      </TableCell>
      <TableCell>
        <div className="max-w-[360px] truncate" title={report.crash_summary}>
          {report.crash_summary || "—"}
        </div>
      </TableCell>
      <TableCell>
        <StateBadge state={report.state} />
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums">
        {formatBytes(report.blob_bytes)}
      </TableCell>
    </TableRow>
  );
}

function DiagnosticReportDetail({
  report,
  downloading,
  deleting,
  onDownload,
  onDelete,
}: {
  report: DiagnosticReport;
  downloading: boolean;
  deleting: boolean;
  onDownload: () => void;
  onDelete: () => void;
}) {
  const device = report.manifest.device_summary;
  const appBuild = report.app_build;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          onClick={onDownload}
          disabled={report.state !== "ready" || downloading}
        >
          <Download />
          {downloading ? "Preparing..." : "Download bundle"}
        </Button>
        <Button type="button" variant="destructive" onClick={onDelete} disabled={deleting}>
          <Trash2 />
          Delete
        </Button>
        {report.state !== "ready" && (
          <span className="text-muted-foreground text-xs">
            Only ready reports can be downloaded.
          </span>
        )}
      </div>

      <div className="surface-panel-subtle grid gap-4 rounded-xl p-4 sm:grid-cols-3">
        <DetailField
          label="Device"
          value={[device.manufacturer, device.model].filter(Boolean).join(" ") || "—"}
        />
        <DetailField label="OS" value={device.os || report.manifest.report.os_version || "—"} />
        <DetailField
          label="App"
          value={`${report.app_version}${appBuild ? ` (${appBuild})` : ""}`}
        />
      </div>

      <div className="grid grid-cols-2 gap-4 text-sm sm:grid-cols-3">
        <DetailField label="State" value={formatToken(report.state)} />
        <DetailField label="User" value={`#${report.user_id}`} />
        <DetailField label="Profile" value={report.profile_id || "—"} mono />
        <DetailField label="Captured" value={formatDateTime(report.captured_at)} />
        <DetailField label="Received" value={formatDateTime(report.received_at)} />
        <DetailField label="Form factor" value={formatToken(device.form_factor)} />
        <DetailField label="Compressed" value={formatBytes(report.blob_bytes)} />
        <DetailField label="Uncompressed" value={formatBytes(report.uncompressed_bytes)} />
        <DetailField label="Report ID" value={report.id} mono />
      </div>

      {report.crash_summary && (
        <div>
          <h3 className="mb-2 text-sm font-semibold">Crash summary</h3>
          <p className="bg-muted rounded-lg p-3 text-sm leading-6 whitespace-pre-wrap">
            {report.crash_summary}
          </p>
        </div>
      )}

      <div>
        <h3 className="mb-2 text-sm font-semibold">Playback sessions</h3>
        {report.playback_session_ids.length > 0 ? (
          <div className="flex flex-col items-start gap-2">
            {report.playback_session_ids.map((sessionID) => (
              <Link
                key={sessionID}
                className="text-primary inline-flex items-center gap-1.5 font-mono text-xs hover:underline"
                to={`/admin/logs?playback_session_id=${encodeURIComponent(sessionID)}&focus=playback`}
              >
                {sessionID}
                <ExternalLink className="size-3" aria-hidden="true" />
              </Link>
            ))}
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">No playback sessions were attached.</p>
        )}
      </div>

      <details className="group border-border overflow-hidden rounded-xl border">
        <summary className="bg-muted/40 hover:bg-muted/70 cursor-pointer px-4 py-3 text-sm font-semibold transition-colors">
          Full manifest JSON
        </summary>
        <pre className="bg-muted/20 max-h-[520px] overflow-auto border-t p-4 font-mono text-xs leading-5 break-all whitespace-pre-wrap">
          {JSON.stringify(report.manifest, null, 2)}
        </pre>
      </details>
    </div>
  );
}

function FeatureStatusBanner({ status }: { status: "disabled" | "storage_unavailable" }) {
  const message =
    status === "disabled"
      ? "Client diagnostic uploads are currently disabled. Use the Client uploads toggle above to enable them. Reports from when the feature was enabled may still be available below."
      : "Client diagnostic storage is currently unavailable. Existing report metadata may still be available below.";
  return (
    <div className="border-border bg-muted/30 text-muted-foreground flex items-start gap-3 rounded-xl border px-4 py-3 text-sm">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <p>{message}</p>
    </div>
  );
}

function EmptyState({ filtered }: { filtered: boolean }) {
  return (
    <div className="mx-auto flex max-w-md flex-col items-center gap-2">
      <div className="bg-muted text-muted-foreground flex size-10 items-center justify-center rounded-xl">
        <Bug className="size-5" aria-hidden="true" />
      </div>
      <h2 className="font-semibold">No diagnostic reports</h2>
      <p className="text-muted-foreground text-sm">
        {filtered
          ? "No reports match these filters. Clients create reports from their Diagnostics setting."
          : "Users can enable diagnostics in the client settings, then send a crash or manual report."}
      </p>
    </div>
  );
}

function StateBadge({ state }: { state: DiagnosticReportState }) {
  const className =
    state === "ready"
      ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
      : state === "failed"
        ? "border-destructive/30 bg-destructive/10 text-destructive"
        : "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400";
  return (
    <Badge variant="outline" className={className}>
      {formatToken(state)}
    </Badge>
  );
}

function DetailField({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground mb-1 text-xs">{label}</div>
      <div className={mono ? "font-mono text-xs break-all" : "text-sm break-words"}>{value}</div>
    </div>
  );
}

function draftFromSearchParams(searchParams: URLSearchParams): FilterDraft {
  return {
    userID: searchParams.get("user_id") ?? "",
    platform: searchParams.get("platform") ?? "all",
    reportType: searchParams.get("report_type") ?? "all",
    from: toLocalDateTimeInput(searchParams.get("from")),
    to: toLocalDateTimeInput(searchParams.get("to")),
    shortID: searchParams.get("short_id") ?? "",
  };
}

function setOrDelete(params: URLSearchParams, key: string, value: string | undefined) {
  const normalized = value?.trim();
  if (normalized) {
    params.set(key, normalized);
  } else {
    params.delete(key);
  }
}

function normalizeUserID(value: string) {
  const normalized = value.trim();
  if (!normalized) return undefined;
  if (!/^[1-9]\d*$/.test(normalized)) return undefined;
  const parsed = Number(normalized);
  return Number.isSafeInteger(parsed) ? String(parsed) : undefined;
}

function toRFC3339(value: string) {
  if (!value.trim()) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}

function toLocalDateTimeInput(value: string | null) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function formatDateTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : formatPreferredDateTime(date);
}

function formatBytes(value?: number) {
  if (value === undefined || value === null) return "—";
  if (value < 1024) return `${value} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let size = value / 1024;
  let unit = units[0];
  for (let index = 1; index < units.length && size >= 1024; index += 1) {
    size /= 1024;
    unit = units[index];
  }
  return `${size >= 10 ? size.toFixed(0) : size.toFixed(1)} ${unit}`;
}

function formatPlatform(value: string) {
  switch (value) {
    case "android-tv":
      return "Android TV";
    case "ios":
      return "iOS";
    case "tvos":
      return "tvOS";
    default:
      return "Android";
  }
}

function formatToken(value: string) {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
