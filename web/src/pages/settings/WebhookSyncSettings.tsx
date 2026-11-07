import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate, useSearchParams } from "react-router";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  Copy,
  ExternalLink,
  Loader2,
  Plus,
  RotateCcw,
  Search,
  Server,
  Trash2,
} from "lucide-react";
import { PageHeader } from "@/components/page/PageHeader";
import { useProfiles } from "@/hooks/queries/profiles";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import type {
  WebhookSyncConnection,
  WebhookSyncDiscoveredUser,
  WebhookSyncEventLog,
  WebhookSyncProfileMapping,
} from "@/api/types";
import {
  useCreateWebhookSyncConnection,
  useDeleteWebhookSyncConnection,
  useRotateWebhookSyncWebhook,
  useUpdateWebhookSyncConnection,
  useUpdateWebhookSyncProfileMappings,
  useWebhookSyncConnections,
  useWebhookSyncEvents,
  useWebhookSyncProfileMappings,
} from "@/hooks/queries/webhook-sync";
import {
  buildPlexAuthURL,
  completePlexAuthentication,
  createPlexPin,
  getPreferredPlexServerURL,
  type BrowserPlexServer,
} from "@/lib/plexAuth";
import { formatLocale } from "@/lib/appLocale";
import { useT, type MessageKey, type Translate } from "@/i18n";

const UNMAPPED_VALUE = "__unmapped__";
/** Item cell of a delivery with no matched item: punctuation, not text. */
const EMPTY_CELL = "\u2014";

const JELLYFIN_TEMPLATE = `{
  "provider": "jellyfin",
  "notification_type": "{{NotificationType}}",
  "timestamp": "{{UtcTimestamp}}",
  "server_name": "{{ServerName}}",
  "user": {
    "id": "{{UserId}}",
    "name": "{{{NotificationUsername}}}"
  },
  "item": {
    "id": "{{ItemId}}",
    "type": "{{ItemType}}",
    "name": "{{{Name}}}",
    "series_name": "{{{SeriesName}}}",
    "year": {{#if_exist Year}}{{Year}}{{else}}0{{/if_exist}},
    "season_number": {{#if_exist SeasonNumber}}{{SeasonNumber}}{{else}}0{{/if_exist}},
    "episode_number": {{#if_exist EpisodeNumber}}{{EpisodeNumber}}{{else}}0{{/if_exist}},
    "runtime_ticks": {{#if_exist RunTimeTicks}}{{RunTimeTicks}}{{else}}0{{/if_exist}},
    "provider_ids": {
      "imdb": "{{Provider_imdb}}",
      "tmdb": "{{Provider_tmdb}}",
      "tvdb": "{{Provider_tvdb}}"
    }
  },
  "playback": {
    "position_ticks": {{#if_exist PlaybackPositionTicks}}{{PlaybackPositionTicks}}{{else}}0{{/if_exist}},
    "played_to_completion": {{#if_equals PlayedToCompletion 'true'}}true{{else}}false{{/if_equals}},
    "runtime_ticks": {{#if_exist RunTimeTicks}}{{RunTimeTicks}}{{else}}0{{/if_exist}}
  },
  "user_data": {
    "save_reason": "{{SaveReason}}",
    "played": {{#if_exist Played}}{{#if_equals Played 'true'}}true{{else}}false{{/if_equals}}{{else}}null{{/if_exist}}
  }
}`;

