import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useProfiles } from "@/hooks/queries/profiles";
import {
  useCreateHistoryImportRun,
  useHistoryImportRun,
  useHistoryImportRuns,
  useHistoryImportSources,
  useLoginEmbyConnect,
} from "@/hooks/queries/history-import";
import type { EmbyConnectLoginResponse, HistoryImportRun } from "@/api/types";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  completePlexAuthentication,
  createPlexPin,
  buildPlexAuthURL,
  getPreferredPlexServerURL,
  type BrowserPlexServer,
} from "@/lib/plexAuth";
import {
  canStartEmbyImport,
  canStartJellyfinImport,
  type EmbyMode,
  type PlexMode,
  type SourceType,
} from "./HistoryImportSettings.utils";
import { cn } from "@/lib/utils";
import { AlertTriangle, CheckCircle2, CircleSlash2, Clock, Loader2, XCircle } from "lucide-react";
import { formatDate } from "@/lib/datetime";

const STATUS_CONFIG = {
  queued: {
    icon: Clock,
    colorClass: "text-info",
    bgClass: "bg-info/10 border-info/20",
    label: "Queued",
  },
  running: {
    icon: Loader2,
    colorClass: "text-warning",
    bgClass: "bg-warning/10 border-warning/20",
    label: "Running",
    spin: true,
  },
  completed: {
    icon: CheckCircle2,
    colorClass: "text-success",
    bgClass: "bg-success/10 border-success/20",
    label: "Completed",
  },
  failed: {
    icon: XCircle,
    colorClass: "text-destructive",
    bgClass: "bg-destructive/10 border-destructive/20",
    label: "Failed",
  },
  cancelled: {
    icon: CircleSlash2,
    colorClass: "text-muted-foreground",
    bgClass: "bg-muted border-border",
    label: "Cancelled",
  },
} as const;

