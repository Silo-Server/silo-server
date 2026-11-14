import { useEffect, useId, useMemo, useRef, useState } from "react";
import { DatabaseBackup, LoaderCircle, Upload } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { toast } from "sonner";

import type { AdminBackupActive, AdminBackupDestination } from "@/api/v2/adminBackups";
import { PageHeader } from "@/components/page/PageHeader";
import { SaveBar } from "@/components/SaveBar";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import {
  backupRunning,
  useAdminBackupCapabilities,
  useAdminBackups,
  useStartAdminBackup,
} from "@/hooks/queries/admin/backups";
import { useAdminBackupUploads } from "@/hooks/queries/admin/backupUploads";
import { useTask } from "@/hooks/queries/admin/tasks";
import { adminKeys } from "@/hooks/queries/keys";
import { useT } from "@/i18n";
import { formatTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { ConfirmKeyDialog, S3SetupDialog } from "@/pages/admin-backups/BackupS3Dialogs";
import {
  BackupLocationGroup,
  BackupScheduleSettings,
  RestoreCommandGroup,
  type FullRestoreSource,
} from "@/pages/admin-backups/BackupSettings";
import { BackupTable } from "@/pages/admin-backups/BackupTable";
import { UploadBackupDialog } from "@/pages/admin-backups/UploadBackupDialog";
import { UploadedBackups } from "@/pages/admin-backups/UploadedBackups";
import {
  BACKUP_DESTINATION_KEY,
  BACKUP_ENABLED_KEY,
  BACKUP_KEEP_KEY,
  BACKUP_ROWS_SHOWN,
  BACKUP_SETTING_KEYS,
  backupRowId,
  backUpNowBlocker,
  backupErrorMessage,
  backupSchedulePhrases,
  defaultBackupLocation,
  isBucketLocation,
  isFinished,
  keyConfirmationNeeded,
  lastRunProblem,
  lockBlocker,
  nextBackupRun,
  activeRestorePath,
  restoreCommand,
  retentionNote,
  retentionPlan,
  visibleWarnings,
  warningText,
  warningTone,
} from "@/pages/admin-backups/backupPresentation";
import { BACKUP_S3_KEYS, s3FieldsFromSettings } from "@/pages/admin-backups/s3SetupModel";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";
// The settings layout loads these field styles for its pages; this page is not one of them.
import "@/styles/admin-settings.css";

/** The current time, refreshed each minute so "today" and "tomorrow" stay right. */
function useMinuteClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 60_000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}

function asDestination(value: string): AdminBackupDestination {
  return value === "s3" ? "s3" : "default";
}

/** What the operation holding the backup lock is doing. */
function ActiveOperation({ active }: { active: AdminBackupActive }) {
  const t = useT();
  switch (active.operation) {
    case "backup":
      return t("web.admin.backups.active.backingUp", { time: formatTime(active.started_at) });
    case "restore": {
      const path = activeRestorePath(active);
      return (
        <>
          {t("web.admin.backups.active.restoreRunning")}{" "}
          {path ? (
            <Link to={path} className="underline underline-offset-2">
              {t("web.admin.backups.active.restoreProgressLink")}
            </Link>
          ) : null}
        </>
      );
    }
    case "upload":
      return t("web.admin.backups.active.checkingUpload");
    default:
      return active.upload_id
        ? t("web.admin.backups.active.deletingUpload")
        : t("web.admin.backups.active.deleting");
  }
}