function formatTimestamp(t: Translate, value?: string | null) {
  if (!value) return t("web.settings.webhooks.never");
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return t("web.settings.webhooks.never");
  return new Intl.DateTimeFormat(formatLocale(), {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function relativeTime(t: Translate, value?: string | null) {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < 60) return t("web.settings.webhooks.justNow");
  if (seconds < 3600)
    return t("web.settings.webhooks.minutesAgo", { minutes: String(Math.floor(seconds / 60)) });
  if (seconds < 86400)
    return t("web.settings.webhooks.hoursAgo", { hours: String(Math.floor(seconds / 3600)) });
  return t("web.settings.webhooks.daysAgo", { days: String(Math.floor(seconds / 86400)) });
}

type ConnectionHealth = "healthy" | "error" | "waiting";

function connectionHealth(connection: WebhookSyncConnection): ConnectionHealth {
  if (connection.last_webhook_error_at) {
    const errorTime = new Date(connection.last_webhook_error_at).getTime();
    const receivedTime = connection.last_webhook_received_at
      ? new Date(connection.last_webhook_received_at).getTime()
      : Number.NEGATIVE_INFINITY;
    if (Number.isFinite(errorTime) && errorTime > receivedTime) {
      return "error";
    }
  }
  if (connection.last_webhook_received_at) return "healthy";
  if (connection.last_webhook_error_message) return "error";
  return "waiting";
}

const HEALTH_DOT: Record<ConnectionHealth, string> = {
  healthy: "bg-emerald-400",
  error: "bg-red-400",
  waiting: "bg-yellow-400/70",
};

const HEALTH_LABEL: Record<ConnectionHealth, MessageKey> = {
  healthy: "web.settings.webhooks.health.healthy",
  error: "web.settings.webhooks.health.error",
  waiting: "web.settings.webhooks.health.waiting",
};

const EVENT_OUTCOME_LABEL: Record<WebhookSyncEventLog["outcome"], MessageKey> = {
  applied: "web.settings.webhooks.outcome.applied",
  ignored: "web.settings.webhooks.outcome.ignored",
  unmatched: "web.settings.webhooks.outcome.unmatched",
  skipped: "web.settings.webhooks.outcome.skipped",
  rejected: "web.settings.webhooks.outcome.rejected",
  error: "web.settings.webhooks.outcome.error",
};

const EVENT_OUTCOME_BADGE: Record<WebhookSyncEventLog["outcome"], string> = {
  applied: "border-emerald-500/30 bg-emerald-500/10 text-emerald-300",
  ignored: "border-slate-500/30 bg-slate-500/10 text-slate-300",
  unmatched: "border-amber-500/30 bg-amber-500/10 text-amber-300",
  skipped: "border-zinc-500/30 bg-zinc-500/10 text-zinc-300",
  rejected: "border-red-500/30 bg-red-500/10 text-red-300",
  error: "border-red-500/30 bg-red-500/10 text-red-300",
};

function providerLabel(provider: WebhookSyncConnection["provider"] | ProviderType) {
  switch (provider) {
    case "plex":
      return "Plex";
    case "emby":
      return "Emby";
    case "jellyfin":
      return "Jellyfin";
  }
}

function eventAttrString(event: WebhookSyncEventLog, key: string) {
  const value = event.attrs?.[key];
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return "";
}

function eventUserLabel(t: Translate, event: WebhookSyncEventLog) {
  const userName = eventAttrString(event, "external_user_name");
  const userID = eventAttrString(event, "external_user_id");
  if (userName && userID) return `${userName} (${userID})`;
  return userName || userID || t("web.settings.webhooks.unknownUser");
}

function eventMatchedItemLabel(event: WebhookSyncEventLog) {
  return eventAttrString(event, "matched_media_item_title") || null;
}

const EVENT_ATTR_LABELS: Record<string, MessageKey> = {
  event_kind: "web.settings.webhooks.attr.eventKind",
  action: "web.settings.webhooks.attr.action",
  external_user_id: "web.settings.webhooks.attr.externalUserId",
  external_user_name: "web.settings.webhooks.attr.externalUserName",
  external_item_id: "web.settings.webhooks.attr.externalItemId",
  media_kind: "web.settings.webhooks.attr.mediaKind",
  matched_media_item_id: "web.settings.webhooks.attr.matchedMediaItemId",
  matched_media_item_title: "web.settings.webhooks.attr.matchedMediaItemTitle",
  profile_id: "web.settings.webhooks.attr.profileId",
  client_ip: "web.settings.webhooks.attr.clientIp",
  content_type: "web.settings.webhooks.attr.contentType",
  user_agent: "web.settings.webhooks.attr.userAgent",
  path_pattern: "web.settings.webhooks.attr.pathPattern",
};

function eventAttrLabel(t: Translate, key: string) {
  const label = EVENT_ATTR_LABELS[key];
  return label ? t(label) : key.replace(/_/g, " ");
}

/** Rich-text tag for a control name in the media server's own interface. */
function hl(chunks: ReactNode) {
  return <span className="text-foreground">{chunks}</span>;
}

type ProviderType = "plex" | "emby" | "jellyfin";
type ConnectionDraft = { serverName: string; defaultProfileId: string };

type EventMatrixTone = "required" | "recommended" | "skip";
type EventMatrixSection = {
  label: MessageKey;
  tone: EventMatrixTone;
  items: { event: MessageKey; note: MessageKey }[];
};

const EVENT_MATRIX_TONE: Record<EventMatrixTone, string> = {
  required: "text-emerald-300",
  recommended: "text-sky-300",
  skip: "text-muted-foreground",
};

function EventMatrix({ sections }: { sections: EventMatrixSection[] }) {
  const t = useT();
  return (
    <div className="border-border/50 divide-border/50 divide-y rounded-md border">
      {sections.map((section) => (
        <div key={section.label} className="space-y-1.5 px-3 py-2.5">
          <p
            className={cn(
              "text-xs font-medium tracking-wide uppercase",
              EVENT_MATRIX_TONE[section.tone],
            )}
          >
            {t(section.label)}
          </p>
          <ul className="space-y-1">
            {section.items.map((item) => (
              <li key={item.event} className="text-muted-foreground text-[13px] leading-relaxed">
                {t("web.settings.webhooks.setup.eventLine", {
                  event: t(item.event),
                  note: t(item.note),
                  name: (chunks: ReactNode) => (
                    <span className="text-foreground font-medium">{chunks}</span>
                  ),
                })}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

export default function WebhookSyncSettings() {
  const t = useT();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { profile } = useCurrentProfile();
  const { data: profiles = [] } = useProfiles();
  const connectionsQuery = useWebhookSyncConnections();
  const createConnectionMutation = useCreateWebhookSyncConnection();
  const deleteConnectionMutation = useDeleteWebhookSyncConnection();
  const rotateWebhookMutation = useRotateWebhookSyncWebhook();
  const updateConnectionMutation = useUpdateWebhookSyncConnection();
  const updateMappingsMutation = useUpdateWebhookSyncProfileMappings();

  const [provider, setProvider] = useState<ProviderType>("plex");
  const [manualServerName, setManualServerName] = useState("");
  const [plexServers, setPlexServers] = useState<BrowserPlexServer[]>([]);
  const [selectedServerId, setSelectedServerId] = useState("");
  const [selectedConnectionId, setSelectedConnectionId] = useState<string>("");
  const [defaultProfileId, setDefaultProfileId] = useState(profile?.id ?? "");
  const [webhookUrls, setWebhookUrls] = useState<Record<string, string>>({});
  const [connectionDraftsById, setConnectionDraftsById] = useState<Record<string, ConnectionDraft>>(
    {},
  );
  const [mappingDraftsByConnection, setMappingDraftsByConnection] = useState<
    Record<string, Record<string, string>>
  >({});
  const [plexAuthPending, setPlexAuthPending] = useState(false);
  const [plexAuthError, setPlexAuthError] = useState<string | null>(null);
  const [eventSearch, setEventSearch] = useState("");
  const [eventOutcomeFilter, setEventOutcomeFilter] = useState<
    WebhookSyncEventLog["outcome"] | "all"
  >("all");
  const [eventPage, setEventPage] = useState(0);
  const [selectedEvent, setSelectedEvent] = useState<WebhookSyncEventLog | null>(null);

  const connections = useMemo(() => connectionsQuery.data ?? [], [connectionsQuery.data]);

  const selectedConnection = useMemo(() => {
    return connections.find((c) => c.id === selectedConnectionId) ?? connections[0] ?? null;
  }, [connections, selectedConnectionId]);

  const currentConnectionId = selectedConnection?.id ?? "";
  const mappingsQuery = useWebhookSyncProfileMappings(currentConnectionId || undefined);
  const eventsQuery = useWebhookSyncEvents(currentConnectionId || undefined);
  const effectiveSelectedServerId = selectedServerId || plexServers[0]?.clientIdentifier || "";
  const effectiveDefaultProfileId = defaultProfileId || profile?.id || profiles[0]?.id || "";
  const currentMappingDrafts = mappingDraftsByConnection[currentConnectionId] ?? {};
  const currentConnectionDraft = currentConnectionId
    ? connectionDraftsById[currentConnectionId]
    : undefined;
  const selectedPlexServer = useMemo(
    () =>
      plexServers.find((server) => server.clientIdentifier === effectiveSelectedServerId) ?? null,
    [effectiveSelectedServerId, plexServers],
  );

  const mappingRows = useMemo(() => {
    const discoveredUsers = mappingsQuery.data?.discovered_users ?? [];
    const mappings = mappingsQuery.data?.mappings ?? [];
    const byUserId = new Map<
      string,
      { user: WebhookSyncDiscoveredUser; mapping?: WebhookSyncProfileMapping }
    >();

    for (const user of discoveredUsers) {
      byUserId.set(user.external_user_id, { user });
    }
    for (const mapping of mappings) {
      const existing = byUserId.get(mapping.external_user_id);
      byUserId.set(mapping.external_user_id, {
        user: existing?.user ?? {
          external_user_id: mapping.external_user_id,
          external_user_name: mapping.external_user_name,
        },
        mapping,
      });
    }

    return Array.from(byUserId.values())
      .map(({ user, mapping }) => ({
        external_user_id: user.external_user_id,
        external_user_name: user.external_user_name,
        silo_profile_id: mapping?.silo_profile_id ?? "",
      }))
      .sort((a, b) => a.external_user_name.localeCompare(b.external_user_name));
  }, [mappingsQuery.data]);

  const EVENTS_PER_PAGE = 15;

  const filteredEvents = useMemo(() => {
    let events = eventsQuery.data ?? [];
    if (eventOutcomeFilter !== "all") {
      events = events.filter((e) => e.outcome === eventOutcomeFilter);
    }
    if (eventSearch.trim()) {
      const q = eventSearch.trim().toLowerCase();
      events = events.filter(
        (e) =>
          e.summary.toLowerCase().includes(q) ||
          eventUserLabel(t, e).toLowerCase().includes(q) ||
          (e.error_message ?? "").toLowerCase().includes(q),
      );
    }
    return events;
  }, [eventsQuery.data, eventOutcomeFilter, eventSearch, t]);

  const eventTotalPages = Math.max(1, Math.ceil(filteredEvents.length / EVENTS_PER_PAGE));
  const eventPageClamped = Math.min(eventPage, eventTotalPages - 1);
  const pagedEvents = useMemo(
    () =>
      filteredEvents.slice(
        eventPageClamped * EVENTS_PER_PAGE,
        eventPageClamped * EVENTS_PER_PAGE + EVENTS_PER_PAGE,
      ),
    [filteredEvents, eventPageClamped],
  );
  const eventRangeStart = filteredEvents.length === 0 ? 0 : eventPageClamped * EVENTS_PER_PAGE + 1;
  const eventRangeEnd = Math.min((eventPageClamped + 1) * EVENTS_PER_PAGE, filteredEvents.length);

  const returnedPlexAuth = searchParams.get("plex_auth");
  const returnedPlexPinId = searchParams.get("plex_pin_id");
  const returnedPlexPinCode = searchParams.get("plex_pin_code");
  const currentConnectionServerName =
    currentConnectionDraft?.serverName ?? selectedConnection?.server_name ?? "";
  const currentConnectionDefaultProfileId =
    currentConnectionDraft?.defaultProfileId ?? selectedConnection?.default_profile_id ?? "";
  const currentConnectionDefaultValue = currentConnectionDefaultProfileId || UNMAPPED_VALUE;
  const currentConnectionHasUnsavedChanges =
    !!selectedConnection &&
    (currentConnectionServerName !== selectedConnection.server_name ||
      currentConnectionDefaultProfileId !== selectedConnection.default_profile_id);

  useEffect(() => {
    if (returnedPlexAuth !== "1") {
      return;
    }

    if (!returnedPlexPinId || !returnedPlexPinCode) {
      setPlexAuthError(t("web.settings.webhooks.plexAuth.missingSession"));
      void navigate("/settings/webhook-sync", { replace: true });
      return;
    }

    const pinID = Number(returnedPlexPinId);
    if (!Number.isFinite(pinID) || pinID <= 0) {
      setPlexAuthError(t("web.settings.webhooks.plexAuth.invalidPin"));
      void navigate("/settings/webhook-sync", { replace: true });
      return;
    }

    let cancelled = false;
    setPlexAuthPending(true);
    setPlexAuthError(null);

    void (async () => {
      try {
        const { servers } = await completePlexAuthentication(pinID, returnedPlexPinCode);
        if (cancelled) {
          return;
        }
        setPlexServers(servers);
        setSelectedServerId(servers[0]?.clientIdentifier ?? "");
      } catch (error) {
        if (cancelled) {
          return;
        }
        setPlexServers([]);
        setSelectedServerId("");
        setPlexAuthError(
          error instanceof Error ? error.message : t("web.settings.webhooks.plexAuth.finishFailed"),
        );
      } finally {
        if (!cancelled) {
          setPlexAuthPending(false);
          void navigate("/settings/webhook-sync", { replace: true });
        }
      }
    })();

    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the plex.tv return is handled once, not again when the language (t) changes
  }, [navigate, returnedPlexAuth, returnedPlexPinCode, returnedPlexPinId]);

  useEffect(() => {
    if (provider !== "plex") {
      setPlexAuthError(null);
      setPlexAuthPending(false);
      setPlexServers([]);
      setSelectedServerId("");
    }
  }, [provider]);

  // Reset delivery filters when the selected connection changes.
  useEffect(() => {
    setEventSearch("");
    setEventOutcomeFilter("all");
    setEventPage(0);
  }, [currentConnectionId]);

  // Reset page when filters change.
  useEffect(() => {
    setEventPage(0);
  }, [eventSearch, eventOutcomeFilter]);

  async function copyText(text: string) {
    try {
      await copyTextToClipboard(text);
      toast.success(t("web.settings.webhooks.copied"));
    } catch {
      toast.error(t("web.settings.webhooks.copyFailed"));
    }
  }

  async function handleStartPlexAuth() {
    setPlexAuthPending(true);
    setPlexAuthError(null);
    setPlexServers([]);
    setSelectedServerId("");

    try {
      const pin = await createPlexPin();
      const forwardURL = new URL("/settings/webhook-sync", window.location.origin);
      forwardURL.searchParams.set("plex_auth", "1");
      forwardURL.searchParams.set("plex_pin_id", String(pin.id));
      forwardURL.searchParams.set("plex_pin_code", pin.code);
      window.location.assign(buildPlexAuthURL(pin.code, forwardURL.toString()));
    } catch (error) {
      setPlexAuthPending(false);
      setPlexAuthError(
        error instanceof Error ? error.message : t("web.settings.webhooks.plexAuth.startFailed"),
      );
    }
  }

  async function handleCreateConnection() {
    if (!effectiveDefaultProfileId || effectiveDefaultProfileId === UNMAPPED_VALUE) {
      return;
    }

    const result = await createConnectionMutation.mutateAsync(
      provider === "plex"
        ? {
            provider: "plex",
            server_id: selectedPlexServer?.clientIdentifier ?? "",
            server_name: selectedPlexServer?.name ?? "",
            base_url: selectedPlexServer ? getPreferredPlexServerURL(selectedPlexServer) : "",
            access_token: selectedPlexServer?.accessToken ?? "",
            default_profile_id: effectiveDefaultProfileId,
          }
        : {
            provider,
            server_name: manualServerName.trim(),
            default_profile_id: effectiveDefaultProfileId,
          },
    );

    setWebhookUrls((current) => ({
      ...current,
      [result.connection.id]: result.webhook_url,
    }));
    setSelectedConnectionId(result.connection.id);
  }

  async function handleSaveMappings() {
    if (!currentConnectionId) {
      return;
    }

    await updateMappingsMutation.mutateAsync({
      connectionId: currentConnectionId,
      body: {
        mappings: mappingRows.map((row) => {
          const value = currentMappingDrafts[row.external_user_id] ?? row.silo_profile_id;
          return {
            external_user_id: row.external_user_id,
            external_user_name: row.external_user_name,
            silo_profile_id: !value || value === UNMAPPED_VALUE ? null : value,
          };
        }),
      },
    });
  }

  function setDraftProfile(externalUserId: string, profileId: string) {
    if (!currentConnectionId) {
      return;
    }
    setMappingDraftsByConnection((current) => ({
      ...current,
      [currentConnectionId]: {
        ...(current[currentConnectionId] ?? {}),
        [externalUserId]: profileId,
      },
    }));
  }

  function setConnectionDraft(update: Partial<ConnectionDraft>) {
    if (!currentConnectionId || !selectedConnection) {
      return;
    }
    setConnectionDraftsById((current) => ({
      ...current,
      [currentConnectionId]: {
        serverName: current[currentConnectionId]?.serverName ?? selectedConnection.server_name,
        defaultProfileId:
          current[currentConnectionId]?.defaultProfileId ?? selectedConnection.default_profile_id,
        ...update,
      },
    }));
  }

  const hasSignedInToPlex = plexServers.length > 0;
  const canCreateConnection =
    effectiveDefaultProfileId &&
    effectiveDefaultProfileId !== UNMAPPED_VALUE &&
    (provider === "plex" ? !!selectedPlexServer : manualServerName.trim().length > 0);

  return (
    <div className="space-y-6 pb-6">
      <PageHeader
        title={t("web.settings.webhooks.title")}
        subtitle={t("web.settings.webhooks.subtitle")}
      />

      <SettingsGroup
        title={t("web.settings.webhooks.add.title")}
        description={t("web.settings.webhooks.add.description")}
      >
        <div className="flex flex-col gap-3">
          <Label className="text-sm font-medium">{t("web.settings.webhooks.add.provider")}</Label>
          <Select value={provider} onValueChange={(value) => setProvider(value as ProviderType)}>
            <SelectTrigger className="w-full sm:w-[260px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="plex">{providerLabel("plex")}</SelectItem>
              <SelectItem value="emby">{providerLabel("emby")}</SelectItem>
              <SelectItem value="jellyfin">{providerLabel("jellyfin")}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {provider === "plex" ? (
          <>
            <div className="border-border/50 flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0 space-y-0.5">
                <Label className="text-sm font-medium">
                  {t("web.settings.webhooks.add.plexAccount")}
                </Label>
                <p className="text-muted-foreground text-[13px] leading-relaxed">
                  {hasSignedInToPlex
                    ? t("web.settings.webhooks.add.plexServers", {
                        count: plexServers.length,
                        count_text: String(plexServers.length),
                      })
                    : t("web.settings.webhooks.add.plexAuthorizeHint")}
                </p>
              </div>
              <div className="flex items-center gap-2">
                {plexAuthPending ? (
                  <Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
                ) : null}
                <Button
                  variant={hasSignedInToPlex ? "outline" : "default"}
                  size="sm"
                  onClick={handleStartPlexAuth}
                  disabled={plexAuthPending}
                >
                  <ExternalLink className="h-3.5 w-3.5" />
                  {hasSignedInToPlex
                    ? t("web.settings.webhooks.add.reauthenticate")
                    : t("web.settings.webhooks.add.signInToPlex")}
                </Button>
              </div>
            </div>

            {plexAuthError ? <p className="text-destructive text-xs">{plexAuthError}</p> : null}

            {hasSignedInToPlex ? (
              <div className="border-border/50 flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0 space-y-0.5">
                  <Label className="text-sm font-medium">
                    {t("web.settings.webhooks.add.server")}
                  </Label>
                  <p className="text-muted-foreground text-[13px] leading-relaxed">
                    {t("web.settings.webhooks.add.serverHint")}
                  </p>
                </div>
                <Select
                  value={effectiveSelectedServerId}
                  onValueChange={(value) => {
                    setSelectedServerId(value);
                  }}
                >
                  <SelectTrigger className="w-full sm:w-[240px]">
                    <SelectValue placeholder={t("web.settings.webhooks.add.selectServer")} />
                  </SelectTrigger>
                  <SelectContent>
                    {plexServers.map((server) => (
                      <SelectItem key={server.clientIdentifier} value={server.clientIdentifier}>
                        {server.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            ) : null}
          </>
        ) : (
          <div className="border-border/50 flex flex-col gap-3 border-t pt-4">
            <Label className="text-sm font-medium">
              {t("web.settings.webhooks.connectionName")}
            </Label>
            <Input
              value={manualServerName}
              onChange={(event) => setManualServerName(event.target.value)}
              placeholder={
                provider === "emby"
                  ? t("web.settings.webhooks.add.embyNamePlaceholder")
                  : t("web.settings.webhooks.add.jellyfinNamePlaceholder")
              }
            />
          </div>
        )}

        <div className="border-border/50 flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0 space-y-0.5">
            <Label className="text-sm font-medium">
              {t("web.settings.webhooks.defaultProfile")}
            </Label>
            <p className="text-muted-foreground text-[13px] leading-relaxed">
              {t("web.settings.webhooks.add.defaultProfileHint")}
            </p>
          </div>
          <Select
            value={effectiveDefaultProfileId || UNMAPPED_VALUE}
            onValueChange={(value) => setDefaultProfileId(value === UNMAPPED_VALUE ? "" : value)}
          >
            <SelectTrigger className="w-full sm:w-[240px]">
              <SelectValue placeholder={t("web.settings.webhooks.chooseProfile")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={UNMAPPED_VALUE}>
                {t("web.settings.webhooks.chooseProfile")}
              </SelectItem>
              {profiles.map((candidate) => (
                <SelectItem key={candidate.id} value={candidate.id}>
                  {profile?.id === candidate.id
                    ? t("web.settings.webhooks.add.currentProfile", { name: candidate.name })
                    : candidate.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="border-border/50 flex justify-end border-t pt-4">
          <Button
            onClick={handleCreateConnection}
            disabled={!canCreateConnection || createConnectionMutation.isPending}
          >
            {createConnectionMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Plus className="h-4 w-4" />
            )}
            {t("web.settings.webhooks.add.create")}
          </Button>
        </div>
      </SettingsGroup>

      {/* ── Connected servers ── */}
      <SettingsGroup
        title={t("web.settings.webhooks.servers.title")}
        description={t("web.settings.webhooks.servers.description")}
      >
        {connectionsQuery.isLoading ? (
          <div className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2 className="h-4 w-4 animate-spin" />
            {t("web.settings.webhooks.servers.loading")}
          </div>
        ) : connectionsQuery.isError ? (
          <div className="flex items-start gap-2.5 rounded-md bg-red-500/10 px-3 py-2.5">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400" />
            <p className="text-sm leading-relaxed text-red-300">
              {connectionsQuery.error instanceof Error
                ? connectionsQuery.error.message
                : t("web.settings.webhooks.servers.loadFailed")}
            </p>
          </div>
        ) : connections.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("web.settings.webhooks.servers.empty")}
          </p>
        ) : (
          <div className="space-y-2">
            {connections.map((connection) => {
              const health = connectionHealth(connection);
              const isSelected = connection.id === currentConnectionId;
              const userCount = connection.user_count ?? 0;

              return (
                <button
                  key={connection.id}
                  type="button"
                  onClick={() => setSelectedConnectionId(connection.id)}
                  className={cn(
                    "group flex w-full items-center justify-between gap-4 rounded-lg px-4 py-3 text-start transition-colors",
                    isSelected ? "bg-accent/50 ring-border ring-1" : "hover:bg-accent/20",
                  )}
                >
                  <div className="flex min-w-0 items-center gap-3">
                    <span className={cn("h-2 w-2 shrink-0 rounded-full", HEALTH_DOT[health])} />
                    <div className="min-w-0">
                      <span className="text-sm font-medium">
                        {connection.server_name}
                        <span className="text-muted-foreground ms-2 text-xs font-normal">
                          {providerLabel(connection.provider)}
                        </span>
                      </span>
                      <p className="text-muted-foreground text-xs">
                        {connection.last_webhook_received_at
                          ? t("web.settings.webhooks.servers.usersLastEvent", {
                              count: userCount,
                              count_text: String(userCount),
                              time:
                                relativeTime(t, connection.last_webhook_received_at) ??
                                formatTimestamp(t, connection.last_webhook_received_at),
                            })
                          : t("web.settings.webhooks.servers.users", {
                              count: userCount,
                              count_text: String(userCount),
                            })}
                      </p>
                    </div>
                  </div>
                  <Badge
                    variant={health === "error" ? "destructive" : "outline"}
                    className="shrink-0 text-xs"
                  >
                    {t(HEALTH_LABEL[health])}
                  </Badge>
                </button>
              );
            })}
          </div>
        )}
      </SettingsGroup>

      {/* ── Selected connection detail ── */}
      {selectedConnection ? (
        <>
          <SettingsGroup
            title={selectedConnection.server_name}
            description={t("web.settings.webhooks.detail.description", {
              provider: providerLabel(selectedConnection.provider),
            })}
          >
            {(() => {
              const health = connectionHealth(selectedConnection);
              const webhookURL =
                webhookUrls[selectedConnection.id] || selectedConnection.webhook_url || "";

              return (
                <>
                  {health === "error" && selectedConnection.last_webhook_error_message ? (
                    <div className="flex items-start gap-2.5 rounded-md bg-red-500/10 px-3 py-2.5">
                      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400" />
                      <div className="min-w-0 space-y-0.5">
                        <p className="text-sm font-medium text-red-300">
                          {t("web.settings.webhooks.detail.errorAt", {
                            time: formatTimestamp(t, selectedConnection.last_webhook_error_at),
                          })}
                        </p>
                        <p className="text-[13px] leading-relaxed text-red-300/80">
                          {selectedConnection.last_webhook_error_message}
                        </p>
                      </div>
                    </div>
                  ) : (
                    <div className="flex items-center gap-2 text-[13px]">
                      <CheckCircle2 className="h-3.5 w-3.5 text-emerald-400" />
                      <span className="text-muted-foreground">
                        {t("web.settings.webhooks.detail.ready")}
                      </span>
                    </div>
                  )}

                  {/* Webhook URL */}
                  <div className="space-y-1.5">
                    <Label className="text-muted-foreground text-xs">
                      {t("web.settings.webhooks.detail.webhookUrl")}
                    </Label>
                    <div className="flex flex-col gap-2 sm:flex-row">
                      <Input value={webhookURL} readOnly className="font-mono text-xs" />
                      <div className="flex gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => webhookURL && void copyText(webhookURL)}
                          disabled={!webhookURL}
                        >
                          <Copy className="h-3.5 w-3.5" />
                          {t("web.settings.webhooks.detail.copy")}
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={async () => {
                            const result = await rotateWebhookMutation.mutateAsync(
                              selectedConnection.id,
                            );
                            setWebhookUrls((current) => ({
                              ...current,
                              [selectedConnection.id]: result.webhook_url,
                            }));
                          }}
                          disabled={rotateWebhookMutation.isPending}
                        >
                          <RotateCcw className="h-3.5 w-3.5" />
                          {t("web.settings.webhooks.detail.rotate")}
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={async () => {
                            await deleteConnectionMutation.mutateAsync(selectedConnection.id);
                            setSelectedConnectionId("");
                          }}
                          disabled={deleteConnectionMutation.isPending}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                          {t("web.settings.webhooks.detail.delete")}
                        </Button>
                      </div>
                    </div>
                  </div>

                  {/* Connection name + default profile */}
                  <div className="border-border/50 space-y-3 border-t pt-4">
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div className="space-y-1.5">
                        <Label className="text-muted-foreground text-xs">
                          {t("web.settings.webhooks.connectionName")}
                        </Label>
                        <Input
                          value={currentConnectionServerName}
                          onChange={(event) =>
                            setConnectionDraft({ serverName: event.target.value })
                          }
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-muted-foreground text-xs">
                          {t("web.settings.webhooks.defaultProfile")}
                        </Label>
                        <Select
                          value={currentConnectionDefaultValue}
                          onValueChange={(value) =>
                            setConnectionDraft({
                              defaultProfileId: value === UNMAPPED_VALUE ? "" : value,
                            })
                          }
                        >
                          <SelectTrigger>
                            <SelectValue placeholder={t("web.settings.webhooks.chooseProfile")} />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value={UNMAPPED_VALUE}>
                              {t("web.settings.webhooks.detail.noDefaultProfile")}
                            </SelectItem>
                            {profiles.map((candidate) => (
                              <SelectItem key={candidate.id} value={candidate.id}>
                                {candidate.name}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                    </div>
                    <div className="flex justify-end">
                      <Button
                        onClick={async () => {
                          if (!selectedConnection) {
                            return;
                          }
                          const trimmedServerName = currentConnectionServerName.trim();
                          if (!trimmedServerName) {
                            return;
                          }
                          await updateConnectionMutation.mutateAsync({
                            connectionId: selectedConnection.id,
                            body: {
                              server_name: trimmedServerName,
                              default_profile_id: currentConnectionDefaultProfileId,
                            },
                          });
                        }}
                        disabled={
                          updateConnectionMutation.isPending ||
                          !currentConnectionHasUnsavedChanges ||
                          currentConnectionServerName.trim().length === 0
                        }
                      >
                        {updateConnectionMutation.isPending ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : null}
                        {t("web.settings.webhooks.detail.save")}
                      </Button>
                    </div>
                  </div>

                  {/* Profile mapping — part of connection config */}
                  <div className="border-border/50 space-y-3 border-t pt-4">
                    <div className="space-y-0.5">
                      <Label className="text-sm font-medium">
                        {t("web.settings.webhooks.mapping.title")}
                      </Label>
                      <p className="text-muted-foreground text-[13px] leading-relaxed">
                        {t("web.settings.webhooks.mapping.description")}
                      </p>
                    </div>

                    {mappingsQuery.isLoading ? (
                      <div className="text-muted-foreground flex items-center gap-2 text-sm">
                        <Loader2 className="h-4 w-4 animate-spin" />
                        {t("web.settings.webhooks.mapping.loading")}
                      </div>
                    ) : mappingsQuery.isError ? (
                      <div className="flex items-start gap-2.5 rounded-md bg-red-500/10 px-3 py-2.5">
                        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400" />
                        <p className="text-sm leading-relaxed text-red-300">
                          {mappingsQuery.error instanceof Error
                            ? mappingsQuery.error.message
                            : t("web.settings.webhooks.mapping.loadFailed")}
                        </p>
                      </div>
                    ) : mappingRows.length > 0 ? (
                      <div className="space-y-3">
                        {mappingRows.map((row) => (
                          <div
                            key={row.external_user_id}
                            className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"
                          >
                            <div className="min-w-0">
                              <p className="text-sm font-medium">{row.external_user_name}</p>
                              <p className="text-muted-foreground truncate text-xs">
                                {row.external_user_id}
                              </p>
                            </div>
                            <Select
                              value={
                                currentMappingDrafts[row.external_user_id] ??
                                row.silo_profile_id ??
                                UNMAPPED_VALUE
                              }
                              onValueChange={(value) =>
                                setDraftProfile(row.external_user_id, value)
                              }
                            >
                              <SelectTrigger className="w-full sm:w-[240px]">
                                <SelectValue
                                  placeholder={t("web.settings.webhooks.chooseProfile")}
                                />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value={UNMAPPED_VALUE}>
                                  {t("web.settings.webhooks.mapping.ignore")}
                                </SelectItem>
                                {profiles.map((candidate) => (
                                  <SelectItem key={candidate.id} value={candidate.id}>
                                    {candidate.name}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </div>
                        ))}

                        <div className="flex justify-end">
                          <Button
                            onClick={handleSaveMappings}
                            disabled={updateMappingsMutation.isPending}
                          >
                            {updateMappingsMutation.isPending ? (
                              <Loader2 className="h-4 w-4 animate-spin" />
                            ) : null}
                            {t("web.settings.webhooks.mapping.save")}
                          </Button>
                        </div>
                      </div>
                    ) : (
                      <p className="text-muted-foreground text-sm">
                        {t("web.settings.webhooks.mapping.empty")}
                      </p>
                    )}
                  </div>

                  {/* Setup instructions */}
                  <details className="border-border/50 border-t pt-4">
                    <summary className="text-muted-foreground flex cursor-pointer items-center gap-2 text-sm font-medium select-none">
                      <Server className="h-3.5 w-3.5" />
                      {t("web.settings.webhooks.setup.title")}
                    </summary>
                    <div className="mt-3">
                      {selectedConnection.provider === "plex" ? (
                        <ol className="text-muted-foreground list-decimal space-y-1.5 ps-5 text-sm leading-relaxed">
                          <li>{t("web.settings.webhooks.setup.plex.step1", { hl })}</li>
                          <li>{t("web.settings.webhooks.setup.plex.step2", { hl })}</li>
                          <li>{t("web.settings.webhooks.setup.plex.step3")}</li>
                          <li>{t("web.settings.webhooks.setup.plex.step4")}</li>
                        </ol>
                      ) : null}
                      {selectedConnection.provider === "emby" ? (
                        <div className="space-y-4 text-sm">
                          <ol className="text-muted-foreground list-decimal space-y-1.5 ps-5 leading-relaxed">
                            <li>{t("web.settings.webhooks.setup.emby.step1", { hl })}</li>
                            <li>{t("web.settings.webhooks.setup.emby.step2", { hl })}</li>
                            <li>{t("web.settings.webhooks.setup.emby.step3")}</li>
                          </ol>

                          <EventMatrix
                            sections={[
                              {
                                label: "web.settings.webhooks.setup.events.required",
                                tone: "required",
                                items: [
                                  {
                                    event: "web.settings.webhooks.setup.emby.eventPlaybackStop",
                                    note: "web.settings.webhooks.setup.emby.notePlaybackStop",
                                  },
                                ],
                              },
                              {
                                label: "web.settings.webhooks.setup.events.recommended",
                                tone: "recommended",
                                items: [
                                  {
                                    event: "web.settings.webhooks.setup.emby.eventFavorites",
                                    note: "web.settings.webhooks.setup.emby.noteFavorites",
                                  },
                                  {
                                    event: "web.settings.webhooks.setup.emby.eventMarkPlayed",
                                    note: "web.settings.webhooks.setup.emby.noteMarkPlayed",
                                  },
                                ],
                              },
                              {
                                label: "web.settings.webhooks.setup.events.skip",
                                tone: "skip",
                                items: [
                                  {
                                    event: "web.settings.webhooks.setup.emby.eventPlaybackOther",
                                    note: "web.settings.webhooks.setup.emby.notePlaybackOther",
                                  },
                                ],
                              },
                            ]}
                          />
                        </div>
                      ) : null}
                      {selectedConnection.provider === "jellyfin" ? (
                        <div className="space-y-4 text-sm">
                          <ol className="text-muted-foreground list-decimal space-y-1.5 ps-5 leading-relaxed">
                            <li>{t("web.settings.webhooks.setup.jellyfin.step1", { hl })}</li>
                            <li>{t("web.settings.webhooks.setup.jellyfin.step2", { hl })}</li>
                            <li>{t("web.settings.webhooks.setup.jellyfin.step3", { hl })}</li>
                            <li>{t("web.settings.webhooks.setup.jellyfin.step4", { hl })}</li>
                            <li>{t("web.settings.webhooks.setup.jellyfin.step5", { hl })}</li>
                          </ol>

                          <div className="space-y-1.5">
                            <Label className="text-muted-foreground text-xs">
                              {t("web.settings.webhooks.setup.jellyfin.template")}
                            </Label>
                            <textarea
                              readOnly
                              value={JELLYFIN_TEMPLATE}
                              className="border-input bg-background min-h-56 w-full rounded-md border px-3 py-2 font-mono text-xs"
                            />
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => void copyText(JELLYFIN_TEMPLATE)}
                            >
                              <Copy className="h-3.5 w-3.5" />
                              {t("web.settings.webhooks.setup.jellyfin.copyTemplate")}
                            </Button>
                          </div>
                        </div>
                      ) : null}
                    </div>
                  </details>
                </>
              );
            })()}
          </SettingsGroup>

          {/* Recent deliveries — table */}
          <SettingsGroup
            title={t("web.settings.webhooks.deliveries.title")}
            description={t("web.settings.webhooks.deliveries.description")}
          >
            {eventsQuery.isLoading ? (
              <div className="text-muted-foreground flex items-center gap-2 text-sm">
                <Loader2 className="h-4 w-4 animate-spin" />
                {t("web.settings.webhooks.deliveries.loading")}
              </div>
            ) : eventsQuery.isError ? (
              <div className="flex items-start gap-2.5 rounded-md bg-red-500/10 px-3 py-2.5">
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400" />
                <p className="text-sm leading-relaxed text-red-300">
                  {eventsQuery.error instanceof Error
                    ? eventsQuery.error.message
                    : t("web.settings.webhooks.deliveries.loadFailed")}
                </p>
              </div>
            ) : (eventsQuery.data?.length ?? 0) > 0 ? (
              <>
                {/* Filter bar */}
                <div className="flex flex-col gap-2 sm:flex-row">
                  <div className="relative flex-1">
                    <Search className="text-muted-foreground pointer-events-none absolute start-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
                    <Input
                      value={eventSearch}
                      onChange={(e) => setEventSearch(e.target.value)}
                      placeholder={t("web.settings.webhooks.deliveries.search")}
                      className="h-8 ps-8 text-xs"
                    />
                  </div>
                  <Select
                    value={eventOutcomeFilter}
                    onValueChange={(v) =>
                      setEventOutcomeFilter(v as WebhookSyncEventLog["outcome"] | "all")
                    }
                  >
                    <SelectTrigger className="h-8 w-full text-xs sm:w-[140px]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">
                        {t("web.settings.webhooks.deliveries.allOutcomes")}
                      </SelectItem>
                      {(Object.keys(EVENT_OUTCOME_LABEL) as WebhookSyncEventLog["outcome"][]).map(
                        (key) => (
                          <SelectItem key={key} value={key}>
                            {t(EVENT_OUTCOME_LABEL[key])}
                          </SelectItem>
                        ),
                      )}
                    </SelectContent>
                  </Select>
                </div>

                {/* Table */}
                {pagedEvents.length > 0 ? (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead className="text-xs">
                          {t("web.settings.webhooks.deliveries.colStatus")}
                        </TableHead>
                        <TableHead className="text-xs">
                          {t("web.settings.webhooks.deliveries.colEvent")}
                        </TableHead>
                        <TableHead className="text-xs">
                          {t("web.settings.webhooks.deliveries.colItem")}
                        </TableHead>
                        <TableHead className="text-xs">
                          {t("web.settings.webhooks.deliveries.colUser")}
                        </TableHead>
                        <TableHead className="text-end text-xs">
                          {t("web.settings.webhooks.deliveries.colTime")}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {pagedEvents.map((event) => (
                        <TableRow
                          key={event.id}
                          className="cursor-pointer"
                          onClick={() => setSelectedEvent(event)}
                        >
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={cn("text-xs", EVENT_OUTCOME_BADGE[event.outcome])}
                            >
                              {t(EVENT_OUTCOME_LABEL[event.outcome])}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <p className="text-sm leading-snug">{event.summary}</p>
                            {event.error_message ? (
                              <p className="mt-0.5 text-xs leading-relaxed text-red-300/80">
                                {event.error_message}
                              </p>
                            ) : null}
                          </TableCell>
                          <TableCell className="max-w-[200px] text-xs">
                            {eventMatchedItemLabel(event) ?? (
                              <span className="text-muted-foreground">{EMPTY_CELL}</span>
                            )}
                          </TableCell>
                          <TableCell className="text-muted-foreground text-xs">
                            {eventUserLabel(t, event)}
                          </TableCell>
                          <TableCell className="text-muted-foreground text-end text-xs">
                            {relativeTime(t, event.received_at) ??
                              formatTimestamp(t, event.received_at)}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                ) : (
                  <p className="text-muted-foreground py-4 text-center text-sm">
                    {t("web.settings.webhooks.deliveries.noMatches")}
                  </p>
                )}

                {/* Pagination */}
                {filteredEvents.length > EVENTS_PER_PAGE ? (
                  <div className="border-border/40 flex items-center justify-between border-t px-1 pt-3">
                    <span className="text-muted-foreground text-xs tracking-tight tabular-nums">
                      {t("web.settings.webhooks.deliveries.range", {
                        start: String(eventRangeStart),
                        end: String(eventRangeEnd),
                        total: String(filteredEvents.length),
                      })}
                    </span>
                    <div className="flex items-center gap-0.5">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        disabled={eventPageClamped <= 0}
                        onClick={() => setEventPage(0)}
                        title={t("web.settings.webhooks.deliveries.firstPage")}
                      >
                        <ChevronsLeft className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        disabled={eventPageClamped <= 0}
                        onClick={() => setEventPage((p) => Math.max(0, p - 1))}
                        title={t("web.settings.webhooks.deliveries.previousPage")}
                      >
                        <ChevronLeft className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        disabled={eventPageClamped >= eventTotalPages - 1}
                        onClick={() => setEventPage((p) => Math.min(eventTotalPages - 1, p + 1))}
                        title={t("web.settings.webhooks.deliveries.nextPage")}
                      >
                        <ChevronRight className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        disabled={eventPageClamped >= eventTotalPages - 1}
                        onClick={() => setEventPage(eventTotalPages - 1)}
                        title={t("web.settings.webhooks.deliveries.lastPage")}
                      >
                        <ChevronsRight className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </div>
                ) : null}
              </>
            ) : (
              <p className="text-muted-foreground text-sm">
                {t("web.settings.webhooks.deliveries.empty")}
              </p>
            )}
          </SettingsGroup>
        </>
      ) : null}

      {/* Event detail dialog */}
      <Dialog open={!!selectedEvent} onOpenChange={(open) => !open && setSelectedEvent(null)}>
        {selectedEvent ? (
          <DialogContent className="sm:max-w-lg">
            <DialogHeader>
              <DialogTitle className="text-base">{selectedEvent.summary}</DialogTitle>
              <DialogDescription>
                {t("web.settings.webhooks.event.receivedHttp", {
                  time: formatTimestamp(t, selectedEvent.received_at),
                  status: String(selectedEvent.http_status),
                })}
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4">
              {/* Outcome + matched item */}
              <div className="flex flex-wrap items-center gap-2">
                <Badge
                  variant="outline"
                  className={cn("text-xs", EVENT_OUTCOME_BADGE[selectedEvent.outcome])}
                >
                  {t(EVENT_OUTCOME_LABEL[selectedEvent.outcome])}
                </Badge>
                {eventMatchedItemLabel(selectedEvent) ? (
                  <span className="text-sm">{eventMatchedItemLabel(selectedEvent)}</span>
                ) : null}
              </div>

              {selectedEvent.error_message ? (
                <p className="text-sm leading-relaxed text-red-300/80">
                  {selectedEvent.error_message}
                </p>
              ) : null}

              {/* Attributes */}
              {Object.keys(selectedEvent.attrs ?? {}).length > 0 ? (
                <div className="space-y-1.5">
                  <Label className="text-muted-foreground text-xs">
                    {t("web.settings.webhooks.event.attributes")}
                  </Label>
                  <div className="grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
                    {Object.entries(selectedEvent.attrs ?? {})
                      .filter(([, v]) => v !== "" && v != null)
                      .sort(([a], [b]) => a.localeCompare(b))
                      .map(([key, value]) => (
                        <div key={key} className="min-w-0">
                          <p className="text-muted-foreground text-xs">{eventAttrLabel(t, key)}</p>
                          <p className="truncate text-xs break-all">
                            {typeof value === "string" ? value : JSON.stringify(value)}
                          </p>
                        </div>
                      ))}
                  </div>
                </div>
              ) : null}

              {/* Body excerpt */}
              {selectedEvent.body_excerpt ? (
                <div className="space-y-1.5">
                  <Label className="text-muted-foreground text-xs">
                    {t("web.settings.webhooks.event.bodyExcerpt")}
                  </Label>
                  <pre className="bg-background max-h-60 overflow-auto rounded-md border px-3 py-2 text-xs whitespace-pre-wrap">
                    {selectedEvent.body_excerpt}
                  </pre>
                </div>
              ) : null}

              {/* Request ID */}
              {selectedEvent.request_id ? (
                <p className="text-muted-foreground text-xs">
                  {t("web.settings.webhooks.event.requestId", { id: selectedEvent.request_id })}
                </p>
              ) : null}
            </div>
          </DialogContent>
        ) : null}
      </Dialog>
    </div>
  );
}