export default function HistoryImportSettings() {
  useEventChannel("history_import");
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { profile } = useCurrentProfile();
  const { data: profiles = [] } = useProfiles();
  const { data: sources = [], isLoading: sourcesLoading } = useHistoryImportSources();
  const { data: recentRuns = [] } = useHistoryImportRuns();

  const [sourceType, setSourceType] = useState<SourceType>("emby");
  const [embyMode, setEmbyMode] = useState<EmbyMode>("connect");
  const [plexMode, setPlexMode] = useState<PlexMode>("oauth");
  const [profileId, setProfileId] = useState(profile?.id ?? "");
  const [activeRunId, setActiveRunId] = useState<string>();

  // Emby state
  const [connectUsername, setConnectUsername] = useState("");
  const [connectPassword, setConnectPassword] = useState("");
  const [connectSession, setConnectSession] = useState<EmbyConnectLoginResponse | null>(null);
  const [connectServerId, setConnectServerId] = useState("");
  const [savedSourceId, setSavedSourceId] = useState("");
  const [savedUsername, setSavedUsername] = useState("");
  const [savedPassword, setSavedPassword] = useState("");
  const [jellyfinServerURL, setJellyfinServerURL] = useState("");
  const [jellyfinUsername, setJellyfinUsername] = useState("");
  const [jellyfinPassword, setJellyfinPassword] = useState("");

  // Plex state
  const [plexServers, setPlexServers] = useState<BrowserPlexServer[]>([]);
  const [plexAccountToken, setPlexAccountToken] = useState("");
  const [plexServerId, setPlexServerId] = useState("");
  const [plexSavedSourceId, setPlexSavedSourceId] = useState("");
  const [plexToken, setPlexToken] = useState("");
  const [plexAuthPending, setPlexAuthPending] = useState(false);
  const [plexAuthError, setPlexAuthError] = useState<string | null>(null);

  const loginMutation = useLoginEmbyConnect();
  const createRunMutation = useCreateHistoryImportRun();
  const { data: activeRun } = useHistoryImportRun(activeRunId);

  const displayRun = activeRun ?? recentRuns[0] ?? null;
  const pending = loginMutation.isPending || createRunMutation.isPending || plexAuthPending;
  const effectiveProfileId = profileId || profile?.id || "";
  const returnedPlexAuth = searchParams.get("plex_auth");
  const returnedPlexPinId = searchParams.get("plex_pin_id");
  const returnedPlexPinCode = searchParams.get("plex_pin_code");

  const embySources = useMemo(() => sources.filter((s) => s.source_type === "emby"), [sources]);
  const plexSources = useMemo(() => sources.filter((s) => s.source_type === "plex"), [sources]);

  const effectiveSavedSourceId = savedSourceId || String(embySources[0]?.id ?? "");
  const effectivePlexSavedSourceId = plexSavedSourceId || String(plexSources[0]?.id ?? "");

  const selectedSavedSource = useMemo(
    () => embySources.find((source) => String(source.id) === effectiveSavedSourceId),
    [effectiveSavedSourceId, embySources],
  );
  const selectedPlexSavedSource = useMemo(
    () => plexSources.find((source) => String(source.id) === effectivePlexSavedSourceId),
    [effectivePlexSavedSourceId, plexSources],
  );
  const selectedPlexOAuthServer = useMemo(
    () => plexServers.find((server) => server.clientIdentifier === plexServerId),
    [plexServerId, plexServers],
  );
  const selectedPlexOAuthServerURL = selectedPlexOAuthServer
    ? getPreferredPlexServerURL(selectedPlexOAuthServer)
    : "";

  useEffect(() => {
    if (returnedPlexAuth !== "1") {
      return;
    }

    if (!returnedPlexPinId || !returnedPlexPinCode) {
      setPlexAuthError(
        "Plex sign-in returned without the expected session details. Please try again.",
      );
      void navigate("/settings/history-import", { replace: true });
      return;
    }

    const pinID = Number(returnedPlexPinId);
    if (!Number.isFinite(pinID) || pinID <= 0) {
      setPlexAuthError("Plex sign-in returned an invalid PIN. Please try again.");
      void navigate("/settings/history-import", { replace: true });
      return;
    }

    let cancelled = false;
    setSourceType("plex");
    setPlexMode("oauth");
    setPlexAuthPending(true);
    setPlexAuthError(null);

    void (async () => {
      try {
        const { accountToken, servers } = await completePlexAuthentication(
          pinID,
          returnedPlexPinCode,
        );
        if (cancelled) {
          return;
        }
        setPlexAccountToken(accountToken);
        setPlexServers(servers);
        setPlexServerId(servers[0]?.clientIdentifier ?? "");
      } catch (error) {
        if (cancelled) {
          return;
        }
        setPlexAccountToken("");
        setPlexServers([]);
        setPlexServerId("");
        setPlexAuthError(error instanceof Error ? error.message : "Failed to finish Plex sign-in");
      } finally {
        if (!cancelled) {
          setPlexAuthPending(false);
          void navigate("/settings/history-import", { replace: true });
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [navigate, returnedPlexAuth, returnedPlexPinCode, returnedPlexPinId]);

  async function handleConnectLogin() {
    const result = await loginMutation.mutateAsync({
      username: connectUsername,
      password: connectPassword,
    });
    setConnectSession(result);
    setConnectServerId(result.servers[0]?.server_id ?? "");
  }

  async function handlePlexLogin() {
    setPlexAuthPending(true);
    setPlexAuthError(null);
    setPlexServers([]);
    setPlexServerId("");

    try {
      const pin = await createPlexPin();
      const forwardURL = new URL("/settings/history-import", window.location.origin);
      forwardURL.searchParams.set("plex_auth", "1");
      forwardURL.searchParams.set("plex_pin_id", String(pin.id));
      forwardURL.searchParams.set("plex_pin_code", pin.code);
      window.location.assign(buildPlexAuthURL(pin.code, forwardURL.toString()));
    } catch (error) {
      setPlexAuthPending(false);
      setPlexAuthError(error instanceof Error ? error.message : "Failed to start Plex sign-in");
    }
  }

  async function handleStartImport() {
    if (!effectiveProfileId) return;

    if (sourceType === "emby") {
      if (embyMode === "connect") {
        const run = await createRunMutation.mutateAsync({
          profile_id: effectiveProfileId,
          source: "emby",
          connect_session_id: connectSession?.connect_session_id,
          server_id: connectServerId,
        });
        setActiveRunId(run.id);
      } else if (embyMode === "saved" && selectedSavedSource) {
        const run = await createRunMutation.mutateAsync({
          profile_id: effectiveProfileId,
          source: "emby",
          source_id: selectedSavedSource.id,
          username: savedUsername,
          password: savedPassword,
        });
        setActiveRunId(run.id);
      }
      return;
    }

    if (sourceType === "plex") {
      if (plexMode === "oauth" && selectedPlexOAuthServerURL && selectedPlexOAuthServer) {
        const run = await createRunMutation.mutateAsync({
          profile_id: effectiveProfileId,
          source: "plex",
          plex_base_url: selectedPlexOAuthServerURL,
          plex_token: selectedPlexOAuthServer.accessToken,
          plex_account_token: plexAccountToken || undefined,
        });
        setActiveRunId(run.id);
      } else if (plexMode === "saved" && selectedPlexSavedSource) {
        const run = await createRunMutation.mutateAsync({
          profile_id: effectiveProfileId,
          source: "plex",
          source_id: selectedPlexSavedSource.id,
          plex_token: plexToken,
        });
        setActiveRunId(run.id);
      }
      return;
    }

    if (sourceType === "jellyfin") {
      const run = await createRunMutation.mutateAsync({
        profile_id: effectiveProfileId,
        source: "jellyfin",
        jellyfin_base_url: jellyfinServerURL,
        jellyfin_username: jellyfinUsername,
        jellyfin_password: jellyfinPassword,
      });
      setActiveRunId(run.id);
      return;
    }
  }

  const canStart =
    sourceType === "emby"
      ? canStartEmbyImport(
          embyMode,
          effectiveProfileId,
          connectSession,
          connectServerId,
          selectedSavedSource,
        )
      : sourceType === "plex"
        ? plexMode === "oauth"
          ? !!effectiveProfileId && !!selectedPlexOAuthServer && !!selectedPlexOAuthServerURL
          : !!effectiveProfileId && !!selectedPlexSavedSource && !!plexToken
        : canStartJellyfinImport(
            effectiveProfileId,
            jellyfinServerURL,
            jellyfinUsername,
            jellyfinPassword,
          );

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">History import</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Import watch history from an external media server into a Silo profile.
        </p>
      </div>

      {/* ── New import ──────────────────────────────────── */}
      <SettingsGroup
        title="New import"
        description="Choose a source, sign in, and pick a profile to receive the watch history."
      >
        {/* Source cards */}
        <div className="grid gap-3 sm:grid-cols-3">
          <SourceCard
            type="emby"
            selected={sourceType === "emby"}
            onClick={() => setSourceType("emby")}
          />
          <SourceCard
            type="plex"
            selected={sourceType === "plex"}
            onClick={() => setSourceType("plex")}
          />
          <SourceCard
            type="jellyfin"
            selected={sourceType === "jellyfin"}
            onClick={() => setSourceType("jellyfin")}
          />
        </div>

        {/* ── Emby config ── */}
        {sourceType === "emby" && (
          <div className="space-y-4">
            <SegmentedControl
              value={embyMode}
              onChange={(v) => setEmbyMode(v as EmbyMode)}
              options={[
                { value: "connect", label: "Emby Connect" },
                { value: "saved", label: "Saved Server" },
              ]}
            />

            {embyMode === "connect" ? (
              <div className="space-y-4">
                <div className="grid gap-4 md:grid-cols-2">
                  <div className="space-y-2">
                    <Label>Email or Username</Label>
                    <Input
                      value={connectUsername}
                      onChange={(e) => setConnectUsername(e.target.value)}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>Password</Label>
                    <Input
                      type="password"
                      value={connectPassword}
                      onChange={(e) => setConnectPassword(e.target.value)}
                    />
                  </div>
                </div>
                <Button onClick={handleConnectLogin} disabled={loginMutation.isPending}>
                  {loginMutation.isPending ? "Connecting\u2026" : "Find Servers"}
                </Button>

                {connectSession && (
                  <div className="surface-panel-subtle space-y-3 rounded-[1.2rem] p-4">
                    <div className="flex items-center gap-2">
                      <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                      <span className="text-sm font-medium">Connected</span>
                    </div>
                    <div className="space-y-2">
                      <Label>Server</Label>
                      <Select value={connectServerId} onValueChange={setConnectServerId}>
                        <SelectTrigger>
                          <SelectValue placeholder="Choose a server" />
                        </SelectTrigger>
                        <SelectContent>
                          {connectSession.servers.map((server) => (
                            <SelectItem key={server.server_id} value={server.server_id}>
                              {server.name}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>
                )}
              </div>
            ) : (
              <div className="space-y-4">
                {sourcesLoading ? (
                  <div className="text-muted-foreground text-sm">Loading saved servers\u2026</div>
                ) : embySources.length === 0 ? (
                  <EmptyNotice>No admin-defined Emby servers are available yet.</EmptyNotice>
                ) : (
                  <>
                    <div className="space-y-2">
                      <Label>Saved Server</Label>
                      <Select value={effectiveSavedSourceId} onValueChange={setSavedSourceId}>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {embySources.map((source) => (
                            <SelectItem key={source.id} value={String(source.id)}>
                              {source.name}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="grid gap-4 md:grid-cols-2">
                      <div className="space-y-2">
                        <Label>Emby Username</Label>
                        <Input
                          value={savedUsername}
                          onChange={(e) => setSavedUsername(e.target.value)}
                        />
                      </div>
                      <div className="space-y-2">
                        <Label>Emby Password</Label>
                        <Input
                          type="password"
                          value={savedPassword}
                          onChange={(e) => setSavedPassword(e.target.value)}
                        />
                      </div>
                    </div>
                  </>
                )}
              </div>
            )}
          </div>
        )}

        {/* ── Plex config ── */}
        {sourceType === "plex" && (
          <div className="space-y-4">
            <SegmentedControl
              value={plexMode}
              onChange={(v) => setPlexMode(v as PlexMode)}
              options={[
                { value: "oauth", label: "Plex Account" },
                { value: "saved", label: "Saved Server" },
              ]}
            />

            {plexMode === "oauth" ? (
              <div className="space-y-4">
                {plexAuthError ? (
                  <div className="space-y-3 rounded-[1.2rem] border border-red-500/30 bg-red-500/[0.06] p-4 text-sm">
                    <div className="flex items-center gap-2 font-medium text-red-400">
                      <XCircle className="h-4 w-4" />
                      Plex sign-in failed
                    </div>
                    <p className="text-red-100/80">{plexAuthError}</p>
                    <Button onClick={handlePlexLogin} disabled={plexAuthPending} variant="outline">
                      {plexAuthPending ? "Starting\u2026" : "Try Again"}
                    </Button>
                  </div>
                ) : plexAuthPending ? (
                  <div className="surface-panel-subtle flex items-center gap-3 rounded-[1.2rem] p-4">
                    <Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
                    <div>
                      <div className="text-sm font-medium">Finishing Plex sign-in\u2026</div>
                      <p className="text-muted-foreground text-[13px]">
                        Continue in Plex, then you'll be returned here automatically.
                      </p>
                    </div>
                  </div>
                ) : plexServers.length === 0 ? (
                  <Button onClick={handlePlexLogin} disabled={plexAuthPending}>
                    {plexAuthPending ? "Starting\u2026" : "Sign in with Plex"}
                  </Button>
                ) : (
                  <div className="surface-panel-subtle space-y-4 rounded-[1.2rem] p-4">
                    <div className="flex items-center gap-2">
                      <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                      <span className="text-sm font-medium">Signed in</span>
                    </div>
                    <div className="space-y-2">
                      <Label>Server</Label>
                      <Select value={plexServerId} onValueChange={setPlexServerId}>
                        <SelectTrigger>
                          <SelectValue placeholder="Choose a server" />
                        </SelectTrigger>
                        <SelectContent>
                          {plexServers.map((server) => (
                            <SelectItem
                              key={server.clientIdentifier}
                              value={server.clientIdentifier}
                            >
                              {server.name}
                              {server.owned ? "" : " (shared)"}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <Button onClick={handlePlexLogin} disabled={plexAuthPending} variant="outline">
                      Reconnect Plex Account
                    </Button>
                  </div>
                )}
              </div>
            ) : (
              <div className="space-y-4">
                {sourcesLoading ? (
                  <div className="text-muted-foreground text-sm">Loading saved servers\u2026</div>
                ) : plexSources.length === 0 ? (
                  <EmptyNotice>No admin-defined Plex servers are available yet.</EmptyNotice>
                ) : (
                  <>
                    <div className="space-y-2">
                      <Label>Saved Server</Label>
                      <Select
                        value={effectivePlexSavedSourceId}
                        onValueChange={setPlexSavedSourceId}
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {plexSources.map((source) => (
                            <SelectItem key={source.id} value={String(source.id)}>
                              {source.name}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-2">
                      <Label>Plex Auth Token</Label>
                      <Input
                        type="password"
                        value={plexToken}
                        onChange={(e) => setPlexToken(e.target.value)}
                        placeholder="Your Plex authentication token"
                      />
                    </div>
                  </>
                )}
              </div>
            )}
          </div>
        )}

        {/* ── Jellyfin config ── */}
        {sourceType === "jellyfin" && (
          <div className="space-y-4">
            <div className="space-y-4">
              <div className="space-y-2">
                <Label>Jellyfin Server URL</Label>
                <Input
                  value={jellyfinServerURL}
                  onChange={(e) => setJellyfinServerURL(e.target.value)}
                  placeholder="https://jellyfin.example.com"
                />
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label>Jellyfin Username</Label>
                  <Input
                    value={jellyfinUsername}
                    onChange={(e) => setJellyfinUsername(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label>Jellyfin Password</Label>
                  <Input
                    type="password"
                    value={jellyfinPassword}
                    onChange={(e) => setJellyfinPassword(e.target.value)}
                  />
                </div>
              </div>
              <p className="text-muted-foreground text-xs leading-relaxed">
                Enter the base URL for the Jellyfin server you want to import from.
              </p>
            </div>
          </div>
        )}

        {/* Action row */}
        <div className="border-border/40 flex flex-col gap-4 border-t pt-4 sm:flex-row sm:items-end sm:justify-between">
          <div className="w-full space-y-2 sm:max-w-[280px]">
            <Label>Import into profile</Label>
            <Select value={effectiveProfileId} onValueChange={setProfileId}>
              <SelectTrigger>
                <SelectValue placeholder="Choose a profile" />
              </SelectTrigger>
              <SelectContent>
                {profiles.map((item) => (
                  <SelectItem key={item.id} value={item.id}>
                    {item.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button onClick={handleStartImport} disabled={!canStart || pending} className="sm:px-8">
            {createRunMutation.isPending ? "Starting\u2026" : "Start Import"}
          </Button>
        </div>
      </SettingsGroup>

      {/* ── Latest import ───────────────────────────────── */}
      <SettingsGroup
        title={activeRunId && activeRun ? "Selected import" : "Latest import"}
        description={
          activeRunId && activeRun
            ? "Details from the selected import run."
            : "Results from the most recent import run."
        }
      >
        <RunSummary run={displayRun} />
      </SettingsGroup>

      {/* ── Import history ──────────────────────────────── */}
      <SettingsGroup
        title="Import history"
        description="Select a past run to view its details above."
      >
        {recentRuns.length === 0 ? (
          <EmptyNotice>No imports have been started yet.</EmptyNotice>
        ) : (
          <div className="space-y-2">
            {recentRuns.map((run) => (
              <HistoryRunCard
                key={run.id}
                run={run}
                active={run.id === (activeRunId ?? recentRuns[0]?.id)}
                onClick={() => setActiveRunId(run.id)}
              />
            ))}
          </div>
        )}
      </SettingsGroup>
    </div>
  );
}

/* ────────────────────────────────────────────────────────────
   Sub-components
   ──────────────────────────────────────────────────────────── */

function SourceCard({
  type,
  selected,
  onClick,
}: {
  type: "emby" | "jellyfin" | "plex";
  selected: boolean;
  onClick: () => void;
}) {
  const isEmby = type === "emby";
  const isJellyfin = type === "jellyfin";
  const label = isEmby ? "Emby" : isJellyfin ? "Jellyfin" : "Plex";
  const description = isEmby
    ? "Emby Connect or direct server"
    : isJellyfin
      ? "Direct server URL + credentials"
      : "Plex account or direct server";
  const letter = isEmby ? "E" : isJellyfin ? "J" : "P";

  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "relative rounded-xl border p-4 text-left transition-all duration-200",
        selected
          ? isEmby
            ? "border-emerald-500/40 bg-emerald-500/[0.06]"
            : isJellyfin
              ? "border-cyan-500/40 bg-cyan-500/[0.06]"
              : "border-amber-500/40 bg-amber-500/[0.06]"
          : "border-border/40 hover:border-border/70 hover:bg-white/[0.02]",
      )}
    >
      <div className="flex items-center gap-3">
        <div
          className={cn(
            "flex h-10 w-10 items-center justify-center rounded-lg text-sm font-bold tracking-tight transition-colors",
            selected
              ? isEmby
                ? "bg-emerald-500/15 text-emerald-400"
                : isJellyfin
                  ? "bg-cyan-500/15 text-cyan-400"
                  : "bg-amber-500/15 text-amber-400"
              : "bg-muted text-muted-foreground",
          )}
        >
          {letter}
        </div>
        <div>
          <div className="text-sm font-medium">{label}</div>
          <div className="text-muted-foreground text-xs">{description}</div>
        </div>
      </div>
      {selected && (
        <div className="absolute top-3 right-3">
          <div
            className={cn(
              "h-2 w-2 rounded-full",
              isEmby ? "bg-emerald-400" : isJellyfin ? "bg-cyan-400" : "bg-amber-400",
            )}
          />
        </div>
      )}
    </button>
  );
}

function SegmentedControl({
  value,
  onChange,
  options,
}: {
  value: string;
  onChange: (value: string) => void;
  options: { value: string; label: string }[];
}) {
  return (
    <div className="surface-panel-subtle flex gap-1 rounded-[1.1rem] p-1">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          onClick={() => onChange(option.value)}
          className={cn(
            "flex-1 rounded-[0.85rem] px-3 py-1.5 text-sm font-medium transition-all duration-200",
            value === option.value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

function EmptyNotice({ children }: { children: React.ReactNode }) {
  return (
    <div className="surface-panel-subtle text-muted-foreground rounded-[1.2rem] px-4 py-6 text-center text-sm">
      {children}
    </div>
  );
}

function RunStatusIndicator({ status }: { status: HistoryImportRun["status"] }) {
  const config = STATUS_CONFIG[status];
  const Icon = config.icon;
  const shouldSpin = "spin" in config && config.spin;

  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium",
        config.bgClass,
        config.colorClass,
      )}
    >
      <Icon className={cn("h-3 w-3", shouldSpin && "animate-spin")} />
      {config.label}
    </span>
  );
}

function RunSummary({ run }: { run: HistoryImportRun | null }) {
  if (!run) {
    return (
      <div className="surface-panel-subtle flex flex-col items-center justify-center rounded-[1.2rem] py-10 text-center">
        <Clock className="text-muted-foreground/40 mb-3 h-8 w-8" />
        <p className="text-muted-foreground text-sm">
          Import summaries will appear here after you start a run.
        </p>
      </div>
    );
  }

  const processed = run.matched + run.unmatched + run.skipped;
  const progressPct = run.fetched > 0 ? Math.min(100, (processed / run.fetched) * 100) : 0;
  const isActive = run.status === "running" || run.status === "queued";

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-0.5">
          <div className="flex items-center gap-2.5">
            <span className="text-sm font-medium">
              {run.source_type === "emby"
                ? "Emby"
                : run.source_type === "jellyfin"
                  ? "Jellyfin"
                  : "Plex"}{" "}
              import
            </span>
            <RunStatusIndicator status={run.status} />
          </div>
          <div className="text-muted-foreground text-xs">
            {formatRelativeTime(run.created_at)}
            {run.completed_at && (
              <span> &middot; took {formatDuration(run.created_at, run.completed_at)}</span>
            )}
          </div>
        </div>
      </div>

      {/* Progress bar for active imports */}
      {isActive && run.fetched > 0 && (
        <div className="space-y-1.5">
          <div className="bg-muted h-1.5 overflow-hidden rounded-full">
            <div
              className="h-full rounded-full bg-amber-400 transition-all duration-700 ease-out"
              style={{ width: `${progressPct}%` }}
            />
          </div>
          <div className="text-muted-foreground text-xs tabular-nums">
            {processed} / {run.fetched} processed
          </div>
        </div>
      )}

      {/* Metrics */}
      <div className="grid grid-cols-3 gap-2 sm:grid-cols-6">
        <MetricCard label="Fetched" value={run.fetched} />
        <MetricCard label="Matched" value={run.matched} accent="positive" />
        <MetricCard label="Unmatched" value={run.unmatched} accent="warning" />
        <MetricCard label="Progress" value={run.progress_updated} accent="positive" />
        <MetricCard label="History" value={run.history_created} accent="positive" />
        <MetricCard label="Watchlist" value={run.watchlist_added} accent="positive" />
        <MetricCard label="Skipped" value={run.skipped} />
      </div>

      {/* Error */}
      {run.error_message && (
        <div className="flex items-start gap-2.5 rounded-[1rem] border border-red-500/30 bg-red-500/[0.06] px-3.5 py-3 text-sm">
          <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
          <span className="text-red-100/90">{run.error_message}</span>
        </div>
      )}

      {/* Warnings */}
      {run.warnings.length > 0 && (
        <div className="space-y-2">
          <Label className="text-sm font-medium">Warnings</Label>
          <div className="space-y-1.5">
            {run.warnings.map((warning) => (
              <div key={warning} className="flex items-start gap-2 text-sm">
                <AlertTriangle className="text-muted-foreground mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span className="text-muted-foreground">{warning}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Unmatched samples */}
      {run.unmatched_samples.length > 0 && (
        <div className="space-y-2">
          <Label className="text-sm font-medium">Unmatched examples</Label>
          <div className="space-y-2">
            {run.unmatched_samples.map((sample, index) => (
              <div
                key={`${sample.title}-${index}`}
                className="surface-panel-subtle rounded-[1rem] px-3.5 py-2.5 text-sm"
              >
                <div className="font-medium">
                  {sample.title}
                  {sample.year ? (
                    <span className="text-muted-foreground ml-1.5 font-normal">
                      ({sample.year})
                    </span>
                  ) : null}
                </div>
                <div className="text-muted-foreground mt-0.5 text-xs">
                  {sample.kind} &middot; {sample.reason}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function MetricCard({
  label,
  value,
  accent,
}: {
  label: string;
  value: number;
  accent?: "positive" | "warning";
}) {
  return (
    <div className="surface-panel-subtle rounded-[1rem] px-3 py-2.5">
      <div
        className={cn(
          "text-lg font-semibold tabular-nums",
          accent === "positive" && value > 0 && "text-emerald-400",
          accent === "warning" && value > 0 && "text-amber-400",
        )}
      >
        {value}
      </div>
      <div className="text-muted-foreground text-[11px] font-medium tracking-wide uppercase">
        {label}
      </div>
    </div>
  );
}

function HistoryRunCard({
  run,
  active,
  onClick,
}: {
  run: HistoryImportRun;
  active: boolean;
  onClick: () => void;
}) {
  const isEmby = run.source_type === "emby";
  const isJellyfin = run.source_type === "jellyfin";

  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center justify-between rounded-[1.2rem] px-4 py-3 text-left transition-all duration-200",
        active ? "surface-panel-subtle ring-1 ring-white/[0.08]" : "hover:surface-panel-subtle",
      )}
    >
      <div className="flex items-center gap-3">
        <div
          className={cn(
            "flex h-8 w-8 items-center justify-center rounded-lg text-xs font-bold",
            isEmby
              ? "bg-emerald-500/10 text-emerald-400"
              : isJellyfin
                ? "bg-cyan-500/10 text-cyan-400"
                : "bg-amber-500/10 text-amber-400",
          )}
        >
          {isEmby ? "E" : isJellyfin ? "J" : "P"}
        </div>
        <div>
          <div className="text-sm font-medium">
            {isEmby ? "Emby" : isJellyfin ? "Jellyfin" : "Plex"} import
          </div>
          <div className="text-muted-foreground text-xs">
            {formatRelativeTime(run.created_at)}
            {run.matched > 0 && <span> &middot; {run.matched} matched</span>}
          </div>
        </div>
      </div>
      <RunStatusIndicator status={run.status} />
    </button>
  );
}

/* ────────────────────────────────────────────────────────────
   Helpers
   ──────────────────────────────────────────────────────────── */

function formatRelativeTime(dateStr: string): string {
  const diffMs = Date.now() - new Date(dateStr).getTime();
  const minutes = Math.floor(diffMs / 60_000);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (minutes < 1) return "Just now";
  if (minutes < 60) return `${minutes}m ago`;
  if (hours < 24) return `${hours}h ago`;
  if (days < 7) return `${days}d ago`;
  return formatDate(dateStr);
}

function formatDuration(startStr: string, endStr: string): string {
  const seconds = Math.floor((new Date(endStr).getTime() - new Date(startStr).getTime()) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s > 0 ? `${m}m ${s}s` : `${m}m`;
}
