import {
  captureAdminSubtitleEditIntent,
  type AdminSubtitleEditIntent,
} from "@/api/v2/adminSubtitleMetadata";
import { useState } from "react";
import { Link } from "react-router";
import type { AdminStoredSubtitle as AdminDownloadedSubtitle } from "@/api/v2/adminSubtitles";
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
import AdminSubtitleDeleteDialog, { type SubtitleDeleteTarget } from "./AdminSubtitleDeleteDialog";
import { downloadAdminSubtitle } from "@/api/v2/adminSubtitleBytes";
import { adminSubtitleListScope } from "@/api/v2/adminSubtitles";
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
import { formatRelativeTime } from "@/lib/date";

interface AdminSubtitlesTableProps {
  subtitles: AdminDownloadedSubtitle[];
  authorityScope: string;
  hasActiveFilters: boolean;
  onResetFilters: () => void;
}

function formatRelative(value: string): string {
  return formatRelativeTime(value, { rounding: "floor", absoluteAfterDays: 30 }) ?? value;
}

export default function AdminSubtitlesTable({
  subtitles,
  authorityScope,
  hasActiveFilters,
  onResetFilters,
}: AdminSubtitlesTableProps) {
  const [editTarget, setEditTarget] = useState<AdminSubtitleEditIntent | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<SubtitleDeleteTarget | null>(null);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);

  async function handleDownload(subtitle: AdminDownloadedSubtitle) {
    setDownloadingId(subtitle.id);
    try {
      await downloadAdminSubtitle(subtitle, authorityScope);
      if (adminSubtitleListScope() !== authorityScope) return;
      toast.success("Subtitle downloaded");
    } catch (err) {
      if (adminSubtitleListScope() !== authorityScope) return;
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
            : "User uploads and provider downloads will appear here."}
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
                      onClick={() => {
                        try {
                          setEditTarget(captureAdminSubtitleEditIntent(subtitle, authorityScope));
                        } catch (error) {
                          toast.error(
                            error instanceof Error
                              ? error.message
                              : "Reload subtitles before editing.",
                          );
                        }
                      }}
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
                      onClick={() => setDeleteTarget({ subtitle, scope: authorityScope })}
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
        intent={editTarget}
        open={editTarget != null}
        onOpenChange={(open) => {
          if (!open) setEditTarget(null);
        }}
      />

      <AdminSubtitleDeleteDialog target={deleteTarget} onClose={() => setDeleteTarget(null)} />
    </>
  );
}
