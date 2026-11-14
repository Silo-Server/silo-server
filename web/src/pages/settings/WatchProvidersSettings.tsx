import { useState } from "react";
import {
  AlertCircle,
  Check,
  CheckCircle2,
  ChevronDown,
  Copy,
  ExternalLink,
  Loader2,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { V2ProblemError } from "@/api/v2/request";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import {
  useConnectWatchProviderAPIKey,
  useDeleteWatchProviderConnection,
  usePollWatchProviderDeviceAuth,
  useStartWatchProviderDeviceAuth,
  useTriggerWatchProviderSync,
  useUpdateWatchProviderConnection,
  useWatchProviderConnection,
  useWatchProviders,
  useWatchProviderSyncRuns,
  WatchProviderAuthMethod,
  type DeviceAuthSession,
  type WatchProviderConnection,
  type WatchProviderConnectionSetting,
  type WatchProviderSyncRun,
} from "@/hooks/queries/watchProviders";
import { Input } from "@/components/ui/input";
import { formatRelativeTime as formatRelativeTimeBase } from "@/lib/date";
import { SchemaForm } from "@/components/admin/plugins/SchemaForm";
import type { PluginConfigSchema } from "@/api/types";
import type { WatchProviderConnectionConfig } from "@/hooks/queries/watchProviders";
import {
  buildConnectionConfig,
  connectionSchemasAreValid,
  renderableConnectionSchemas,
} from "./watchProviderConnectionConfig";
import { formatLocale } from "@/lib/appLocale";
import { useT, type Translate } from "@/i18n";

function formatRelativeTime(t: Translate, value?: string) {
  return (
    formatRelativeTimeBase(value, {
      rounding: "floor",
      justNowLabel: t("web.settings.watchProviders.justNow"),
    }) ?? t("web.settings.watchProviders.never")
  );
}

function formatRetryAfter(t: Translate, seconds?: number) {
  if (!seconds || seconds <= 0) return "";
  if (seconds < 60) return t("web.settings.watchProviders.duration.seconds", { seconds: seconds });
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return t("web.settings.watchProviders.duration.minutes", { minutes: minutes });
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes > 0
    ? t("web.settings.watchProviders.duration.hoursMinutes", {
        hours: hours,
        minutes: remainingMinutes,
      })
    : t("web.settings.watchProviders.duration.hours", { hours: hours });
}

type StatusVariant = "connected" | "disconnected" | "pending" | "error";

const STATUS_PILL_STYLES: Record<StatusVariant, string> = {
  connected: "bg-emerald-500/15 text-emerald-300 ring-emerald-500/25",
  disconnected: "bg-muted/50 text-muted-foreground ring-border/60",
  pending: "bg-amber-500/15 text-amber-300 ring-amber-500/30",
  error: "bg-rose-500/15 text-rose-300 ring-rose-500/25",
};

function StatusPill({ variant, children }: { variant: StatusVariant; children: React.ReactNode }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset",
        STATUS_PILL_STYLES[variant],
      )}
    >
      <span
        aria-hidden
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          variant === "connected" && "bg-emerald-400",
          variant === "disconnected" && "bg-muted-foreground/60",
          variant === "pending" && "animate-pulse bg-amber-400",
          variant === "error" && "bg-rose-400",
        )}
      />
      {children}
    </span>
  );
}

function ToggleRow({
  id,
  label,
  description,
  checked,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  description: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="border-border/40 flex items-start justify-between gap-4 border-t py-3 first:border-t-0 first:pt-0 sm:border-t-0 sm:py-1.5 sm:first:pt-1.5">
      <div className="min-w-0 flex-1">
        <Label htmlFor={id} className="text-sm font-medium">
          {label}
        </Label>
        <p className="text-muted-foreground mt-0.5 text-xs leading-snug">{description}</p>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={onChange}
        className="mt-0.5 shrink-0"
      />
    </div>
  );
}

/** The connection's value for a boolean setting, or the setting's default when it has none. */
function booleanSettingValue(
  values: Record<string, unknown> | undefined,
  setting: WatchProviderConnectionSetting,
) {
  const value = values?.[setting.key];
  if (typeof value === "boolean") return value;
  return setting.default_value === true;
}