export default function AdminBackups() {
  const t = useT();
  const now = useMinuteClock();
  const queryClient = useQueryClient();
  const capabilities = useAdminBackupCapabilities();
  const backups = useAdminBackups();
  const backupTask = useTask("backup");
  const form = useSettingsForm({ keys: BACKUP_SETTING_KEYS });
  const startBackup = useStartAdminBackup();
  // `?backup=<id>` (the recovery point a restore report links to) opens with
  // that backup selected.
  const [searchParams] = useSearchParams();
  const linkedId = searchParams.get("backup");
  const [selectedId, setSelectedId] = useState<string | null>(linkedId);
  const [showAll, setShowAll] = useState(false);
  // The backup this page started, and when, so its outcome is read only from
  // a list fetched after the start.
  const started = useRef<{ id: string; at: number } | null>(null);
  const blockerId = useId();

  const list = backups.data;
  const items = useMemo(() => list?.items ?? [], [list]);
  const linkedIndex = linkedId ? items.findIndex((item) => item.id === linkedId) : -1;
  const rowsExpanded = showAll || (selectedId === linkedId && linkedIndex >= BACKUP_ROWS_SHOWN);
  const linkedFound = linkedIndex >= 0;
  useEffect(() => {
    if (linkedId && linkedFound)
      document.getElementById(backupRowId(linkedId))?.scrollIntoView?.({ block: "center" });
  }, [linkedId, linkedFound]);
  const warnings = list?.warnings ?? [];
  const running = backupRunning(list);
  const shownWarnings = visibleWarnings(warnings, {
    backingUp: running,
    neverBackedUp: (list?.items.length ?? 0) === 0 && !list?.last_success_at,
  });
  const active = list?.active;
  const busy = Boolean(list?.busy || active || running);
  const configured = capabilities.data ? capabilities.data.state === "available" : true;
  const selectiveRestore = capabilities.data?.selective_restore === true;
  const uploadAvailable = capabilities.data?.upload === true;
  const uploads = useAdminBackupUploads(uploadAvailable);
  const [uploading, setUploading] = useState(false);
  const [settingUpS3, setSettingUpS3] = useState(false);
  const [confirmingKey, setConfirmingKey] = useState(false);
  const destinations = capabilities.data?.destinations;
  const s3Offered = destinations?.includes("s3") ?? false;
  const savedDestination =
    list?.destination ?? asDestination(form.getPersistedValue(BACKUP_DESTINATION_KEY));
  // Unstaged, the choice is whatever the server uses now. The S3 dialog saves
  // on its own, so reading the form's local copy would show the old choice.
  const draftDestination = form.isDirty(BACKUP_DESTINATION_KEY)
    ? asDestination(form.getValue(BACKUP_DESTINATION_KEY))
    : savedDestination;
  const s3Configured = Boolean(
    form.getPersistedValue(BACKUP_S3_KEYS.endpoint).trim() &&
    form.getPersistedValue(BACKUP_S3_KEYS.bucket).trim(),
  );
  const needsKey = keyConfirmationNeeded(list);

  // The status line and the strip describe what is saved, not a draft.
  const keep = list?.keep ?? (Number(form.getPersistedValue(BACKUP_KEEP_KEY)) || 7);
  const enabled = list?.enabled ?? form.getPersistedValue(BACKUP_ENABLED_KEY) !== "false";
  const taskNextRunAt = backupTask.data?.next_run_at;
  const next = enabled ? nextBackupRun(now, taskNextRunAt) : null;
  // Once the scheduled time passes, read the schedule again instead of
  // showing the next run as unknown until something else refreshes it.
  useEffect(() => {
    if (taskNextRunAt && Date.parse(taskNextRunAt) <= now.getTime())
      void queryClient.invalidateQueries({ queryKey: adminKeys.task("backup") });
  }, [now, queryClient, taskNextRunAt]);
  const plan = useMemo(() => retentionPlan(items, keep), [items, keep]);
  const phrases = backupSchedulePhrases({
    lastSuccessAt: list?.last_success_at,
    enabled,
    busy,
    next,
    now,
  });
  const lastRunWarning = lastRunProblem(list?.last_run, now);

  const location = defaultBackupLocation({
    // Before capabilities load, assume the server has its own storage.
    hasDefault: destinations ? destinations.includes("default") : true,
    destination: list?.destination,
    items,
    privateBucket: form.getPersistedValue("s3.private_bucket"),
  });
  // The command restores without --accept-lost-credentials, so it names a
  // backup this server verified: the selected one, else the newest.
  const verified = (b: (typeof items)[number]) => isFinished(b) && b.status !== "unverified";
  const commandBackup =
    items.find((item) => item.id === selectedId && verified(item)) ?? items.find(verified);
  const command = restoreCommand(commandBackup?.location);
  let commandSource: FullRestoreSource = "download";
  if (commandBackup) commandSource = isBucketLocation(commandBackup.location) ? "bucket" : "folder";

  const lock = lockBlocker(list?.busy ?? false, active);
  const blocker = startBackup.isPending
    ? null
    : backUpNowBlocker({
        configured,
        lock,
        warnings,
        destinationAvailable: list?.destination_available ?? true,
      });

  // Report how the backup this page started ended, once a list read after the
  // start shows its run. A run that ends some other way (another server's
  // list, a reload) shows in the status line's last-run warning instead.
  const listUpdatedAt = backups.dataUpdatedAt;
  const lastRun = list?.last_run;
  useEffect(() => {
    const pending = started.current;
    if (!pending || listUpdatedAt <= pending.at || busy) return;
    started.current = null;
    if (lastRun?.id !== pending.id) return;
    if (lastRun.status === "complete") toast.success(t("web.admin.backups.toast.finished"));
    else if (lastRun.status === "complete_with_warnings")
      toast.success(t("web.admin.backups.toast.finishedWithWarnings"), {
        description: t("web.admin.backups.toast.finishedWithWarningsDescription"),
      });
    else toast.error(t("web.admin.backups.toast.didntFinish"), { description: lastRun.error });
  }, [busy, lastRun, listUpdatedAt, t]);

  const backUpNow = () =>
    startBackup.mutate(undefined, {
      onSuccess: (result) => {
        started.current = { id: result.id, at: Date.now() };
        toast.success(t("web.admin.backups.toast.started"), {
          description: t("web.admin.backups.toast.startedDescription"),
        });
      },
      onError: (error) =>
        toast.error(t("web.admin.backups.toast.startFailed"), {
          description: backupErrorMessage(error),
        }),
    });

  const save = async () => {
    await form.save();
    void queryClient.invalidateQueries({ queryKey: adminKeys.backups() });
    void queryClient.invalidateQueries({ queryKey: adminKeys.task("backup") });
  };

  const backUpNowButton = (
    <Button
      onClick={backUpNow}
      disabled={blocker !== null || startBackup.isPending || !list}
      aria-describedby={blocker ? blockerId : undefined}
    >
      {startBackup.isPending ? (
        <LoaderCircle data-icon="inline-start" className="motion-safe:animate-spin" />
      ) : (
        <DatabaseBackup data-icon="inline-start" aria-hidden="true" />
      )}
      {t("web.admin.backups.backUpNow.button")}
    </Button>
  );

  return (
    <div className="space-y-8 pb-24">
      <UnsavedChangesGuard />
      <div>
        <PageHeader
          title={t("web.admin.backups.header.title")}
          subtitle={t("web.admin.backups.header.subtitle")}
          className="mb-4"
          actions={
            <div className="flex flex-col items-end gap-1">
              <div className="flex flex-wrap justify-end gap-2">
                {uploadAvailable ? (
                  <Button variant="outline" onClick={() => setUploading(true)}>
                    <Upload data-icon="inline-start" aria-hidden="true" />
                    {t("web.admin.backups.header.upload")}
                  </Button>
                ) : null}
                {backUpNowButton}
              </div>
              {blocker ? (
                <p id={blockerId} className="text-muted-foreground max-w-72 text-end text-xs">
                  {blocker}
                </p>
              ) : null}
            </div>
          }
        />

        {list ? (
          <div className="space-y-1.5" role="status">
            <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
              <span
                aria-hidden="true"
                className={cn(
                  "size-2 shrink-0 rounded-full",
                  !list.last_success_at
                    ? "bg-muted-foreground/50"
                    : shownWarnings.some((warning) => warning.code === "no_recent_backup")
                      ? "bg-amber-500"
                      : "bg-green-500",
                )}
              />
              <span>{phrases.last}</span>
              {phrases.next ? <span className="text-muted-foreground">{phrases.next}</span> : null}
            </p>
            {active ? (
              <div className="max-w-md space-y-1 pt-1">
                <div className="flex items-baseline justify-between gap-3 text-sm">
                  <span>
                    <ActiveOperation active={active} />
                  </span>
                  {active.operation === "backup" ? (
                    <span className="text-muted-foreground tabular-nums">
                      {t("web.admin.backups.active.percent", {
                        percent: Math.round(active.progress),
                      })}
                    </span>
                  ) : null}
                </div>
                {active.operation === "backup" ? (
                  <Progress
                    value={active.progress}
                    aria-label={t("web.admin.backups.active.progressLabel")}
                  />
                ) : null}
                {active.message ? (
                  <p className="text-muted-foreground text-xs">{active.message}</p>
                ) : null}
              </div>
            ) : null}
            {lastRunWarning ? (
              <div>
                <SettingFieldStatus tone="warn">{lastRunWarning}</SettingFieldStatus>
              </div>
            ) : null}
            {shownWarnings.map((warning) => (
              <div key={warning.code} className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <SettingFieldStatus tone={warningTone(warning)}>
                  {warningText(warning)}
                </SettingFieldStatus>
                {warning.code === "key_not_confirmed" && needsKey ? (
                  <Button variant="outline" size="sm" onClick={() => setConfirmingKey(true)}>
                    {t("web.admin.backups.confirmKey.button")}
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
        ) : null}
      </div>

      {backups.isPending ? (
        <div className="space-y-4" role="status" aria-label={t("web.admin.backups.list.loading")}>
          <Skeleton className="h-28 w-full rounded-xl" />
          <Skeleton className="h-48 w-full rounded-2xl" />
        </div>
      ) : backups.isError ? (
        <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
          <p>
            {t("web.admin.backups.list.loadFailed", {
              reason: backupErrorMessage(backups.error),
            })}
          </p>
          <Button variant="outline" size="sm" onClick={() => void backups.refetch()}>
            {t("common.tryAgain")}
          </Button>
        </div>
      ) : items.length === 0 && running ? (
        <div className="surface-panel rounded-2xl p-6" role="status">
          <p className="font-medium">{t("web.admin.backups.list.firstRunning")}</p>
          <p className="text-muted-foreground text-sm">
            {t("web.admin.backups.list.firstRunningDetail")}
          </p>
        </div>
      ) : items.length === 0 ? (
        <div className="surface-panel space-y-3 rounded-2xl p-6">
          <p className="font-medium">{t("web.admin.backups.list.empty")}</p>
          <p className="text-muted-foreground text-sm">
            {configured
              ? t("web.admin.backups.list.emptyConfigured")
              : t("web.admin.backups.list.emptyUnconfigured")}
          </p>
          {configured ? backUpNowButton : null}
        </div>
      ) : (
        <section aria-labelledby="saved-backups-heading" className="space-y-6">
          <div className="space-y-3">
            <div className="space-y-1">
              <h2 id="saved-backups-heading" className="text-[15px] font-semibold tracking-tight">
                {t("web.admin.backups.list.heading")}
              </h2>
              <p className="text-muted-foreground text-[13px]">
                {retentionNote({ keep, plan, next, running, now, t })}
              </p>
              {list?.encrypted ? (
                <p className="text-muted-foreground text-[13px]">
                  {t("web.admin.backups.list.encrypted")}
                </p>
              ) : null}
            </div>
            <BackupTable
              items={items}
              selectedId={selectedId}
              showAll={rowsExpanded}
              onShowAllChange={(value) => {
                setShowAll(value);
                if (!value && selectedId === linkedId) setSelectedId(null);
              }}
              active={active}
              deleteBlocker={lock}
              selectiveRestore={selectiveRestore}
            />
          </div>
        </section>
      )}

      {uploads.data ? <UploadedBackups uploads={uploads.data} /> : null}

      {form.isLoading ? (
        <div
          className="space-y-4"
          role="status"
          aria-label={t("web.admin.backups.settings.loading")}
        >
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : form.loadError ? (
        <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
          <p>{t("web.admin.backups.settings.loadFailed")}</p>
          <Button variant="outline" size="sm" onClick={() => void form.retryLoad()}>
            {t("common.tryAgain")}
          </Button>
        </div>
      ) : (
        <div className="max-w-[70rem] space-y-5">
          <BackupScheduleSettings form={form} />
          <BackupLocationGroup
            location={location}
            s3Offered={s3Offered}
            destination={draftDestination}
            savedDestination={savedDestination}
            dirty={form.isDirty(BACKUP_DESTINATION_KEY)}
            s3Configured={s3Configured}
            onDestinationChange={(destination) =>
              destination === savedDestination
                ? form.resetValue(BACKUP_DESTINATION_KEY)
                : form.setValue(BACKUP_DESTINATION_KEY, destination)
            }
            onSetUpS3={() => setSettingUpS3(true)}
            onConfirmKey={needsKey ? () => setConfirmingKey(true) : undefined}
          />
          {configured ? (
            <RestoreCommandGroup
              command={command}
              source={commandSource}
              encrypted={commandBackup?.encrypted ?? false}
            />
          ) : null}
        </div>
      )}

      {uploadAvailable && capabilities.data ? (
        <UploadBackupDialog
          open={uploading}
          onOpenChange={setUploading}
          maxBytes={capabilities.data.max_upload_bytes}
        />
      ) : null}

      {list && s3Offered ? (
        <S3SetupDialog
          open={settingUpS3}
          onOpenChange={setSettingUpS3}
          // The dialog saved the destination itself; drop a staged choice.
          onSaved={() => form.resetValue(BACKUP_DESTINATION_KEY)}
          initial={s3FieldsFromSettings(form.getPersistedValue)}
          secretConfigured={form.sensitiveConfigured.includes(BACKUP_S3_KEYS.secretKey)}
          keyCheck={list.key_check}
        />
      ) : null}
      {list && needsKey ? (
        <ConfirmKeyDialog
          open={confirmingKey}
          onOpenChange={setConfirmingKey}
          keyCheck={list.key_check}
        />
      ) : null}

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
