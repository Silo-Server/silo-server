import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { AlertTriangle, ChevronDown, ChevronRight, Play, Loader2, RotateCcw } from "lucide-react";
import { toast } from "sonner";
import { Skeleton } from "@/components/ui/skeleton";
import type { AdminSettingsConnectionCheckRequest, ConnectionCheckResponse } from "@/api/types";
import type { components } from "@/api/v2/schema";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatRelativeTime } from "@/lib/date";
import {
  useAdminServerSettings,
  useCheckAdminSettingsConnection,
  useUpdateServerSettings,
  useAdminSensitiveStatus,
} from "@/hooks/queries/admin/settings";
import {
  useRecommendationsStatus,
  useTriggerEmbeddings,
  useTriggerTasteProfiles,
  useTriggerRecommendations,
  useTriggerCowatch,
  useResetEmbeddings,
} from "@/hooks/queries/admin/recommendations";
import {
  buildRecommendationSections,
  getAllRecommendationFields,
  parseRecommendationEmbeddingLock,
  type RecFieldDef,
} from "@/pages/admin-settings/recommendationsSettings";
import {
  RECOMMENDATION_PROVIDER_PRESETS,
  matchRecommendationProviderPreset,
  type RecommendationProviderPreset,
} from "@/lib/recommendation-provider-presets";

type RecJobRun = components["schemas"]["AdminRecommendationJobRun"];

interface RecLocalValues {
  [key: string]: string;
}

interface RecCollapsedState {
  [title: string]: boolean;
}

interface RecSettingFieldProps {
  field: RecFieldDef;
  serverValue: string;
  localValues: RecLocalValues;
  sensitiveConfigured: string[];
  isPending: boolean;
  onLocalChange: (key: string, value: string) => void;
  onCommit: (key: string, value: string) => void;
  onToggle: (key: string, checked: boolean) => void;
}

const EMBEDDING_SECTION_TITLE = "Embedding Configuration";
const EMBEDDING_BASE_URL_KEY = "recommendations.embedding_base_url";
const EMBEDDING_MODEL_KEY = "recommendations.embedding_model";
const EMBEDDING_AUTH_TOKEN_KEY = "recommendations.embedding_auth_token";
const EMBEDDING_CHECK_KEYS = [
  "recommendations.enabled",
  EMBEDDING_BASE_URL_KEY,
  EMBEDDING_MODEL_KEY,
  EMBEDDING_AUTH_TOKEN_KEY,
] as const;