function StatCell({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground text-xs font-medium tracking-wider uppercase">
        {label}
      </div>
      <div className="mt-0.5 truncate text-sm font-medium">{value}</div>
    </div>
  );
}

function ErrorBanner({ message, hint }: { message: string; hint?: string }) {
  return (
    <div className="flex items-start gap-2.5 rounded-xl bg-rose-500/10 px-3 py-2.5 ring-1 ring-rose-500/20 ring-inset">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-rose-300" />
      <div className="min-w-0 flex-1 text-xs leading-snug">
        <div className="font-medium text-rose-200">{message}</div>
        {hint ? <div className="mt-0.5 text-rose-200/70">{hint}</div> : null}
      </div>
    </div>
  );
}

function AuthCodeBlock({
  displayName,
  session,
  pollPending,
  onCopy,
  onPoll,
  onCancel,
  codeCopied,
}: {
  displayName: string;
  session: DeviceAuthSession;
  pollPending: boolean;
  onCopy: () => void;
  onPoll: () => void;
  onCancel: () => void;
  codeCopied: boolean;
}) {
  const t = useT();
  const name = { provider: displayName };
  return (
    <div className="border-primary/30 bg-primary/5 rounded-xl border border-dashed p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium">
            {t("web.settings.watchProviders.auth.copyCodeFirst")}
          </div>
          <div className="text-muted-foreground mt-0.5 text-xs leading-snug">
            {t("web.settings.watchProviders.auth.copyCodeHint", name)}
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onCancel}
          aria-label={t("web.settings.watchProviders.auth.cancel")}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-stretch">
        <div className="border-border bg-background/60 flex min-h-10 flex-1 items-center justify-center rounded-md border px-3 font-mono text-base tracking-[0.2em] sm:text-lg">
          {session.user_code}
        </div>
        <Button type="button" variant="outline" size="sm" onClick={onCopy}>
          {codeCopied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          {codeCopied
            ? t("web.settings.watchProviders.auth.copied")
            : t("web.settings.watchProviders.auth.copyCode")}
        </Button>
      </div>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row">
        <Button type="button" variant="outline" size="sm" asChild className="sm:flex-1">
          <a href={session.verification_url} target="_blank" rel="noreferrer">
            <ExternalLink className="h-4 w-4" />
            {t("web.settings.watchProviders.auth.openActivation", name)}
          </a>
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={pollPending}
          onClick={onPoll}
          className="sm:flex-1"
        >
          {pollPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
          {t("web.settings.watchProviders.auth.entered")}
        </Button>
      </div>
    </div>
  );
}

function APIKeyBlock({
  displayName,
  providerKey,
  configSchemas,
  pending,
  onSubmit,
  onCancel,
}: {
  displayName: string;
  providerKey: string;
  configSchemas: PluginConfigSchema[];
  pending: boolean;
  onSubmit: (apiKey: string, connectionConfig: WatchProviderConnectionConfig) => void;
  onCancel: () => void;
}) {
  const t = useT();
  const [value, setValue] = useState("");
  const [connectionConfig, setConnectionConfig] = useState<WatchProviderConnectionConfig>({});
  const [configValidity, setConfigValidity] = useState<Record<string, boolean>>({});
  const trimmed = value.trim();
  const renderableSchemas = renderableConnectionSchemas(configSchemas);
  const configValid = connectionSchemasAreValid(
    renderableSchemas,
    connectionConfig,
    configValidity,
  );

  return (
    <div className="border-primary/30 bg-primary/5 rounded-xl border border-dashed p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium">
            {t("web.settings.watchProviders.apiKey.title", { provider: displayName })}
          </div>
          <div className="text-muted-foreground mt-0.5 text-xs leading-snug">
            {t("web.settings.watchProviders.apiKey.hint", { provider: displayName })}
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onCancel}
          aria-label={t("web.settings.watchProviders.apiKey.cancel")}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>
      {renderableSchemas.map((schema) => (
        <div key={schema.key} className="mt-4 space-y-2">
          <div>
            <div className="text-sm font-medium">{schema.title || schema.key}</div>
            {schema.description ? (
              <div className="text-muted-foreground mt-0.5 text-xs leading-snug">
                {schema.description}
              </div>
            ) : null}
          </div>
          <SchemaForm
            descriptor={schema.admin_form}
            values={connectionConfig[schema.key] ?? {}}
            onChange={(next) =>
              setConnectionConfig((current) => ({ ...current, [schema.key]: next }))
            }
            idPrefix={`watch-provider-${providerKey}-${schema.key}`}
            onValidityChange={(valid) =>
              setConfigValidity((current) => ({ ...current, [schema.key]: valid }))
            }
          />
        </div>
      ))}
      <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-stretch">
        <Input
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder={t("web.settings.watchProviders.apiKey.placeholder")}
          value={value}
          onChange={(event) => setValue(event.target.value)}
          className="font-mono"
        />
        <Button
          type="button"
          size="sm"
          disabled={pending || trimmed.length === 0 || !configValid}
          onClick={() =>
            onSubmit(trimmed, buildConnectionConfig(renderableSchemas, connectionConfig))
          }
          className="sm:flex-none"
        >
          {pending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}
          {t("web.settings.watchProviders.connect")}
        </Button>
      </div>
    </div>
  );
}

