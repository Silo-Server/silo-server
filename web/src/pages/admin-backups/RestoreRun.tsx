import { useState } from "react";
import { Link } from "react-router";
import {
  CircleCheck,
  CircleMinus,
  CircleX,
  Download,
  LoaderCircle,
  TriangleAlert,
} from "lucide-react";

import type { AdminBackup, AdminBackupActive } from "@/api/v2/adminBackups";
import type {
  RestoreJob,
  RestoreReportAccount,
  RestoreReportItem,
} from "@/api/v2/adminBackupRestore";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { useT, type MessageKey } from "@/i18n";
import { formatTime } from "@/lib/datetime";
import { DATA_TYPE_LABELS, dataCountsPhrase, titleList } from "@/lib/profileDataTypes";
import { cn } from "@/lib/utils";
import { FieldGroup } from "@/pages/admin-settings/FieldGroup";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { backupDateTime } from "./backupPresentation";
import {
  REASON_SENTENCES,
  REPORT_ERROR_SENTENCES,
  SECTION_TITLES,
  displayLabel,
  reportHeadline,
} from "./restoreModel";

const OUTCOME = {
  restored: {
    label: "web.admin.backups.restore.outcome.restored",
    Icon: CircleCheck,
    tone: "text-green-600 dark:text-green-400",
  },
  unchanged: {
    label: "web.admin.backups.restore.outcome.unchanged",
    Icon: CircleMinus,
    tone: "text-muted-foreground",
  },
  left_out: {
    label: "web.admin.backups.restore.outcome.leftOut",
    Icon: TriangleAlert,
    tone: "text-amber-600 dark:text-amber-400",
  },
  failed: {
    label: "web.admin.backups.restore.outcome.failed",
    Icon: CircleX,
    tone: "text-destructive",
  },
} satisfies Record<RestoreReportItem["outcome"], { label: MessageKey; [key: string]: unknown }>;

function ReportRow({ item }: { item: RestoreReportItem }) {
  const t = useT();
  const outcome = OUTCOME[item.outcome];
  const details = [
    item.outcome === "left_out" && item.reason ? t(REASON_SENTENCES[item.reason]) : null,
    item.error ?? null,
    item.message ?? null,
    ...item.notes,
    item.missing_blobs.length > 0
      ? t("web.admin.backups.restore.run.filesMissing", {
          count: item.missing_blobs.length,
          count_text: String(item.missing_blobs.length),
        })
      : null,
    item.restart_keys.length > 0
      ? t("web.admin.backups.restore.run.restartKeys", { keys: item.restart_keys.join(", ") })
      : item.restart_required
        ? t("web.admin.backups.restore.run.restart")
        : null,
  ].filter((detail): detail is string => Boolean(detail));
  return (
    <li className="border-border/60 flex items-start gap-3 border-b py-3 last:border-b-0">
      <outcome.Icon aria-hidden="true" className={cn("mt-0.5 size-4 shrink-0", outcome.tone)} />
      <div className="min-w-0 flex-1">
        <p className="text-sm">
          {t("web.admin.backups.restore.run.itemLine", {
            label: displayLabel(item),
            section: t(SECTION_TITLES[item.section]),
            outcome: t(outcome.label),
            b: (chunks) => <span className="font-medium">{chunks}</span>,
            muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
          })}
        </p>
        {details.map((detail, position) => (
          <p key={position} className="text-muted-foreground mt-0.5 text-[13px] break-words">
            {detail}
          </p>
        ))}
        {item.account ? (
          <AccountReport account={item.account} described={Boolean(item.message)} />
        ) : null}
      </div>
    </li>
  );
}

function ReportList({ label, items }: { label: string; items: readonly RestoreReportItem[] }) {
  if (items.length === 0) return null;
  return (
    <FieldGroup label={label}>
      <ul>
        {items.map((item) => (
          <ReportRow key={`${item.section}:${item.id}`} item={item} />
        ))}
      </ul>
    </FieldGroup>
  );
}

