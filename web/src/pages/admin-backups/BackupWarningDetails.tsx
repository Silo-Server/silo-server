import { useId, useState, type ReactNode } from "react";
import { Copy, TriangleAlert } from "lucide-react";
import { toast } from "sonner";

import type { AdminBackup, AdminBackupOmitted } from "@/api/v2/adminBackups";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";

import { Disclosure } from "./Disclosure";
import {
  groupMissingBlobs,
  groupOmissions,
  missingGroupTitle,
  missingHeadline,
  NAMES_SHOWN,
  nameSampleText,
  omissionGroupTitle,
  warningDetailsReport,
  type MissingFileGroup,
  type OmissionGroup,
} from "./warningDetails";

/** A list long enough to need its own scroll: names or keys, selectable to copy. */
const LONG_LIST =
  "bg-background/60 max-h-48 overflow-y-auto rounded-md border px-3 py-2 text-xs select-text";

/** One section's headline: the only amber line, with the warning icon. */
function Headline({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-start gap-1.5 text-[13px] text-amber-600 dark:text-amber-400">
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </p>
  );
}

function OmissionGroupRow({ group }: { group: OmissionGroup }) {
  const t = useT();
  const listId = useId();
  const [open, setOpen] = useState(false);
  return (
    <li className="min-w-0 space-y-1">
      <p className="text-[13px]">{omissionGroupTitle(group)}</p>
      <p
        title={group.detail}
        className={cn(
          "text-muted-foreground font-mono text-xs",
          open ? "break-words whitespace-pre-wrap" : "truncate",
        )}
      >
        {group.detail}
      </p>
      <p className="text-muted-foreground text-xs break-words">{nameSampleText(group.labels)}</p>
      {group.labels.length > NAMES_SHOWN ? (
        <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
          {open
            ? t("web.admin.backups.warnings.hideNames")
            : t("web.admin.backups.warnings.showAllNames", {
                count: group.labels.length,
                count_text: group.labels.length,
              })}
        </Disclosure>
      ) : null}
      {open ? (
        <ul id={listId} className={cn(LONG_LIST, "gap-x-6 sm:columns-2 lg:columns-3")}>
          {group.labels.map((label, position) => (
            <li key={`${label}-${position}`} className="break-words">
              {label}
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  );
}

type OmittedContext = "backup" | "restore";

/**
 * Accounts a restore set left out, grouped by cause. Collapsed to its
 * headline until opened; `context` words the headline for the Backups page
 * or the restore chooser.
 */
export function OmittedAccounts({
  omitted,
  context,
}: {
  omitted: readonly AdminBackupOmitted[];
  context: OmittedContext;
}) {
  const t = useT();
  const bodyId = useId();
  const [open, setOpen] = useState(false);
  if (omitted.length === 0) return null;
  const groups = groupOmissions(omitted);
  const headline = t(
    context === "restore"
      ? "web.admin.backups.warnings.omittedRestore"
      : "web.admin.backups.warnings.omittedBackup",
    { count: omitted.length, count_text: omitted.length },
  );
  return (
    <div className="min-w-0 space-y-1.5">
      <Headline>{headline}</Headline>
      <Disclosure open={open} onOpenChange={setOpen} controls={bodyId}>
        {open
          ? t("web.admin.backups.warnings.hideAccounts")
          : t("web.admin.backups.warnings.showAccounts")}
      </Disclosure>
      {open ? (
        <ul id={bodyId} className="space-y-3 pt-1">
          {groups.map((group) => (
            <OmissionGroupRow
              key={`${group.reason}:${group.cause}:${group.detail}`}
              group={group}
            />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function MissingGroupRow({ group }: { group: MissingFileGroup }) {
  const t = useT();
  const listId = useId();
  const [open, setOpen] = useState(false);
  return (
    <li className="min-w-0 space-y-1">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-[13px]">{missingGroupTitle(group)}</span>
        <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
          {open
            ? t("web.admin.backups.warnings.hideGroupFiles")
            : t("web.admin.backups.warnings.showGroupFiles")}
        </Disclosure>
      </div>
      {open ? (
        <ul id={listId} className={cn(LONG_LIST, "font-mono")}>
          {group.keys.map((key, position) => (
            <li key={`${key}-${position}`} className="break-all">
              {key}
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  );
}

/** Files missing when the backup copied them, grouped by what referenced them. */
function MissingFiles({ blobs, total }: { blobs: AdminBackup["missing_blobs"]; total: number }) {
  const t = useT();
  const bodyId = useId();
  const [open, setOpen] = useState(false);
  if (blobs.length === 0) return null;
  return (
    <div className="min-w-0 space-y-1.5">
      <Headline>{missingHeadline(blobs.length, total)}</Headline>
      <Disclosure open={open} onOpenChange={setOpen} controls={bodyId}>
        {open
          ? t("web.admin.backups.warnings.hideFiles")
          : t("web.admin.backups.warnings.showFiles")}
      </Disclosure>
      {open ? (
        <div id={bodyId} className="space-y-2 pt-1">
          <p className="text-muted-foreground text-xs">
            {t("web.admin.backups.warnings.missingExplanation")}
          </p>
          <ul className="space-y-2">
            {groupMissingBlobs(blobs).map((group) => (
              <MissingGroupRow key={group.source} group={group} />
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

/** A backup's warnings as headlines with grouped details, and a plain-text copy for an issue. */
export function BackupWarningDetails({ backup }: { backup: AdminBackup }) {
  const t = useT();
  const copy = () =>
    copyTextToClipboard(
      warningDetailsReport({
        backupId: backup.id,
        omitted: backup.restore_set_omitted,
        missing: backup.missing_blobs,
        missingTotal: backup.missing_blobs_total,
      }),
    ).then(
      () => toast.success(t("web.admin.backups.warnings.copied")),
      () => toast.error(t("web.admin.backups.warnings.copyFailed")),
    );
  return (
    <div className="min-w-0 space-y-3">
      <OmittedAccounts omitted={backup.restore_set_omitted} context="backup" />
      <MissingFiles blobs={backup.missing_blobs} total={backup.missing_blobs_total} />
      <Button variant="outline" size="sm" onClick={() => void copy()}>
        <Copy data-icon="inline-start" aria-hidden="true" />
        {t("web.admin.backups.warnings.copy")}
      </Button>
    </div>
  );
}
