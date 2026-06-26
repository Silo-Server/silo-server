import { useState } from "react";
import {
  AlertCircle,
  Check,
  CheckCircle2,
  Copy,
  ExternalLink,
  Loader2,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { ApiClientError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
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
  type WatchProviderSyncRun,
} from "@/hooks/queries/watchProviders";
import { Input } from "@/components/ui/input";

function formatRelativeTime(value?: string) {
  if (!value) return "Never";
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return "Never";
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
  if (seconds < 60) return "Just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function formatRetryAfter(seconds?: number) {
  if (!seconds || seconds <= 0) return "";
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes > 0 ? `${hours}h ${remainingMinutes}m` : `${hours}h`;
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
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[11px] font-medium ring-1 ring-inset",
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

function StatCell({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground text-[10px] font-medium tracking-wider uppercase">
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
  return (
    <div className="border-primary/30 bg-primary/5 rounded-xl border border-dashed p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium">Copy this code first</div>
          <div className="text-muted-foreground mt-0.5 text-xs leading-snug">
            {displayName} will ask for it after the activation page opens.
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onCancel}
          aria-label="Cancel activation"
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
          {codeCopied ? "Copied" : "Copy code"}
        </Button>
      </div>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row">
        <Button type="button" variant="outline" size="sm" asChild className="sm:flex-1">
          <a href={session.verification_url} target="_blank" rel="noreferrer">
            <ExternalLink className="h-4 w-4" />
            Open {displayName} activation
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
          I&apos;ve entered it
        </Button>
      </div>
    </div>
  );
}

function APIKeyBlock({
  displayName,
  pending,
  onSubmit,
  onCancel,
}: {
  displayName: string;
  pending: boolean;
  onSubmit: (apiKey: string) => void;
  onCancel: () => void;
}) {
  const [value, setValue] = useState("");
  const trimmed = value.trim();

  return (
    <div className="border-primary/30 bg-primary/5 rounded-xl border border-dashed p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium">Paste your {displayName} API key</div>
          <div className="text-muted-foreground mt-0.5 text-xs leading-snug">
            Find it under your account settings on the {displayName} site.
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onCancel}
          aria-label="Cancel connection"
        >
          <X className="h-4 w-4" />
        </Button>
      </div>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-stretch">
        <Input
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder="API key"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          className="font-mono"
        />
        <Button
          type="button"
          size="sm"
          disabled={pending || trimmed.length === 0}
          onClick={() => onSubmit(trimmed)}
          className="sm:flex-none"
        >
          {pending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}
          Connect
        </Button>
      </div>
    </div>
  );
}

interface ConnectedRunInfo {
  imported: { watched: number; progress: number; favorites: number; watchlist: number };
  exported: {
    watched: number;
    favorites: number;
    favoriteRemovals: number;
    watchlist: number;
    watchlistRemovals: number;
  };
  errorMessage?: string;
  errorHint?: string;
}

function deriveRunInfo(
  connection: WatchProviderConnection,
  latestRun: WatchProviderSyncRun | undefined,
): ConnectedRunInfo {
  const imported = {
    watched: latestRun?.inbound_watched_imported ?? 0,
    progress: latestRun?.inbound_progress_imported ?? 0,
    favorites: latestRun?.inbound_favorites_imported ?? 0,
    watchlist: latestRun?.inbound_watchlist_imported ?? 0,
  };
  const exported = {
    watched: latestRun?.outbound_sent ?? 0,
    favorites: latestRun?.outbound_favorites_sent ?? 0,
    favoriteRemovals: latestRun?.favorite_removals_sent ?? 0,
    watchlist: latestRun?.outbound_watchlist_sent ?? 0,
    watchlistRemovals: latestRun?.watchlist_removals_sent ?? 0,
  };
  let errorMessage: string | undefined;
  let errorHint: string | undefined;
  if (latestRun?.status === "failed" && latestRun.error) {
    errorMessage = latestRun.error;
    errorHint = `Failed ${formatRelativeTime(latestRun.completed_at ?? latestRun.started_at)}.`;
  } else if (connection.last_error) {
    errorMessage = connection.last_error;
    errorHint = connection.last_scrobble_error_at
      ? `Last seen ${formatRelativeTime(connection.last_scrobble_error_at)}.`
      : undefined;
  }
  return { imported, exported, errorMessage, errorHint };
}

function formatLastSync(connection: WatchProviderConnection, latestRun?: WatchProviderSyncRun) {
  const candidates = [
    latestRun?.completed_at,
    connection.last_inbound_sync_at,
    connection.last_progress_sync_at,
    connection.last_outbound_sync_at,
    connection.last_favorites_sync_at,
    connection.last_watchlist_sync_at,
  ].filter((value): value is string => Boolean(value));
  if (candidates.length === 0) return "Never synced";
  const newest = candidates
    .map((value) => new Date(value).getTime())
    .reduce((a, b) => Math.max(a, b), 0);
  if (!Number.isFinite(newest) || newest <= 0) return "Never synced";
  return `Synced ${formatRelativeTime(new Date(newest).toISOString())}`;
}

function WatchProviderCard({ providerKey }: { providerKey: string }) {
  const { data: connection, isLoading } = useWatchProviderConnection(providerKey);
  const updateConnection = useUpdateWatchProviderConnection(providerKey);
  const startAuth = useStartWatchProviderDeviceAuth(providerKey);
  const pollAuth = usePollWatchProviderDeviceAuth(providerKey);
  const connectAPIKey = useConnectWatchProviderAPIKey(providerKey);
  const deleteConnection = useDeleteWatchProviderConnection(providerKey);
  const syncNow = useTriggerWatchProviderSync(providerKey);
  const { data: syncRunsData } = useWatchProviderSyncRuns(
    providerKey,
    Boolean(connection?.connected),
  );
  const [authSession, setAuthSession] = useState<DeviceAuthSession | null>(null);
  const [codeCopied, setCodeCopied] = useState(false);
  const [apiKeyPrompt, setApiKeyPrompt] = useState(false);

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
    syncNow.error instanceof ApiClientError && syncNow.error.status === 429
      ? syncNow.error.details?.retry_after_seconds
      : undefined;
  const syncDisabled = syncNow.isPending || syncRunning || Boolean(cooldownSeconds);
  const syncButtonLabel = syncRunning
    ? "Syncing..."
    : cooldownSeconds
      ? `Cooldown ${formatRetryAfter(cooldownSeconds)}`
      : "Sync now";

  const isBusy =
    updateConnection.isPending ||
    startAuth.isPending ||
    pollAuth.isPending ||
    connectAPIKey.isPending ||
    deleteConnection.isPending;
  const displayName = connection.display_name;
  const usesAPIKey = connection.auth_method === WatchProviderAuthMethod.APIKey;
  const showAuth = Boolean(authSession) && !connection.connected;
  const showAPIKey = usesAPIKey && apiKeyPrompt && !connection.connected;
  const runInfo = connection.connected ? deriveRunInfo(connection, latestRun) : null;
  const hasError = Boolean(runInfo?.errorMessage);
  const favoritesSyncEnabled =
    connection.import_favorites_enabled || connection.export_favorites_enabled;
  const watchlistSyncEnabled =
    connection.import_watchlist_enabled || connection.export_watchlist_enabled;

  let statusVariant: StatusVariant;
  let statusLabel: string;
  if (showAuth || showAPIKey) {
    statusVariant = "pending";
    statusLabel = usesAPIKey ? "API key required" : "Activation pending";
  } else if (connection.connected && hasError) {
    statusVariant = "error";
    statusLabel = "Sync error";
  } else if (connection.connected) {
    statusVariant = "connected";
    statusLabel = "Connected";
  } else {
    statusVariant = "disconnected";
    statusLabel = "Not connected";
  }

  const subtitleText = (() => {
    if (showAuth) return "Waiting for you to enter the code below.";
    if (showAPIKey) return `Paste your ${displayName} API key to finish connecting.`;
    if (connection.connected) {
      const username = connection.provider_username || displayName;
      return `${username} · ${formatLastSync(connection, latestRun)}`;
    }
    if (!connection.credentials_configured) return "Server credentials required.";
    if (usesAPIKey)
      return `Connect with your ${displayName} API key to import watch history and scrobble playback.`;
    return "Connect to start importing watch history and scrobbling playback.";
  })();

  const handleCopyCode = async () => {
    if (!authSession) return;
    try {
      await navigator.clipboard.writeText(authSession.user_code);
      setCodeCopied(true);
      toast.success(`${displayName} code copied`);
    } catch {
      toast.error(`Failed to copy ${displayName} code`);
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

  const handleSubmitAPIKey = (apiKey: string) => {
    connectAPIKey.mutate(apiKey, {
      onSuccess: () => {
        setApiKeyPrompt(false);
      },
    });
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
                  if (window.confirm(`Disconnect ${displayName}?`)) {
                    deleteConnection.mutate();
                  }
                }}
                className="flex-1 sm:flex-none"
              >
                <Trash2 className="h-4 w-4" />
                <span className="sm:inline">Disconnect</span>
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
              Connect
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
                label="Last imported"
                value={`${runInfo.imported.watched.toLocaleString()} watched · ${runInfo.imported.progress.toLocaleString()} progress · ${runInfo.imported.favorites.toLocaleString()} favorites · ${runInfo.imported.watchlist.toLocaleString()} watchlist`}
              />
              <StatCell
                label="Last exported"
                value={`${(runInfo.exported.watched + runInfo.exported.favorites + runInfo.exported.favoriteRemovals + runInfo.exported.watchlist + runInfo.exported.watchlistRemovals).toLocaleString()} sent`}
              />
            </div>
            <div className="hidden grid-cols-5 gap-3 sm:grid">
              <StatCell
                label="Watched"
                value={`${runInfo.imported.watched.toLocaleString()} imported`}
              />
              <StatCell
                label="Progress"
                value={`${runInfo.imported.progress.toLocaleString()} resumed`}
              />
              <StatCell
                label="Favorites"
                value={`${runInfo.imported.favorites.toLocaleString()} imported`}
              />
              <StatCell
                label="Watchlist"
                value={`${runInfo.imported.watchlist.toLocaleString()} imported`}
              />
              <StatCell
                label="Exported"
                value={`${(runInfo.exported.watched + runInfo.exported.favorites + runInfo.exported.favoriteRemovals + runInfo.exported.watchlist + runInfo.exported.watchlistRemovals).toLocaleString()} sent`}
              />
            </div>
          </div>

          <div className="grid grid-cols-1 gap-x-6 gap-y-1 sm:grid-cols-2">
            <ToggleRow
              id={`watch-provider-${providerKey}-import-watched`}
              label="Import watched history"
              description={`Bring completed ${displayName} plays into this profile.`}
              checked={connection.import_watched_enabled}
              disabled={isBusy}
              onChange={(checked) => updateConnection.mutate({ import_watched_enabled: checked })}
            />
            <ToggleRow
              id={`watch-provider-${providerKey}-import-progress`}
              label="Import paused progress"
              description={`Use newer ${displayName} resume points when local progress is older.`}
              checked={connection.import_progress_enabled}
              disabled={isBusy}
              onChange={(checked) => updateConnection.mutate({ import_progress_enabled: checked })}
            />
            <ToggleRow
              id={`watch-provider-${providerKey}-export-watched`}
              label="Send watched changes"
              description="Send local watched marks and completed plays to this provider."
              checked={connection.export_watched_enabled}
              disabled={isBusy}
              onChange={(checked) => updateConnection.mutate({ export_watched_enabled: checked })}
            />
            <ToggleRow
              id={`watch-provider-${providerKey}-export-unwatched`}
              label="Send unwatched changes"
              description="When you mark something unwatched, remove matching history from this provider."
              checked={connection.export_unwatched_enabled}
              disabled={isBusy}
              onChange={(checked) => updateConnection.mutate({ export_unwatched_enabled: checked })}
            />
            {connection.capabilities.import_favorites ||
            connection.capabilities.export_favorites ? (
              <ToggleRow
                id={`watch-provider-${providerKey}-favorites`}
                label="Sync favorites"
                description={`Import ${displayName} movie and show favorites, and send local favorite adds.`}
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
                label="Sync favorite removals"
                description="Remove provider-synced favorites on the other side when they are explicitly unfavorited."
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
                label="Sync watchlist"
                description={`Import your ${displayName} watchlist, and send local watchlist adds.`}
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
                label="Sync watchlist removals"
                description="Remove provider-synced watchlist items on the other side when they are removed locally."
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
                label="Mirror watchlist order"
                description={`Order your Silo watchlist to match its ${displayName} sort order. Items not on ${displayName} stay at the bottom.`}
                checked={connection.sync_watchlist_order_enabled}
                disabled={isBusy || !connection.import_watchlist_enabled}
                onChange={(checked) =>
                  updateConnection.mutate({ sync_watchlist_order_enabled: checked })
                }
              />
            ) : null}
            <ToggleRow
              id={`watch-provider-${providerKey}-scrobble`}
              label="Scrobble playback"
              description="Report starts, pauses, resumes, and stops live during playback."
              checked={connection.scrobble_enabled}
              disabled={isBusy}
              onChange={(checked) => updateConnection.mutate({ scrobble_enabled: checked })}
            />
          </div>
        </div>
      ) : null}
    </section>
  );
}

export default function WatchProvidersSettings() {
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
      <div className="space-y-2">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Watch Providers</h2>
        <p className="text-muted-foreground text-[13px] leading-relaxed sm:text-sm">
          Connect external trackers to import watch history, sync paused progress, and scrobble
          playback in real time.
        </p>
      </div>

      {providers.length === 0 ? (
        <section className="surface-panel rounded-[1.7rem] border-0 px-4 py-5 shadow-none sm:px-6">
          <div className="text-muted-foreground text-sm">
            No watch providers are registered on this server.
          </div>
        </section>
      ) : (
        providers.map((provider) => (
          <WatchProviderCard key={provider.key} providerKey={provider.key} />
        ))
      )}
    </div>
  );
}
