import { useState, useEffect } from "react";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { AlertTriangle, ChevronDown, ChevronRight, Play, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Skeleton } from "@/components/ui/skeleton";
import type { AdminSettingsConnectionCheckRequest, ConnectionCheckResponse } from "@/api/types";
import type { components } from "@/api/v2/schema";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
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
import { useRefreshAIProviders } from "@/hooks/queries/admin/aiProviders";
import { EmbeddingProviderField } from "@/pages/admin-settings/AIProviders";
import { ModelField } from "@/pages/admin-settings/AIModelField";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";
import { RecEmbeddingLockCard, RecEmbeddingResetCard } from "./AdminRecommendationsEmbedding";

type RecJobRun = components["schemas"]["AdminRecommendationJobRun"];

interface RecLocalValues {
  [key: string]: string;
}

interface RecCollapsedState {
  [sectionId: string]: boolean;
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

const EMBEDDING_PROVIDER_KEY = "recommendations.embedding_provider_id";
const EMBEDDING_MODEL_KEY = "recommendations.embedding_model";
const EMBEDDING_CHECK_KEYS = [
  "recommendations.enabled",
  EMBEDDING_PROVIDER_KEY,
  EMBEDDING_MODEL_KEY,
] as const;
// Model names; not translated.
const EMBEDDING_MODEL_EXAMPLES = "text-embedding-3-large, qwen3-embedding:0.6b";

/** Which run a job card line describes: the last full run, or work between full runs. */
type RecRunKind = "run" | "catchUp" | "staleSweep" | "startupBuild";

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
  const t = useT();
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
    const placeholder = isConfigured
      ? t("web.admin.recommendations.settings.configured")
      : (hint ?? t("web.admin.recommendations.settings.notConfigured"));
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
            {t("web.admin.recommendations.settings.clearCredential")}
          </Button>
        )}
        {confirmClear && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-muted-foreground text-xs">
              {t("web.admin.recommendations.settings.clearQuestion")}
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
              {t("web.admin.recommendations.settings.confirmClear")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => setConfirmClear(false)}
              disabled={isPending}
            >
              {t("common.cancel")}
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

function RecJobLastRun({ run, kind = "run" }: { run?: RecJobRun; kind?: RecRunKind }) {
  const t = useT();
  if (!run) {
    return (
      <p className="text-muted-foreground text-xs">{t("web.admin.recommendations.jobs.noRun")}</p>
    );
  }
  const when = formatRelativeTime(run.completed_at) ?? run.completed_at;
  if (run.status === "failed") {
    return (
      <div className="space-y-0.5">
        <p className="text-destructive text-xs font-medium">
          {t("web.admin.recommendations.jobs.runFailed", { kind, when })}
        </p>
        {run.error && (
          <p className="text-muted-foreground line-clamp-3 text-xs break-words" title={run.error}>
            {run.error}
          </p>
        )}
      </div>
    );
  }
  return (
    <p className="text-muted-foreground text-xs">
      {t("web.admin.recommendations.jobs.runCompleted", { kind, when })}
    </p>
  );
}

function RecJobStatusCard({
  title,
  count,
  total,
  running,
  lastRun,
  secondaryRun,
  secondaryRunKind,
  note,
  onTrigger,
  triggerPending,
}: {
  title: string;
  count: number;
  total?: number;
  running: boolean;
  lastRun?: RecJobRun;
  /** Work that runs between full runs (a catch-up pass, the stale sweep), shown beside the last full run. */
  secondaryRun?: RecJobRun;
  secondaryRunKind?: RecRunKind;
  note?: string;
  onTrigger: () => void;
  triggerPending: boolean;
}) {
  const t = useT();
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
              ? t("web.admin.recommendations.jobs.itemsProgress", {
                  count: total,
                  done_text: count.toLocaleString(formatLocale()),
                  count_text: total.toLocaleString(formatLocale()),
                })
              : t("web.admin.recommendations.jobs.entries", {
                  count,
                  count_text: count.toLocaleString(formatLocale()),
                })}
          </p>
          {note && <p className="text-muted-foreground text-xs">{note}</p>}
          <RecJobLastRun run={lastRun} />
          {secondaryRun && <RecJobLastRun run={secondaryRun} kind={secondaryRunKind} />}
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
              {t("web.admin.recommendations.jobs.running")}
            </>
          ) : (
            <>
              <Play className="size-3.5" />
              {t("web.admin.recommendations.jobs.run")}
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
          <p className="text-muted-foreground mt-1 text-end text-xs">
            {t("web.admin.recommendations.jobs.percent", { percent: pct })}
          </p>
        </div>
      )}
    </div>
  );
}

