import { useEffect, useId, useMemo, useRef, useState } from "react";
import { DatabaseBackup, LoaderCircle } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";

import { toast } from "sonner";

import type { AdminBackupActive } from "@/api/v2/adminBackups";
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
import { useTask } from "@/hooks/queries/admin/tasks";
import { adminKeys } from "@/hooks/queries/keys";
import { formatTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import {
  BackupLocationGroup,
  BackupScheduleSettings,
  RestoreCommandGroup,
  type FullRestoreSource,
} from "@/pages/admin-backups/BackupSettings";
import { BackupTable } from "@/pages/admin-backups/BackupTable";
import {
  BACKUP_ENABLED_KEY,
  BACKUP_KEEP_KEY,
  BACKUP_SETTING_KEYS,
  backUpNowBlocker,
  backupErrorMessage,
  backupSchedulePhrases,
  isFinished,
  lastRunProblem,
  lockBlocker,
  nextBackupRun,
  restoreCommand,
  retentionNote,
  retentionPlan,
  visibleWarnings,
  type BackupLocation,
} from "@/pages/admin-backups/backupPresentation";
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

/** What the operation holding the backup lock is doing. */
function ActiveOperation({ active }: { active: AdminBackupActive }) {
  switch (active.operation) {
    case "backup":
      return `Backing up, started at ${formatTime(active.started_at)}.`;
    default:
      return "Deleting a backup.";
  }
}

export default function AdminBackups() {
  const now = useMinuteClock();
  const queryClient = useQueryClient();
  const capabilities = useAdminBackupCapabilities();
  const backups = useAdminBackups();
  const backupTask = useTask("backup");
  const form = useSettingsForm({ keys: BACKUP_SETTING_KEYS });
  const startBackup = useStartAdminBackup();
  const [showAll, setShowAll] = useState(false);
  // The backup this page started, and when, so its outcome is read only from
  // a list fetched after the start.
  const started = useRef<{ id: string; at: number } | null>(null);
  const blockerId = useId();

  const list = backups.data;
  const items = useMemo(() => list?.items ?? [], [list]);
  const warnings = list?.warnings ?? [];
  const running = backupRunning(list);
  const shownWarnings = visibleWarnings(warnings, {
    backingUp: running,
    neverBackedUp: (list?.items.length ?? 0) === 0 && !list?.last_success_at,
  });
  const active = list?.active;
  const busy = Boolean(list?.busy || active || running);
  const sqliteUserStore = warnings.some((warning) => warning.code === "sqlite_user_store");
  const configured = capabilities.data ? capabilities.data.state === "available" : true;
  const destinations = capabilities.data?.destinations;

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

  // Before capabilities load, assume the server has its own storage.
  const location: BackupLocation =
    destinations && !destinations.includes("default") ? "none" : "local";
  const commandBackup = items.find(isFinished);
  const command = restoreCommand(commandBackup?.location);
  const commandSource: FullRestoreSource = commandBackup ? "folder" : "download";

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
    if (lastRun.status === "complete") toast.success("Backup finished");
    else if (lastRun.status === "complete_with_warnings")
      toast.success("Backup finished with warnings", {
        description: "Some files were missing. The table lists them.",
      });
    else toast.error("The backup didn't finish", { description: lastRun.error });
  }, [busy, lastRun, listUpdatedAt]);

  const backUpNow = () =>
    startBackup.mutate(undefined, {
      onSuccess: (result) => {
        started.current = { id: result.id, at: Date.now() };
        toast.success("Backup started", {
          description: "It keeps running if you leave this page.",
        });
      },
      onError: (error) =>
        toast.error("Couldn't start a backup", {
          description: backupErrorMessage(error, { sqliteUserStore }),
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
      Back up now
    </Button>
  );

  return (
    <div className="space-y-8 pb-24">
      <UnsavedChangesGuard />
      <div>
        <PageHeader
          title="Backups"
          subtitle="Every night Silo saves its database and the files people uploaded."
          className="mb-4"
          actions={
            <div className="flex flex-col items-end gap-1">
              <div className="flex flex-wrap justify-end gap-2">{backUpNowButton}</div>
              {blocker ? (
                <p id={blockerId} className="text-muted-foreground max-w-72 text-right text-xs">
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
                      {Math.round(active.progress)}%
                    </span>
                  ) : null}
                </div>
                {active.operation === "backup" ? (
                  <Progress value={active.progress} aria-label="Backup progress" />
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
              <div key={warning.code}>
                <SettingFieldStatus tone="warn">{warning.message}</SettingFieldStatus>
              </div>
            ))}
          </div>
        ) : null}
      </div>

      {backups.isPending ? (
        <div className="space-y-4" role="status" aria-label="Loading backups">
          <Skeleton className="h-28 w-full rounded-xl" />
          <Skeleton className="h-48 w-full rounded-2xl" />
        </div>
      ) : backups.isError ? (
        <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
          <p>Couldn&apos;t read the backups. {backupErrorMessage(backups.error)}</p>
          <Button variant="outline" size="sm" onClick={() => void backups.refetch()}>
            Try again
          </Button>
        </div>
      ) : items.length === 0 && running ? (
        <div className="surface-panel rounded-2xl p-6" role="status">
          <p className="font-medium">Taking the first backup.</p>
          <p className="text-muted-foreground text-sm">It shows here when it finishes.</p>
        </div>
      ) : items.length === 0 ? (
        <div className="surface-panel space-y-3 rounded-2xl p-6">
          <p className="font-medium">No backups yet.</p>
          <p className="text-muted-foreground text-sm">
            {configured
              ? "Take the first one now instead of waiting for the schedule."
              : "This server has nowhere to keep them yet. Where backups are kept, below, says what to set up."}
          </p>
          {configured ? backUpNowButton : null}
        </div>
      ) : (
        <section aria-labelledby="saved-backups-heading" className="space-y-6">
          <div className="space-y-3">
            <div className="space-y-1">
              <h2 id="saved-backups-heading" className="text-[15px] font-semibold tracking-tight">
                Saved backups
              </h2>
              <p className="text-muted-foreground text-[13px]">
                {retentionNote({ keep, plan, next, running, now })}
              </p>
            </div>
            <BackupTable
              items={items}
              showAll={showAll}
              onShowAllChange={setShowAll}
              sqliteUserStore={sqliteUserStore}
              active={active}
              deleteBlocker={lock}
            />
          </div>
        </section>
      )}

      {form.isLoading ? (
        <div className="space-y-4" role="status" aria-label="Loading settings">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : form.loadError ? (
        <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
          <p>Couldn&apos;t read the backup settings. Check that Silo is running, then try again.</p>
          <Button variant="outline" size="sm" onClick={() => void form.retryLoad()}>
            Try again
          </Button>
        </div>
      ) : (
        <div className="max-w-[70rem] space-y-5">
          <BackupScheduleSettings form={form} />
          <BackupLocationGroup location={location} />
          {configured ? <RestoreCommandGroup command={command} source={commandSource} /> : null}
        </div>
      )}

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
