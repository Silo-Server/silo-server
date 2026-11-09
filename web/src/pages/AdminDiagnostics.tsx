import { useEffect, useMemo, useState } from "react";
import type { FormEvent, KeyboardEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { Download, ExternalLink, FilterX, Trash2 } from "lucide-react";
import { toast } from "sonner";

import type { DiagnosticReport, DiagnosticReportState, DiagnosticReportSummary } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { DateTimePicker } from "@/components/DateTimePicker";
import { PageHeader } from "@/components/page/PageHeader";
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
import { useDateTimeFormat } from "@/hooks/useDateTimeFormat";
import {
  downloadDiagnosticReport,
  useDeleteDiagnosticReport,
  useDiagnosticReport,
  useDiagnosticReports,
  useDiagnosticsStatus,
  useUpdateDiagnosticsUploadsEnabled,
} from "@/hooks/queries/admin/diagnostics";
import {
  draftFromSearchParams,
  FILTER_KEYS,
  normalizeUserID,
  setOrDelete,
  toRFC3339,
  type FilterDraft,
} from "./AdminDiagnostics.filters";
import { EmptyState, FeatureStatusBanner, FilterField } from "./AdminDiagnostics.parts";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import { useT, type MessageKey, type Translate } from "@/i18n";

const PAGE_SIZE = 25;
const EM_DASH = "—";

// Labels for known report types and states; their English matches formatToken().
const REPORT_TYPE_LABELS: Record<string, MessageKey> = {
  crash: "web.admin.diagnostics.reportType.crash",
  anr: "web.admin.diagnostics.reportType.anr",
  native_crash: "web.admin.diagnostics.reportType.nativeCrash",
  hang: "web.admin.diagnostics.reportType.hang",
  abnormal_exit: "web.admin.diagnostics.reportType.abnormalExit",
  manual: "web.admin.diagnostics.reportType.manual",
};

const STATE_LABELS: Record<DiagnosticReportState, MessageKey> = {
  receiving: "web.admin.diagnostics.state.receiving",
  ready: "web.admin.diagnostics.state.ready",
  failed: "web.admin.diagnostics.state.failed",
};
export default function AdminDiagnostics() {
  const t = useT();
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
      toast.error(t("web.admin.diagnostics.filters.invalidUserId"));
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
      toast.error(
        error instanceof Error ? error.message : t("web.admin.diagnostics.detail.downloadFailed"),
      );
    } finally {
      setDownloading(false);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.admin.diagnostics.page.title")}
        subtitle={t("web.admin.diagnostics.page.subtitle")}
        actions={
          <div className="flex items-center gap-5 sm:gap-6">
            <div className="flex items-center gap-2">
              <Label
                htmlFor="diagnostics-uploads-enabled"
                className="text-muted-foreground text-sm"
              >
                {t("web.admin.diagnostics.page.clientUploads")}
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
                {t("web.admin.diagnostics.page.retention")}
              </div>
              <div className="text-sm">
                {status.data
                  ? t("web.admin.diagnostics.page.retentionDays", {
                      count: status.data.retention_days,
                      count_text: String(status.data.retention_days),
                    })
                  : t("web.admin.diagnostics.page.loading")}
              </div>
            </div>
          </div>
        }
      />

      {status.data?.status !== undefined && status.data.status !== "available" && (
        <FeatureStatusBanner status={status.data.status} />
      )}

      <form
        className="surface-panel-subtle flex flex-wrap items-end gap-x-3 gap-y-4 rounded-2xl p-4"
        onSubmit={applyFilters}
      >
        <FilterField
          label={t("web.admin.diagnostics.filters.user")}
          htmlFor="diagnostics-user"
          className="w-24"
        >
          <Input
            id="diagnostics-user"
            type="number"
            min={1}
            step={1}
            inputMode="numeric"
            placeholder={t("web.admin.diagnostics.filters.userAny")}
            value={filters.userID}
            onChange={(event) => setFilter("userID", event.target.value)}
          />
        </FilterField>
        <FilterField
          label={t("web.admin.diagnostics.filters.platform")}
          htmlFor="diagnostics-platform"
          className="min-w-36 flex-1"
        >
          <Select value={filters.platform} onValueChange={(value) => setFilter("platform", value)}>
            <SelectTrigger id="diagnostics-platform" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("web.admin.diagnostics.filters.allPlatforms")}</SelectItem>
              <SelectItem value="android">{formatPlatform("android")}</SelectItem>
              <SelectItem value="android-tv">{formatPlatform("android-tv")}</SelectItem>
              <SelectItem value="ios">{formatPlatform("ios")}</SelectItem>
              <SelectItem value="tvos">{formatPlatform("tvos")}</SelectItem>
              <SelectItem value="roku">{formatPlatform("roku")}</SelectItem>
            </SelectContent>
          </Select>
        </FilterField>
        <FilterField
          label={t("web.admin.diagnostics.filters.reportType")}
          htmlFor="diagnostics-type"
          className="min-w-36 flex-1"
        >
          <Select
            value={filters.reportType}
            onValueChange={(value) => setFilter("reportType", value)}
          >
            <SelectTrigger id="diagnostics-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("web.admin.diagnostics.filters.allTypes")}</SelectItem>
              <SelectItem value="crash">{t("web.admin.diagnostics.filters.type.crash")}</SelectItem>
              <SelectItem value="anr">{t("web.admin.diagnostics.filters.type.anr")}</SelectItem>
              <SelectItem value="native_crash">
                {t("web.admin.diagnostics.filters.type.nativeCrash")}
              </SelectItem>
              <SelectItem value="hang">{t("web.admin.diagnostics.filters.type.hang")}</SelectItem>
              <SelectItem value="abnormal_exit">
                {t("web.admin.diagnostics.filters.type.abnormalExit")}
              </SelectItem>
              <SelectItem value="manual">
                {t("web.admin.diagnostics.filters.type.manual")}
              </SelectItem>
            </SelectContent>
          </Select>
        </FilterField>
        <FilterField
          label={t("web.admin.diagnostics.filters.from")}
          htmlFor="diagnostics-from"
          className="min-w-44 flex-1"
        >
          <DateTimePicker
            id="diagnostics-from"
            value={filters.from}
            onChange={(value) => setFilter("from", value)}
          />
        </FilterField>
        <FilterField
          label={t("web.admin.diagnostics.filters.to")}
          htmlFor="diagnostics-to"
          className="min-w-44 flex-1"
        >
          <DateTimePicker
            id="diagnostics-to"
            value={filters.to}
            onChange={(value) => setFilter("to", value)}
          />
        </FilterField>
        <FilterField
          label={t("web.admin.diagnostics.filters.shortId")}
          htmlFor="diagnostics-short-id"
          className="min-w-32 flex-1"
        >
          <Input
            id="diagnostics-short-id"
            className="font-mono uppercase placeholder:normal-case"
            placeholder={t("web.admin.diagnostics.filters.shortIdPlaceholder")}
            value={filters.shortID}
            onChange={(event) => setFilter("shortID", event.target.value)}
          />
        </FilterField>
        <div className="ml-auto flex gap-2">
          <Button type="submit">{t("web.admin.diagnostics.filters.apply")}</Button>
          <Button
            type="button"
            variant="outline"
            onClick={clearFilters}
            disabled={!hasAppliedFilters}
            aria-label={t("web.admin.diagnostics.filters.clearLabel")}
          >
            <FilterX />
            {t("web.admin.diagnostics.filters.clear")}
          </Button>
        </div>
      </form>

      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("web.admin.diagnostics.columns.received")}</TableHead>
              <TableHead>{t("web.admin.diagnostics.columns.shortId")}</TableHead>
              <TableHead>{t("web.admin.diagnostics.columns.user")}</TableHead>
              <TableHead>{t("web.admin.diagnostics.columns.platform")}</TableHead>
              <TableHead>{t("web.admin.diagnostics.columns.type")}</TableHead>
              <TableHead>{t("web.admin.diagnostics.columns.summary")}</TableHead>
              <TableHead>{t("web.admin.diagnostics.columns.state")}</TableHead>
              <TableHead className="text-right">
                {t("web.admin.diagnostics.columns.bundle")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {reports.isLoading && (
              <TableRow>
                <TableCell colSpan={8} className="text-muted-foreground py-10 text-center">
                  {t("web.admin.diagnostics.list.loading")}
                </TableCell>
              </TableRow>
            )}
            {reports.isError && (
              <TableRow>
                <TableCell colSpan={8} className="text-destructive py-10 text-center">
                  {reports.error instanceof Error
                    ? reports.error.message
                    : t("web.admin.diagnostics.list.loadError")}
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
          {t("web.admin.diagnostics.pagination.previous")}
        </Button>
        <div className="text-muted-foreground text-xs">
          {reports.isFetching
            ? t("web.admin.diagnostics.pagination.refreshing")
            : reports.data?.next_cursor
              ? t("web.admin.diagnostics.pagination.pageWithMore", {
                  page: String(activeCursorStack.length + 1),
                })
              : reports.data?.reports.length
                ? t("web.admin.diagnostics.pagination.page", {
                    page: String(activeCursorStack.length + 1),
                  })
                : ""}
        </div>
        <Button
          type="button"
          variant="outline"
          disabled={!reports.data?.next_cursor || reports.isFetching}
          onClick={goNext}
        >
          {t("web.admin.diagnostics.pagination.next")}
        </Button>
      </div>

      <Sheet
        open={selectedID !== undefined}
        onOpenChange={(open) => !open && setSelectedID(undefined)}
      >
        <SheetContent className="w-full sm:max-w-3xl">
          <SheetHeader>
            <SheetTitle className="font-mono">
              {selectedReport.data?.short_id ?? t("web.admin.diagnostics.sheet.titleFallback")}
            </SheetTitle>
            <SheetDescription>
              {selectedReport.data
                ? t("web.admin.diagnostics.sheet.description", {
                    type: reportTypeLabel(t, selectedReport.data.report_type),
                    received: formatDateTime(selectedReport.data.received_at),
                  })
                : t("web.admin.diagnostics.sheet.descriptionFallback")}
            </SheetDescription>
          </SheetHeader>
          <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8">
            {selectedReport.isLoading && (
              <p className="text-muted-foreground py-8 text-sm">
                {t("web.admin.diagnostics.sheet.loading")}
              </p>
            )}
            {selectedReport.isError && (
              <p className="text-destructive py-8 text-sm">
                {selectedReport.error instanceof Error
                  ? selectedReport.error.message
                  : t("web.admin.diagnostics.sheet.loadError")}
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
        title={t("web.admin.diagnostics.delete.title")}
        description={t("web.admin.diagnostics.delete.description", {
          id: selectedReport.data?.short_id ?? "",
        })}
        confirmLabel={t("web.admin.diagnostics.delete.confirm")}
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

function DiagnosticReportRow({
  report,
  onSelect,
}: {
  report: DiagnosticReportSummary;
  onSelect: (id: string) => void;
}) {
  const t = useT();
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
      aria-label={t("web.admin.diagnostics.row.openLabel", { id: report.short_id })}
    >
      <TableCell className="whitespace-nowrap">{formatDateTime(report.received_at)}</TableCell>
      <TableCell className="font-mono text-xs font-medium">{report.short_id}</TableCell>
      <TableCell>
        <div>{t("web.admin.diagnostics.userRef", { id: String(report.user_id) })}</div>
        {report.profile_id && (
          <div className="text-muted-foreground max-w-[120px] truncate text-xs">
            {report.profile_id}
          </div>
        )}
      </TableCell>
      <TableCell>
        <div>{formatPlatform(report.platform)}</div>
        <div className="text-muted-foreground text-xs">
          {report.app_build
            ? t("web.admin.diagnostics.row.versionWithBuild", {
                version: report.app_version,
                build: report.app_build,
              })
            : t("web.admin.diagnostics.row.version", { version: report.app_version })}
        </div>
      </TableCell>
      <TableCell>
        <Badge variant="outline">{reportTypeLabel(t, report.report_type)}</Badge>
      </TableCell>
      <TableCell>
        <div className="max-w-[360px] truncate" title={report.crash_summary}>
          {report.crash_summary || EM_DASH}
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
  const t = useT();
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
          {downloading
            ? t("web.admin.diagnostics.detail.preparing")
            : t("web.admin.diagnostics.detail.download")}
        </Button>
        <Button type="button" variant="destructive" onClick={onDelete} disabled={deleting}>
          <Trash2 />
          {t("web.admin.diagnostics.detail.delete")}
        </Button>
        {report.state !== "ready" && (
          <span className="text-muted-foreground text-xs">
            {t("web.admin.diagnostics.detail.onlyReadyDownloadable")}
          </span>
        )}
      </div>

      <div className="surface-panel-subtle grid gap-4 rounded-xl p-4 sm:grid-cols-3">
        <DetailField
          label={t("web.admin.diagnostics.detail.device")}
          value={[device.manufacturer, device.model].filter(Boolean).join(" ") || EM_DASH}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.os")}
          value={device.os || report.manifest.report.os_version || EM_DASH}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.app")}
          value={
            appBuild
              ? t("web.admin.diagnostics.detail.appVersionWithBuild", {
                  version: report.app_version,
                  build: appBuild,
                })
              : report.app_version
          }
        />
      </div>

      <div className="grid grid-cols-2 gap-4 text-sm sm:grid-cols-3">
        <DetailField
          label={t("web.admin.diagnostics.detail.state")}
          value={stateLabel(t, report.state)}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.user")}
          value={t("web.admin.diagnostics.userRef", { id: String(report.user_id) })}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.profile")}
          value={report.profile_id || EM_DASH}
          mono
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.captured")}
          value={formatDateTime(report.captured_at)}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.received")}
          value={formatDateTime(report.received_at)}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.formFactor")}
          value={formatToken(device.form_factor)}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.compressed")}
          value={formatBytes(report.blob_bytes)}
        />
        <DetailField
          label={t("web.admin.diagnostics.detail.uncompressed")}
          value={formatBytes(report.uncompressed_bytes)}
        />
        <DetailField label={t("web.admin.diagnostics.detail.reportId")} value={report.id} mono />
      </div>

      {report.crash_summary && (
        <div>
          <h3 className="mb-2 text-sm font-semibold">
            {t("web.admin.diagnostics.detail.crashSummary")}
          </h3>
          <p className="bg-muted rounded-lg p-3 text-sm leading-6 whitespace-pre-wrap">
            {report.crash_summary}
          </p>
        </div>
      )}

      <div>
        <h3 className="mb-2 text-sm font-semibold">
          {t("web.admin.diagnostics.detail.playbackSessions")}
        </h3>
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
          <p className="text-muted-foreground text-sm">
            {t("web.admin.diagnostics.detail.noPlaybackSessions")}
          </p>
        )}
      </div>

      <details className="group border-border overflow-hidden rounded-xl border">
        <summary className="bg-muted/40 hover:bg-muted/70 cursor-pointer px-4 py-3 text-sm font-semibold transition-colors">
          {t("web.admin.diagnostics.detail.manifestJson")}
        </summary>
        <pre className="bg-muted/20 max-h-[520px] overflow-auto border-t p-4 font-mono text-xs leading-5 break-all whitespace-pre-wrap">
          {JSON.stringify(report.manifest, null, 2)}
        </pre>
      </details>
    </div>
  );
}

function StateBadge({ state }: { state: DiagnosticReportState }) {
  const t = useT();
  const className =
    state === "ready"
      ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
      : state === "failed"
        ? "border-destructive/30 bg-destructive/10 text-destructive"
        : "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400";
  return (
    <Badge variant="outline" className={className}>
      {stateLabel(t, state)}
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

function formatDateTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : formatPreferredDateTime(date);
}

function formatBytes(value?: number) {
  if (value === undefined || value === null) return EM_DASH;
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
    case "roku":
      return "Roku";
    default:
      return "Android";
  }
}

function reportTypeLabel(t: Translate, value: string) {
  const key = REPORT_TYPE_LABELS[value] as MessageKey | undefined;
  return key ? t(key) : formatToken(value);
}

function stateLabel(t: Translate, value: DiagnosticReportState) {
  const key = STATE_LABELS[value] as MessageKey | undefined;
  return key ? t(key) : formatToken(value);
}

function formatToken(value: string) {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