/** An account's report: whether it was created, each profile, and each data type's counts. */
function AccountReport({
  account,
  described,
}: {
  account: RestoreReportAccount;
  /** The item's own message already says what happened to the account. */
  described: boolean;
}) {
  const t = useT();
  return (
    <div className="mt-2 space-y-2">
      {described ? null : (
        <p className="text-[13px]">
          {account.created
            ? t("web.admin.backups.restore.run.accountCreated")
            : t("web.admin.backups.restore.run.accountMerged")}
        </p>
      )}
      {account.profiles.length > 0 ? (
        <ul className="border-border/60 space-y-2 border-l pl-3">
          {account.profiles.map((profile) => {
            const outcome = OUTCOME[profile.outcome];
            return (
              <li key={profile.id}>
                <p className="flex items-center gap-1.5 text-[13px]">
                  <outcome.Icon aria-hidden="true" className={cn("size-3.5", outcome.tone)} />
                  <span className="font-medium">{profile.label}</span>
                  <span className="text-muted-foreground">
                    {t("web.admin.backups.restore.run.profileOutcome", {
                      outcome: profile.created
                        ? t("web.admin.backups.restore.outcome.created")
                        : t(outcome.label),
                    })}
                  </span>
                </p>
                {[
                  profile.outcome === "left_out" && profile.reason
                    ? t(REASON_SENTENCES[profile.reason])
                    : null,
                  profile.message ?? null,
                  ...profile.notes,
                ]
                  .filter((line): line is string => Boolean(line))
                  .map((line, position) => (
                    <p key={position} className="text-muted-foreground text-xs break-words">
                      {line}
                    </p>
                  ))}
                {profile.data.length > 0 ? (
                  <ul className="mt-1 space-y-0.5 text-xs">
                    {profile.data.map((data) => (
                      <li key={data.type}>
                        {t("web.admin.backups.restore.run.dataLine", {
                          label: DATA_TYPE_LABELS[data.type],
                          counts: dataCountsPhrase(data),
                          muted: (chunks) => (
                            <span className="text-muted-foreground tabular-nums">{chunks}</span>
                          ),
                        })}
                        {data.notes.map((note, position) => (
                          <span key={position} className="text-muted-foreground block">
                            {note}
                          </span>
                        ))}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
      {account.unmatched.length > 0 ? (
        <p className="text-muted-foreground text-xs break-words">
          {t("web.admin.backups.restore.run.unmatched", { titles: titleList(account.unmatched) })}
        </p>
      ) : null}
      {account.hidden.length > 0 ? (
        <p className="text-muted-foreground text-xs break-words">
          {t("web.admin.backups.restore.run.hidden", { titles: titleList(account.hidden) })}
        </p>
      ) : null}
    </div>
  );
}

function downloadReport(job: RestoreJob) {
  const blob = new Blob([JSON.stringify(job.backup_restore_result ?? {}, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  const result = job.backup_restore_result;
  anchor.download = `silo-restore-report-${result?.backup_id || result?.upload_id || job.id}.json`;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

export interface RestoreRunProps {
  job: RestoreJob;
  /** This server's backups, to name the recovery point by its time. */
  backups: AdminBackup[];
  /** The lock holder from the backups list, for the item being restored. */
  active: AdminBackupActive | undefined;
  onCancel: () => void;
  cancelPending: boolean;
  onChooseAgain: () => void;
}

/** Step 3: progress while the job runs, then its report. */
export function RestoreRun({
  job,
  backups,
  active,
  onCancel,
  cancelPending,
  onChooseAgain,
}: RestoreRunProps) {
  const t = useT();
  const [confirmCancel, setConfirmCancel] = useState(false);
  const report = job.backup_restore_result;
  const items = report?.items ?? [];

  if (!job.terminal) {
    const progress = job.progress;
    const percent =
      progress && progress.total > 0 ? Math.round((progress.current / progress.total) * 100) : 0;
    const stopping = job.state === "canceling";
    const progressNote =
      progress && progress.total > 0
        ? t("web.admin.backups.restore.run.steps", {
            total: progress.total,
            current_text: String(progress.current),
            total_text: String(progress.total),
          })
        : t("web.admin.backups.restore.run.backsUpFirst");
    return (
      <div className="max-w-[70rem] space-y-5">
        <div className="surface-panel space-y-3 rounded-2xl p-5" role="status">
          <div className="flex items-center gap-2 text-sm font-medium">
            <LoaderCircle aria-hidden="true" className="size-4 motion-safe:animate-spin" />
            {stopping
              ? t("web.admin.backups.restore.run.stopping")
              : job.state === "queued"
                ? t("web.admin.backups.restore.run.waiting")
                : t("web.admin.backups.restore.run.restoring")}
          </div>
          <Progress value={percent} aria-label={t("web.admin.backups.restore.run.progressLabel")} />
          <p className="text-muted-foreground text-[13px]">
            {active?.operation === "restore" && active.message
              ? t("web.admin.backups.restore.run.withServerMessage", {
                  text: progressNote,
                  message: active.message,
                })
              : progressNote}
          </p>
          <Button
            variant="outline"
            size="sm"
            disabled={!job.cancelable || stopping || cancelPending}
            onClick={() => setConfirmCancel(true)}
          >
            {t("web.admin.backups.restore.run.stop")}
          </Button>
        </div>
        <ReportList label={t("web.admin.backups.restore.run.doneSoFar")} items={items} />
        <ConfirmDialog
          open={confirmCancel}
          onOpenChange={setConfirmCancel}
          title={t("web.admin.backups.restore.run.stopTitle")}
          description={t("web.admin.backups.restore.run.stopDescription")}
          confirmLabel={t("web.admin.backups.restore.run.stop")}
          cancelLabel={t("web.admin.backups.restore.run.keepRestoring")}
          variant="destructive"
          onConfirm={onCancel}
        />
      </div>
    );
  }

  const recovery = report?.before_restore_backup_id
    ? backups.find((backup) => backup.id === report.before_restore_backup_id)
    : undefined;
  const code = report?.error_code ?? (report?.interrupted ? "interrupted" : undefined);
  let ending: string | null = null;
  if (job.state === "canceled") {
    ending = t("web.admin.backups.restore.run.canceled");
  } else if (code) ending = t(REPORT_ERROR_SENTENCES[code]);
  else if (job.state === "failed") ending = t(REPORT_ERROR_SENTENCES.failed);
  // The server's own words, when they add to the sentence above.
  const detail =
    ending && code !== "interrupted"
      ? (report?.error ?? (report ? null : job.failure?.detail))
      : null;

  return (
    <div className="max-w-[70rem] space-y-5">
      <div className="space-y-2">
        <p className="text-base font-medium">
          {report ? reportHeadline(report.counts) : t("web.admin.backups.restore.run.noReport")}
        </p>
        {job.finished_at ? (
          <p className="text-muted-foreground text-[13px]">
            {t("web.admin.backups.restore.run.finished", {
              date: backupDateTime(job.finished_at),
            })}
          </p>
        ) : null}
        {ending ? <SettingFieldStatus tone="warn">{ending}</SettingFieldStatus> : null}
        {detail ? <p className="text-muted-foreground text-[13px] break-words">{detail}</p> : null}
      </div>

      <ReportList label={t("web.admin.backups.restore.run.report")} items={items} />

      {report?.before_restore_backup_id ? (
        <p className="text-muted-foreground text-sm leading-relaxed">
          {t("web.admin.backups.restore.run.recovery", {
            copy: recovery ? formatTime(recovery.created_at) : report.before_restore_backup_id,
            link: (chunks) => (
              <Link
                to={`/admin/backups?backup=${encodeURIComponent(report.before_restore_backup_id!)}`}
                className="text-foreground underline underline-offset-2"
              >
                {chunks}
              </Link>
            ),
            code: (chunks) => <code className="font-mono text-xs">{chunks}</code>,
          })}
        </p>
      ) : null}

      <div className="flex flex-wrap gap-2">
        {report ? (
          <Button variant="outline" onClick={() => downloadReport(job)}>
            <Download data-icon="inline-start" aria-hidden="true" />
            {t("web.admin.backups.restore.run.downloadReport")}
          </Button>
        ) : null}
        <Button variant="outline" onClick={onChooseAgain}>
          {t("web.admin.backups.restore.run.chooseAgain")}
        </Button>
      </div>
    </div>
  );
}
