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
import { useAdminBackupUploads } from "@/hooks/queries/admin/backupUploads";
import {
  useCancelRestore,
  useRestoreJob,
  useRestoreJobs,
  useRestoreSet,
  useStartRestore,
  type RestoreSource,
} from "@/hooks/queries/admin/backupRestore";
import { adminKeys } from "@/hooks/queries/keys";
import { useT, type MessageKey, type Translate } from "@/i18n";
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

const STEPS: readonly MessageKey[] = [
  "web.admin.backups.restore.steps.choose",
  "web.admin.backups.restore.steps.review",
  "web.admin.backups.restore.steps.restore",
];

function StepIndicator({ current }: { current: number }) {
  const t = useT();
  return (
    <nav aria-label={t("web.admin.backups.restore.steps.label")}>
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
              {t(label)}
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

/**
 * The page heading. A backup of this server is named by its date; an upload
 * by the server it came from, and its subtitle says it was uploaded.
 */
function restoreHeading(
  t: Translate,
  {
    taken,
    version,
    set,
    fromUpload,
  }: {
    taken: Date | null;
    version: string | undefined;
    set: { same_server: boolean; server_name: string; server_id: string } | undefined;
    fromUpload: boolean;
  },
): { title: string; subtitle: string | undefined } {
  if (!taken) return { title: t("web.admin.backups.restore.heading.default"), subtitle: undefined };
  const known = knownSiloVersion(version);
  const name = set ? sourceServerName(set) : null;
  // A server without a name is "another server"; its id is only a detail.
  const idDetail =
    set && !set.same_server && !name
      ? t("web.admin.backups.restore.heading.serverId", { id: shortServerId(set.server_id) })
      : null;
  const sentence = (parts: (string | null)[]) =>
    t("web.admin.backups.restore.heading.subtitle", { parts: parts.filter(Boolean).join(", ") });
  const time = formatTime(taken);
  const withVersion = known
    ? t("web.admin.backups.restore.heading.withVersion", { version: known })
    : null;
  if (fromUpload) {
    const origin = set?.same_server
      ? t("web.admin.backups.restore.heading.uploadedFromThis")
      : t("web.admin.backups.restore.heading.uploadedFromAnother");
    let source = "other";
    if (!name) source = set?.same_server ? "this" : "another";
    return {
      title: t("web.admin.backups.restore.heading.upload", {
        source,
        date: takenDate(taken),
        name: name ?? "",
      }),
      subtitle: sentence([
        origin,
        t("web.admin.backups.restore.heading.takenAt", { time }),
        withVersion,
        idDetail,
      ]),
    };
  }
  let where: string | null = null;
  if (set) {
    if (set.same_server) where = t("web.admin.backups.restore.heading.onThisServer");
    else
      where = name
        ? t("web.admin.backups.restore.heading.fromServer", { name })
        : t("web.admin.backups.restore.heading.fromAnother");
  }
  return {
    title: t("web.admin.backups.restore.heading.backup", { date: takenDate(taken) }),
    subtitle: sentence([
      t("web.admin.backups.restore.heading.takenAtFirst", { time }),
      withVersion,
      where,
      idDetail,
    ]),
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
 * `/admin/backups/:id/restore` and `/admin/backups/uploads/:uploadId/restore`:
 * choose, review, and run a selective restore from a backup of this server or
 * from an uploaded restore set.
 */
export default function AdminBackupRestore() {
  const t = useT();
  const { id: backupId = "", uploadId = "" } = useParams();
  const source: RestoreSource = uploadId
    ? { kind: "upload", id: uploadId }
    : { kind: "backup", id: backupId };
  const id = source.id;
  const fromUpload = source.kind === "upload";
  const [params, setParams] = useSearchParams();
  const jobId = params.get("job");
  const queryClient = useQueryClient();
  const capabilities = useAdminBackupCapabilities();
  const backups = useAdminBackups();
  const uploads = useAdminBackupUploads(fromUpload);
  const jobs = useRestoreJobs();
  const [openUnverified, setOpenUnverified] = useState(false);
  const preview = useRestoreSet(jobId ? null : source, openUnverified);
  const job = useRestoreJob(jobId);
  const start = useStartRestore(source);
  const cancel = useCancelRestore();
  const [reviewing, setReviewing] = useState(false);
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [dataTypes, setDataTypes] = useState<ReadonlySet<RestoreDataTypeId>>(DEFAULT_DATA_TYPES);
  const [refusal, setRefusal] = useState<RestoreRefusal | null>(null);
  const [acceptUnauthenticated, setAcceptUnauthenticated] = useState(false);

  const set = preview.data;
  const index = useMemo(() => (set ? indexItems(set) : new Map<string, IndexEntry>()), [set]);
  // A reloaded preview can make earlier choices unavailable; only what the
  // current preview still offers counts as chosen.
  const selection = useMemo(
    () => new Set([...chosen].filter((key) => entrySelectable(index.get(key)))),
    [chosen, index],
  );

  const list = backups.data;
  const backup = fromUpload ? undefined : list?.items.find((item) => item.id === id);
  const upload = fromUpload ? uploads.data?.find((item) => item.id === id) : undefined;
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
    return (fromUpload ? named?.upload_id : named?.backup_id) === id;
  };
  const runningJob = jobId
    ? undefined
    : jobs.data?.find((candidate) => !candidate.terminal && forThisBackup(candidate));
  if (runningJob) return <Navigate to={`?job=${encodeURIComponent(runningJob.id)}`} replace />;
  const lastJob = jobs.data?.find(forThisBackup);

  // The report step reads no preview; an upload's own record names its source then.
  const uploadSource = upload?.source;
  const origin =
    set ??
    (uploadSource
      ? { ...uploadSource, same_server: uploadSource.server_id === list?.server_id }
      : undefined);
  const takenAt = backup?.created_at ?? origin?.created_at;
  const taken = takenAt ? new Date(takenAt) : null;
  const version = set?.silo_version ?? backup?.silo_version ?? uploadSource?.silo_version;
  const { title, subtitle } = restoreHeading(t, { taken, version, set: origin, fromUpload });

  const step = jobId ? 2 : reviewing ? 1 : 0;

  const restore = () => {
    if (!set) return;
    setRefusal(null);
    start.mutate(
      {
        digest: set.digest,
        selection: selectionRequest(set, selection, dataTypes),
        acceptUnauthenticated: !set.authenticated && acceptUnauthenticated,
      },
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
        {t("web.admin.backups.restore.unavailable")}
      </p>
    );
  } else if (jobId) {
    body = job.isPending ? (
      <Skeleton className="h-40 w-full rounded-2xl" />
    ) : job.isError || !job.data ? (
      <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
        <p>
          {t("web.admin.backups.restore.jobLoadFailed", { reason: backupErrorMessage(job.error) })}
        </p>
        <Button variant="outline" size="sm" onClick={() => void job.refetch()}>
          {t("common.tryAgain")}
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
              toast.error(t("web.admin.backups.restore.stopFailed"), {
                description: backupErrorMessage(error),
              }),
          })
        }
        onChooseAgain={() => setParams({})}
      />
    );
  } else if (preview.isPending) {
    body = (
      <div className="space-y-4" role="status" aria-label={t("web.admin.backups.restore.reading")}>
        <Skeleton className="h-32 w-full rounded-xl" />
        <Skeleton className="h-32 w-full rounded-xl" />
      </div>
    );
  } else if (
    preview.error instanceof V2ProblemError &&
    preview.error.problemType === "backup_unauthenticated"
  ) {
    body = (
      <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
        <p className="font-medium">{t("web.admin.backups.restore.unverifiedTitle")}</p>
        <p className="text-muted-foreground text-sm">
          {t("web.admin.backups.restore.unverifiedBody")}
        </p>
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            setOpenUnverified(true);
            setAcceptUnauthenticated(true);
          }}
        >
          {t("web.admin.backups.restore.openAnyway")}
        </Button>
      </div>
    );
  } else if (preview.isError || !set) {
    body = (
      <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
        <p>
          {t(
            fromUpload
              ? "web.admin.backups.restore.openFailedUpload"
              : "web.admin.backups.restore.openFailed",
            { reason: backupErrorMessage(preview.error) },
          )}
        </p>
        <Button variant="outline" size="sm" onClick={() => void preview.refetch()}>
          {t("common.tryAgain")}
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
        acceptUnauthenticated={acceptUnauthenticated}
        onAcceptUnauthenticated={setAcceptUnauthenticated}
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
            {t(
              lastJob.finished_at
                ? fromUpload
                  ? "web.admin.backups.restore.lastRun.uploadAt"
                  : "web.admin.backups.restore.lastRun.backupAt"
                : fromUpload
                  ? "web.admin.backups.restore.lastRun.uploadEarlier"
                  : "web.admin.backups.restore.lastRun.backupEarlier",
              {
                date: lastJob.finished_at ? backupDateTime(lastJob.finished_at) : "",
                link: (chunks) => (
                  <Link
                    to={`?job=${encodeURIComponent(lastJob.id)}`}
                    className="text-foreground underline underline-offset-2"
                  >
                    {chunks}
                  </Link>
                ),
              },
            )}
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
        {t("web.admin.backups.restore.backLink")}
      </Link>
      <PageHeader title={title} subtitle={subtitle} className="mb-4" />
      <StepIndicator current={step} />
      {body}
    </div>
  );
}
