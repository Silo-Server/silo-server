import { useMemo, useState } from "react";
import { Link } from "react-router";
import { Loader2, Play, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import type { MarkerProviderConfig, TaskInfo } from "@/api/types";
import { TaskStatusBadge } from "@/components/admin/TaskStatusBadge";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useTasks, useRunTask } from "@/hooks/queries/admin/tasks";
import {
  useMarkerProviders,
  useUpdateMarkerProvider,
  useValidateMarkerProvider,
} from "@/hooks/queries/admin/markers";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";

const INTRO_SETTING_KEYS = ["markers.mode", "markers.lazy_playback"];
const INTEGER_INPUT_PATTERN = /^[+-]?\d+$/;

function formatRate(value: number) {
  return `${Math.round(value * 100)}%`;
}

function ProviderSettingsCard() {
  const providers = useMarkerProviders();

  if (providers.isLoading) {
    return (
      <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
        <Skeleton className="h-5 w-40" />
        <div className="mt-4 space-y-3">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      </div>
    );
  }

  const providerList = providers.data?.providers ?? [];
  if (providerList.length === 0) {
    return (
      <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
        <h3 className="text-sm font-semibold">Marker Providers</h3>
        <p className="text-muted-foreground mt-2 text-sm">
          No marker provider plugins are installed or enabled.
        </p>
      </div>
    );
  }

  return (
    <div className="max-w-2xl space-y-4">
      {providerList.map((provider) => (
        <ProviderSettingsForm
          key={[
            provider.provider,
            provider.fetch_enabled,
            provider.fetch_priority,
            provider.contribute_enabled,
            provider.contribute_auto_local,
            provider.contribute_min_confidence,
            provider.is_submitter,
          ].join(":")}
          provider={provider}
        />
      ))}
    </div>
  );
}

