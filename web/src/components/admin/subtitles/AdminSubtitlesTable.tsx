import { useState } from "react";
import { Link } from "react-router";
import type { AdminDownloadedSubtitle } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { downloadAdminSubtitle } from "@/hooks/queries/admin/subtitles";
import { getLanguageName } from "@/player/utils/languageNames";
import { cn } from "@/lib/utils";
import { Download, Ear, Loader2, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";
import AdminSubtitleEditSheet from "./AdminSubtitleEditSheet";
import {
  basenameFromPath,
  formatChipClass,
  languageChipClass,
  providerBadgeClass,
  providerLabel,
  staggerRowClass,
} from "./subtitleAdminStyles";
import { formatDate } from "@/lib/datetime";

interface AdminSubtitlesTableProps {
  subtitles: AdminDownloadedSubtitle[];
  hasActiveFilters: boolean;
  onResetFilters: () => void;
  onDelete: (subtitle: AdminDownloadedSubtitle) => void;
  isDeleting: boolean;
}

function formatRelative(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const deltaMs = Date.now() - date.getTime();
  const minutes = Math.floor(deltaMs / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  return formatDate(date);
}

export default function AdminSubtitlesTable({
  subtitles,
  hasActiveFilters,
  onResetFilters,
  onDelete,
  isDeleting,
}: AdminSubtitlesTableProps) {
  const [editTarget, setEditTarget] = useState<AdminDownloadedSubtitle | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminDownloadedSubtitle | null>(null);
  const [downloadingId, setDownloadingId] = useState<number | null>(null);

  async function handleDownload(subtitle: AdminDownloadedSubtitle) {
    setDownloadingId(subtitle.id);
    try {
      await downloadAdminSubtitle(subtitle);
      toast.success("Subtitle downloaded");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to download subtitle");
    } finally {
      setDownloadingId(null);
    }
  }

  if (subtitles.length === 0) {
    return (
      <div className="surface-panel rounded-2xl border-0 px-6 py-16 text-center">
        <div className="caption-empty-state mx-auto mb-5 max-w-md space-y-1.5">
          <span />
          <span />
          <span />
        </div>
        <h2 className="text-lg font-semibold tracking-tight">
          {hasActiveFilters ? "No subtitles match these filters" : "No stored subtitles yet"}
        </h2>
        <p className="text-muted-foreground mx-auto mt-2 max-w-lg text-sm leading-relaxed">
          {hasActiveFilters
            ? "Try widening the provider, language, or uploader filters to see more results."
            : "User uploads and provider downloads will appear here once subtitles are stored in S3."}
        </p>
        {hasActiveFilters && (
          <Button type="button" variant="outline" className="mt-5" onClick={onResetFilters}>
            Reset filters
          </Button>
        )}
      </div>
    );
  }

  return (
    <>
      <div className="surface-panel overflow-x-auto rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Media</TableHead>
              <TableHead>File</TableHead>
              <TableHead>Language</TableHead>
              <TableHead>Provider</TableHead>
              <TableHead>Release</TableHead>
              <TableHead>Format</TableHead>
              <TableHead className="w-10">HI</TableHead>
              <TableHead>Uploader</TableHead>
              <TableHead>Added</TableHead>
              <TableHead className="w-[120px] text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {subtitles.map((subtitle, index) => (
              <TableRow key={subtitle.id} className={cn("group", staggerRowClass(index))}>
                <TableCell className="max-w-[220px]">
                  <div className="space-y-1">
                    {subtitle.media_content_id ? (
                      <Link
                        to={`/item/${encodeURIComponent(subtitle.media_content_id)}`}
                        className="hover:text-primary line-clamp-2 font-semibold transition-colors hover:underline"
                      >
                        {subtitle.media_title || subtitle.media_content_id}
                      </Link>
                    ) : (
                      <div className="line-clamp-2 font-semibold">
                        {subtitle.media_title || "Unknown media"}
                      </div>
                    )}
                    {subtitle.media_type === "episode" && (
                      <Badge variant="outline" className="text-[10px] tracking-[0.12em] uppercase">
                        Episode
                      </Badge>
                    )}
                  </div>
                </TableCell>
                <TableCell
                  className="text-muted-foreground max-w-[180px] truncate font-mono text-xs"
                  title={subtitle.file_path}
                >
                  {basenameFromPath(subtitle.file_path)}
                </TableCell>
                <TableCell>
                  <span
                    className={cn(
                      "inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs font-medium",
                      languageChipClass(),
                    )}
                  >
                    <span className="font-semibold tracking-[0.08em] uppercase">
                      {subtitle.language}
                    </span>
                    <span className="text-muted-foreground hidden sm:inline">
                      {getLanguageName(subtitle.language)}
                    </span>
                  </span>
                </TableCell>
                <TableCell>
                  <span
                    className={cn(
                      "inline-flex rounded-full border px-2.5 py-1 text-xs font-medium",
                      providerBadgeClass(subtitle.provider),
                    )}
                  >
                    {providerLabel(subtitle.provider)}
                  </span>
                </TableCell>
                <TableCell
                  className="max-w-[200px] truncate font-mono text-xs"
                  title={subtitle.release_name}
                >
                  {subtitle.release_name || "—"}
                </TableCell>
                <TableCell>
                  <span className={cn("inline-flex rounded px-2 py-0.5", formatChipClass())}>
                    .{subtitle.format}
                  </span>
                </TableCell>
                <TableCell>
                  {subtitle.hearing_impaired ? (
                    <span className="inline-flex items-center gap-1 text-xs font-medium text-amber-200">
                      <Ear className="h-3.5 w-3.5" aria-hidden="true" />
                      HI
                    </span>
                  ) : null}
                </TableCell>
                <TableCell className="text-sm">{subtitle.uploader_username || "—"}</TableCell>
                <TableCell className="text-muted-foreground text-sm" title={subtitle.created_at}>
                  {formatRelative(subtitle.created_at)}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-1 opacity-100 transition-opacity sm:opacity-0 sm:group-focus-within:opacity-100 sm:group-hover:opacity-100">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      aria-label={`Edit subtitle ${subtitle.id}`}
                      onClick={() => setEditTarget(subtitle)}
                    >
                      <Pencil className="h-4 w-4" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      aria-label={`Download subtitle ${subtitle.id}`}
                      disabled={downloadingId === subtitle.id}
                      onClick={() => void handleDownload(subtitle)}
                    >
                      {downloadingId === subtitle.id ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Download className="h-4 w-4" />
                      )}
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="text-destructive hover:text-destructive h-8 w-8"
                      aria-label={`Delete subtitle ${subtitle.id}`}
                      onClick={() => setDeleteTarget(subtitle)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <AdminSubtitleEditSheet
        subtitle={editTarget}
        open={editTarget != null}
        onOpenChange={(open) => {
          if (!open) setEditTarget(null);
        }}
      />

      <ConfirmDialog
        open={deleteTarget != null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
        title="Delete subtitle?"
        description={
          deleteTarget
            ? `Remove ${providerLabel(deleteTarget.provider)} ${deleteTarget.language.toUpperCase()} subtitles for "${deleteTarget.media_title || "this media"}"? This deletes the stored file from S3.`
            : ""
        }
        confirmLabel="Delete"
        variant="destructive"
        isPending={isDeleting}
        onConfirm={() => {
          if (deleteTarget) {
            onDelete(deleteTarget);
            setDeleteTarget(null);
          }
        }}
      />
    </>
  );
}
