import { Fragment, useId, useState } from "react";
import { Download, LoaderCircle, Trash2 } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";

import type {
  AdminBackup,
  AdminBackupActive,
  AdminBackupDownloadLink,
} from "@/api/v2/adminBackups";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAdminBackupDownloadLinks, useDeleteAdminBackup } from "@/hooks/queries/admin/backups";
import { formatTime } from "@/lib/datetime";
import { formatFileSize } from "@/lib/mediaFormat";
import { cn } from "@/lib/utils";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { BackupWarningDetails } from "./BackupWarningDetails";
import {
  BACKUP_KIND_LABELS,
  BACKUP_ROWS_SHOWN,
  BACKUP_STATUS_LABELS,
  OTHER_KEY_REASON,
  backupDateTime,
  backupErrorMessage,
  backupRowId,
  canRestoreParts,
  encryptedWithOtherKey,
  isFinished,
  knownSiloVersion,
  otherKeyNote,
  restorePath,
} from "./backupPresentation";

function takenLabel(backup: AdminBackup) {
  return backupDateTime(backup.created_at);
}

function BackupStatus({
  backup,
  active,
  expanded,
  onToggle,
  detailsId,
}: {
  backup: AdminBackup;
  /** The operation holding the lock, when it is about this backup. */
  active: AdminBackupActive | undefined;
  expanded: boolean;
  onToggle: () => void;
  detailsId: string;
}) {
  if (active?.operation === "delete") {
    return (
      <SettingFieldStatus tone="muted">
        <LoaderCircle aria-hidden="true" className="size-3.5 motion-safe:animate-spin" />
        Deleting
      </SettingFieldStatus>
    );
  }
  const label = BACKUP_STATUS_LABELS[backup.status];
  switch (backup.status) {
    case "complete":
      return <SettingFieldStatus tone="ok">{label}</SettingFieldStatus>;
    case "complete_with_warnings": {
      const missing = backup.missing_blobs.length;
      const omitted = backup.restore_set_omitted.length;
      return (
        <div className="flex flex-col items-start gap-0.5 max-sm:items-end max-sm:text-right">
          <SettingFieldStatus tone="warn">{label}</SettingFieldStatus>
          {missing > 0 || omitted > 0 ? (
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={expanded}
              aria-controls={detailsId}
              className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 rounded-sm text-xs underline-offset-2 outline-none hover:underline focus-visible:ring-[3px]"
            >
              {expanded ? "Hide details" : "Show details"}
            </button>
          ) : null}
        </div>
      );
    }
    case "in_progress":
      return (
        <SettingFieldStatus tone="muted">
          <LoaderCircle aria-hidden="true" className="size-3.5 motion-safe:animate-spin" />
          {active?.operation === "backup" ? `${label}, ${Math.round(active.progress)}%` : label}
        </SettingFieldStatus>
      );
    default:
      return <SettingFieldStatus tone="muted">{label}</SettingFieldStatus>;
  }
}

function DownloadRow({ link }: { link: AdminBackupDownloadLink }) {
  return (
    <li className="flex items-center justify-between gap-3 py-2.5">
      <div className="min-w-0">
        <p className="truncate font-mono text-sm">{link.name}</p>
        <p className="text-muted-foreground text-xs tabular-nums">
          {formatFileSize(link.size_bytes, { fallback: "0 B" })}
        </p>
      </div>
      <Button asChild variant="outline" size="sm">
        <a href={link.url} download={link.name} target="_blank" rel="noopener noreferrer">
          <Download data-icon="inline-start" aria-hidden="true" />
          Download
          <span className="sr-only"> {link.name}</span>
        </a>
      </Button>
    </li>
  );
}

/** What the downloaded files are for, in the download dialog's description. */
function downloadHint(backup: AdminBackup): string {
  const restoreSet = backup.encrypted ? "the decrypted restore set" : "restore-set.tar";
  return `To restore it in full, put the files in one folder. To restore parts of it on another server, upload ${restoreSet} there.`;
}

