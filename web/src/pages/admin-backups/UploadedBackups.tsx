import { useState } from "react";
import { Trash2 } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";

import type { AdminBackupUpload } from "@/api/v2/adminBackupUpload";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  useCompleteAdminBackupUpload,
  useDeleteAdminBackupUpload,
} from "@/hooks/queries/admin/backupUploads";
import { useT, type Translate } from "@/i18n";
import { formatFileSize } from "@/lib/mediaFormat";

import {
  backupDateTime,
  backupErrorMessage,
  knownSiloVersion,
  shortServerId,
  sourceServerName,
  uploadRestorePath,
} from "./backupPresentation";

function uploadLabel(t: Translate, upload: AdminBackupUpload): string {
  const source = upload.source;
  if (!source) {
    return t("web.admin.backups.uploads.unfinished", { date: backupDateTime(upload.created_at) });
  }
  return t("web.admin.backups.uploads.label", {
    server: sourceServerName(source) ?? t("web.admin.backups.uploads.anotherServer"),
    taken: backupDateTime(source.created_at),
  });
}

/** The line under an upload's label: its size, where it came from, and how long it stays. */
function uploadDetails(t: Translate, upload: AdminBackupUpload): string {
  const parts = [formatFileSize(upload.size_bytes, { fallback: "—" })];
  const source = upload.source;
  if (source && !sourceServerName(source)) {
    parts.push(t("web.admin.backups.uploads.serverId", { id: shortServerId(source.server_id) }));
  }
  const version = source ? knownSiloVersion(source.silo_version) : null;
  if (version) parts.push(t("web.admin.backups.uploads.siloVersion", { version }));
  if (upload.status !== "complete") parts.push(t("web.admin.backups.uploads.deletedAfter"));
  else if (upload.expires_at) {
    parts.push(
      t("web.admin.backups.uploads.keptUntil", { date: backupDateTime(upload.expires_at) }),
    );
  }
  return parts.join(" · ");
}

/**
 * Whether every part of an upload arrived but its check never finished: the
 * server checking it stopped, or the dialog that sent it was closed before
 * it completed. Completing it again checks it.
 */
function fullyReceived(upload: AdminBackupUpload): boolean {
  return upload.status === "uploading" && upload.received_parts === upload.part_count;
}

/**
 * Restore sets uploaded from other servers. Checked ones are kept a week
 * after their last use; unfinished ones are deleted a day after their last
 * part.
 */
export function UploadedBackups({ uploads }: { uploads: readonly AdminBackupUpload[] }) {
  const t = useT();
  const [deleting, setDeleting] = useState<AdminBackupUpload | null>(null);
  const remove = useDeleteAdminBackupUpload();
  const check = useCompleteAdminBackupUpload();
  // Rejected uploads hold no file; the dialog that sent them said why.
  const shown = uploads.filter((upload) => upload.status !== "rejected");
  if (shown.length === 0) return null;

  const confirmDelete = () => {
    const target = deleting;
    if (!target) return;
    remove.mutate(target.id, {
      onSuccess: () => {
        setDeleting(null);
        toast.success(t("web.admin.backups.uploads.deleted"));
      },
      onError: (error) => {
        setDeleting(null);
        toast.error(t("web.admin.backups.uploads.deleteFailed"), {
          description: backupErrorMessage(error),
        });
      },
    });
  };

  return (
    <section aria-labelledby="uploaded-backups-heading" className="space-y-3">
      <div>
        <h2 id="uploaded-backups-heading" className="text-[15px] font-semibold tracking-tight">
          {t("web.admin.backups.uploads.heading")}
        </h2>
        <p className="text-muted-foreground text-[13px]">
          {t("web.admin.backups.uploads.description")}
        </p>
      </div>
      <ul className="surface-panel divide-border/60 divide-y rounded-2xl">
        {shown.map((upload) => {
          const label = uploadLabel(t, upload);
          return (
            <li key={upload.id} className="flex items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium">{label}</p>
                <p className="text-muted-foreground text-xs tabular-nums">
                  {uploadDetails(t, upload)}
                </p>
              </div>
              <div className="flex shrink-0 gap-1">
                {upload.status === "complete" ? (
                  <Button asChild variant="ghost" size="sm">
                    <Link to={uploadRestorePath(upload.id)}>
                      {t("web.admin.backups.uploads.restore", {
                        label,
                        sr: (chunks) => <span className="sr-only">{chunks}</span>,
                      })}
                    </Link>
                  </Button>
                ) : null}
                {fullyReceived(upload) ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={check.isPending}
                    onClick={() =>
                      check.mutate(upload.id, {
                        onError: (error) =>
                          toast.error(t("web.admin.backups.uploads.checkFailed"), {
                            description: backupErrorMessage(error),
                          }),
                      })
                    }
                  >
                    {t("web.admin.backups.uploads.checkAgain", {
                      label,
                      sr: (chunks) => <span className="sr-only">{chunks}</span>,
                    })}
                  </Button>
                ) : null}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setDeleting(upload)}
                  aria-label={t("web.admin.backups.uploads.deleteLabel", { label })}
                >
                  <Trash2 aria-hidden="true" />
                </Button>
              </div>
            </li>
          );
        })}
      </ul>
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("web.admin.backups.uploads.deleteTitle")}
        description={
          deleting
            ? t("web.admin.backups.uploads.deleteDescription", { label: uploadLabel(t, deleting) })
            : ""
        }
        confirmLabel={t("web.admin.backups.uploads.deleteConfirm")}
        variant="destructive"
        irreversible
        isPending={remove.isPending}
        onConfirm={confirmDelete}
      />
    </section>
  );
}
