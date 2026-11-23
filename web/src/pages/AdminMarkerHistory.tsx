import { useState } from "react";
import { Link } from "react-router";
import { RefreshCw } from "lucide-react";
import type { MarkerEditAuditEntry, MarkerSegment } from "@/api/types";
import { PageHeader } from "@/components/page/PageHeader";
import { PathText } from "@/components/PathText";
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
import { MARKER_LABEL_KEYS, formatClock } from "@/lib/markers";
import { formatTime, preferredDateLocale } from "@/lib/datetime";
import { numberText, type Translate, useT } from "@/i18n";

const LIMIT_OPTIONS = ["25", "50", "100"] as const;

export default function AdminMarkerHistory() {
  const t = useT();
  const [limit, setLimit] = useState("50");
  const history = useAllMarkerEditHistory(Number(limit));
  const rows = history.data ?? [];

  return (
    <div className="space-y-6 pb-4 sm:pb-6">
      <PageHeader
        title={t("web.admin.markers.history.title")}
        subtitle={t("web.admin.markers.history.subtitle")}
        actions={
          <div className="flex items-center gap-2">
            <Select value={limit} onValueChange={setLimit}>
              <SelectTrigger
                className="w-[8rem]"
                aria-label={t("web.admin.markers.history.limitLabel")}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {LIMIT_OPTIONS.map((option) => (
                  <SelectItem key={option} value={option}>
                    {t("web.admin.markers.history.limitOption", { count: Number(option) })}
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
              {t("web.admin.markers.history.refresh")}
            </Button>
          </div>
        }
      />

      <div className="border-border bg-surface overflow-hidden rounded-lg border">
        {history.isLoading ? (
          <MarkerHistorySkeleton />
        ) : history.isError ? (
          <div className="text-destructive px-4 py-8 text-center text-sm">
            {t("web.admin.markers.history.loadFailed")}
          </div>
        ) : rows.length === 0 ? (
          <div className="text-muted-foreground px-4 py-8 text-center text-sm">
            {t("web.admin.markers.history.empty")}
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("web.admin.markers.history.column.when")}</TableHead>
                <TableHead>{t("web.admin.markers.history.column.item")}</TableHead>
                <TableHead>{t("web.admin.markers.history.column.segment")}</TableHead>
                <TableHead>{t("web.admin.markers.history.column.change")}</TableHead>
                <TableHead>{t("web.admin.markers.history.column.actor")}</TableHead>
                <TableHead>{t("web.admin.markers.history.column.request")}</TableHead>
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
  const t = useT();
  const title =
    row.media_title ||
    fileName(row.file_path) ||
    t("web.admin.markers.history.fileFallback", { id: String(row.media_file_id) });
  // The label as text, for the tooltip of a row with a type.
  const itemLabel = row.item_type
    ? t("web.admin.markers.history.itemWithType", { title, type: row.item_type })
    : undefined;
  // The title on its own element takes its own direction without turning the
  // translated words around it. Without a type the title is the whole label, so
  // it carries the tooltip, which then reads in the title's direction too.
  const titleNode = (
    <span dir="auto" className="truncate" title={row.item_type ? undefined : title}>
      {title}
    </span>
  );
  const label = row.item_type
    ? t("web.admin.markers.history.itemWithType", { title: titleNode, type: row.item_type })
    : titleNode;

  return (
    <TableRow>
      <TableCell className="text-muted-foreground text-xs">
        {formatHistoryDate(row.created_at)}
      </TableCell>
      <TableCell>
        {/* The words around the title are flex items that keep the page's
            direction and don't shrink, so a cut label cuts the title, which
            keeps its start. The link cuts its own text, so the box around it
            doesn't clip its focus outline. */}
        <div className="w-fit max-w-[18rem] font-medium">
          {row.item_id ? (
            <Link
              to={`/item/${encodeURIComponent(row.item_id)}`}
              className="hover:text-primary flex overflow-hidden whitespace-pre hover:underline"
              title={itemLabel}
            >
              {label}
            </Link>
          ) : (
            <span className="flex overflow-hidden whitespace-pre" title={itemLabel}>
              {label}
            </span>
          )}
        </div>
        {/* A path reads left to right in any language, so a cut one keeps its
            start; w-fit keeps it on the page's start side, and the tooltip
            shows the rest. The fallback label follows the page. */}
        <div
          dir={row.file_path ? "ltr" : undefined}
          className="text-muted-foreground w-fit max-w-[18rem] truncate text-xs"
          title={row.file_path || undefined}
        >
          {row.file_path ? (
            <PathText path={row.file_path} />
          ) : (
            t("web.admin.markers.history.mediaFileFallback", { id: String(row.media_file_id) })
          )}
        </div>
      </TableCell>
      <TableCell>{t(MARKER_LABEL_KEYS[row.segment])}</TableCell>
      <TableCell>
        <div className="flex items-center gap-2">
          <Badge variant={row.action === "clear" ? "outline" : "secondary"}>
            {row.action === "clear"
              ? t("web.admin.markers.history.actionCleared")
              : t("web.admin.markers.history.actionSet")}
          </Badge>
          <span className="text-muted-foreground font-mono text-xs">
            {t("web.admin.markers.history.rangeChange", {
              before: formatHistoryRange(t, row.before),
              after: formatHistoryRange(t, row.after),
            })}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <div className="font-medium">
          {row.username ?? t("web.admin.markers.history.unknownUser")}
        </div>
        {row.impersonator_username && (
          <div className="text-muted-foreground text-xs">
            {t("web.admin.markers.history.impersonatedVia", { name: row.impersonator_username })}
          </div>
        )}
      </TableCell>
      <TableCell>
        <div className="text-muted-foreground max-w-[13rem] truncate text-xs">
          {row.request_id ?? t("web.admin.markers.history.noRequestId")}
        </div>
        {/* An address or user agent reads left to right in any language, so an
            IPv6 address keeps its order and a cut one keeps its start; w-fit
            keeps it on the page's start side. */}
        <div
          dir="ltr"
          className="text-muted-foreground w-fit max-w-[13rem] truncate text-xs"
          title={row.client_ip ?? row.user_agent}
        >
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

function formatHistoryRange(t: Translate, marker: MarkerSegment | null): string {
  if (!marker || marker.start == null || marker.end == null)
    return t("web.admin.markers.history.rangeNone");
  return numberText(`${formatClock(marker.start)}-${formatClock(marker.end)}`);
}

function formatHistoryDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const day = date.toLocaleDateString(preferredDateLocale(), { month: "short", day: "numeric" });
  return `${day}, ${formatTime(date)}`;
}

function fileName(path: string | undefined): string {
  if (!path) return "";
  return path.split(/[\\/]/).pop() ?? path;
}
