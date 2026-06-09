import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import {
  AlertTriangle,
  Check,
  Library,
  Plug,
  Plus,
  RefreshCw,
  Save,
  Settings2,
  SlidersHorizontal,
  Trash2,
  X,
} from "lucide-react";
import type {
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  PluginCapability,
  PluginInstallation,
  RequestApprovalMode,
  RequestIntegration,
  RequestIntegrationOptions,
  RequestIntegrationValidationError,
  RequestLimitMode,
  RequestSettings,
  RequestTarget,
  RequestUserLimit,
} from "@/api/types";
import { SchemaForm } from "@/components/admin/plugins/SchemaForm";
import { buildSchemaValues, parseFieldTypes } from "@/components/admin/plugins/schemaFormUtils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useDebounce } from "@/hooks/useDebounce";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import {
  useAdminMediaRequests,
  useApproveMediaRequest,
  useCreateRequestIntegration,
  useDeclineMediaRequest,
  useDeleteRequestIntegration,
  useLoadRequestIntegrationOptions,
  useRequestIntegrations,
  useRequestSettings,
  useRequestUserLimit,
  useRetryMediaRequest,
  useUpdateRequestIntegration,
  useUpdateRequestSettings,
  useUpdateRequestUserLimit,
} from "@/hooks/queries/useRequests";
import {
  formatMediaType,
  formatRequestDate,
  formatRequestOutcome,
  formatRequestStatus,
  requestOutcomeBadgeVariant,
  requestStatusBadgeVariant,
  REQUEST_OUTCOMES,
  REQUEST_STATUSES,
} from "@/lib/mediaRequests";
import { applyExclusivity } from "./requestExclusivity";
import { supportedMediaTypesForConfig } from "./requestIntegrationMediaTypes";

type StatusFilter = MediaRequestStatus | "all";
type OutcomeFilter = MediaRequestOutcome | "all";

const ADMIN_REQUEST_TABS = ["queue", "settings", "integrations", "overrides"] as const;
type AdminRequestTab = (typeof ADMIN_REQUEST_TABS)[number];

function normalizeAdminRequestTab(value: string | null): AdminRequestTab {
  return ADMIN_REQUEST_TABS.includes(value as AdminRequestTab)
    ? (value as AdminRequestTab)
    : "queue";
}

