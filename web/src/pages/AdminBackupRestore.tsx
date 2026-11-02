import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowLeft, Check } from "lucide-react";
import { Link, Navigate, useParams, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  jobIdFromLocation,
  type RestoreDataTypeId,
  type RestoreJob,
} from "@/api/v2/adminBackupRestore";
import { V2ProblemError } from "@/api/v2/request";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminBackupCapabilities, useAdminBackups } from "@/hooks/queries/admin/backups";
import {
  useCancelRestore,
  useRestoreJob,
  useRestoreJobs,
  useRestoreSet,
  useStartRestore,
} from "@/hooks/queries/admin/backupRestore";
import { adminKeys } from "@/hooks/queries/keys";
import { formatTime, preferredDateLocale } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { RestoreChoose } from "@/pages/admin-backups/RestoreChoose";
import { RestoreReview, type RestoreRefusal } from "@/pages/admin-backups/RestoreReview";
import { RestoreRun } from "@/pages/admin-backups/RestoreRun";
import {
  backupDateTime,
  backupErrorMessage,
  knownSiloVersion,
  shortServerId,
  sourceServerName,
} from "@/pages/admin-backups/backupPresentation";
import {
  DEFAULT_DATA_TYPES,
  entrySelectable,
  indexItems,
  selectionRequest,
  type IndexEntry,
} from "@/pages/admin-backups/restoreModel";
// The settings layout loads these field styles for its pages; this page is not one of them.
import "@/styles/admin-settings.css";

const STEPS = ["Choose", "Review", "Restore"] as const;