function ProviderSettingsForm({ provider }: { provider: MarkerProviderConfig }) {
  const updateProvider = useUpdateMarkerProvider();
  const validateProvider = useValidateMarkerProvider();
  const displayName = provider.display_name || provider.provider;
  const minConfidenceID = `marker-min-confidence-${provider.provider.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  const priorityID = `marker-priority-${provider.provider.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  const [fetchEnabled, setFetchEnabled] = useState(provider.fetch_enabled);
  const [fetchPriority, setFetchPriority] = useState(String(provider.fetch_priority));
  const [contributeEnabled, setContributeEnabled] = useState(provider.contribute_enabled);
  const [autoLocal, setAutoLocal] = useState(provider.contribute_auto_local);
  const [minConfidence, setMinConfidence] = useState(
    String(provider.contribute_min_confidence ?? 0.95),
  );

  const parsedMinConfidence = Number.parseFloat(minConfidence);
  const confidenceValid =
    Number.isFinite(parsedMinConfidence) && parsedMinConfidence >= 0 && parsedMinConfidence <= 1;
  const fetchPriorityInput = fetchPriority.trim();
  const parsedFetchPriority = Number(fetchPriorityInput);
  const priorityValid =
    INTEGER_INPUT_PATTERN.test(fetchPriorityInput) && Number.isInteger(parsedFetchPriority);
  const dirty =
    provider.fetch_enabled !== fetchEnabled ||
    provider.fetch_priority !== parsedFetchPriority ||
    provider.contribute_enabled !== contributeEnabled ||
    provider.contribute_auto_local !== autoLocal ||
    provider.contribute_min_confidence !== parsedMinConfidence;

  function save() {
    if (!priorityValid) {
      toast.error("Fetch priority must be a whole number.");
      return;
    }
    if (!confidenceValid) {
      toast.error("Minimum confidence must be between 0 and 1.");
      return;
    }

    updateProvider.mutate({
      provider: provider.provider,
      patch: {
        fetch_enabled: fetchEnabled,
        fetch_priority: parsedFetchPriority,
        contribute_enabled: contributeEnabled,
        contribute_auto_local: contributeEnabled && autoLocal,
        contribute_min_confidence: parsedMinConfidence,
      },
    });
  }

  function validate() {
    validateProvider.mutate({ provider: provider.provider, displayName });
  }

  const validation = validateProvider.data;

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex flex-col gap-1">
        <h3 className="text-sm font-semibold">{displayName}</h3>
        <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
          Controls online marker lookup and whether locally generated markers can be submitted.
        </p>
        <p className="text-muted-foreground text-xs">
          {provider.source_type === "plugin" && provider.plugin_id
            ? `Plugin ${provider.plugin_id} / ${provider.capability_id || provider.provider}`
            : provider.provider}
        </p>
      </div>

      <div className="divide-border divide-y">
        <SettingField
          label="Use for Online Marker Lookup"
          type="toggle"
          value={fetchEnabled ? "true" : "false"}
          onChange={(value) => setFetchEnabled(value === "true")}
        />
        <div className="space-y-1 py-2">
          <Label htmlFor={priorityID}>Fetch Priority</Label>
          <Input
            id={priorityID}
            type="number"
            value={fetchPriority}
            step={1}
            onChange={(event) => setFetchPriority(event.target.value)}
            className="w-full sm:w-40"
            aria-invalid={!priorityValid}
          />
          <p className="text-muted-foreground text-xs">Lower numbers win when providers overlap.</p>
        </div>
        <SettingField
          label="Allow Contributions"
          type="toggle"
          value={contributeEnabled ? "true" : "false"}
          onChange={(value) => {
            const next = value === "true";
            setContributeEnabled(next);
            if (!next) setAutoLocal(false);
          }}
          disabled={!provider.is_submitter}
        />
        <SettingField
          label="Auto-submit Local Markers"
          type="toggle"
          value={autoLocal ? "true" : "false"}
          onChange={(value) => setAutoLocal(value === "true")}
          disabled={!provider.is_submitter || !contributeEnabled}
          hint="Scheduled contribution only sends scanner markers that meet the confidence floor."
        />
        <div className="space-y-1 py-2">
          <Label htmlFor={minConfidenceID}>Minimum Confidence</Label>
          <Input
            id={minConfidenceID}
            type="number"
            value={minConfidence}
            min={0}
            max={1}
            step={0.01}
            onChange={(event) => setMinConfidence(event.target.value)}
            className="w-full sm:w-40"
            aria-invalid={!confidenceValid}
            disabled={!provider.is_submitter}
          />
          <p className="text-muted-foreground text-xs">
            Use a decimal from 0 to 1. The default recommendation is 0.95.
          </p>
        </div>
      </div>

      {validation && (
        <div className="border-border bg-muted/20 mt-4 rounded-md border px-3 py-3">
          {validation.valid && validation.stats ? (
            <dl className="grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2">
              <div className="flex justify-between gap-3">
                <dt className="text-muted-foreground">Total submissions</dt>
                <dd className="font-medium">{validation.stats.total}</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-muted-foreground">Accepted</dt>
                <dd className="font-medium">{validation.stats.accepted}</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-muted-foreground">Pending</dt>
                <dd className="font-medium">{validation.stats.pending}</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-muted-foreground">Rejected</dt>
                <dd className="font-medium">{validation.stats.rejected}</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-muted-foreground">Acceptance rate</dt>
                <dd className="font-medium">{formatRate(validation.stats.acceptance_rate)}</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-muted-foreground">Best streak</dt>
                <dd className="font-medium">{validation.stats.best_streak}</dd>
              </div>
            </dl>
          ) : (
            <p className="text-destructive text-xs">{validation.error || "Validation failed."}</p>
          )}
        </div>
      )}

      <div className="mt-4 flex flex-col justify-end gap-2 sm:flex-row">
        {provider.is_submitter && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={validate}
            disabled={validateProvider.isPending}
          >
            {validateProvider.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <RefreshCw className="h-3.5 w-3.5" />
            )}
            Validate
          </Button>
        )}
        <Button
          type="button"
          size="sm"
          onClick={save}
          disabled={!dirty || !priorityValid || !confidenceValid || updateProvider.isPending}
        >
          {updateProvider.isPending ? "Saving..." : "Save Provider Settings"}
        </Button>
      </div>
    </div>
  );
}