export default function AdminRequests() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = normalizeAdminRequestTab(searchParams.get("tab"));

  function setActiveTab(value: string) {
    const nextTab = normalizeAdminRequestTab(value);
    const next = new URLSearchParams(searchParams);

    if (nextTab === "queue") {
      next.delete("tab");
    } else {
      next.set("tab", nextTab);
    }

    setSearchParams(next, { replace: true });
  }

  return (
    <div className="space-y-6">
      <div className="page-header">
        <div className="space-y-2">
          <h1 className="text-3xl font-semibold tracking-normal text-balance sm:text-4xl">
            Requests
          </h1>
          <p className="text-muted-foreground max-w-2xl text-sm leading-6">
            Review media requests, set limits, and manage Radarr or Sonarr routing.
          </p>
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab} className="gap-5">
        <TabsList variant="line" className="border-border w-full justify-start border-b">
          <TabsTrigger value="queue">Queue</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="integrations">Integrations</TabsTrigger>
          <TabsTrigger value="overrides">User Overrides</TabsTrigger>
        </TabsList>
        <TabsContent value="queue">
          <RequestQueueTab />
        </TabsContent>
        <TabsContent value="settings">
          <RequestSettingsTab />
        </TabsContent>
        <TabsContent value="integrations">
          <RequestIntegrationsTab />
        </TabsContent>
        <TabsContent value="overrides">
          <UserOverridesTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function RequestQueueTab() {
  const [status, setStatus] = useState<StatusFilter>("all");
  const [outcome, setOutcome] = useState<OutcomeFilter>("all");
  const requests = useAdminMediaRequests({ status, outcome, limit: 100 });
  const users = useAdminUsers();
  const approve = useApproveMediaRequest();
  const decline = useDeclineMediaRequest();
  const retry = useRetryMediaRequest();
  const [declineTarget, setDeclineTarget] = useState<MediaRequest | null>(null);
  const [declineReason, setDeclineReason] = useState("");
  const usernamesByID = useMemo(() => {
    return new Map((users.data ?? []).map((user) => [user.id, user.username]));
  }, [users.data]);

  function handleDecline(request: MediaRequest) {
    setDeclineTarget(request);
    setDeclineReason("");
  }

  function confirmDecline() {
    if (!declineTarget) return;
    decline.mutate({ id: declineTarget.id, reason: declineReason });
    setDeclineTarget(null);
    setDeclineReason("");
  }

  return (
    <div className="space-y-4">
      <div className="border-border bg-card flex flex-wrap items-center gap-3 rounded-lg border p-3">
        <Select value={status} onValueChange={(value) => setStatus(value as StatusFilter)}>
          <SelectTrigger className="w-[170px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {REQUEST_STATUSES.map((value) => (
              <SelectItem key={value} value={value}>
                {value === "all" ? "All statuses" : formatRequestStatus(value)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={outcome} onValueChange={(value) => setOutcome(value as OutcomeFilter)}>
          <SelectTrigger className="w-[170px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {REQUEST_OUTCOMES.map((value) => (
              <SelectItem key={value} value={value}>
                {value === "all" ? "All outcomes" : formatRequestOutcome(value)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="sm"
          onClick={() => requests.refetch()}
          disabled={requests.isFetching}
        >
          <RefreshCw className="h-4 w-4" />
          Refresh
        </Button>
      </div>

      {requests.isLoading ? (
        <RowsSkeleton />
      ) : requests.isError ? (
        <EmptyPanel title="Requests failed" detail="The request queue could not be loaded." />
      ) : (
        <div className="border-border bg-card overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Title</TableHead>
                <TableHead>Requested</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Outcome</TableHead>
                <TableHead>Integration</TableHead>
                <TableHead className="w-[240px]">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(requests.data ?? []).length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="text-muted-foreground py-8 text-center">
                    No requests match the current filters.
                  </TableCell>
                </TableRow>
              ) : (
                requests.data?.map((request) => (
                  <RequestQueueRow
                    key={request.id}
                    request={request}
                    requesterUsername={
                      request.requested_by_user_id
                        ? usernamesByID.get(request.requested_by_user_id)
                        : undefined
                    }
                    approving={approve.isPending && approve.variables === request.id}
                    declining={decline.isPending && decline.variables?.id === request.id}
                    retrying={retry.isPending && retry.variables === request.id}
                    onApprove={() => approve.mutate(request.id)}
                    onDecline={() => handleDecline(request)}
                    onRetry={() => retry.mutate(request.id)}
                  />
                ))
              )}
            </TableBody>
          </Table>
        </div>
      )}
      <Dialog
        open={declineTarget !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeclineTarget(null);
            setDeclineReason("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Decline request</DialogTitle>
            <DialogDescription>
              {declineTarget
                ? `"${declineTarget.title}" will be marked declined. Add an optional note for the requester.`
                : null}
            </DialogDescription>
          </DialogHeader>
          <Label htmlFor="decline-reason" className="text-sm">
            Reason (optional)
          </Label>
          <textarea
            id="decline-reason"
            className="border-input bg-background text-foreground focus-visible:ring-ring min-h-[88px] w-full rounded-md border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:outline-none"
            value={declineReason}
            onChange={(event) => setDeclineReason(event.target.value)}
            placeholder="e.g. duplicate of an existing request"
          />
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setDeclineTarget(null);
                setDeclineReason("");
              }}
            >
              Cancel
            </Button>
            <Button onClick={confirmDecline} disabled={decline.isPending}>
              Decline
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function RequestQueueRow({
  request,
  requesterUsername,
  approving,
  declining,
  retrying,
  onApprove,
  onDecline,
  onRetry,
}: {
  request: MediaRequest;
  requesterUsername?: string;
  approving: boolean;
  declining: boolean;
  retrying: boolean;
  onApprove: () => void;
  onDecline: () => void;
  onRetry: () => void;
}) {
  const canApprove = request.status === "pending" && request.outcome === "active";
  const canDecline = request.status !== "completed" && request.outcome === "active";
  const canRetry = request.outcome === "failed";
  const requesterLabel = requesterUsername ?? `User ${request.requested_by_user_id}`;
  const requestDetailHref = `/requests/${request.media_type}/${request.tmdb_id}`;

  return (
    <TableRow>
      <TableCell>
        <div className="min-w-[220px]">
          <div className="flex flex-wrap items-center gap-2">
            <Link to={requestDetailHref} className="font-medium hover:underline">
              {request.title}
            </Link>
            <Badge variant="secondary">{formatMediaType(request.media_type)}</Badge>
          </div>
          <div className="text-muted-foreground mt-1 flex flex-wrap gap-x-3 text-xs">
            {request.year ? <span>{request.year}</span> : null}
            <span>TMDB {request.tmdb_id}</span>
            {request.requested_by_user_id ? (
              <Link
                to={`/admin/users/${request.requested_by_user_id}`}
                className="hover:text-foreground hover:underline"
              >
                {requesterLabel}
              </Link>
            ) : null}
            {request.library_content_id ? (
              <Link
                to={`/item/${encodeURIComponent(request.library_content_id)}`}
                className="hover:text-foreground inline-flex items-center gap-1 hover:underline"
              >
                <Library className="h-3 w-3" />
                Library
              </Link>
            ) : null}
          </div>
          {request.last_error ? (
            <p className="text-destructive mt-1 max-w-md text-xs">{request.last_error}</p>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground text-xs">{formatRequestDate(request)}</TableCell>
      <TableCell>
        <Badge variant={requestStatusBadgeVariant(request.status)}>
          {formatRequestStatus(request.status)}
        </Badge>
      </TableCell>
      <TableCell>
        <Badge variant={requestOutcomeBadgeVariant(request.outcome)}>
          {formatRequestOutcome(request.outcome)}
        </Badge>
      </TableCell>
      <TableCell className="text-muted-foreground text-xs">
        {request.targets?.length ? (
          <div className="flex flex-col gap-1.5">
            {request.is_anime ? (
              <Badge variant="secondary" className="w-fit">
                Anime
              </Badge>
            ) : null}
            {request.targets.map((target) => (
              <RequestTargetBadge key={target.id} target={target} />
            ))}
          </div>
        ) : (
          "Not submitted"
        )}
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={onApprove}
            disabled={!canApprove || approving}
          >
            <Check className="h-4 w-4" />
            Approve
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={onDecline}
            disabled={!canDecline || declining}
          >
            <X className="h-4 w-4" />
            Decline
          </Button>
          <Button size="sm" variant="outline" onClick={onRetry} disabled={!canRetry || retrying}>
            <RefreshCw className="h-4 w-4" />
            Retry
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

function RequestTargetBadge({ target }: { target: RequestTarget }) {
  const qualityLabel = target.quality === "2160p" ? "2160p" : "1080p";
  const instanceLabel = target.instance_name || target.integration_kind || "Unknown";
  const failed = target.status === "failed";
  const statusLabel = target.status === "failed" ? "Failed" : formatRequestStatus(target.status);
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="outline">{qualityLabel}</Badge>
        <span className="text-foreground">{instanceLabel}</span>
        <Badge variant={failed ? "destructive" : "secondary"}>{statusLabel}</Badge>
        {target.external_status ? <span>{target.external_status}</span> : null}
      </div>
      {failed && target.last_error ? (
        <p className="text-destructive flex max-w-xs items-start gap-1">
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
          <span>{target.last_error}</span>
        </p>
      ) : null}
    </div>
  );
}

type SettingsFormState = {
  requests_enabled: boolean;
  global_max_requests: string;
  global_window_days: string;
  global_auto_approval_enabled: boolean;
  force_dual_quality: boolean;
  updated_at: string;
};

function RequestSettingsTab() {
  const settings = useRequestSettings();

  if (settings.isLoading) return <RowsSkeleton />;
  if (settings.isError) {
    return <EmptyPanel title="Settings failed" detail="Request settings could not be loaded." />;
  }
  if (!settings.data) {
    return <EmptyPanel title="No settings" detail="Request settings are not available." />;
  }

  return <RequestSettingsForm key={settings.data.updated_at} settings={settings.data} />;
}

function RequestSettingsForm({ settings }: { settings: RequestSettings }) {
  const updateSettings = useUpdateRequestSettings();
  const [form, setForm] = useState<SettingsFormState>(() => ({
    requests_enabled: settings.requests_enabled,
    global_max_requests: String(settings.global_max_requests),
    global_window_days: String(settings.global_window_days),
    global_auto_approval_enabled: settings.global_auto_approval_enabled,
    force_dual_quality: settings.force_dual_quality,
    updated_at: settings.updated_at,
  }));

  function saveSettings() {
    const payload: RequestSettings = {
      requests_enabled: form.requests_enabled,
      global_max_requests: Math.max(0, Number(form.global_max_requests) || 0),
      global_window_days: Math.max(1, Number(form.global_window_days) || 1),
      global_auto_approval_enabled: form.global_auto_approval_enabled,
      force_dual_quality: form.force_dual_quality,
      updated_at: form.updated_at,
    };
    updateSettings.mutate(payload);
  }

  return (
    <div className="border-border bg-card max-w-3xl space-y-5 rounded-lg border p-5">
      <div className="flex items-center gap-2">
        <Settings2 className="text-primary h-4 w-4" />
        <h2 className="text-lg font-semibold tracking-normal">Global Settings</h2>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <SwitchField
          label="Requests enabled"
          checked={form.requests_enabled}
          onCheckedChange={(checked) =>
            setForm((current) => ({ ...current, requests_enabled: checked }))
          }
        />
        <SwitchField
          label="Auto approval"
          checked={form.global_auto_approval_enabled}
          onCheckedChange={(checked) =>
            setForm((current) => ({ ...current, global_auto_approval_enabled: checked }))
          }
        />
        <Field label="Max requests">
          <Input
            type="number"
            min={0}
            value={form.global_max_requests}
            onChange={(event) =>
              setForm((current) => ({ ...current, global_max_requests: event.target.value }))
            }
          />
        </Field>
        <Field label="Window days">
          <Input
            type="number"
            min={1}
            value={form.global_window_days}
            onChange={(event) =>
              setForm((current) => ({ ...current, global_window_days: event.target.value }))
            }
          />
        </Field>
        <div className="sm:col-span-2">
          <SwitchField
            label="Always fulfill in both 1080p and 4K"
            description="Applies to all requests when both a Default HD and Default 4K instance exist, regardless of user role."
            checked={form.force_dual_quality}
            onCheckedChange={(checked) =>
              setForm((current) => ({ ...current, force_dual_quality: checked }))
            }
          />
        </div>
      </div>

      <Button onClick={saveSettings} disabled={updateSettings.isPending}>
        <Save className="h-4 w-4" />
        Save Settings
      </Button>
    </div>
  );
}

// Host chrome owned by Silo; everything arr-specific now lives in pluginConfig
// and is rendered by the plugin's connection descriptor via <SchemaForm>.
type IntegrationFormState = {
  id: string;
  name: string;
  enabled: boolean;
  base_url: string;
  api_key_ref: string;
  has_api_key: boolean;
  // Selected installed plugin (which request-router plugin fulfills this connection).
  // Empty string means "none selected"; the backend requires a non-empty value.
  installation_id: string;
  // The specific request_router.v1 capability sub-id on that installation. Tracked
  // alongside installation_id because one installation can expose more than one
  // capability, so installation_id alone can't identify the chosen backend.
  capability_id: string;
};

// All request integrations are fulfilled by a plugin exposing this capability.
const REQUEST_ROUTER_CAPABILITY = "request_router.v1";

type RequestRouterInstallation = {
  installationID: number;
  pluginID: string;
  capability: PluginCapability;
};

// requestRouterInstallations flattens installed plugins to one entry per
// request_router.v1 capability so the form can offer an installation selector.
function requestRouterInstallations(
  installations: PluginInstallation[],
): RequestRouterInstallation[] {
  const out: RequestRouterInstallation[] = [];
  for (const installation of installations) {
    for (const capability of installation.capabilities ?? []) {
      if (
        capability.type === REQUEST_ROUTER_CAPABILITY ||
        capability.id === REQUEST_ROUTER_CAPABILITY
      ) {
        out.push({
          installationID: installation.id,
          pluginID: installation.plugin_id,
          capability,
        });
      }
    }
  }
  return out;
}

function installationOptionLabel(entry: RequestRouterInstallation): string {
  const name = entry.capability.display_name || entry.pluginID;
  return `${name} (${entry.capability.id})`;
}

// installationOptionValue is the <Select> option value: it encodes both the
// installation and the capability sub-id so multiple capabilities on one
// installation stay distinct (installation_id alone would collide).
function installationOptionValue(entry: RequestRouterInstallation): string {
  return `${entry.installationID}:${entry.capability.id}`;
}

function RequestIntegrationsTab() {
  const integrations = useRequestIntegrations();

  if (integrations.isLoading) return <RowsSkeleton />;
  if (integrations.isError) {
    return (
      <EmptyPanel title="Integrations failed" detail="Request integrations could not be loaded." />
    );
  }

  const list = integrations.data ?? [];
  const integrationsKey =
    list.length === 0
      ? "empty"
      : list.map((integration) => `${integration.id}:${integration.updated_at ?? ""}`).join("|");

  return <RequestIntegrationsForm key={integrationsKey} integrations={list} />;
}

type IntegrationCard = {
  key: string;
  form: IntegrationFormState;
  pluginConfig: Record<string, unknown>;
  source: RequestIntegration | null;
};

let integrationCardCounter = 0;

function nextCardKey(): string {
  integrationCardCounter += 1;
  return `card-${integrationCardCounter}`;
}

function integrationToForm(integration?: RequestIntegration): IntegrationFormState {
  return {
    id: integration?.id ?? "",
    name: integration?.name ?? "",
    enabled: integration?.enabled ?? true,
    base_url: integration?.base_url ?? "",
    api_key_ref: "",
    has_api_key: integration?.has_api_key ?? false,
    installation_id: integration?.installation_id ? String(integration.installation_id) : "",
    capability_id: integration?.capability_id ?? "",
  };
}

type ConnectionOptionsStatus = "idle" | "loading" | "error";

// useConnectionOptions debounces a ListConfigOptions probe keyed on the connection
// IDENTITY (base URL + key ref + installation + capability) — NOT the full
// plugin_config, so editing a quality profile / switch doesn't re-probe the arr
// API. It backs the dynamic SELECT options (root folders, quality profiles, tags)
// the descriptor declares. The probe is silent (no toasts); callers surface
// failures inline via `status`. A generation counter enforces latest-wins so a
// slow older probe can't overwrite a newer result, and options are cleared
// whenever a probe can't run.
function useConnectionOptions(
  connectionID: string,
  draft: {
    base_url: string;
    api_key_ref: string;
    has_api_key: boolean;
    installation_id?: number;
    capability_id: string;
    plugin_config: Record<string, unknown>;
  },
) {
  const load = useLoadRequestIntegrationOptions();
  const [options, setOptions] = useState<RequestIntegrationOptions>({});
  const [status, setStatus] = useState<ConnectionOptionsStatus>("idle");
  // Latest-wins guard: each probe captures the generation it started with and
  // only commits its result if no newer probe has begun since.
  const genRef = useRef(0);

  const canLoad =
    draft.base_url.trim().length > 0 &&
    Boolean(draft.installation_id) &&
    draft.capability_id.trim().length > 0 &&
    Boolean(draft.api_key_ref.trim() || draft.has_api_key);

  // Options depend ONLY on the connection identity. plugin_config is still sent
  // in the request body so the plugin can resolve options, but it is not part of
  // the signature — see #2.
  const sig = JSON.stringify({
    u: draft.base_url,
    k: draft.api_key_ref,
    i: draft.installation_id,
    c: draft.capability_id,
  });
  const debouncedSig = useDebounce(sig, 400);

  // Snapshot the latest connection inputs so the debounced effect sends the
  // current plugin_config without re-firing when only plugin_config changes.
  const draftRef = useRef(draft);
  draftRef.current = draft;

  useEffect(() => {
    if (!canLoad) {
      // Bump the generation so any in-flight probe is invalidated, and clear any
      // stale options from a previous/invalid connection so they never linger.
      genRef.current += 1;
      setOptions({});
      setStatus("idle");
      return;
    }
    const current = draftRef.current;
    const gen = ++genRef.current;
    setStatus("loading");
    load
      .mutateAsync({
        id: connectionID || "new",
        body: {
          base_url: current.base_url,
          api_key_ref: current.api_key_ref.trim() || undefined,
          capability_id: current.capability_id,
          installation_id: current.installation_id,
          plugin_config: current.plugin_config,
        },
      })
      .then((loaded) => {
        if (gen === genRef.current) {
          setOptions(loaded);
          setStatus("idle");
        }
      })
      .catch(() => {
        if (gen === genRef.current) {
          setOptions({});
          setStatus("error");
        }
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedSig, canLoad]);
  return { options, status };
}

function RequestIntegrationsForm({ integrations }: { integrations: RequestIntegration[] }) {
  const installationsQuery = useAdminPluginInstallations();
  const routerInstallations = useMemo(
    () => requestRouterInstallations(installationsQuery.data ?? []),
    [installationsQuery.data],
  );
  // When exactly one request-router plugin is installed, default new/unseeded
  // connections to it so the admin doesn't have to pick.
  const defaultSelection: Pick<IntegrationFormState, "installation_id" | "capability_id"> =
    routerInstallations.length === 1
      ? {
          installation_id: String(routerInstallations[0]?.installationID ?? ""),
          capability_id: routerInstallations[0]?.capability.id ?? "",
        }
      : { installation_id: "", capability_id: "" };

  const [cards, setCards] = useState<IntegrationCard[]>(() =>
    integrations.map((integration) => ({
      key: integration.id || nextCardKey(),
      form: integrationToForm(integration),
      pluginConfig: { ...(integration.plugin_config ?? {}) },
      source: integration,
    })),
  );

  function updateCard(key: string, patch: Partial<IntegrationFormState>) {
    setCards((current) =>
      current.map((card) =>
        card.key === key ? { ...card, form: { ...card.form, ...patch } } : card,
      ),
    );
  }

  function updateCardConfig(key: string, pluginConfig: Record<string, unknown>) {
    setCards((current) => {
      const mapped = current.map((card) => ({
        key: card.key,
        installationId: card.form.installation_id,
        config: card.key === key ? pluginConfig : card.pluginConfig,
      }));
      const fieldsFor = (installationId: string) =>
        routerInstallations.find((entry) => String(entry.installationID) === installationId)
          ?.capability.config_schema?.[0]?.admin_form?.fields ?? [];
      const next = applyExclusivity(mapped, key, pluginConfig, fieldsFor);
      const byKey = new Map(next.map((entry) => [entry.key, entry.config]));
      return current.map((card) => ({
        ...card,
        pluginConfig: byKey.get(card.key) ?? card.pluginConfig,
      }));
    });
  }

  function addCard() {
    setCards((current) => [
      ...current,
      {
        key: nextCardKey(),
        form: { ...integrationToForm(), ...defaultSelection },
        pluginConfig: {},
        source: null,
      },
    ]);
  }

  function removeCard(key: string) {
    setCards((current) => current.filter((card) => card.key !== key));
  }

  const noRouterPlugin = !installationsQuery.isLoading && routerInstallations.length === 0;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold tracking-normal">Connections</h2>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={addCard}
          disabled={noRouterPlugin}
        >
          <Plus className="h-4 w-4" />
          Add connection
        </Button>
      </div>
      {noRouterPlugin ? (
        <EmptyPanel
          title="No request-router plugin installed"
          detail={`Install a plugin that exposes the ${REQUEST_ROUTER_CAPABILITY} capability before adding connections.`}
        />
      ) : cards.length === 0 ? (
        <EmptyPanel
          title="No connections"
          detail="Add a connection and pick a plugin to route requests."
        />
      ) : (
        <div className="grid gap-4 xl:grid-cols-2">
          {cards.map((card) => (
            <IntegrationEditor
              key={card.key}
              form={card.form}
              pluginConfig={card.pluginConfig}
              installations={routerInstallations}
              installationsLoading={installationsQuery.isLoading}
              source={card.source}
              onChange={(patch) => updateCard(card.key, patch)}
              onConfigChange={(config) => updateCardConfig(card.key, config)}
              onRemove={() => removeCard(card.key)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function IntegrationEditor({
  form,
  pluginConfig,
  installations,
  installationsLoading,
  source,
  onChange,
  onConfigChange,
  onRemove,
}: {
  form: IntegrationFormState;
  pluginConfig: Record<string, unknown>;
  installations: RequestRouterInstallation[];
  installationsLoading: boolean;
  source: RequestIntegration | null;
  onChange: (patch: Partial<IntegrationFormState>) => void;
  onConfigChange: (config: Record<string, unknown>) => void;
  onRemove: () => void;
}) {
  const isNew = form.id === "";
  const createIntegration = useCreateRequestIntegration();
  const updateIntegration = useUpdateRequestIntegration();
  const deleteIntegration = useDeleteRequestIntegration();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  // SchemaForm owns config validation and reports it up; the editor consumes the
  // result instead of re-running validateSchemaValues itself.
  const [schemaValid, setSchemaValid] = useState(true);

  // Any edit invalidates a prior failed save's server-side errors, so clear them
  // wholesale on edit. A corrected field's stale "does not exist" then disappears.
  function clearSaveErrors() {
    setFieldErrors((current) => (Object.keys(current).length === 0 ? current : {}));
    setFormError((current) => (current === null ? current : null));
  }

  function patchForm(patch: Partial<IntegrationFormState>) {
    clearSaveErrors();
    onChange(patch);
  }

  function patchConfig(config: Record<string, unknown>) {
    clearSaveErrors();
    onConfigChange(config);
  }

  // Default-select the only installed request-router plugin once installations
  // have loaded, when this connection has no installation set yet. Depend on a
  // stable scalar (not the array identity) so a refetch returning the same single
  // plugin does not re-fire and re-select after a deliberate clear.
  const soleInstallation = installations.length === 1 ? installations[0] : undefined;
  const soleInstallationID =
    soleInstallation !== undefined ? String(soleInstallation.installationID) : undefined;
  useEffect(() => {
    if (form.installation_id || soleInstallation === undefined) return;
    onChange({
      installation_id: String(soleInstallation.installationID),
      capability_id: soleInstallation.capability.id,
    });
    // onChange identity is stable per card; intentionally depend on the data only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form.installation_id, soleInstallationID]);

  const selectedInstallationID = Number(form.installation_id);
  const hasInstallation = Number.isInteger(selectedInstallationID) && selectedInstallationID > 0;
  // Match on both installation and capability sub-id so a multi-capability
  // installation resolves the exact backend; fall back to installation-only for
  // connections whose capability_id isn't set yet (adopts that installation's
  // capability).
  const selected =
    installations.find(
      (entry) =>
        entry.installationID === selectedInstallationID &&
        entry.capability.id === form.capability_id,
    ) ?? installations.find((entry) => entry.installationID === selectedInstallationID);

  // Switching the selected plugin must drop the previous plugin's config (so its
  // keys never reach the new plugin's schema in the options probe or save) and
  // clear stale save errors (handled by patchForm -> clearSaveErrors).
  function handlePluginChange(value: string) {
    const entry = installations.find((e) => installationOptionValue(e) === value);
    if (!entry) return;
    if (
      entry.installationID === selectedInstallationID &&
      entry.capability.id === form.capability_id
    ) {
      return; // no actual change; keep existing config
    }
    patchForm({
      installation_id: String(entry.installationID),
      capability_id: entry.capability.id,
    });
    onConfigChange({});
  }
  const selectedConfigSchema = selected?.capability.config_schema?.[0];
  const descriptor = selectedConfigSchema?.admin_form;
  const title = selected?.capability.display_name || selected?.pluginID || "Connection";

  const { options, status: optionsStatus } = useConnectionOptions(form.id, {
    base_url: form.base_url,
    api_key_ref: form.api_key_ref,
    has_api_key: form.has_api_key,
    installation_id: hasInstallation ? selectedInstallationID : undefined,
    capability_id: selected?.capability.id ?? "",
    plugin_config: pluginConfig,
  });

  // Once dynamic options arrive, default each empty single-SELECT field to its
  // first option (restores the old root_folder/quality_profile pre-selection
  // generically). MULTI_SELECT (tags) is left empty; values the admin already
  // chose are never clobbered. Read config/onChange from refs so the effect keys
  // only on options + descriptor and doesn't re-fire on unrelated edits.
  const configRef = useRef(pluginConfig);
  configRef.current = pluginConfig;
  const onConfigChangeRef = useRef(onConfigChange);
  onConfigChangeRef.current = onConfigChange;
  useEffect(() => {
    if (!descriptor) return;
    const current = configRef.current;
    const patch: Record<string, unknown> = {};
    for (const field of descriptor.fields) {
      if (field.control !== "SELECT" || !field.dynamic_options) continue;
      const first = options[field.key]?.[0];
      if (!first) continue;
      const value = current[field.key];
      const empty = value === undefined || value === null || value === "";
      if (empty) patch[field.key] = first.value;
    }
    if (Object.keys(patch).length > 0) {
      onConfigChangeRef.current({ ...current, ...patch });
    }
  }, [options, descriptor]);

  const saving = createIntegration.isPending || updateIntegration.isPending;
  // New instances must carry an API key (there's no saved key to fall back on);
  // edits may leave it blank to keep the stored key (has_api_key).
  const hasApiKey = form.api_key_ref.trim().length > 0 || form.has_api_key;
  // schemaValid is reported by SchemaForm via onValidityChange. With no descriptor
  // (no plugin form) there's nothing to validate, so the chrome checks govern.
  const canSave =
    form.name.trim().length > 0 &&
    form.base_url.trim().length > 0 &&
    hasApiKey &&
    hasInstallation &&
    (!descriptor || schemaValid);

  function handleSave() {
    // Coercion is driven by the plugin's declared json_schema types so e.g. a
    // numeric string isn't sent where a string is declared (and vice versa).
    const fieldTypes = parseFieldTypes(selectedConfigSchema?.json_schema);
    const nextPluginConfig = descriptor
      ? buildSchemaValues(descriptor, pluginConfig, fieldTypes)
      : pluginConfig;
    // plugin_config is the sole source of truth for fulfillment; the host owns
    // only the generic connection chrome (name, base_url, api_key, installation).
    const payload = {
      id: form.id,
      name: form.name.trim(),
      enabled: form.enabled,
      base_url: form.base_url.trim(),
      api_key_ref: form.api_key_ref.trim() || undefined,
      capability_id: selected?.capability.id ?? "",
      installation_id: hasInstallation ? selectedInstallationID : undefined,
      supported_media_types: supportedMediaTypesForConfig(nextPluginConfig, source),
      plugin_config: nextPluginConfig,
    } as RequestIntegration;

    setFieldErrors({});
    setFormError(null);
    const mut = isNew ? createIntegration : updateIntegration;
    mut.mutate(payload, {
      onError: (err) => {
        const body = (err as { body?: unknown })?.body as
          | RequestIntegrationValidationError
          | undefined;
        if (body?.field_errors) setFieldErrors(body.field_errors);
        if (body?.form_error) setFormError(body.form_error);
      },
    });
  }

  return (
    <div className="border-border bg-card space-y-5 rounded-lg border p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Plug className="text-primary h-4 w-4" />
          <h2 className="text-lg font-semibold tracking-normal">{title}</h2>
          {form.has_api_key ? <Badge variant="secondary">Key saved</Badge> : null}
          {isNew ? <Badge variant="outline">New</Badge> : null}
        </div>
        <label className="flex cursor-pointer items-center gap-2 text-sm select-none">
          <span className="text-muted-foreground">{form.enabled ? "Enabled" : "Disabled"}</span>
          <Switch checked={form.enabled} onCheckedChange={(enabled) => patchForm({ enabled })} />
        </label>
      </div>

      {formError ? (
        <p className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm">
          {formError}
        </p>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name">
          <Input
            value={form.name}
            onChange={(event) => patchForm({ name: event.target.value })}
            placeholder="Connection name"
          />
        </Field>
        <Field label="API key or setting key">
          <Input
            value={form.api_key_ref}
            onChange={(event) => patchForm({ api_key_ref: event.target.value })}
            placeholder={form.has_api_key ? "Leave blank to keep saved key" : "API key"}
          />
        </Field>
      </div>

      <Field label="Base URL">
        <Input
          value={form.base_url}
          onChange={(event) => patchForm({ base_url: event.target.value })}
          placeholder="http://localhost:7878"
        />
      </Field>

      <Field label="Plugin">
        {installationsLoading ? (
          <Skeleton className="h-9 w-full rounded-md" />
        ) : installations.length === 0 ? (
          <p className="text-destructive text-xs">
            No installed plugin exposes the {REQUEST_ROUTER_CAPABILITY} capability. Install a
            request-router plugin before adding connections.
          </p>
        ) : (
          <Select
            value={selected ? installationOptionValue(selected) : ""}
            onValueChange={handlePluginChange}
          >
            <SelectTrigger className="w-full">
              <SelectValue placeholder="Select plugin" />
            </SelectTrigger>
            <SelectContent>
              {installations.map((entry) => (
                <SelectItem
                  key={installationOptionValue(entry)}
                  value={installationOptionValue(entry)}
                >
                  {installationOptionLabel(entry)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {!installationsLoading && installations.length > 0 && !hasInstallation ? (
          <p className="text-destructive text-xs">Select a plugin to fulfill this connection.</p>
        ) : null}
      </Field>

      {descriptor ? (
        <div className="space-y-2">
          <SchemaForm
            descriptor={descriptor}
            values={pluginConfig}
            onChange={patchConfig}
            dynamicOptions={options}
            optionsLoading={optionsStatus === "loading"}
            errors={fieldErrors}
            onValidityChange={setSchemaValid}
            idPrefix={`conn-${form.id || form.installation_id || "new"}`}
          />
          {optionsStatus === "error" && form.base_url.trim().length > 0 ? (
            <p className="border-destructive/40 bg-destructive/10 text-destructive flex items-start gap-2 rounded-md border px-3 py-2 text-xs">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>
                Couldn&apos;t load options from the service — check the base URL and API key, then
                edit a field to retry.
              </span>
            </p>
          ) : null}
        </div>
      ) : hasInstallation ? (
        <p className="text-muted-foreground text-sm">
          This plugin does not expose a connection configuration form.
        </p>
      ) : (
        <p className="text-muted-foreground text-sm">
          Select a plugin to configure this connection.
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" onClick={handleSave} disabled={!canSave || saving}>
          <Save className="h-4 w-4" />
          {isNew ? "Create connection" : "Save"}
        </Button>
        {isNew ? (
          <Button type="button" variant="ghost" onClick={onRemove}>
            <X className="h-4 w-4" />
            Discard
          </Button>
        ) : (
          <Button
            type="button"
            variant="ghost"
            className="text-destructive"
            onClick={() => setConfirmDelete(true)}
            disabled={deleteIntegration.isPending}
          >
            <Trash2 className="h-4 w-4" />
            Delete
          </Button>
        )}
      </div>

      <Dialog
        open={confirmDelete}
        onOpenChange={(open) => {
          if (!open) setConfirmDelete(false);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete connection</DialogTitle>
            <DialogDescription>
              {`"${form.name.trim() || title}" will be permanently removed. New requests will no longer route to this connection.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                deleteIntegration.mutate(form.id);
                setConfirmDelete(false);
              }}
              disabled={deleteIntegration.isPending}
            >
              <Trash2 className="h-4 w-4" />
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

type UserLimitFormState = {
  limit_mode: RequestLimitMode;
  max_requests: string;
  window_days: string;
  approval_mode: RequestApprovalMode;
};

function UserOverridesTab() {
  const users = useAdminUsers();
  const [selectedUserID, setSelectedUserID] = useState<number | undefined>();
  const effectiveUserID = selectedUserID ?? users.data?.[0]?.id;
  const limit = useRequestUserLimit(effectiveUserID);

  const selectedUser = useMemo(
    () => users.data?.find((user) => user.id === effectiveUserID),
    [effectiveUserID, users.data],
  );

  if (users.isLoading) return <RowsSkeleton />;
  if (users.isError) {
    return <EmptyPanel title="Users failed" detail="Users could not be loaded." />;
  }

  return (
    <div className="border-border bg-card max-w-3xl space-y-5 rounded-lg border p-5">
      <div className="flex items-center gap-2">
        <SlidersHorizontal className="text-primary h-4 w-4" />
        <h2 className="text-lg font-semibold tracking-normal">User Overrides</h2>
      </div>

      <Field label="User">
        <Select
          value={effectiveUserID ? String(effectiveUserID) : ""}
          onValueChange={(value) => setSelectedUserID(Number(value))}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder="Select user" />
          </SelectTrigger>
          <SelectContent>
            {(users.data ?? []).map((user) => (
              <SelectItem key={user.id} value={String(user.id)}>
                {user.username}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>

      {limit.isLoading ? (
        <RowsSkeleton />
      ) : limit.isError || !limit.data || !effectiveUserID ? (
        <EmptyPanel title="Limit failed" detail="The selected user limit could not be loaded." />
      ) : (
        <UserLimitEditor
          key={userLimitFormKey(limit.data)}
          userID={effectiveUserID}
          limit={limit.data}
          userAvailable={Boolean(selectedUser)}
        />
      )}
    </div>
  );
}

function UserLimitEditor({
  userID,
  limit,
  userAvailable,
}: {
  userID: number;
  limit: RequestUserLimit;
  userAvailable: boolean;
}) {
  const updateLimit = useUpdateRequestUserLimit();
  const [form, setForm] = useState<UserLimitFormState>(() => ({
    limit_mode: limit.limit_mode,
    max_requests: limit.max_requests == null ? "" : String(limit.max_requests),
    window_days: limit.window_days == null ? "" : String(limit.window_days),
    approval_mode: limit.approval_mode,
  }));

  function saveLimit() {
    const custom = form.limit_mode === "custom";
    const payload: RequestUserLimit = {
      user_id: userID,
      limit_mode: form.limit_mode,
      approval_mode: form.approval_mode,
      max_requests: custom ? Math.max(0, Number(form.max_requests) || 0) : undefined,
      window_days: custom ? Math.max(1, Number(form.window_days) || 1) : undefined,
    };
    updateLimit.mutate({ userId: userID, body: payload });
  }

  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Limit mode">
          <Select
            value={form.limit_mode}
            onValueChange={(value) =>
              setForm((current) => ({ ...current, limit_mode: value as RequestLimitMode }))
            }
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="inherit">Inherit</SelectItem>
              <SelectItem value="custom">Custom</SelectItem>
              <SelectItem value="unlimited">Unlimited</SelectItem>
              <SelectItem value="blocked">Blocked</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field label="Approval mode">
          <Select
            value={form.approval_mode}
            onValueChange={(value) =>
              setForm((current) => ({
                ...current,
                approval_mode: value as RequestApprovalMode,
              }))
            }
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="inherit">Inherit</SelectItem>
              <SelectItem value="manual">Manual</SelectItem>
              <SelectItem value="auto">Auto</SelectItem>
              <SelectItem value="blocked">Blocked</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {form.limit_mode === "custom" ? (
          <>
            <Field label="Max requests">
              <Input
                type="number"
                min={0}
                value={form.max_requests}
                onChange={(event) =>
                  setForm((current) => ({ ...current, max_requests: event.target.value }))
                }
              />
            </Field>
            <Field label="Window days">
              <Input
                type="number"
                min={1}
                value={form.window_days}
                onChange={(event) =>
                  setForm((current) => ({ ...current, window_days: event.target.value }))
                }
              />
            </Field>
          </>
        ) : null}
      </div>

      <Button onClick={saveLimit} disabled={!userAvailable || updateLimit.isPending}>
        <Save className="h-4 w-4" />
        Save Override
      </Button>
    </>
  );
}

function userLimitFormKey(limit: RequestUserLimit): string {
  return `${limit.user_id}:${limit.updated_at ?? ""}:${limit.limit_mode}:${limit.approval_mode}`;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      {children}
    </div>
  );
}

function SwitchField({
  label,
  checked,
  onCheckedChange,
  description,
  disabled,
}: {
  label: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  description?: string;
  disabled?: boolean;
}) {
  return (
    <div className="border-border flex items-center justify-between gap-3 rounded-lg border p-3">
      <div className="space-y-1">
        <Label>{label}</Label>
        {description ? <p className="text-muted-foreground text-xs">{description}</p> : null}
      </div>
      <Switch checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
    </div>
  );
}

function RowsSkeleton() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 5 }).map((_, index) => (
        <Skeleton key={index} className="h-16 rounded-lg" />
      ))}
    </div>
  );
}

function EmptyPanel({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="border-border bg-card flex flex-col items-center justify-center gap-2 rounded-lg border px-4 py-10 text-center">
      <p className="text-sm font-semibold">{title}</p>
      <p className="text-muted-foreground max-w-sm text-sm">{detail}</p>
    </div>
  );
}
