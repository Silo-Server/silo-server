import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { PageHeader } from "@/components/page/PageHeader";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useAuth } from "@/hooks/useAuth";
import { useProfiles } from "@/hooks/queries/profiles";
import {
  useCreateHistoryImportRun,
  useHistoryImportCapability,
  useHistoryImportRun,
  useHistoryImportRuns,
  type PersonalImportRun,
  useHistoryImportSources,
  useLoginEmbyConnect,
} from "@/hooks/queries/history-import";
import type { EmbyConnectLoginResponse } from "@/api/types";
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
  getPlexFallbackURLs,
  loadPlexHome,
  resolvePlexHomeUserServer,
  type BrowserPlexServer,
  type PlexHome,
  type PlexHomeUser,
} from "@/lib/plexAuth";
import {
  canStartEmbyImport,
  canStartJellyfinImport,
  type EmbyMode,
  type PlexMode,
  type SourceType,
} from "./HistoryImportSettings.utils";
import { cn } from "@/lib/utils";
import { isActingAdmin } from "@/lib/permissions";
import { AlertTriangle, CheckCircle2, CircleSlash2, Clock, Loader2, XCircle } from "lucide-react";
import { formatRelativeTime as formatRelativeTimeBase } from "@/lib/date";
import { useT, type Translate } from "@/i18n";

const STATUS_CONFIG = {
  queued: {
    icon: Clock,
    colorClass: "text-info",
    bgClass: "bg-info/10 border-info/20",
    labelKey: "web.settings.historyImport.status.queued",
  },
  running: {
    icon: Loader2,
    colorClass: "text-warning",
    bgClass: "bg-warning/10 border-warning/20",
    labelKey: "web.settings.historyImport.status.running",
    spin: true,
  },
  completed: {
    icon: CheckCircle2,
    colorClass: "text-success",
    bgClass: "bg-success/10 border-success/20",
    labelKey: "web.settings.historyImport.status.completed",
  },
  failed: {
    icon: XCircle,
    colorClass: "text-destructive",
    bgClass: "bg-destructive/10 border-destructive/20",
    labelKey: "web.settings.historyImport.status.failed",
  },
  canceling: {
    icon: Loader2,
    colorClass: "text-muted-foreground",
    bgClass: "bg-muted border-border",
    labelKey: "web.settings.historyImport.status.canceling",
    spin: true,
  },
  cancelled: {
    icon: CircleSlash2,
    colorClass: "text-muted-foreground",
    bgClass: "bg-muted border-border",
    labelKey: "web.settings.historyImport.status.cancelled",
  },
} as const;

const emptyPlexHome: PlexHome = { accountUUID: "", users: [] };

function plexHomeUserLabel(t: Translate, user: PlexHomeUser, accountUUID: string): string {
  if (user.uuid === accountUUID)
    return t("web.settings.historyImport.plexHome.userYou", { name: user.title });
  if (user.restricted)
    return t("web.settings.historyImport.plexHome.userManaged", { name: user.title });
  return user.title;
}

// Brand names and their card letters; not translated.
const SOURCE_NAMES: Record<SourceType, string> = {
  emby: "Emby",
  jellyfin: "Jellyfin",
  plex: "Plex",
};
const SOURCE_LETTERS: Record<SourceType, string> = { emby: "E", jellyfin: "J", plex: "P" };
const JELLYFIN_URL_EXAMPLE = "https://jellyfin.example.com";