function StepIndicator({ current }: { current: number }) {
  return (
    <nav aria-label="Restore steps">
      <ol className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        {STEPS.map((label, index) => {
          const done = index < current;
          const active = index === current;
          return (
            <li
              key={label}
              aria-current={active ? "step" : undefined}
              className={cn(
                "flex items-center gap-2",
                active ? "text-foreground font-medium" : "text-muted-foreground",
              )}
            >
              <span
                aria-hidden="true"
                className={cn(
                  "grid size-6 place-items-center rounded-full border text-xs tabular-nums",
                  active && "border-foreground",
                  done && "bg-foreground text-background border-foreground",
                )}
              >
                {done ? <Check className="size-3.5" strokeWidth={3} /> : index + 1}
              </span>
              {label}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

const takenDate = (taken: Date) =>
  taken.toLocaleDateString(preferredDateLocale(), {
    weekday: "long",
    month: "short",
    day: "numeric",
  });

/** The page heading: the backup's date, and where it was taken in the subtitle. */
function restoreHeading({
  taken,
  version,
  set,
}: {
  taken: Date | null;
  version: string | undefined;
  set: { same_server: boolean; server_name: string; server_id: string } | undefined;
}): { title: string; subtitle: string | undefined } {
  if (!taken) return { title: "Restore from a backup", subtitle: undefined };
  const known = knownSiloVersion(version);
  const name = set ? sourceServerName(set) : null;
  // A server without a name is "another server"; its id is only a detail.
  const idDetail =
    set && !set.same_server && !name ? `server ${shortServerId(set.server_id)}` : null;
  const sentence = (parts: (string | null)[]) => {
    const text = parts.filter(Boolean).join(", ");
    return `${text.charAt(0).toUpperCase()}${text.slice(1)}.`;
  };
  const time = `taken at ${formatTime(taken)}`;
  const withVersion = known ? `with Silo ${known}` : null;
  let where: string | null = null;
  if (set) where = set.same_server ? "on this server" : `from ${name ?? "another server"}`;
  return {
    title: `Restore from ${takenDate(taken)}`,
    subtitle: sentence([time, withVersion, where, idDetail]),
  };
}

function refusalOf(error: unknown): RestoreRefusal {
  if (error instanceof V2ProblemError) {
    return {
      message: error.message,
      runningJobId: error.status === 409 ? jobIdFromLocation(error.headers.get("Location")) : null,
      previewChanged: error.problemType === "restore_set_changed",
    };
  }
  return { message: backupErrorMessage(error), runningJobId: null, previewChanged: false };
}

/**
 * `/admin/backups/:id/restore`: choose, review, and run a selective restore
 * from a backup of this server.
 */
export default function AdminBackupRestore() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const jobId = params.get("job");
  const queryClient = useQueryClient();
  const capabilities = useAdminBackupCapabilities();
  const backups = useAdminBackups();
  const jobs = useRestoreJobs();
  const preview = useRestoreSet(jobId ? null : id);
  const job = useRestoreJob(jobId);
  const start = useStartRestore(id);
  const cancel = useCancelRestore();
  const [reviewing, setReviewing] = useState(false);
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [dataTypes, setDataTypes] = useState<ReadonlySet<RestoreDataTypeId>>(DEFAULT_DATA_TYPES);
  const [refusal, setRefusal] = useState<RestoreRefusal | null>(null);

  const set = preview.data;
  const index = useMemo(() => (set ? indexItems(set) : new Map<string, IndexEntry>()), [set]);
  // A reloaded preview can make earlier choices unavailable; only what the
  // current preview still offers counts as chosen.
  const selection = useMemo(
    () => new Set([...chosen].filter((key) => entrySelectable(index.get(key)))),
    [chosen, index],
  );

  const list = backups.data;
  const backup = list?.items.find((item) => item.id === id);
  const active = list?.active;
  const terminal = job.data?.terminal ?? false;

  // The report names the recovery point a restore takes first; read the list
  // again once the job ends so that copy is in it.
  useEffect(() => {
    if (terminal) void queryClient.invalidateQueries({ queryKey: adminKeys.backups() });
  }, [queryClient, terminal]);

  // A queued or running restore of this source without its job in the
  // address (a reload, another admin) is shown instead of the chooser.
  const forThisBackup = (candidate: RestoreJob) => {
    const named = candidate.backup_restore_source ?? candidate.backup_restore_result;
    return named?.backup_id === id;
  };
  const runningJob = jobId
    ? undefined
    : jobs.data?.find((candidate) => !candidate.terminal && forThisBackup(candidate));
  if (runningJob) return <Navigate to={`?job=${encodeURIComponent(runningJob.id)}`} replace />;
  const lastJob = jobs.data?.find(forThisBackup);

  const takenAt = backup?.created_at ?? set?.created_at;
  const taken = takenAt ? new Date(takenAt) : null;
  const version = set?.silo_version ?? backup?.silo_version;
  const { title, subtitle } = restoreHeading({ taken, version, set });

  const step = jobId ? 2 : reviewing ? 1 : 0;

  const restore = () => {
    if (!set) return;
    setRefusal(null);
    start.mutate(
      { digest: set.digest, selection: selectionRequest(set, selection, dataTypes) },
      {
        onSuccess: (started) => {
          setReviewing(false);
          setParams({ job: started.id });
        },
        onError: (error) => setRefusal(refusalOf(error)),
      },
    );
  };

  const leaveReview = () => {
    setRefusal(null);
    setReviewing(false);
  };

  const reloadPreview = () => {
    leaveReview();
    void preview.refetch();
  };

  let body: ReactNode;
  if (capabilities.data && !capabilities.data.selective_restore) {
    body = (
      <p role="alert" className="surface-panel rounded-2xl p-6">
        Restoring parts of a backup isn&apos;t available on this server.
      </p>
    );
  } else if (jobId) {
    body = job.isPending ? (
      <Skeleton className="h-40 w-full rounded-2xl" />
    ) : job.isError || !job.data ? (
      <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
        <p>Couldn&apos;t read the restore. {backupErrorMessage(job.error)}</p>
        <Button variant="outline" size="sm" onClick={() => void job.refetch()}>
          Try again
        </Button>
      </div>
    ) : (
      <RestoreRun
        job={job.data}
        backups={list?.items ?? []}
        active={active}
        cancelPending={cancel.isPending}
        onCancel={() =>
          cancel.mutate(job.data.id, {
            onError: (error) =>
              toast.error("Couldn't stop the restore", {
                description: backupErrorMessage(error),
              }),
          })
        }
        onChooseAgain={() => setParams({})}
      />
    );
  } else if (preview.isPending) {
    body = (
      <div className="space-y-4" role="status" aria-label="Reading the backup">
        <Skeleton className="h-32 w-full rounded-xl" />
        <Skeleton className="h-32 w-full rounded-xl" />
      </div>
    );
  } else if (preview.isError || !set) {
    body = (
      <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
        <p>Couldn&apos;t open this backup for restoring. {backupErrorMessage(preview.error)}</p>
        <Button variant="outline" size="sm" onClick={() => void preview.refetch()}>
          Try again
        </Button>
      </div>
    );
  } else if (reviewing) {
    body = (
      <RestoreReview
        set={set}
        selection={selection}
        dataTypes={dataTypes}
        onBack={leaveReview}
        onRestore={restore}
        pending={start.isPending}
        refusal={refusal}
        onOpenRunning={(running) => {
          leaveReview();
          setParams({ job: running });
        }}
        onReloadPreview={reloadPreview}
      />
    );
  } else {
    body = (
      <div className="space-y-5">
        {lastJob ? (
          <p className="text-muted-foreground text-sm">
            A restore from this backup ran{" "}
            {lastJob.finished_at ? backupDateTime(lastJob.finished_at) : "earlier"}.{" "}
            <Link
              to={`?job=${encodeURIComponent(lastJob.id)}`}
              className="text-foreground underline underline-offset-2"
            >
              See its report
            </Link>
          </p>
        ) : null}
        <RestoreChoose
          set={set}
          selection={selection}
          onSelectionChange={setChosen}
          dataTypes={dataTypes}
          onDataTypesChange={setDataTypes}
          onReview={() => setReviewing(true)}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6 pb-16">
      <Link
        to="/admin/backups"
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-sm"
      >
        <ArrowLeft aria-hidden="true" className="size-4" />
        Backups
      </Link>
      <PageHeader title={title} subtitle={subtitle} className="mb-4" />
      <StepIndicator current={step} />
      {body}
    </div>
  );
}