function numberFromResultData(data: Record<string, unknown> | undefined, key: string) {
  const value = data?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function formatTaskResult(task: TaskInfo | undefined) {
  const result = task?.last_execution;
  if (!result) return "Never run";

  const data = result.result_data;
  if (task.key === "contribute_markers") {
    const submitted = numberFromResultData(data, "submitted");
    const skipped = numberFromResultData(data, "skipped");
    const failed = numberFromResultData(data, "failed");
    const retryAfter = numberFromResultData(data, "retry_after_seconds");
    const parts = [
      submitted != null ? `${submitted} submitted` : null,
      skipped != null ? `${skipped} skipped` : null,
      failed != null ? `${failed} failed` : null,
      retryAfter != null ? `retry after ${retryAfter}s` : null,
    ].filter(Boolean);
    if (parts.length > 0) return parts.join(", ");
  }

  return new Date(result.completed_at).toLocaleString();
}

function TaskActionRow({
  task,
  fallbackName,
  fallbackDescription,
  onRun,
  pending,
}: {
  task: TaskInfo | undefined;
  fallbackName: string;
  fallbackDescription: string;
  onRun: () => void;
  pending: boolean;
}) {
  const key = task?.key;
  const running = task?.state === "running" || task?.state === "cancelling";

  return (
    <div className="border-border flex flex-col gap-3 border-b py-4 last:border-b-0 sm:flex-row sm:items-center">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-sm font-semibold">{task?.name ?? fallbackName}</h3>
          {task?.last_execution && <TaskStatusBadge result={task.last_execution} />}
        </div>
        <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
          {task?.description ?? fallbackDescription}
        </p>
        <p className="text-muted-foreground mt-1 text-xs">Last result: {formatTaskResult(task)}</p>
      </div>

      <div className="flex flex-col gap-2 sm:flex-row">
        {key && (
          <Button variant="outline" size="sm" asChild>
            <Link to={`/admin/tasks/${key}`}>History</Link>
          </Button>
        )}
        <Button type="button" size="sm" onClick={onRun} disabled={!task || running || pending}>
          {pending ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Play className="h-3.5 w-3.5" />
          )}
          {running ? "Running" : "Run Now"}
        </Button>
      </div>
    </div>
  );
}

function IntroTasksCard() {
  useEventChannel("tasks");
  const { data: tasks } = useTasks();
  const runTask = useRunTask();
  const [pendingTask, setPendingTask] = useState<string | null>(null);

  const detectTask = tasks?.find((task) => task.key === "detect_intro_markers");
  const contributeTask = tasks?.find((task) => task.key === "contribute_markers");

  async function run(key: string) {
    setPendingTask(key);
    try {
      await runTask.mutateAsync(key);
    } finally {
      setPendingTask(null);
    }
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-1">
      <TaskActionRow
        task={detectTask}
        fallbackName="Populate Markers"
        fallbackDescription="Populates intro and credits markers for opted-in libraries."
        onRun={() => void run("detect_intro_markers")}
        pending={pendingTask === "detect_intro_markers"}
      />
      <TaskActionRow
        task={contributeTask}
        fallbackName="Contribute Markers"
        fallbackDescription="Submits high-confidence local intro markers to enabled providers."
        onRun={() => void run("contribute_markers")}
        pending={pendingTask === "contribute_markers"}
      />
    </div>
  );
}

export default function IntroSettings() {
  const form = useSettingsForm({ keys: useMemo(() => INTRO_SETTING_KEYS, []) });

  if (form.isLoading) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading intro settings">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-24 w-full max-w-2xl" />
        <Skeleton className="h-40 w-full max-w-2xl" />
        <span className="sr-only">Loading intro settings</span>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Intro Markers</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure marker lookup, local marker generation, and provider contribution.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Marker Lookup">
          <SettingField
            label="Mode"
            type="select"
            options={[
              { value: "off", label: "Off" },
              { value: "local", label: "Local" },
              { value: "both", label: "Local + Online" },
              { value: "online", label: "Online Only" },
            ]}
            value={form.getValue("markers.mode") || "local"}
            onChange={(value) => form.setValue("markers.mode", value)}
          />
          <SettingField
            label="Fetch Markers at Playback if Missing"
            type="toggle"
            value={form.getValue("markers.lazy_playback") || "false"}
            onChange={(value) => form.setValue("markers.lazy_playback", value)}
          />
        </FieldGroup>

        <ProviderSettingsCard />
        <IntroTasksCard />
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}