export default function HistoryImportSettings() {
  const t = useT();
  useEventChannel("history_import");
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { profile } = useCurrentProfile();
  const { user } = useAuth();
  const { data: profiles = [] } = useProfiles();
  // The server lets the primary profile import into any profile on the
  // account, on an admin account too; every other profile, including a
  // non-primary profile on an admin account, imports only into itself.
  const canImportForOthers = isActingAdmin(user, profile) || profile?.is_primary === true;
  const profileNames = useMemo(
    () => new Map(profiles.map((item) => [item.id, item.name])),
    [profiles],
  );
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
  // Plex Home: plexAccountToken outlives a started run, so after one import
  // finishes the next Home member can be picked without signing in again.
  const [plexHome, setPlexHome] = useState<PlexHome>(emptyPlexHome);
  const [plexHomeUserId, setPlexHomeUserId] = useState("");
  const [plexHomeUserPin, setPlexHomeUserPin] = useState("");
  const [plexSwitchPending, setPlexSwitchPending] = useState(false);
  const [plexSwitchError, setPlexSwitchError] = useState<string | null>(null);
  // Counts Plex sign-ins so a Home lookup that finishes after a newer sign-in
  // (or a reconnect) is dropped.
  const plexHomeRequest = useRef(0);

  const loginMutation = useLoginEmbyConnect();
  const createRunMutation = useCreateHistoryImportRun();
  const {
    data: activeRun,
    error: runError,
    refetch: refreshRun,
  } = useHistoryImportRun(activeRunId ?? recentRuns[0]?.id);

  const displayRun = activeRun ?? recentRuns[0] ?? null;
  const pending =
    loginMutation.isPending || createRunMutation.isPending || plexAuthPending || plexSwitchPending;
  const effectiveProfileId = canImportForOthers
    ? profileId || profile?.id || ""
    : (profile?.id ?? "");
  const startError = createRunMutation.error;
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
  // Undefined means the signed-in account itself; no switch is needed.
  const selectedPlexHomeUser = plexHome.users.find(
    (user) => user.uuid === plexHomeUserId && user.uuid !== plexHome.accountUUID,
  );
  const selectedPlexOAuthServerURL = selectedPlexOAuthServer
    ? getPreferredPlexServerURL(selectedPlexOAuthServer)
    : "";
  const { data: importCapability } = useHistoryImportCapability();

  useEffect(() => {
    if (returnedPlexAuth !== "1") {
      return;
    }

    if (!returnedPlexPinId || !returnedPlexPinCode) {
      setPlexAuthError(t("web.settings.historyImport.plexError.missingSession"));
      void navigate("/settings/history-import", { replace: true });
      return;
    }

    const pinID = Number(returnedPlexPinId);
    if (!Number.isFinite(pinID) || pinID <= 0) {
      setPlexAuthError(t("web.settings.historyImport.plexError.invalidPin"));
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
        setPlexHomeUserPin("");
        // Home users are optional, so a slow lookup must not hold up importing
        // the signed-in account. It outlives this effect, which reruns once the
        // sign-in query is cleared, so it checks the request count instead.
        const request = ++plexHomeRequest.current;
        void loadPlexHome(accountToken).then((home) => {
          if (plexHomeRequest.current !== request) {
            return;
          }
          setPlexHome(home);
          setPlexHomeUserId(home.accountUUID);
        });
      } catch (error) {
        if (cancelled) {
          return;
        }
        setPlexAccountToken("");
        setPlexServers([]);
        setPlexServerId("");
        setPlexHome(emptyPlexHome);
        setPlexHomeUserId("");
        setPlexAuthError(
          error instanceof Error
            ? error.message
            : t("web.settings.historyImport.plexError.finishFailed"),
        );
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
  }, [navigate, returnedPlexAuth, returnedPlexPinCode, returnedPlexPinId, t]);

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
    plexHomeRequest.current += 1;
    setPlexHome(emptyPlexHome);
    setPlexHomeUserId("");
    setPlexHomeUserPin("");
    setPlexSwitchError(null);

    try {
      const pin = await createPlexPin();
      const forwardURL = new URL("/settings/history-import", window.location.origin);
      forwardURL.searchParams.set("plex_auth", "1");
      forwardURL.searchParams.set("plex_pin_id", String(pin.id));
      forwardURL.searchParams.set("plex_pin_code", pin.code);
      window.location.assign(buildPlexAuthURL(pin.code, forwardURL.toString()));
    } catch (error) {
      setPlexAuthPending(false);
      setPlexAuthError(
        error instanceof Error
          ? error.message
          : t("web.settings.historyImport.plexError.startFailed"),
      );
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
        // Starting a run consumes the Connect session; another import needs a
        // fresh sign-in.
        setConnectSession(null);
        setConnectServerId("");
        setConnectPassword("");
      } else if (embyMode === "saved" && selectedSavedSource) {
        const run = await createRunMutation.mutateAsync({
          profile_id: effectiveProfileId,
          source: "emby",
          source_id: selectedSavedSource.id,
          username: savedUsername.trim(),
          password: savedPassword,
        });
        setActiveRunId(run.id);
      }
      return;
    }

    if (sourceType === "plex") {
      if (plexMode === "oauth" && selectedPlexOAuthServerURL && selectedPlexOAuthServer) {
        let server = selectedPlexOAuthServer;
        let accountToken = plexAccountToken;
        if (selectedPlexHomeUser) {
          // Import as the Home member: their own server token and plex.tv
          // token, so the run reads their watch state and watchlist.
          setPlexSwitchError(null);
          setPlexSwitchPending(true);
          try {
            const resolved = await resolvePlexHomeUserServer(
              plexAccountToken,
              selectedPlexHomeUser,
              plexHomeUserPin.trim(),
              selectedPlexOAuthServer.clientIdentifier,
            );
            server = resolved.server;
            accountToken = resolved.accountToken;
          } catch (error) {
            setPlexSwitchError(
              error instanceof Error
                ? error.message
                : t("web.settings.historyImport.plexHome.switchFailed"),
            );
            return;
          } finally {
            setPlexSwitchPending(false);
          }
        }
        // The server races these and keeps the first that answers, so a server whose
        // preferred address is blocked by a reverse proxy still imports.
        const fallbackURLs = importCapability?.plex_connection_fallback
          ? getPlexFallbackURLs(server)
          : [];
        const run = await createRunMutation.mutateAsync({
          profile_id: effectiveProfileId,
          source: "plex",
          plex_base_url: getPreferredPlexServerURL(server),
          ...(fallbackURLs.length > 0 ? { plex_base_urls: fallbackURLs } : {}),
          plex_token: server.accessToken,
          plex_account_token: accountToken || undefined,
        });
        setActiveRunId(run.id);
        setPlexHomeUserPin("");
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
        jellyfin_base_url: jellyfinServerURL.trim(),
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
          savedUsername,
        )
      : sourceType === "plex"
        ? plexMode === "oauth"
          ? !!effectiveProfileId &&
            !!selectedPlexOAuthServer &&
            !!selectedPlexOAuthServerURL &&
            (!selectedPlexHomeUser?.protected || plexHomeUserPin.trim() !== "")
          : !!effectiveProfileId && !!selectedPlexSavedSource && !!plexToken
        : canStartJellyfinImport(
            effectiveProfileId,
            jellyfinServerURL,
            jellyfinUsername,
            jellyfinPassword,
          );

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.historyImport.title")}
        subtitle={t("web.settings.historyImport.subtitle")}
      />

      {/* ── New import ──────────────────────────────────── */}
      <SettingsGroup
        title={t("web.settings.historyImport.newImport.title")}
        description={t("web.settings.historyImport.newImport.description")}
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
                { value: "connect", label: t("web.settings.historyImport.mode.embyConnect") },
                { value: "saved", label: t("web.settings.historyImport.savedServer") },
              ]}
            />

            {embyMode === "connect" ? (
              <div className="space-y-4">
                <div className="grid gap-4 md:grid-cols-2">
                  <div className="space-y-2">
                    <Label>{t("web.settings.historyImport.emailOrUsername")}</Label>
                    <Input
                      value={connectUsername}
                      onChange={(e) => setConnectUsername(e.target.value)}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>{t("web.settings.historyImport.password")}</Label>
                    <Input
                      type="password"
                      value={connectPassword}
                      onChange={(e) => setConnectPassword(e.target.value)}
                    />
                  </div>
                </div>
                <Button onClick={handleConnectLogin} disabled={loginMutation.isPending}>
                  {loginMutation.isPending
                    ? t("web.settings.historyImport.connecting")
                    : t("web.settings.historyImport.findServers")}
                </Button>

                {connectSession && (
                  <div className="surface-panel-subtle space-y-3 rounded-[1.2rem] p-4">
                    <div className="flex items-center gap-2">
                      <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                      <span className="text-sm font-medium">
                        {t("web.settings.historyImport.connected")}
                      </span>
                    </div>
                    <div className="space-y-2">
                      <Label>{t("web.settings.historyImport.server")}</Label>
                      <Select value={connectServerId} onValueChange={setConnectServerId}>
                        <SelectTrigger>
                          <SelectValue placeholder={t("web.settings.historyImport.chooseServer")} />
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
                  <div className="text-muted-foreground text-sm">
                    {t("web.settings.historyImport.loadingSavedServers")}
                  </div>
                ) : embySources.length === 0 ? (
                  <EmptyNotice>{t("web.settings.historyImport.noEmbyServers")}</EmptyNotice>
                ) : (
                  <>
                    <div className="space-y-2">
                      <Label>{t("web.settings.historyImport.savedServer")}</Label>
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
                        <Label>{t("web.settings.historyImport.embyUsername")}</Label>
                        <Input
                          value={savedUsername}
                          onChange={(e) => setSavedUsername(e.target.value)}
                        />
                      </div>
                      <div className="space-y-2">
                        <Label>{t("web.settings.historyImport.embyPassword")}</Label>
                        <Input
                          type="password"
                          placeholder={t("web.settings.historyImport.embyPasswordPlaceholder")}
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
                { value: "oauth", label: t("web.settings.historyImport.mode.plexAccount") },
                { value: "saved", label: t("web.settings.historyImport.savedServer") },
              ]}
            />

            {plexMode === "oauth" ? (
              <div className="space-y-4">
                {plexAuthError ? (
                  <div className="space-y-3 rounded-[1.2rem] border border-red-500/30 bg-red-500/[0.06] p-4 text-sm">
                    <div className="flex items-center gap-2 font-medium text-red-400">
                      <XCircle className="h-4 w-4" />
                      {t("web.settings.historyImport.plexSignInFailed")}
                    </div>
                    <p className="text-red-100/80">{plexAuthError}</p>
                    <Button onClick={handlePlexLogin} disabled={plexAuthPending} variant="outline">
                      {plexAuthPending
                        ? t("web.settings.historyImport.starting")
                        : t("web.settings.historyImport.tryAgain")}
                    </Button>
                  </div>
                ) : plexAuthPending ? (
                  <div className="surface-panel-subtle flex items-center gap-3 rounded-[1.2rem] p-4">
                    <Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
                    <div>
                      <div className="text-sm font-medium">
                        {t("web.settings.historyImport.finishingPlexSignIn")}
                      </div>
                      <p className="text-muted-foreground text-[13px]">
                        {t("web.settings.historyImport.plexContinue")}
                      </p>
                    </div>
                  </div>
                ) : plexServers.length === 0 ? (
                  <Button onClick={handlePlexLogin} disabled={plexAuthPending}>
                    {plexAuthPending
                      ? t("web.settings.historyImport.starting")
                      : t("web.settings.historyImport.signInWithPlex")}
                  </Button>
                ) : (
                  <div className="surface-panel-subtle space-y-4 rounded-[1.2rem] p-4">
                    <div className="flex items-center gap-2">
                      <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                      <span className="text-sm font-medium">
                        {t("web.settings.historyImport.signedIn")}
                      </span>
                    </div>
                    <div className="space-y-2">
                      <Label>{t("web.settings.historyImport.server")}</Label>
                      <Select
                        value={plexServerId}
                        onValueChange={setPlexServerId}
                        disabled={pending}
                      >
                        <SelectTrigger>
                          <SelectValue placeholder={t("web.settings.historyImport.chooseServer")} />
                        </SelectTrigger>
                        <SelectContent>
                          {plexServers.map((server) => (
                            <SelectItem
                              key={server.clientIdentifier}
                              value={server.clientIdentifier}
                            >
                              {server.owned
                                ? server.name
                                : t("web.settings.historyImport.sharedServer", {
                                    name: server.name,
                                  })}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    {plexHome.users.length > 1 && (
                      <div className="space-y-2">
                        <Label>{t("web.settings.historyImport.plexHome.user")}</Label>
                        <Select
                          value={plexHomeUserId}
                          disabled={pending}
                          onValueChange={(value) => {
                            setPlexHomeUserId(value);
                            setPlexHomeUserPin("");
                            setPlexSwitchError(null);
                          }}
                        >
                          <SelectTrigger aria-label={t("web.settings.historyImport.plexHome.user")}>
                            <SelectValue
                              placeholder={t("web.settings.historyImport.plexHome.choose")}
                            />
                          </SelectTrigger>
                          <SelectContent>
                            {plexHome.users.map((user) => (
                              <SelectItem key={user.uuid} value={user.uuid}>
                                {plexHomeUserLabel(t, user, plexHome.accountUUID)}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <p className="text-muted-foreground text-xs leading-relaxed">
                          {t("web.settings.historyImport.plexHome.hint")}
                        </p>
                      </div>
                    )}
                    {selectedPlexHomeUser?.protected && (
                      <div className="space-y-2">
                        <Label htmlFor="plex-home-user-pin">
                          {t("web.settings.historyImport.plexHome.pin", {
                            name: selectedPlexHomeUser.title,
                          })}
                        </Label>
                        <Input
                          id="plex-home-user-pin"
                          type="password"
                          inputMode="numeric"
                          autoComplete="off"
                          value={plexHomeUserPin}
                          onChange={(e) => setPlexHomeUserPin(e.target.value)}
                          disabled={pending}
                        />
                      </div>
                    )}
                    <Button onClick={handlePlexLogin} disabled={plexAuthPending} variant="outline">
                      {t("web.settings.historyImport.reconnectPlex")}
                    </Button>
                  </div>
                )}
              </div>
            ) : (
              <div className="space-y-4">
                {sourcesLoading ? (
                  <div className="text-muted-foreground text-sm">
                    {t("web.settings.historyImport.loadingSavedServers")}
                  </div>
                ) : plexSources.length === 0 ? (
                  <EmptyNotice>{t("web.settings.historyImport.noPlexServers")}</EmptyNotice>
                ) : (
                  <>
                    <div className="space-y-2">
                      <Label>{t("web.settings.historyImport.savedServer")}</Label>
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
                      <Label>{t("web.settings.historyImport.plexToken")}</Label>
                      <Input
                        type="password"
                        value={plexToken}
                        onChange={(e) => setPlexToken(e.target.value)}
                        placeholder={t("web.settings.historyImport.plexTokenPlaceholder")}
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
                <Label>{t("web.settings.historyImport.jellyfinServerUrl")}</Label>
                <Input
                  value={jellyfinServerURL}
                  onChange={(e) => setJellyfinServerURL(e.target.value)}
                  placeholder={JELLYFIN_URL_EXAMPLE}
                />
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label>{t("web.settings.historyImport.jellyfinUsername")}</Label>
                  <Input
                    value={jellyfinUsername}
                    onChange={(e) => setJellyfinUsername(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{t("web.settings.historyImport.jellyfinPassword")}</Label>
                  <Input
                    type="password"
                    value={jellyfinPassword}
                    onChange={(e) => setJellyfinPassword(e.target.value)}
                  />
                </div>
              </div>
              <p className="text-muted-foreground text-xs leading-relaxed">
                {t("web.settings.historyImport.jellyfinHelp")}
              </p>
            </div>
          </div>
        )}

        {/* Action row */}
        <div className="border-border/40 flex flex-col gap-4 border-t pt-4 sm:flex-row sm:items-end sm:justify-between">
          <div className="w-full space-y-2 sm:max-w-[280px]">
            <Label>{t("web.settings.historyImport.importIntoProfile")}</Label>
            {canImportForOthers ? (
              <Select value={effectiveProfileId} onValueChange={setProfileId} disabled={pending}>
                <SelectTrigger aria-label={t("web.settings.historyImport.importIntoProfile")}>
                  <SelectValue placeholder={t("web.settings.historyImport.chooseProfile")} />
                </SelectTrigger>
                <SelectContent>
                  {profiles.map((item) => (
                    <SelectItem key={item.id} value={item.id}>
                      {item.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <p className="text-muted-foreground text-sm">
                {profile?.name
                  ? t("web.settings.historyImport.ownProfileOnlyNamed", { name: profile.name })
                  : t("web.settings.historyImport.ownProfileOnly")}
              </p>
            )}
          </div>
          <Button
            onClick={() => void handleStartImport().catch(() => undefined)}
            disabled={!canStart || pending}
            className="sm:px-8"
          >
            {createRunMutation.isPending || plexSwitchPending
              ? t("web.settings.historyImport.starting")
              : t("web.settings.historyImport.startImport")}
          </Button>
        </div>
        {startError && (
          <p role="alert" className="text-destructive text-sm">
            {/* The server's reason is shown as is: a profile that can't import for
                others only ever targets itself, so its 403 is some other refusal
                (demo mode, say), and the cross-profile refusal already explains itself. */}
            {startError.message}
          </p>
        )}
        {plexSwitchError && sourceType === "plex" && (
          <p role="alert" className="text-destructive text-sm">
            {plexSwitchError}
          </p>
        )}
      </SettingsGroup>

      {/* ── Latest import ───────────────────────────────── */}
      <SettingsGroup
        title={
          activeRunId && activeRun
            ? t("web.settings.historyImport.selectedImport.title")
            : t("web.settings.historyImport.latestImport.title")
        }
        description={
          activeRunId && activeRun
            ? t("web.settings.historyImport.selectedImport.description")
            : t("web.settings.historyImport.latestImport.description")
        }
      >
        {runError && (
          <div role="alert" className="text-destructive mb-3 text-sm">
            <p>{t("web.settings.historyImport.refreshFailed")}</p>
            <Button variant="outline" size="sm" onClick={() => void refreshRun()}>
              {t("web.settings.historyImport.refreshStatus")}
            </Button>
          </div>
        )}
        <RunSummary run={displayRun} />
      </SettingsGroup>

      {/* ── Import history ──────────────────────────────── */}
      <SettingsGroup
        title={t("web.settings.historyImport.history.title")}
        description={t("web.settings.historyImport.history.description")}
      >
        {recentRuns.length === 0 ? (
          <EmptyNotice>{t("web.settings.historyImport.history.empty")}</EmptyNotice>
        ) : (
          <div className="space-y-2">
            {recentRuns.map((run) => (
              <HistoryRunCard
                key={run.id}
                run={run}
                profileName={canImportForOthers ? profileNames.get(run.profile_id) : undefined}
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
  const t = useT();
  const isEmby = type === "emby";
  const isJellyfin = type === "jellyfin";
  const label = SOURCE_NAMES[type];
  const description = t(`web.settings.historyImport.source.${type}`);
  const letter = SOURCE_LETTERS[type];

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

function RunStatusIndicator({ status }: { status: PersonalImportRun["status"] }) {
  const t = useT();
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
      {t(config.labelKey)}
    </span>
  );
}

function RunSummary({ run }: { run: PersonalImportRun | null }) {
  const t = useT();
  if (!run) {
    return (
      <div className="surface-panel-subtle flex flex-col items-center justify-center rounded-[1.2rem] py-10 text-center">
        <Clock className="text-muted-foreground/40 mb-3 h-8 w-8" />
        <p className="text-muted-foreground text-sm">
          {t("web.settings.historyImport.summary.empty")}
        </p>
      </div>
    );
  }

  // Skipped items were matched first, so they are already in run.matched.
  const processed = run.matched + run.unmatched;
  const isActive = run.status === "running" || run.status === "queued";

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-0.5">
          <div className="flex items-center gap-2.5">
            <span className="text-sm font-medium">
              {t("web.settings.historyImport.runTitle", {
                source: SOURCE_NAMES[sourceKind(run.source_type)],
              })}
            </span>
            <RunStatusIndicator status={run.status} />
          </div>
          <div className="text-muted-foreground text-xs">
            {run.completed_at
              ? t("web.settings.historyImport.timeWithDuration", {
                  time: formatRelativeTime(t, run.created_at),
                  duration: formatDuration(t, run.created_at, run.completed_at),
                  detail: (chunks) => <span>{chunks}</span>,
                })
              : formatRelativeTime(t, run.created_at)}
          </div>
        </div>
      </div>

      {/* Progress bar for active imports */}
      {isActive && run.fetched > 0 && (
        <div className="space-y-1.5">
          <div className="bg-muted h-1.5 overflow-hidden rounded-full">
            <div className="h-full w-1/3 animate-pulse rounded-full bg-amber-400" />
          </div>
          <div className="text-muted-foreground text-xs tabular-nums">
            {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- reworded on main; its catalog key is a follow-up */}
            {processed.toLocaleString()} processed so far
          </div>
        </div>
      )}

      {/* Metrics */}
      <div className="grid grid-cols-3 gap-2 sm:grid-cols-7">
        <MetricCard label={t("web.settings.historyImport.metric.fetched")} value={run.fetched} />
        <MetricCard
          label={t("web.settings.historyImport.metric.matched")}
          value={run.matched}
          accent="positive"
        />
        <MetricCard
          label={t("web.settings.historyImport.metric.unmatched")}
          value={run.unmatched}
          accent="warning"
        />
        <MetricCard
          label={t("web.settings.historyImport.metric.progress")}
          value={run.progress_updated}
          accent="positive"
        />
        <MetricCard
          label={t("web.settings.historyImport.metric.history")}
          value={run.history_created}
          accent="positive"
        />
        <MetricCard
          label={t("web.settings.historyImport.metric.watchlist")}
          value={run.watchlist_added}
          accent="positive"
        />
        <MetricCard
          label={t("web.settings.historyImport.metric.favorites")}
          value={run.favorites_imported}
          accent="positive"
        />
        <MetricCard label={t("web.settings.historyImport.metric.skipped")} value={run.skipped} />
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
          <Label className="text-sm font-medium">{t("web.settings.historyImport.warnings")}</Label>
          <div className="space-y-1.5">
            {run.warnings.map((warning, index) => (
              <div key={`${index}-${warning}`} className="flex items-start gap-2 text-sm">
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
          <Label className="text-sm font-medium">
            {t("web.settings.historyImport.unmatchedExamples")}
          </Label>
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
                      {t("web.settings.historyImport.sampleYear", { year: String(sample.year) })}
                    </span>
                  ) : null}
                </div>
                <div className="text-muted-foreground mt-0.5 text-xs">
                  {t("web.settings.historyImport.sampleDetail", {
                    kind: sample.kind,
                    reason: sample.reason,
                  })}
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
      <div className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
        {label}
      </div>
    </div>
  );
}

function HistoryRunCard({
  run,
  profileName,
  active,
  onClick,
}: {
  run: PersonalImportRun;
  /** The profile the run imported into, shown to those who import for several. */
  profileName?: string;
  active: boolean;
  onClick: () => void;
}) {
  const t = useT();
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
          {SOURCE_LETTERS[sourceKind(run.source_type)]}
        </div>
        <div>
          <div className="text-sm font-medium">
            {profileName
              ? t("web.settings.historyImport.runTitleForProfile", {
                  source: SOURCE_NAMES[sourceKind(run.source_type)],
                  profile: profileName,
                  muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
                })
              : t("web.settings.historyImport.runTitle", {
                  source: SOURCE_NAMES[sourceKind(run.source_type)],
                })}
          </div>
          <div className="text-muted-foreground text-xs">
            {run.matched > 0
              ? t("web.settings.historyImport.timeWithMatched", {
                  time: formatRelativeTime(t, run.created_at),
                  count: String(run.matched),
                  detail: (chunks) => <span>{chunks}</span>,
                })
              : formatRelativeTime(t, run.created_at)}
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

/** A run's source as a card type; anything unexpected shows as Plex, as before. */
function sourceKind(source: string): SourceType {
  return source === "emby" || source === "jellyfin" ? source : "plex";
}

function formatRelativeTime(t: Translate, dateStr: string): string {
  return (
    formatRelativeTimeBase(dateStr, {
      rounding: "floor",
      justNowLabel: t("web.settings.historyImport.justNow"),
      absoluteAfterDays: 7,
    }) ?? ""
  );
}

function formatDuration(t: Translate, startStr: string, endStr: string): string {
  const seconds = Math.floor((new Date(endStr).getTime() - new Date(startStr).getTime()) / 1000);
  if (seconds < 60)
    return t("web.settings.historyImport.duration.seconds", { seconds: String(seconds) });
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s > 0
    ? t("web.settings.historyImport.duration.minutesSeconds", {
        minutes: String(m),
        seconds: String(s),
      })
    : t("web.settings.historyImport.duration.minutes", { minutes: String(m) });
}
