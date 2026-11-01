import { useState } from "react";
import { Trash2 } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";

import type { AdminBackupUpload } from "@/api/v2/adminBackupUpload";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { useDeleteAdminBackupUpload } from "@/hooks/queries/admin/backupUploads";
import { formatFileSize } from "@/lib/mediaFormat";

import {
  backupDateTime,
  backupErrorMessage,
  knownSiloVersion,
  shortServerId,
  sourceServerName,
  uploadRestorePath,
} from "./backupPresentation";

function uploadLabel(upload: AdminBackupUpload): string {
  const source = upload.source;
  if (!source) {
    return `Unfinished upload from ${backupDateTime(upload.created_at)}`;
  }
  const taken = backupDateTime(source.created_at);
  return `${sourceServerName(source) ?? "Another server"}, ${taken}`;
}

/** The line under an upload's label: its size, where it came from, and how long it stays. */
function uploadDetails(upload: AdminBackupUpload): string {
  const parts = [formatFileSize(upload.size_bytes, { fallback: "—" })];
  const source = upload.source;
  if (source && !sourceServerName(source)) parts.push(`server ${shortServerId(source.server_id)}`);
  const version = source ? knownSiloVersion(source.silo_version) : null;
  if (version) parts.push(`Silo ${version}`);
  if (upload.status !== "complete") parts.push("deleted 24 hours after its last part");
  else if (upload.expires_at) {
    parts.push(`kept until ${backupDateTime(upload.expires_at)} unless restored from again`);
  }
  return parts.join(" · ");
}

/**
 * Restore sets uploaded from other servers. Checked ones are kept a week
 * after their last use; unfinished ones are deleted a day after their last
 * part.
 */
export function UploadedBackups({ uploads }: { uploads: readonly AdminBackupUpload[] }) {
  const [deleting, setDeleting] = useState<AdminBackupUpload | null>(null);
  const remove = useDeleteAdminBackupUpload();
  // Rejected uploads hold no file; the dialog that sent them said why.
  const shown = uploads.filter((upload) => upload.status !== "rejected");
  if (shown.length === 0) return null;

  const confirmDelete = () => {
    const target = deleting;
    if (!target) return;
    remove.mutate(target.id, {
      onSuccess: () => {
        setDeleting(null);
        toast.success("Uploaded backup deleted");
      },
      onError: (error) => {
        setDeleting(null);
        toast.error("Couldn't delete the uploaded backup", {
          description: backupErrorMessage(error),
        });
      },
    });
  };

  return (
    <section aria-labelledby="uploaded-backups-heading" className="space-y-3">
      <div>
        <h2 id="uploaded-backups-heading" className="text-[15px] font-semibold tracking-tight">
          Uploaded backups
        </h2>
        <p className="text-muted-foreground text-[13px]">
          Restore sets from other servers, kept in this server&apos;s storage until you delete them.
        </p>
      </div>
      <ul className="surface-panel divide-border/60 divide-y rounded-2xl">
        {shown.map((upload) => {
          const label = uploadLabel(upload);
          return (
            <li key={upload.id} className="flex items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium">{label}</p>
                <p className="text-muted-foreground text-xs tabular-nums">
                  {uploadDetails(upload)}
                </p>
              </div>
              <div className="flex shrink-0 gap-1">
                {upload.status === "complete" ? (
                  <Button asChild variant="ghost" size="sm">
                    <Link to={uploadRestorePath(upload.id)}>
                      Restore…
                      <span className="sr-only"> from {label}</span>
                    </Link>
                  </Button>
                ) : null}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setDeleting(upload)}
                  aria-label={`Delete ${label}`}
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
        title="Delete this uploaded backup?"
        description={
          deleting ? `${uploadLabel(deleting)} is removed from this server's storage.` : ""
        }
        confirmLabel="Delete"
        variant="destructive"
        irreversible
        isPending={remove.isPending}
        onConfirm={confirmDelete}
      />
    </section>
  );
}