function RecSettingField({
  field,
  serverValue,
  localValues,
  sensitiveConfigured,
  isPending,
  onLocalChange,
  onCommit,
  onToggle,
}: RecSettingFieldProps) {
  const { key, label, type, hint, defaultValue } = field;
  const effectiveServerValue = serverValue || defaultValue || "";
  const [confirmClear, setConfirmClear] = useState(false);

  if (type === "toggle") {
    const checked = serverValue === "true";
    return (
      <div className="flex items-center justify-between py-3">
        <div className="space-y-0.5">
          <Label htmlFor={key} className="text-sm font-medium">
            {label}
          </Label>
          {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
        </div>
        <Switch
          id={key}
          checked={checked}
          onCheckedChange={(val) => onToggle(key, val)}
          disabled={isPending}
        />
      </div>
    );
  }

  if (type === "password") {
    const isConfigured = sensitiveConfigured.includes(key);
    const localVal = localValues[key] ?? "";
    const placeholder = isConfigured ? "configured" : (hint ?? "Not configured");
    return (
      <div className="space-y-1 py-2">
        <Label htmlFor={key} className="text-sm font-medium">
          {label}
        </Label>
        <Input
          id={key}
          type="password"
          placeholder={placeholder}
          value={localVal}
          onChange={(e) => onLocalChange(key, e.target.value)}
          onBlur={() => {
            if (localVal !== "") {
              onCommit(key, localVal);
            }
          }}
          disabled={isPending}
          className="max-w-md"
        />
        {isConfigured && !confirmClear && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => setConfirmClear(true)}
            disabled={isPending}
          >
            Clear credential
          </Button>
        )}
        {confirmClear && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-muted-foreground text-xs">
              Remove this credential from the server?
            </span>
            <Button
              type="button"
              size="sm"
              variant="destructive"
              onClick={() => {
                onLocalChange(key, "");
                onCommit(key, "");
                setConfirmClear(false);
              }}
              disabled={isPending}
            >
              Confirm clear
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => setConfirmClear(false)}
              disabled={isPending}
            >
              Cancel
            </Button>
          </div>
        )}
        {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
      </div>
    );
  }

  if (type === "number") {
    const localVal = localValues[key] ?? effectiveServerValue;
    return (
      <div className="space-y-1 py-2">
        <Label htmlFor={key} className="text-sm font-medium">
          {label}
        </Label>
        <Input
          id={key}
          type="number"
          value={localVal}
          onChange={(e) => onLocalChange(key, e.target.value)}
          onBlur={() => {
            if (localVal !== effectiveServerValue) {
              onCommit(key, localVal);
            }
          }}
          disabled={isPending}
          className="w-40"
        />
        {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
      </div>
    );
  }

  const localVal = localValues[key] ?? effectiveServerValue;
  return (
    <div className="space-y-1 py-2">
      <Label htmlFor={key} className="text-sm font-medium">
        {label}
      </Label>
      <Input
        id={key}
        type="text"
        value={localVal}
        onChange={(e) => onLocalChange(key, e.target.value)}
        onBlur={() => {
          if (localVal !== effectiveServerValue) {
            onCommit(key, localVal);
          }
        }}
        disabled={isPending}
        className="max-w-md"
        placeholder={hint}
      />
      {hint && type === "duration" && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  );
}

function RecJobLastRun({ run }: { run?: RecJobRun }) {
  if (!run) {
    return <p className="text-muted-foreground text-xs">No finished run recorded</p>;
  }
  const when = formatRelativeTime(run.completed_at) ?? run.completed_at;
  if (run.status === "failed") {
    return (
      <div className="space-y-0.5">
        <p className="text-destructive text-xs font-medium">Last run failed {when}</p>
        {run.error && (
          <p className="text-muted-foreground line-clamp-3 text-xs break-words" title={run.error}>
            {run.error}
          </p>
        )}
      </div>
    );
  }
  return <p className="text-muted-foreground text-xs">Last run completed {when}</p>;
}

function RecJobStatusCard({
  title,
  count,
  total,
  running,
  lastRun,
  note,
  onTrigger,
  triggerPending,
}: {
  title: string;
  count: number;
  total?: number;
  running: boolean;
  lastRun?: RecJobRun;
  note?: string;
  onTrigger: () => void;
  triggerPending: boolean;
}) {
  const hasProgress = total !== undefined && total > 0;
  const pct = hasProgress ? Math.round((count / total) * 100) : 0;
  const isDisabled = running || triggerPending;

  return (
    <div className="surface-panel min-w-0 rounded-2xl border-0 px-4 py-4 sm:px-5">
      <div className="flex min-w-0 flex-col gap-3">
        <div className="min-w-0 space-y-1">
          <span className="text-sm font-semibold">{title}</span>
          <p className="text-muted-foreground text-xs">
            {hasProgress
              ? `${count.toLocaleString()} / ${total.toLocaleString()} items`
              : `${count.toLocaleString()} ${count === 1 ? "entry" : "entries"}`}
          </p>
          {note && <p className="text-muted-foreground text-xs">{note}</p>}
          <RecJobLastRun run={lastRun} />
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={onTrigger}
          disabled={isDisabled}
          className="w-full justify-center"
        >
          {running ? (
            <>
              <Loader2 className="size-3.5 animate-spin" />
              Running...
            </>
          ) : (
            <>
              <Play className="size-3.5" />
              Run
            </>
          )}
        </Button>
      </div>

      {hasProgress && (
        <div className="mt-3">
          <div className="bg-muted h-2 w-full overflow-hidden rounded-full">
            <div
              className="bg-primary h-full rounded-full transition-all duration-500"
              style={{ width: `${pct}%` }}
            />
          </div>
          <p className="text-muted-foreground mt-1 text-right text-xs">{pct}%</p>
        </div>
      )}
    </div>
  );
}

function RecEmbeddingLockCard({
  lock,
}: {
  lock: ReturnType<typeof parseRecommendationEmbeddingLock>;
}) {
  if (!lock) {
    return null;
  }

  return (
    <div className="surface-panel max-w-3xl rounded-2xl border-0 px-5 py-4">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <h2 className="text-sm font-semibold">Embedding Lock</h2>
          <p className="text-muted-foreground text-sm">
            This installation is locked to the embedding space of its first stored embedding.
          </p>
        </div>
        <Badge variant="outline" className="shrink-0">
          Locked
        </Badge>
      </div>

      <dl className="mt-4 grid gap-4 sm:grid-cols-3">
        <div className="space-y-1">
          <dt className="text-muted-foreground text-xs tracking-wide uppercase">Model</dt>
          <dd className="text-sm font-medium">{lock.model}</dd>
        </div>
        <div className="space-y-1">
          <dt className="text-muted-foreground text-xs tracking-wide uppercase">
            Source dimensions
          </dt>
          <dd className="text-sm font-medium">{lock.sourceDimensions}</dd>
        </div>
        <div className="space-y-1">
          <dt className="text-muted-foreground text-xs tracking-wide uppercase">
            Storage dimensions
          </dt>
          <dd className="text-sm font-medium">{lock.storageDimensions}</dd>
        </div>
      </dl>

      <p className="text-muted-foreground mt-4 text-xs">{lock.note}</p>
    </div>
  );
}

function RecEmbeddingResetCard({
  lockConflict,
  disabled,
  isPending,
  onReset,
}: {
  lockConflict: string;
  disabled: boolean;
  isPending: boolean;
  onReset: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="surface-panel max-w-3xl space-y-3 rounded-2xl border-0 px-5 py-4">
      {lockConflict && (
        <div
          role="alert"
          className="border-warning/30 bg-warning/10 text-warning flex items-start gap-3 rounded-xl border px-4 py-3 text-sm"
        >
          <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0" />
          <span>{lockConflict}</span>
        </div>
      )}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <h2 className="text-sm font-semibold">Reset Embeddings</h2>
          <p className="text-muted-foreground text-sm">
            Deletes the embedding lock, every item embedding and personal recommendation rows, and
            clears taste profiles, so you can switch embedding models. Run the embedding job
            afterwards; taste profiles rebuild as their titles are embedded again.
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          className="text-destructive shrink-0"
          onClick={() => setConfirming(true)}
          disabled={disabled || isPending}
        >
          {isPending ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <RotateCcw className="size-3.5" />
          )}
          Reset embeddings
        </Button>
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Reset embeddings?"
        description="Every item embedding and personal recommendation row is deleted, and taste profiles are cleared. Personal recommendations and semantic search stay unavailable until the embedding job has run again."
        confirmLabel="Reset embeddings"
        variant="destructive"
        isPending={isPending}
        onConfirm={() => {
          setConfirming(false);
          onReset();
        }}
      />
    </div>
  );
}