function DownloadDialogBody({
  backup,
  links,
  sqliteUserStore,
}: {
  backup: AdminBackup;
  links: AdminBackupDownloadLink[];
  sqliteUserStore: boolean;
}) {
  const restoreSet = useAdminBackupDownloadLinks();
  const [restoreSetLinks, setRestoreSetLinks] = useState<AdminBackupDownloadLink[]>([]);
  const reasonId = useId();
  const expires = links[0]?.expires_at;
  const otherKey = encryptedWithOtherKey(backup);
  const fetchRestoreSet = () =>
    restoreSet.mutate(
      { id: backup.id, scope: "restore-set" },
      {
        onSuccess: setRestoreSetLinks,
        onError: (error) =>
          toast.error("Couldn't prepare the restore set", {
            description: backupErrorMessage(error, { sqliteUserStore }),
          }),
      },
    );
  return (
    <>
      <DialogHeader>
        <DialogTitle>Download backup</DialogTitle>
        <DialogDescription>
          {`${takenLabel(backup)}. `}
          {downloadHint(backup)}
          {expires ? ` The links stop working at ${formatTime(expires)}.` : null}
        </DialogDescription>
      </DialogHeader>
      {backup.encrypted ? (
        <p className="bg-muted/40 rounded-lg border px-3 py-2 text-[13px] leading-relaxed">
          {otherKey
            ? `These files are encrypted with another SECRET_KEY${backup.key_check ? ` (key check ${backup.key_check})` : ""}. To read one, run `
            : "These files are encrypted with this server's SECRET_KEY. To read one, run "}
          <code className="font-mono text-xs">silo backup decrypt &lt;file&gt;.age</code>
          {otherKey ? " with that key set; " : " with SECRET_KEY set; "}
          <code className="font-mono text-xs">silo backup restore --from &lt;folder&gt;</code> reads
          the encrypted folder directly.
        </p>
      ) : null}
      <ul className="divide-border/60 divide-y">
        {links.map((link) => (
          <DownloadRow key={link.name} link={link} />
        ))}
      </ul>
      {backup.encrypted && canRestoreParts(backup) ? (
        <div className="space-y-2 border-t pt-3">
          {restoreSetLinks.length > 0 ? (
            <ul className="divide-border/60 divide-y">
              {restoreSetLinks.map((link) => (
                <DownloadRow key={link.name} link={link} />
              ))}
            </ul>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={fetchRestoreSet}
                disabled={otherKey || restoreSet.isPending}
                aria-describedby={otherKey ? reasonId : undefined}
              >
                {restoreSet.isPending ? (
                  <LoaderCircle
                    data-icon="inline-start"
                    aria-hidden="true"
                    className="motion-safe:animate-spin"
                  />
                ) : (
                  <Download data-icon="inline-start" aria-hidden="true" />
                )}
                Download the restore set (decrypted)
              </Button>
              {otherKey ? (
                <p id={reasonId} className="text-muted-foreground text-xs">
                  {OTHER_KEY_REASON}
                </p>
              ) : null}
            </>
          )}
        </div>
      ) : null}
    </>
  );
}