export default function AdminRecommendations() {
  const t = useT();
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
  const refreshProviders = useRefreshAIProviders();

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
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seeds only keys missing from localValues; depending on it would rerun on every edit
  }, [settings, sensitiveData]);

  function handleLocalChange(key: string, value: string) {
    setDirtyKeys((prev) => new Set(prev).add(key));
    setConnectionResult(null);
    setLocalValues((prev) => ({ ...prev, [key]: value }));
  }

  /** Saves one setting and reports whether the server accepted it. */
  async function handleCommit(key: string, value: string): Promise<boolean> {
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
      return true;
    } catch {
      // useUpdateServerSettings already reports failures.
      return false;
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

  async function handleResetEmbeddings() {
    try {
      const deleted = await resetEmbeddings.mutateAsync();
      toast.success(
        t("web.admin.recommendations.reset.done", {
          embeddings: t("web.admin.recommendations.reset.embeddingCount", {
            count: deleted.embeddings,
            count_text: deleted.embeddings.toLocaleString(formatLocale()),
          }),
          profiles: t("web.admin.recommendations.reset.tasteProfileCount", {
            count: deleted.taste_profiles,
            count_text: deleted.taste_profiles.toLocaleString(formatLocale()),
          }),
        }),
      );
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t("web.admin.recommendations.reset.failed"),
      );
    }
  }

  function toggleSection(id: string) {
    setCollapsed((prev) => ({ ...prev, [id]: !prev[id] }));
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
        message:
          error instanceof Error
            ? error.message
            : t("web.admin.recommendations.embedding.checkFailed"),
      });
    }
  }

  if (isLoading)
    return (
      <div className="space-y-6 pb-4 sm:pb-6">
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
  const embeddingProviderID =
    localValues[EMBEDDING_PROVIDER_KEY] ?? serverSettings[EMBEDDING_PROVIDER_KEY] ?? "";
  const embeddingModel =
    localValues[EMBEDDING_MODEL_KEY] ?? serverSettings[EMBEDDING_MODEL_KEY] ?? "";

  return (
    <div className="space-y-6 pb-4 sm:pb-6">
      <PageHeader
        title={t("web.admin.recommendations.page.title")}
        subtitle={t("web.admin.recommendations.page.subtitle")}
      />

      {restartRequired && (
        <div className="border-warning/30 bg-warning/10 text-warning flex max-w-3xl items-center gap-3 rounded-xl border px-4 py-3 text-sm">
          <AlertTriangle className="h-4 w-4 flex-shrink-0" />
          <span>{t("web.admin.recommendations.page.restartRequired")}</span>
        </div>
      )}

      {status && (
        <div className="space-y-3">
          <h2 className="text-lg font-medium tracking-tight">
            {t("web.admin.recommendations.jobs.title")}
          </h2>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <RecJobStatusCard
              title={t("web.admin.recommendations.jobs.embeddings")}
              count={status.embeddings.count}
              total={status.embeddings.total}
              running={status.embeddings.running}
              lastRun={status.embeddings.last_run}
              secondaryRun={status.embeddings.last_partial_run}
              secondaryRunKind="catchUp"
              onTrigger={() => triggerEmbeddings.mutate()}
              triggerPending={triggerEmbeddings.isPending}
            />
            <RecJobStatusCard
              title={t("web.admin.recommendations.jobs.tasteProfiles")}
              count={status.taste_profiles.count}
              running={status.taste_profiles.running}
              lastRun={status.taste_profiles.last_run}
              secondaryRun={status.stale_sweep.last_run}
              secondaryRunKind="staleSweep"
              note={
                status.stale_sweep.pending > 0
                  ? t("web.admin.recommendations.jobs.profilesWaiting", {
                      count: status.stale_sweep.pending,
                      count_text: status.stale_sweep.pending.toLocaleString(formatLocale()),
                    })
                  : undefined
              }
              onTrigger={() => triggerTasteProfiles.mutate()}
              triggerPending={triggerTasteProfiles.isPending}
            />
            <RecJobStatusCard
              title={t("web.admin.recommendations.jobs.cowatch")}
              count={status.cowatch.count}
              running={status.cowatch.running}
              lastRun={status.cowatch.last_run}
              onTrigger={() => triggerCowatch.mutate()}
              triggerPending={triggerCowatch.isPending}
            />
            <RecJobStatusCard
              title={t("web.admin.recommendations.jobs.recommendations")}
              count={status.recommendations.count}
              running={status.recommendations.running}
              lastRun={status.recommendations.last_run}
              secondaryRun={status.recommendations.last_partial_run}
              secondaryRunKind="startupBuild"
              // Profile refreshes also write rows, so this is not job health;
              // the last run above is.
              note={
                status.cache_refreshed_at
                  ? t("web.admin.recommendations.jobs.cacheRefreshed", {
                      when:
                        formatRelativeTime(status.cache_refreshed_at) ?? status.cache_refreshed_at,
                    })
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
          const isOpen = !collapsed[section.id];
          // The embedding section holds the model; its provider and check render with it.
          const isEmbeddingSection = section.fields.some(
            (field) => field.key === EMBEDDING_MODEL_KEY,
          );
          return (
            <div key={section.id} className="surface-panel max-w-3xl rounded-2xl border-0">
              <button
                type="button"
                onClick={() => toggleSection(section.id)}
                className="hover:bg-accent/40 flex w-full items-center justify-between rounded-2xl px-5 py-4 text-start transition-colors"
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
                  {isEmbeddingSection && (
                    <div>
                      <EmbeddingProviderField
                        value={embeddingProviderID}
                        disabled={updateSettings.isPending}
                        onChange={(id) => {
                          setConnectionResult(null);
                          void handleCommit(EMBEDDING_PROVIDER_KEY, id).then((saved) => {
                            // A refused choice must not stay on screen as if saved.
                            if (!saved) {
                              setLocalValues((prev) => ({
                                ...prev,
                                [EMBEDDING_PROVIDER_KEY]:
                                  serverSettings[EMBEDDING_PROVIDER_KEY] ?? "",
                              }));
                              return;
                            }
                            // It changes which provider AI Services reports in use.
                            void refreshProviders();
                          });
                        }}
                      />
                      <ModelField
                        label={t("web.admin.recommendations.embedding.model")}
                        providerID={embeddingProviderID}
                        kind="embeddings"
                        value={embeddingModel}
                        placeholder={EMBEDDING_MODEL_EXAMPLES}
                        onChange={(next) => handleLocalChange(EMBEDDING_MODEL_KEY, next)}
                        onCommit={(next) => {
                          if (next !== (serverSettings[EMBEDDING_MODEL_KEY] ?? "")) {
                            void handleCommit(EMBEDDING_MODEL_KEY, next);
                          }
                        }}
                      />
                    </div>
                  )}
                  {section.fields.map((field) =>
                    // The embedding model renders above, with the provider's suggestions.
                    field.key === EMBEDDING_MODEL_KEY ? null : (
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
                    ),
                  )}
                  {isEmbeddingSection ? (
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