export default function AdminRecommendations() {
  const { data: settings, isLoading } = useAdminServerSettings();
  const { data: sensitiveData } = useAdminSensitiveStatus();
  const updateSettings = useUpdateServerSettings();
  const checkConnection = useCheckAdminSettingsConnection();
  const { data: status } = useRecommendationsStatus();

  const triggerEmbeddings = useTriggerEmbeddings();
  const triggerTasteProfiles = useTriggerTasteProfiles();
  const triggerCowatch = useTriggerCowatch();
  const triggerRecommendations = useTriggerRecommendations();
  const resetEmbeddings = useResetEmbeddings();

  const [localValues, setLocalValues] = useState<RecLocalValues>({});
  const [dirtyKeys, setDirtyKeys] = useState<Set<string>>(new Set());
  const [collapsed, setCollapsed] = useState<RecCollapsedState>({});
  const [restartRequired, setRestartRequired] = useState(false);
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);

  useEffect(() => {
    if (!settings) return;
    const next: RecLocalValues = {};
    for (const field of getAllRecommendationFields()) {
      if (field.type === "password" || field.type === "toggle") continue;
      const serverVal = settings[field.key] || field.defaultValue || "";
      if (!(field.key in localValues)) {
        next[field.key] = serverVal;
      }
    }
    setLocalValues((prev) => {
      const merged: RecLocalValues = { ...prev };
      for (const [key, value] of Object.entries(next)) {
        if (!(key in merged)) {
          merged[key] = value;
        }
      }
      return merged;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [settings, sensitiveData]);

  function handleLocalChange(key: string, value: string) {
    setDirtyKeys((prev) => new Set(prev).add(key));
    setConnectionResult(null);
    setLocalValues((prev) => ({ ...prev, [key]: value }));
  }

  async function handleCommit(key: string, value: string) {
    setLocalValues((prev) => ({ ...prev, [key]: value }));
    try {
      const result = await updateSettings.mutateAsync({ [key]: value });
      const field = getAllRecommendationFields().find((candidate) => candidate.key === key);
      setLocalValues((prev) => ({
        ...prev,
        [key]: field?.type === "password" ? "" : (result.values[key] ?? value),
      }));
      setDirtyKeys((prev) => {
        const next = new Set(prev);
        next.delete(key);
        return next;
      });
      setRestartRequired((current) => current || result.restart_required);
    } catch {
      // useUpdateServerSettings already reports failures.
    }
  }

  function handleToggle(key: string, checked: boolean) {
    setConnectionResult(null);
    setLocalValues((prev) => ({ ...prev, [key]: checked ? "true" : "false" }));
    updateSettings.mutate(
      { [key]: checked ? "true" : "false" },
      {
        onSuccess: (result) => {
          setRestartRequired((current) => current || result.restart_required);
        },
      },
    );
  }

  async function applyEmbeddingPreset(preset: RecommendationProviderPreset) {
    try {
      const result = await updateSettings.mutateAsync({
        [EMBEDDING_BASE_URL_KEY]: preset.baseUrl,
        [EMBEDDING_MODEL_KEY]: preset.model,
      });
      setLocalValues((prev) => ({
        ...prev,
        [EMBEDDING_BASE_URL_KEY]: result.values[EMBEDDING_BASE_URL_KEY] ?? preset.baseUrl,
        [EMBEDDING_MODEL_KEY]: result.values[EMBEDDING_MODEL_KEY] ?? preset.model,
      }));
      setDirtyKeys((prev) => {
        const next = new Set(prev);
        next.delete(EMBEDDING_BASE_URL_KEY);
        next.delete(EMBEDDING_MODEL_KEY);
        return next;
      });

      setRestartRequired((current) => current || result.restart_required);
      setConnectionResult(null);
    } catch {
      // useUpdateServerSettings already reports failures.
    }
  }

  async function handleResetEmbeddings() {
    try {
      const deleted = await resetEmbeddings.mutateAsync();
      toast.success(
        `Embeddings reset: deleted ${deleted.embeddings.toLocaleString()} embeddings and cleared ${deleted.taste_profiles.toLocaleString()} taste profiles.`,
      );
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to reset embeddings");
    }
  }

  function toggleSection(title: string) {
    setCollapsed((prev) => ({ ...prev, [title]: !prev[title] }));
  }

  function buildEmbeddingCheckRequest(
    serverSettings: Record<string, string>,
  ): AdminSettingsConnectionCheckRequest {
    return {
      values: Object.fromEntries(
        EMBEDDING_CHECK_KEYS.map((key) => [key, localValues[key] ?? serverSettings[key] ?? ""]),
      ),
      dirty_keys: EMBEDDING_CHECK_KEYS.filter((key) => dirtyKeys.has(key)),
    };
  }

  async function handleCheckConnection(serverSettings: Record<string, string>) {
    try {
      setConnectionResult(
        await checkConnection.mutateAsync({
          kind: "recommendations_embedding",
          body: buildEmbeddingCheckRequest(serverSettings),
        }),
      );
    } catch (error) {
      setConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  if (isLoading)
    return (
      <div className="page-shell space-y-6 py-4 sm:py-6">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-28 rounded-2xl" />
          ))}
        </div>
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-16 max-w-3xl rounded-2xl" />
        ))}
      </div>
    );

  const sensitiveConfigured = sensitiveData?.configured ?? [];
  const serverSettings: Record<string, string> = settings ?? {};
  const embeddingLock = parseRecommendationEmbeddingLock(
    serverSettings["recommendations.embedding_lock"],
  );
  const selectedEmbeddingPreset = matchRecommendationProviderPreset(
    localValues[EMBEDDING_BASE_URL_KEY] ?? serverSettings[EMBEDDING_BASE_URL_KEY],
    localValues[EMBEDDING_MODEL_KEY] ?? serverSettings[EMBEDDING_MODEL_KEY],
  );

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Recommendations</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Configure the AI-powered recommendation engine. Requires pgvector and an
            OpenAI-compatible embedding endpoint.
          </p>
        </div>
      </div>

      {restartRequired && (
        <div className="border-warning/30 bg-warning/10 text-warning flex max-w-3xl items-center gap-3 rounded-xl border px-4 py-3 text-sm">
          <AlertTriangle className="h-4 w-4 flex-shrink-0" />
          <span>
            One or more settings were changed. A server restart is required for changes to take
            effect.
          </span>
        </div>
      )}

      {status && (
        <div className="space-y-3">
          <h2 className="text-lg font-medium tracking-tight">Job status</h2>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <RecJobStatusCard
              title="Embeddings"
              count={status.embeddings.count}
              total={status.embeddings.total}
              running={status.embeddings.running}
              lastRun={status.embeddings.last_run}
              onTrigger={() => triggerEmbeddings.mutate()}
              triggerPending={triggerEmbeddings.isPending}
            />
            <RecJobStatusCard
              title="Taste Profiles"
              count={status.taste_profiles.count}
              running={status.taste_profiles.running}
              lastRun={status.taste_profiles.last_run}
              onTrigger={() => triggerTasteProfiles.mutate()}
              triggerPending={triggerTasteProfiles.isPending}
            />
            <RecJobStatusCard
              title="Co-Watch Matrix"
              count={status.cowatch.count}
              running={status.cowatch.running}
              lastRun={status.cowatch.last_run}
              onTrigger={() => triggerCowatch.mutate()}
              triggerPending={triggerCowatch.isPending}
            />
            <RecJobStatusCard
              title="Recommendations"
              count={status.recommendations.count}
              running={status.recommendations.running}
              lastRun={status.recommendations.last_run}
              note={
                status.cache_refreshed_at
                  ? `Cache refreshed ${formatRelativeTime(status.cache_refreshed_at) ?? status.cache_refreshed_at}`
                  : undefined
              }
              onTrigger={() => triggerRecommendations.mutate()}
              triggerPending={triggerRecommendations.isPending}
            />
          </div>
        </div>
      )}

      <div className="space-y-3">
        <RecEmbeddingLockCard lock={embeddingLock} />
        {status && (
          <RecEmbeddingResetCard
            lockConflict={status.lock_conflict}
            disabled={
              status.embeddings.running ||
              status.taste_profiles.running ||
              status.recommendations.running
            }
            isPending={resetEmbeddings.isPending}
            onReset={() => void handleResetEmbeddings()}
          />
        )}

        {buildRecommendationSections().map((section) => {
          const isOpen = !collapsed[section.title];
          return (
            <div key={section.title} className="surface-panel max-w-3xl rounded-2xl border-0">
              <button
                type="button"
                onClick={() => toggleSection(section.title)}
                className="hover:bg-accent/40 flex w-full items-center justify-between rounded-2xl px-5 py-4 text-left transition-colors"
              >
                <span className="text-sm font-semibold">{section.title}</span>
                {isOpen ? (
                  <ChevronDown className="text-muted-foreground h-4 w-4" />
                ) : (
                  <ChevronRight className="text-muted-foreground h-4 w-4" />
                )}
              </button>

              {isOpen && (
                <div className="border-border divide-border divide-y border-t px-5 pb-4">
                  {section.title === EMBEDDING_SECTION_TITLE && (
                    <div className="space-y-3 py-3">
                      <div className="space-y-0.5">
                        <Label className="text-sm font-medium">Provider Presets</Label>
                        <p className="text-muted-foreground text-xs">
                          Choose a provider to fill the base URL and model.
                        </p>
                      </div>
                      <div className="flex flex-wrap gap-2">
                        {RECOMMENDATION_PROVIDER_PRESETS.map((preset) => {
                          const selected = selectedEmbeddingPreset?.id === preset.id;
                          return (
                            <button
                              key={preset.id}
                              type="button"
                              aria-pressed={selected}
                              onClick={() => void applyEmbeddingPreset(preset)}
                              disabled={updateSettings.isPending}
                              className={`min-w-[8.5rem] rounded-md border px-3 py-2 text-left transition-colors ${
                                selected
                                  ? "border-foreground/20 bg-foreground/10 text-foreground"
                                  : "border-border bg-background hover:bg-accent/30 text-foreground"
                              }`}
                            >
                              <div className="text-sm font-medium">{preset.label}</div>
                              {preset.tag && (
                                <div className="text-muted-foreground mt-0.5 text-xs">
                                  {preset.tag}
                                </div>
                              )}
                            </button>
                          );
                        })}
                      </div>
                      {selectedEmbeddingPreset && (
                        <p className="text-muted-foreground text-xs">
                          {selectedEmbeddingPreset.description}
                        </p>
                      )}
                    </div>
                  )}
                  {section.fields.map((field) => (
                    <RecSettingField
                      key={field.key}
                      field={field}
                      serverValue={serverSettings[field.key] ?? ""}
                      localValues={localValues}
                      sensitiveConfigured={sensitiveConfigured}
                      isPending={updateSettings.isPending}
                      onLocalChange={handleLocalChange}
                      onCommit={(key, value) => void handleCommit(key, value)}
                      onToggle={handleToggle}
                    />
                  ))}
                  {section.title === EMBEDDING_SECTION_TITLE ? (
                    // ConnectionCheckAction brings its own row padding.
                    <div>
                      <ConnectionCheckAction
                        onClick={() => handleCheckConnection(serverSettings)}
                        result={connectionResult}
                        isPending={checkConnection.isPending}
                        disabled={updateSettings.isPending}
                      />
                    </div>
                  ) : null}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