function DownloadDialog({
  backup,
  links,
  sqliteUserStore,
  onClose,
}: {
  backup: AdminBackup | null;
  links: AdminBackupDownloadLink[];
  sqliteUserStore: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog open={backup !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        {backup ? (
          <DownloadDialogBody
            key={backup.id}
            backup={backup}
            links={links}
            sqliteUserStore={sqliteUserStore}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

/** A table cell inside a phone row's grid: no table padding, text may wrap. */
const PHONE_CELL = "max-sm:block max-sm:p-0 max-sm:whitespace-normal";

export interface BackupTableProps {
  /** Newest first, as the list returns them. */
  items: AdminBackup[];
  selectedId: string | null;
  showAll: boolean;
  onShowAllChange: (showAll: boolean) => void;
  /** The SQLite user store is in use; its conflicts are not "busy" conflicts. */
  sqliteUserStore: boolean;
  /** The operation holding the backup lock, on any server. */
  active: AdminBackupActive | undefined;
  /** Why deleting is unavailable now, from the lock; null when it is free. */
  deleteBlocker: string | null;
  /** This server can restore parts of a backup. */
  selectiveRestore: boolean;
}

export function BackupTable({
  items,
  selectedId,
  showAll,
  onShowAllChange,
  sqliteUserStore,
  active,
  deleteBlocker,
  selectiveRestore,
}: BackupTableProps) {
  const deleteBlockerId = useId();
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [deleting, setDeleting] = useState<AdminBackup | null>(null);
  const [downloading, setDownloading] = useState<AdminBackup | null>(null);
  const [links, setLinks] = useState<AdminBackupDownloadLink[]>([]);
  const deleteBackup = useDeleteAdminBackup();
  const downloadLinks = useAdminBackupDownloadLinks();

  const rows = showAll ? items : items.slice(0, BACKUP_ROWS_SHOWN);
  const hidden = items.length - BACKUP_ROWS_SHOWN;

  const toggle = (id: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  const download = (backup: AdminBackup) =>
    downloadLinks.mutate(
      { id: backup.id },
      {
        onSuccess: (result) => {
          setLinks(result);
          setDownloading(backup);
        },
        onError: (error) =>
          toast.error("Couldn't prepare the download", {
            description: backupErrorMessage(error, { sqliteUserStore }),
          }),
      },
    );

  const confirmDelete = () => {
    const target = deleting;
    if (!target) return;
    deleteBackup.mutate(target.id, {
      onSuccess: () => {
        setDeleting(null);
        toast.success("Backup deleted");
      },
      onError: (error) => {
        setDeleting(null);
        toast.error("Couldn't delete the backup", {
          description: backupErrorMessage(error, { sqliteUserStore }),
        });
      },
    });
  };

  return (
    <div className="space-y-3">
      {deleteBlocker ? (
        <p id={deleteBlockerId} className="text-muted-foreground text-[13px]">
          Backups can&apos;t be deleted right now. {deleteBlocker}
        </p>
      ) : null}
      {/* Below `sm` each row stacks into a small grid (date and status on one
          line, size under the date, actions under both) instead of scrolling
          sideways. */}
      <div className="surface-panel overflow-hidden rounded-2xl">
        <Table className="max-sm:block">
          <TableHeader className="max-sm:hidden">
            <TableRow>
              <TableHead>Taken</TableHead>
              <TableHead className="text-right">Size</TableHead>
              <TableHead className="hidden md:table-cell">Silo version</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody className="max-sm:block">
            {rows.map((backup) => {
              const detailsId = `${backupRowId(backup.id)}-missing`;
              const open = expanded.has(backup.id);
              const pendingDownload =
                downloadLinks.isPending && downloadLinks.variables?.id === backup.id;
              const otherKey = encryptedWithOtherKey(backup);
              const otherKeyId = `${backupRowId(backup.id)}-key`;
              const restorable = selectiveRestore && canRestoreParts(backup);
              const restoreLabel = (
                <>
                  Restore…
                  <span className="sr-only"> from {takenLabel(backup)}</span>
                </>
              );
              return (
                <Fragment key={backup.id}>
                  <TableRow
                    id={backupRowId(backup.id)}
                    data-state={selectedId === backup.id ? "selected" : undefined}
                    className={cn(
                      "scroll-mt-24",
                      "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:items-start max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-3 max-sm:py-3",
                    )}
                  >
                    <TableCell className={PHONE_CELL}>
                      <div className="font-medium">{takenLabel(backup)}</div>
                      <div className="text-muted-foreground text-xs">
                        {BACKUP_KIND_LABELS[backup.kind]}
                      </div>
                      {otherKey ? (
                        <div id={otherKeyId} className="mt-1 max-w-80 space-y-0.5 text-xs">
                          <p className="text-amber-600 dark:text-amber-400">
                            {otherKeyNote(backup)}
                          </p>
                          <p className="text-muted-foreground whitespace-normal">
                            {OTHER_KEY_REASON}
                          </p>
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell
                      className={cn(
                        "text-right tabular-nums",
                        PHONE_CELL,
                        "max-sm:text-muted-foreground max-sm:row-start-2 max-sm:text-left max-sm:text-xs",
                      )}
                    >
                      {formatFileSize(backup.size_bytes, { fallback: "—" })}
                    </TableCell>
                    <TableCell className="text-muted-foreground hidden md:table-cell">
                      {knownSiloVersion(backup.silo_version) ?? "—"}
                    </TableCell>
                    <TableCell
                      className={cn(
                        PHONE_CELL,
                        "max-sm:col-start-2 max-sm:row-start-1 max-sm:flex max-sm:justify-end",
                      )}
                    >
                      <BackupStatus
                        backup={backup}
                        active={active?.backup_id === backup.id ? active : undefined}
                        expanded={open}
                        onToggle={() => toggle(backup.id)}
                        detailsId={detailsId}
                      />
                    </TableCell>
                    <TableCell className={cn(PHONE_CELL, "max-sm:col-span-2 max-sm:row-start-3")}>
                      <div className="flex flex-wrap justify-end gap-1 max-sm:-ml-2.5 max-sm:justify-start">
                        {restorable && otherKey ? (
                          <Button variant="ghost" size="sm" disabled aria-describedby={otherKeyId}>
                            {restoreLabel}
                          </Button>
                        ) : restorable ? (
                          <Button asChild variant="ghost" size="sm">
                            <Link to={restorePath(backup.id)}>{restoreLabel}</Link>
                          </Button>
                        ) : null}
                        {isFinished(backup) ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => download(backup)}
                            disabled={downloadLinks.isPending}
                          >
                            {pendingDownload ? (
                              <LoaderCircle
                                data-icon="inline-start"
                                aria-hidden="true"
                                className="motion-safe:animate-spin"
                              />
                            ) : (
                              <Download data-icon="inline-start" aria-hidden="true" />
                            )}
                            Download
                          </Button>
                        ) : null}
                        {backup.status !== "in_progress" ? (
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => setDeleting(backup)}
                            disabled={deleteBlocker !== null}
                            aria-describedby={deleteBlocker ? deleteBlockerId : undefined}
                            aria-label={`Delete the backup from ${takenLabel(backup)}`}
                          >
                            <Trash2 aria-hidden="true" />
                          </Button>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                  {open ? (
                    <TableRow id={detailsId} className="hover:bg-transparent max-sm:block">
                      <TableCell colSpan={5} className="whitespace-normal max-sm:block">
                        {/* Zero width so long names and errors can't widen the table;
                            min-w-full then fills the cell. */}
                        <div className="w-0 min-w-full">
                          <BackupWarningDetails backup={backup} />
                        </div>
                      </TableCell>
                    </TableRow>
                  ) : null}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>
      </div>
      {hidden > 0 ? (
        <Button variant="outline" size="sm" onClick={() => onShowAllChange(!showAll)}>
          {showAll ? "Show fewer" : `Show all ${items.length}`}
        </Button>
      ) : null}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title="Delete this backup?"
        description={
          deleting ? `The backup taken ${takenLabel(deleting)} is removed from storage.` : ""
        }
        confirmLabel="Delete backup"
        variant="destructive"
        irreversible
        isPending={deleteBackup.isPending}
        onConfirm={confirmDelete}
      />
      <DownloadDialog
        backup={downloading}
        links={links}
        sqliteUserStore={sqliteUserStore}
        onClose={() => setDownloading(null)}
      />
    </div>
  );
}