interface ConnectedRunInfo {
  imported: {
    watched: number;
    progress: number;
    favorites: number;
    watchlist: number;
    ratings: number;
  };
  exported: {
    watched: number;
    favorites: number;
    favoriteRemovals: number;
    watchlist: number;
    watchlistRemovals: number;
    ratings: number;
  };
  errorMessage?: string;
  errorHint?: string;
}

function deriveRunInfo(
  t: Translate,
  connection: WatchProviderConnection,
  latestRun: WatchProviderSyncRun | undefined,
): ConnectedRunInfo {
  const imported = {
    watched: latestRun?.inbound_watched_imported ?? 0,
    progress: latestRun?.inbound_progress_imported ?? 0,
    favorites: latestRun?.inbound_favorites_imported ?? 0,
    watchlist: latestRun?.inbound_watchlist_imported ?? 0,
    ratings: latestRun?.inbound_ratings_imported ?? 0,
  };
  const exported = {
    watched: latestRun?.outbound_sent ?? 0,
    favorites: latestRun?.outbound_favorites_sent ?? 0,
    favoriteRemovals: latestRun?.favorite_removals_sent ?? 0,
    watchlist: latestRun?.outbound_watchlist_sent ?? 0,
    watchlistRemovals: latestRun?.watchlist_removals_sent ?? 0,
    ratings: latestRun?.outbound_ratings_sent ?? 0,
  };
  let errorMessage: string | undefined;
  let errorHint: string | undefined;
  if (latestRun?.status === "failed" && latestRun.error) {
    errorMessage = latestRun.error;
    errorHint = t("web.settings.watchProviders.failedAgo", {
      time: formatRelativeTime(t, latestRun.completed_at ?? latestRun.started_at),
    });
  } else if (connection.last_error) {
    errorMessage = connection.last_error;
    errorHint = connection.last_scrobble_error_at
      ? t("web.settings.watchProviders.lastSeen", {
          time: formatRelativeTime(t, connection.last_scrobble_error_at),
        })
      : undefined;
  }
  return { imported, exported, errorMessage, errorHint };
}

function formatLastSync(
  t: Translate,
  connection: WatchProviderConnection,
  latestRun?: WatchProviderSyncRun,
) {
  const candidates = [
    latestRun?.completed_at,
    connection.last_inbound_sync_at,
    connection.last_progress_sync_at,
    connection.last_outbound_sync_at,
    connection.last_favorites_sync_at,
    connection.last_watchlist_sync_at,
  ].filter((value): value is string => Boolean(value));
  if (candidates.length === 0) return t("web.settings.watchProviders.neverSynced");
  const newest = candidates
    .map((value) => new Date(value).getTime())
    .reduce((a, b) => Math.max(a, b), 0);
  if (!Number.isFinite(newest) || newest <= 0) return t("web.settings.watchProviders.neverSynced");
  return t("web.settings.watchProviders.synced", {
    time: formatRelativeTime(t, new Date(newest).toISOString()),
  });
}

function WatchProviderCard({
  providerKey,
  connectionSettings = [],
}: {
  providerKey: string;
  connectionSettings?: WatchProviderConnectionSetting[];
}) {
  const t = useT();
  const { data: savedConnection, isLoading, isFetching } = useWatchProviderConnection(providerKey);
  const updateConnection = useUpdateWatchProviderConnection(providerKey);
  const startAuth = useStartWatchProviderDeviceAuth(providerKey);
  const pollAuth = usePollWatchProviderDeviceAuth(providerKey);
  const connectAPIKey = useConnectWatchProviderAPIKey(providerKey);
  const deleteConnection = useDeleteWatchProviderConnection(providerKey);
  const syncNow = useTriggerWatchProviderSync(providerKey);
  const { data: syncRunsData } = useWatchProviderSyncRuns(
    providerKey,
    Boolean(savedConnection?.connected),
  );
  const [authSession, setAuthSession] = useState<DeviceAuthSession | null>(null);
  const [codeCopied, setCodeCopied] = useState(false);
  const [apiKeyPrompt, setApiKeyPrompt] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsConflict =
    updateConnection.error instanceof V2ProblemError && updateConnection.error.status === 412;
  // A settings conflict opens the section so the attempted change and its
  // choices are visible. Opening it in state, not deriving visibility from
  // the error, keeps it open while a retry clears the error.
  const [conflictShown, setConflictShown] = useState(false);
  if (settingsConflict !== conflictShown) {
    setConflictShown(settingsConflict);
    if (settingsConflict) setSettingsOpen(true);
  }
  const connection =
    savedConnection && settingsConflict
      ? {
          ...savedConnection,
          ...updateConnection.variables,
          connection_settings: {
            ...savedConnection.connection_settings,
            ...updateConnection.variables?.connection_settings,
          },
        }
      : savedConnection;

  if (isLoading || !connection) {
    return (
      <section className="surface-panel rounded-[1.7rem] border-0 px-4 py-5 shadow-none sm:px-6">
        <div className="bg-muted/60 h-5 w-40 animate-pulse rounded" />
        <div className="bg-muted/40 mt-4 h-20 animate-pulse rounded-xl" />
      </section>
    );
  }

  const latestRun = syncRunsData?.runs?.[0];
  const syncRunning = latestRun?.status === "queued" || latestRun?.status === "running";
  const cooldownSeconds =
    syncNow.error instanceof V2ProblemError && syncNow.error.status === 429
      ? syncNow.error.retryAfterSeconds
      : undefined;
  const syncDisabled = syncNow.isPending || syncRunning || Boolean(cooldownSeconds);
  const syncButtonLabel = syncRunning
    ? t("web.settings.watchProviders.syncing")
    : cooldownSeconds
      ? t("web.settings.watchProviders.cooldown", { time: formatRetryAfter(t, cooldownSeconds) })
      : t("web.settings.watchProviders.syncNow");

  const isBusy =
    (connection.connected && !connection.etag) ||
    updateConnection.isPending ||
    startAuth.isPending ||
    pollAuth.isPending ||
    connectAPIKey.isPending ||
    deleteConnection.isPending;
  const displayName = connection.display_name;
  const name = { provider: displayName };
  const usesAPIKey = connection.auth_method === WatchProviderAuthMethod.APIKey;
  const showAuth = Boolean(authSession) && !connection.connected;
  const showAPIKey = usesAPIKey && apiKeyPrompt && !connection.connected;
  const runInfo = connection.connected ? deriveRunInfo(t, connection, latestRun) : null;
  const settingsPanelId = `watch-provider-${providerKey}-settings`;
  const hasError = Boolean(runInfo?.errorMessage);
  const favoritesSyncEnabled =
    connection.import_favorites_enabled || connection.export_favorites_enabled;
  const watchlistSyncEnabled =
    connection.import_watchlist_enabled || connection.export_watchlist_enabled;
  const supportsRatings =
    connection.capabilities.import_ratings || connection.capabilities.export_ratings;
  const importedCounts = runInfo
    ? {
        watched: runInfo.imported.watched.toLocaleString(formatLocale()),
        progress: runInfo.imported.progress.toLocaleString(formatLocale()),
        favorites: runInfo.imported.favorites.toLocaleString(formatLocale()),
        watchlist: runInfo.imported.watchlist.toLocaleString(formatLocale()),
      }
    : { watched: "", progress: "", favorites: "", watchlist: "" };
  const sentLabel = runInfo
    ? t("web.settings.watchProviders.stats.sent", {
        count: (
          runInfo.exported.watched +
          runInfo.exported.favorites +
          runInfo.exported.favoriteRemovals +
          runInfo.exported.watchlist +
          runInfo.exported.watchlistRemovals +
          runInfo.exported.ratings
        ).toLocaleString(formatLocale()),
      })
    : "";

  let statusVariant: StatusVariant;
  let statusLabel: string;
  if (showAuth || showAPIKey) {
    statusVariant = "pending";
    statusLabel = usesAPIKey
      ? t("web.settings.watchProviders.status.apiKeyRequired")
      : t("web.settings.watchProviders.status.activationPending");
  } else if (connection.connected && hasError) {
    statusVariant = "error";
    statusLabel = t("web.settings.watchProviders.status.syncError");
  } else if (connection.connected) {
    statusVariant = "connected";
    statusLabel = t("web.settings.watchProviders.status.connected");
  } else {
    statusVariant = "disconnected";
    statusLabel = t("web.settings.watchProviders.status.notConnected");
  }

  const subtitleText = (() => {
    if (showAuth) return t("web.settings.watchProviders.subtitle.waitingForCode");
    if (showAPIKey) return t("web.settings.watchProviders.subtitle.pasteApiKey", name);
    if (connection.connected) {
      const username = connection.provider_username || displayName;
      return t("web.settings.watchProviders.subtitle.connected", {
        username,
        last_sync: formatLastSync(t, connection, latestRun),
      });
    }
    if (!connection.credentials_configured)
      return t("web.settings.watchProviders.subtitle.setupRequired");
    if (usesAPIKey) return t("web.settings.watchProviders.subtitle.connectApiKey", name);
    return t("web.settings.watchProviders.subtitle.connect");
  })();

  const handleCopyCode = async () => {
    if (!authSession) return;
    try {
      await copyTextToClipboard(authSession.user_code);
      setCodeCopied(true);
      toast.success(t("web.settings.watchProviders.auth.codeCopied", name));
    } catch {
      toast.error(t("web.settings.watchProviders.auth.copyFailed", name));
    }
  };

  const handlePoll = () => {
    if (!authSession) return;
    pollAuth.mutate(authSession.id, {
      onSuccess: () => {
        setAuthSession(null);
        setCodeCopied(false);
      },
    });
  };

  const handleCancelAuth = () => {
    setAuthSession(null);
    setCodeCopied(false);
  };

  const handleStartConnect = () => {
    if (usesAPIKey) {
      setApiKeyPrompt(true);
      return;
    }
    startAuth.mutate(undefined, {
      onSuccess: (session) => {
        setAuthSession(session);
        setCodeCopied(false);
      },
    });
  };

  const handleSubmitAPIKey = (apiKey: string, connectionConfig: WatchProviderConnectionConfig) => {
    connectAPIKey.mutate(
      { apiKey, connectionConfig },
      {
        onSuccess: () => {
          setApiKeyPrompt(false);
        },
      },
    );
  };

  const handleCancelAPIKey = () => {
    setApiKeyPrompt(false);
  };

  return (
    <section className="surface-panel rounded-[1.7rem] border-0 px-4 py-5 shadow-none sm:px-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-foreground truncate text-base font-semibold tracking-tight">
              {displayName}
            </h3>
            <StatusPill variant={statusVariant}>{statusLabel}</StatusPill>
          </div>
          <p className="text-muted-foreground mt-1 text-[13px] leading-snug">{subtitleText}</p>
        </div>
        <div className="flex shrink-0 gap-2 sm:justify-end">
          {showAuth || showAPIKey ? null : connection.connected ? (
            <>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={isBusy || syncDisabled}
                onClick={() => syncNow.mutate()}
                className="flex-1 sm:flex-none"
              >
                {syncNow.isPending || syncRunning ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <RefreshCw className="h-4 w-4" />
                )}
                {syncButtonLabel}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={isBusy}
                onClick={() => {
                  if (window.confirm(t("web.settings.watchProviders.disconnectConfirm", name))) {
                    deleteConnection.mutate();
                  }
                }}
                className="flex-1 sm:flex-none"
              >
                <Trash2 className="h-4 w-4" />
                <span className="sm:inline">{t("web.settings.watchProviders.disconnect")}</span>
              </Button>
            </>
          ) : (
            <Button
              type="button"
              size="sm"
              disabled={!connection.credentials_configured || isBusy}
              onClick={handleStartConnect}
              className="flex-1 sm:flex-none"
            >
              {startAuth.isPending || connectAPIKey.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <CheckCircle2 className="h-4 w-4" />
              )}
              {t("web.settings.watchProviders.connect")}
            </Button>
          )}
        </div>
      </header>

      {showAuth && authSession ? (
        <div className="mt-4">
          <AuthCodeBlock
            displayName={displayName}
            session={authSession}
            pollPending={pollAuth.isPending}
            onCopy={handleCopyCode}
            onPoll={handlePoll}
            onCancel={handleCancelAuth}
            codeCopied={codeCopied}
          />
        </div>
      ) : null}

      {showAPIKey ? (
        <div className="mt-4">
          <APIKeyBlock
            displayName={displayName}
            providerKey={providerKey}
            configSchemas={connection.connection_config_schema ?? []}
            pending={connectAPIKey.isPending}
            onSubmit={handleSubmitAPIKey}
            onCancel={handleCancelAPIKey}
          />
        </div>
      ) : null}

      {connection.connected && runInfo ? (
        <div className="mt-4 space-y-4">
          {runInfo.errorMessage ? (
            <ErrorBanner message={runInfo.errorMessage} hint={runInfo.errorHint} />
          ) : null}

          <div className="bg-background/40 rounded-xl px-3 py-3 ring-1 ring-white/5 ring-inset">
            <div className="grid grid-cols-2 gap-3 sm:hidden">
              <StatCell
                label={t("web.settings.watchProviders.stats.lastImported")}
                value={
                  supportsRatings
                    ? t("web.settings.watchProviders.stats.importedSummaryWithRatings", {
                        ...importedCounts,
                        ratings: runInfo.imported.ratings.toLocaleString(formatLocale()),
                      })
                    : t("web.settings.watchProviders.stats.importedSummary", importedCounts)
                }
              />
              <StatCell
                label={t("web.settings.watchProviders.stats.lastExported")}
                value={sentLabel}
              />
            </div>
            <div
              className={cn(
                "hidden gap-3 sm:grid",
                supportsRatings ? "grid-cols-6" : "grid-cols-5",
              )}
            >
              <StatCell
                label={t("web.settings.watchProviders.stats.watched")}
                value={t("web.settings.watchProviders.stats.imported", {
                  count: importedCounts.watched,
                })}
              />
              <StatCell
                label={t("web.settings.watchProviders.stats.progress")}
                value={t("web.settings.watchProviders.stats.resumed", {
                  count: importedCounts.progress,
                })}
              />
              <StatCell
                label={t("web.settings.watchProviders.stats.favorites")}
                value={t("web.settings.watchProviders.stats.imported", {
                  count: importedCounts.favorites,
                })}
              />
              <StatCell
                label={t("web.settings.watchProviders.stats.watchlist")}
                value={t("web.settings.watchProviders.stats.imported", {
                  count: importedCounts.watchlist,
                })}
              />
              {supportsRatings ? (
                <StatCell
                  label={t("web.settings.watchProviders.stats.ratings")}
                  value={t("web.settings.watchProviders.stats.imported", {
                    count: runInfo.imported.ratings.toLocaleString(formatLocale()),
                  })}
                />
              ) : null}
              <StatCell label={t("web.settings.watchProviders.stats.exported")} value={sentLabel} />
            </div>
          </div>

          <button
            type="button"
            aria-expanded={settingsOpen}
            aria-controls={settingsPanelId}
            onClick={() => setSettingsOpen(!settingsOpen)}
            className="text-muted-foreground hover:text-foreground flex w-full items-center justify-between gap-3 rounded-lg py-1 text-start text-sm font-medium transition-colors"
          >
            {t("web.settings.watchProviders.settings")}
            <ChevronDown
              aria-hidden="true"
              className={cn("h-4 w-4 shrink-0 transition-transform", settingsOpen && "rotate-180")}
            />
          </button>
          {settingsOpen ? (
            <div id={settingsPanelId}>
              {settingsConflict && (
                <div role="alert" className="border-border mb-4 rounded-xl border p-4 text-sm">
                  <p>{t("web.settings.watchProviders.conflict.message")}</p>
                  <div className="mt-3 flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      disabled={isFetching || updateConnection.isPending}
                      onClick={() => {
                        if (updateConnection.variables)
                          updateConnection.mutate(updateConnection.variables);
                      }}
                    >
                      {t("web.settings.watchProviders.conflict.apply")}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={isFetching}
                      onClick={() => updateConnection.reset()}
                    >
                      {t("web.settings.watchProviders.conflict.useLatest")}
                    </Button>
                  </div>
                </div>
              )}
              <div className="grid grid-cols-1 gap-x-6 gap-y-1 sm:grid-cols-2">
                <ToggleRow
                  id={`watch-provider-${providerKey}-import-watched`}
                  label={t("web.settings.watchProviders.toggle.importWatched.label")}
                  description={t(
                    "web.settings.watchProviders.toggle.importWatched.description",
                    name,
                  )}
                  checked={connection.import_watched_enabled}
                  disabled={isBusy}
                  onChange={(checked) =>
                    updateConnection.mutate({ import_watched_enabled: checked })
                  }
                />
                <ToggleRow
                  id={`watch-provider-${providerKey}-import-progress`}
                  label={t("web.settings.watchProviders.toggle.importProgress.label")}
                  description={t(
                    "web.settings.watchProviders.toggle.importProgress.description",
                    name,
                  )}
                  checked={connection.import_progress_enabled}
                  disabled={isBusy}
                  onChange={(checked) =>
                    updateConnection.mutate({ import_progress_enabled: checked })
                  }
                />
                <ToggleRow
                  id={`watch-provider-${providerKey}-export-watched`}
                  label={t("web.settings.watchProviders.toggle.exportWatched.label")}
                  description={t("web.settings.watchProviders.toggle.exportWatched.description")}
                  checked={connection.export_watched_enabled}
                  disabled={isBusy}
                  onChange={(checked) =>
                    updateConnection.mutate({ export_watched_enabled: checked })
                  }
                />
                {connection.capabilities.import_favorites ||
                connection.capabilities.export_favorites ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-favorites`}
                    label={t("web.settings.watchProviders.toggle.favorites.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.favorites.description",
                      name,
                    )}
                    checked={favoritesSyncEnabled}
                    disabled={isBusy}
                    onChange={(checked) =>
                      updateConnection.mutate({
                        import_favorites_enabled: checked,
                        export_favorites_enabled: checked,
                        sync_favorite_removals_enabled: checked
                          ? connection.sync_favorite_removals_enabled
                          : false,
                      })
                    }
                  />
                ) : null}
                {connection.capabilities.remove_favorites ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-favorite-removals`}
                    label={t("web.settings.watchProviders.toggle.favoriteRemovals.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.favoriteRemovals.description",
                    )}
                    checked={connection.sync_favorite_removals_enabled}
                    disabled={isBusy || !favoritesSyncEnabled}
                    onChange={(checked) =>
                      updateConnection.mutate({ sync_favorite_removals_enabled: checked })
                    }
                  />
                ) : null}
                {connection.capabilities.import_watchlist ||
                connection.capabilities.export_watchlist ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-watchlist`}
                    label={t("web.settings.watchProviders.toggle.watchlist.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.watchlist.description",
                      name,
                    )}
                    checked={watchlistSyncEnabled}
                    disabled={isBusy}
                    onChange={(checked) =>
                      updateConnection.mutate({
                        import_watchlist_enabled: checked,
                        export_watchlist_enabled: checked,
                        sync_watchlist_removals_enabled: checked
                          ? connection.sync_watchlist_removals_enabled
                          : false,
                      })
                    }
                  />
                ) : null}
                {connection.capabilities.remove_watchlist ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-watchlist-removals`}
                    label={t("web.settings.watchProviders.toggle.watchlistRemovals.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.watchlistRemovals.description",
                    )}
                    checked={connection.sync_watchlist_removals_enabled}
                    disabled={isBusy || !watchlistSyncEnabled}
                    onChange={(checked) =>
                      updateConnection.mutate({ sync_watchlist_removals_enabled: checked })
                    }
                  />
                ) : null}
                {connection.capabilities.provides_watchlist_order ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-watchlist-order`}
                    label={t("web.settings.watchProviders.toggle.watchlistOrder.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.watchlistOrder.description",
                      name,
                    )}
                    checked={connection.sync_watchlist_order_enabled}
                    disabled={isBusy || !connection.import_watchlist_enabled}
                    onChange={(checked) =>
                      updateConnection.mutate({ sync_watchlist_order_enabled: checked })
                    }
                  />
                ) : null}
                {connection.capabilities.import_ratings ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-import-ratings`}
                    label={t("web.settings.watchProviders.toggle.importRatings.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.importRatings.description",
                      name,
                    )}
                    checked={connection.import_ratings_enabled}
                    disabled={isBusy}
                    onChange={(checked) =>
                      updateConnection.mutate({ import_ratings_enabled: checked })
                    }
                  />
                ) : null}
                {connection.capabilities.export_ratings ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-export-ratings`}
                    label={t("web.settings.watchProviders.toggle.exportRatings.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.exportRatings.description",
                      name,
                    )}
                    checked={connection.export_ratings_enabled}
                    disabled={isBusy}
                    onChange={(checked) =>
                      updateConnection.mutate({ export_ratings_enabled: checked })
                    }
                  />
                ) : null}
                {connection.capabilities.sync_dropped ? (
                  <ToggleRow
                    id={`watch-provider-${providerKey}-sync-dropped`}
                    label={t("web.settings.watchProviders.toggle.syncDropped.label")}
                    description={t(
                      "web.settings.watchProviders.toggle.syncDropped.description",
                      name,
                    )}
                    checked={connection.sync_dropped_enabled}
                    disabled={isBusy}
                    onChange={(checked) =>
                      updateConnection.mutate({ sync_dropped_enabled: checked })
                    }
                  />
                ) : null}
                <ToggleRow
                  id={`watch-provider-${providerKey}-scrobble`}
                  label={t("web.settings.watchProviders.toggle.scrobble.label")}
                  description={t("web.settings.watchProviders.toggle.scrobble.description")}
                  checked={connection.scrobble_enabled}
                  disabled={isBusy}
                  onChange={(checked) => updateConnection.mutate({ scrobble_enabled: checked })}
                />
                {connectionSettings
                  .filter((setting) => setting.type === "boolean")
                  .map((setting) => (
                    <ToggleRow
                      key={setting.key}
                      id={`watch-provider-${providerKey}-setting-${setting.key}`}
                      label={setting.label}
                      description={setting.description ?? ""}
                      checked={booleanSettingValue(connection.connection_settings, setting)}
                      disabled={isBusy}
                      onChange={(checked) =>
                        updateConnection.mutate({ connection_settings: { [setting.key]: checked } })
                      }
                    />
                  ))}
              </div>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

export default function WatchProvidersSettings() {
  const t = useT();
  const { data, isLoading } = useWatchProviders();

  if (isLoading) {
    return (
      <div className="space-y-4">
        <section className="surface-panel rounded-[1.7rem] border-0 px-4 py-5 shadow-none sm:px-6">
          <div className="bg-muted/60 h-5 w-40 animate-pulse rounded" />
          <div className="bg-muted/40 mt-4 h-20 animate-pulse rounded-xl" />
        </section>
      </div>
    );
  }

  const providers = data?.providers ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.watchProviders.title")}
        subtitle={t("web.settings.watchProviders.subtitle")}
      />

      {providers.length === 0 ? (
        <section className="surface-panel rounded-[1.7rem] border-0 px-4 py-5 shadow-none sm:px-6">
          <div className="text-muted-foreground text-sm">
            {t("web.settings.watchProviders.none")}
          </div>
        </section>
      ) : (
        providers.map((provider) => (
          <WatchProviderCard
            key={provider.key}
            providerKey={provider.key}
            connectionSettings={provider.connection_settings}
          />
        ))
      )}
    </div>
  );
}
