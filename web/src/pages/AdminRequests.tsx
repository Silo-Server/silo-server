import { useId, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronRight,
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
  AutoscanPathRewrite,
  AutoscanSettings,
  AutoscanSource,
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestApprovalMode,
  RequestIntegration,
  RequestIntegrationOptions,
  RequestLimitMode,
  RequestSettings,
  RequestTarget,
  RequestUserLimit,
} from "@/api/types";
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
import { useAdminUsers } from "@/hooks/queries/admin/users";
import {
  useAutoscanSettings,
  useAutoscanSources,
  useTriggerAutoscan,
  useUpdateAutoscanSettings,
  useUpdateAutoscanSource,
} from "@/hooks/queries/useAutoscan";
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

type StatusFilter = MediaRequestStatus | "all";
type OutcomeFilter = MediaRequestOutcome | "all";

const ADMIN_REQUEST_TABS = ["queue", "settings", "integrations", "autoscan", "overrides"] as const;
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
          <TabsTrigger value="autoscan">Autoscan</TabsTrigger>
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
        <TabsContent value="autoscan">
          <AutoscanTab />
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

type IntegrationFormState = {
  id: string;
  name: string;
  kind: "radarr" | "sonarr";
  enabled: boolean;
  is_4k: boolean;
  is_default: boolean;
  is_default_4k: boolean;
  base_url: string;
  api_key_ref: string;
  root_folder: string;
  quality_profile_id: string;
  tags: string;
  anime_enabled: boolean;
  anime_quality_profile_id: string;
  anime_root_folder: string;
  anime_tags: string;
  search_on_add: boolean;
  minimum_availability: string;
  series_type: string;
  season_folder: boolean;
  has_api_key: boolean;
};

const INTEGRATION_KINDS: Array<"radarr" | "sonarr"> = ["radarr", "sonarr"];

const MINIMUM_AVAILABILITY_OPTIONS = [
  { value: "announced", label: "Announced" },
  { value: "inCinemas", label: "In Cinemas" },
  { value: "released", label: "Released" },
  { value: "preDB", label: "PreDB" },
] as const;

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
  source: RequestIntegration | null;
};

let integrationCardCounter = 0;

function nextCardKey(): string {
  integrationCardCounter += 1;
  return `card-${integrationCardCounter}`;
}

function RequestIntegrationsForm({ integrations }: { integrations: RequestIntegration[] }) {
  const [cards, setCards] = useState<IntegrationCard[]>(() =>
    integrations.map((integration) => ({
      key: integration.id || nextCardKey(),
      form: integrationToForm(integration.kind === "sonarr" ? "sonarr" : "radarr", integration),
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

  function addCard(kind: "radarr" | "sonarr") {
    setCards((current) => [
      ...current,
      { key: nextCardKey(), form: integrationToForm(kind), source: null },
    ]);
  }

  function removeCard(key: string) {
    setCards((current) => current.filter((card) => card.key !== key));
  }

  return (
    <div className="space-y-8">
      {INTEGRATION_KINDS.map((kind) => {
        const kindCards = cards.filter((card) => card.form.kind === kind);
        const title = kind === "radarr" ? "Radarr" : "Sonarr";
        return (
          <div key={kind} className="space-y-4">
            <div className="flex items-center justify-between gap-3">
              <h2 className="text-lg font-semibold tracking-normal">{title} instances</h2>
              <Button type="button" variant="outline" size="sm" onClick={() => addCard(kind)}>
                <Plus className="h-4 w-4" />
                Add {title} instance
              </Button>
            </div>
            {kindCards.length === 0 ? (
              <EmptyPanel
                title={`No ${title} instances`}
                detail={`Add a ${title} instance to route requests.`}
              />
            ) : (
              <div className="grid gap-4 xl:grid-cols-2">
                {kindCards.map((card) => (
                  <IntegrationEditor
                    key={card.key}
                    form={card.form}
                    source={card.source}
                    onChange={(patch) => updateCard(card.key, patch)}
                    onRemove={() => removeCard(card.key)}
                  />
                ))}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function IntegrationEditor({
  form,
  source,
  onChange,
  onRemove,
}: {
  form: IntegrationFormState;
  source: RequestIntegration | null;
  onChange: (patch: Partial<IntegrationFormState>) => void;
  onRemove: () => void;
}) {
  const title = form.kind === "radarr" ? "Radarr" : "Sonarr";
  const isNew = form.id === "";
  const createIntegration = useCreateRequestIntegration();
  const updateIntegration = useUpdateRequestIntegration();
  const deleteIntegration = useDeleteRequestIntegration();
  const [animeOpen, setAnimeOpen] = useState(form.anime_enabled);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const animePanelID = useId();
  const isDirty = !isNew && source !== null && isIntegrationDirty(form, source);
  const loadOptions = useLoadRequestIntegrationOptions();
  const [options, setOptions] = useState<RequestIntegrationOptions | null>(null);
  const rootFolders = rootFolderChoices(options, form.root_folder);
  const qualityProfiles = qualityProfileChoices(options, form.quality_profile_id);
  const animeRootFolders = rootFolderChoices(options, form.anime_root_folder);
  const animeQualityProfiles = qualityProfileChoices(options, form.anime_quality_profile_id);
  const tags = options?.tags ?? [];
  const selectedTags = parseTags(form.tags);
  const selectedAnimeTags = parseTags(form.anime_tags);
  const canLoadOptions =
    form.base_url.trim().length > 0 && Boolean(form.api_key_ref.trim() || form.has_api_key);

  async function handleLoadOptions() {
    try {
      const loaded = await loadOptions.mutateAsync({
        id: form.id || "new",
        body: {
          kind: form.kind,
          base_url: form.base_url,
          api_key_ref: form.api_key_ref.trim() || undefined,
        },
      });
      setOptions(loaded);

      const patch: Partial<IntegrationFormState> = {};
      if (!form.root_folder && loaded.root_folders[0]?.path) {
        patch.root_folder = loaded.root_folders[0].path;
      }
      if (!form.quality_profile_id && loaded.quality_profiles[0]?.id) {
        patch.quality_profile_id = String(loaded.quality_profiles[0].id);
      }
      if (Object.keys(patch).length > 0) {
        onChange(patch);
      }
    } catch {
      // Error toast is handled by useLoadRequestIntegrationOptions.onError.
    }
  }

  const saving = createIntegration.isPending || updateIntegration.isPending;
  // New instances must carry an API key (there's no saved key to fall back on);
  // edits may leave it blank to keep the stored key (has_api_key).
  const hasApiKey = form.api_key_ref.trim().length > 0 || form.has_api_key;
  const canSave = form.name.trim().length > 0 && form.base_url.trim().length > 0 && hasApiKey;

  function handleSave() {
    const payload = formToIntegration(form);
    if (isNew) {
      createIntegration.mutate(payload);
    } else {
      updateIntegration.mutate(payload);
    }
  }

  return (
    <div className="border-border bg-card space-y-5 rounded-lg border p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Plug className="text-primary h-4 w-4" />
          <h2 className="text-lg font-semibold tracking-normal">{title}</h2>
          {form.has_api_key ? <Badge variant="secondary">Key saved</Badge> : null}
          {isNew ? <Badge variant="outline">New</Badge> : null}
          {isDirty ? <Badge variant="secondary">Unsaved changes</Badge> : null}
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={handleLoadOptions}
            disabled={!canLoadOptions || loadOptions.isPending}
          >
            <RefreshCw className="h-4 w-4" />
            {loadOptions.isPending ? "Loading" : "Test connection"}
          </Button>
          <Switch checked={form.enabled} onCheckedChange={(enabled) => onChange({ enabled })} />
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name">
          <Input
            value={form.name}
            onChange={(event) => onChange({ name: event.target.value })}
            placeholder={`${title} instance`}
          />
        </Field>
        <SwitchField
          label="4K instance"
          checked={form.is_4k}
          onCheckedChange={(is_4k) =>
            onChange({
              is_4k,
              is_default: is_4k ? false : form.is_default,
              is_default_4k: is_4k ? form.is_default_4k : false,
            })
          }
        />
        <SwitchField
          label="Default (HD)"
          description={
            form.is_4k
              ? "Disabled because this is a 4K instance."
              : "Default instance for 1080p requests."
          }
          checked={form.is_default}
          disabled={form.is_4k}
          onCheckedChange={(is_default) => onChange({ is_default })}
        />
        <SwitchField
          label="Default 4K"
          description={
            form.is_4k
              ? "Default instance for 2160p requests."
              : "Enable the 4K instance toggle to set this."
          }
          checked={form.is_default_4k}
          disabled={!form.is_4k}
          onCheckedChange={(is_default_4k) => onChange({ is_default_4k })}
        />
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Base URL">
          <Input
            value={form.base_url}
            onChange={(event) => onChange({ base_url: event.target.value })}
            placeholder="http://localhost:7878"
          />
        </Field>
        <Field label="API key or setting key">
          <Input
            value={form.api_key_ref}
            onChange={(event) => onChange({ api_key_ref: event.target.value })}
            placeholder={form.has_api_key ? "Leave blank to keep saved key" : "API key"}
          />
        </Field>
        <Field label="Root folder">
          {rootFolders.length > 0 ? (
            <Select
              value={form.root_folder}
              onValueChange={(root_folder) => onChange({ root_folder })}
            >
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select root folder" />
              </SelectTrigger>
              <SelectContent>
                {rootFolders.map((folder) => (
                  <SelectItem key={folder.path} value={folder.path}>
                    {rootFolderLabel(folder)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <Input
              value={form.root_folder}
              onChange={(event) => onChange({ root_folder: event.target.value })}
              placeholder="/media"
            />
          )}
        </Field>
        <Field label="Quality profile">
          {qualityProfiles.length > 0 ? (
            <Select
              value={form.quality_profile_id}
              onValueChange={(quality_profile_id) => onChange({ quality_profile_id })}
            >
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select quality profile" />
              </SelectTrigger>
              <SelectContent>
                {qualityProfiles.map((profile) => (
                  <SelectItem key={profile.id} value={String(profile.id)}>
                    {profile.name} ({profile.id})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <Input
              type="number"
              min={1}
              value={form.quality_profile_id}
              onChange={(event) => onChange({ quality_profile_id: event.target.value })}
            />
          )}
        </Field>
        <Field label="Tags">
          <Input
            value={form.tags}
            onChange={(event) => onChange({ tags: event.target.value })}
            placeholder="1, 2"
          />
          {tags.length > 0 ? (
            <div className="flex flex-wrap gap-2">
              {tags.map((tag) => {
                const selected = selectedTags.includes(tag.id);
                return (
                  <Button
                    key={tag.id}
                    type="button"
                    size="xs"
                    variant={selected ? "secondary" : "outline"}
                    onClick={() => onChange({ tags: toggleTag(form.tags, tag.id) })}
                  >
                    {tag.label || `Tag ${tag.id}`}
                  </Button>
                );
              })}
            </div>
          ) : null}
        </Field>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <SwitchField
          label="Search on add"
          checked={form.search_on_add}
          onCheckedChange={(search_on_add) => onChange({ search_on_add })}
        />
        {form.kind === "sonarr" ? (
          <>
            <Field label="Series type">
              <Input
                value={form.series_type}
                onChange={(event) => onChange({ series_type: event.target.value })}
              />
            </Field>
            <SwitchField
              label="Season folders"
              checked={form.season_folder}
              onCheckedChange={(season_folder) => onChange({ season_folder })}
            />
          </>
        ) : (
          <Field label="Minimum availability">
            <Select
              value={form.minimum_availability}
              onValueChange={(minimum_availability) => onChange({ minimum_availability })}
            >
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select availability" />
              </SelectTrigger>
              <SelectContent>
                {minimumAvailabilityChoices(form.minimum_availability).map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
      </div>

      {options ? (
        <p className="text-muted-foreground text-xs">
          Loaded {options.root_folders.length} root folder
          {options.root_folders.length === 1 ? "" : "s"}, {options.quality_profiles.length} quality
          profile{options.quality_profiles.length === 1 ? "" : "s"}, and {options.tags.length} tag
          {options.tags.length === 1 ? "" : "s"}.
        </p>
      ) : null}

      <div className="border-border space-y-4 rounded-lg border p-3">
        <button
          type="button"
          className="flex w-full items-center justify-between gap-2 text-left"
          onClick={() => setAnimeOpen((open) => !open)}
          aria-expanded={animeOpen}
          aria-controls={animePanelID}
        >
          <div className="flex items-center gap-2">
            {animeOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
            <span className="text-sm font-medium">Anime overrides</span>
            {form.anime_enabled ? <Badge variant="secondary">On</Badge> : null}
          </div>
        </button>
        {animeOpen ? (
          <div id={animePanelID} className="space-y-4" role="region" aria-label="Anime overrides">
            <SwitchField
              label="Enable anime overrides"
              description="Route anime requests to a dedicated profile, root folder, and tags."
              checked={form.anime_enabled}
              onCheckedChange={(anime_enabled) => onChange({ anime_enabled })}
            />
            {form.anime_enabled ? (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Anime quality profile">
                  {animeQualityProfiles.length > 0 ? (
                    <Select
                      value={form.anime_quality_profile_id}
                      onValueChange={(anime_quality_profile_id) =>
                        onChange({ anime_quality_profile_id })
                      }
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Select quality profile" />
                      </SelectTrigger>
                      <SelectContent>
                        {animeQualityProfiles.map((profile) => (
                          <SelectItem key={profile.id} value={String(profile.id)}>
                            {profile.name} ({profile.id})
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <Input
                      type="number"
                      min={1}
                      value={form.anime_quality_profile_id}
                      onChange={(event) =>
                        onChange({ anime_quality_profile_id: event.target.value })
                      }
                    />
                  )}
                </Field>
                <Field label="Anime root folder">
                  {animeRootFolders.length > 0 ? (
                    <Select
                      value={form.anime_root_folder}
                      onValueChange={(anime_root_folder) => onChange({ anime_root_folder })}
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Select root folder" />
                      </SelectTrigger>
                      <SelectContent>
                        {animeRootFolders.map((folder) => (
                          <SelectItem key={folder.path} value={folder.path}>
                            {rootFolderLabel(folder)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <Input
                      value={form.anime_root_folder}
                      onChange={(event) => onChange({ anime_root_folder: event.target.value })}
                      placeholder="/media/anime"
                    />
                  )}
                </Field>
                <Field label="Anime tags">
                  <Input
                    value={form.anime_tags}
                    onChange={(event) => onChange({ anime_tags: event.target.value })}
                    placeholder="1, 2"
                  />
                  {tags.length > 0 ? (
                    <div className="flex flex-wrap gap-2">
                      {tags.map((tag) => {
                        const selected = selectedAnimeTags.includes(tag.id);
                        return (
                          <Button
                            key={tag.id}
                            type="button"
                            size="xs"
                            variant={selected ? "secondary" : "outline"}
                            onClick={() =>
                              onChange({ anime_tags: toggleTag(form.anime_tags, tag.id) })
                            }
                          >
                            {tag.label || `Tag ${tag.id}`}
                          </Button>
                        );
                      })}
                    </div>
                  ) : null}
                </Field>
              </div>
            ) : null}
          </div>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" onClick={handleSave} disabled={!canSave || saving}>
          <Save className="h-4 w-4" />
          {isNew ? "Create instance" : "Save"}
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
            <DialogTitle>Delete instance</DialogTitle>
            <DialogDescription>
              {`"${form.name.trim() || title}" will be permanently removed. New requests will no longer route to this instance.`}
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

function integrationToForm(
  kind: "radarr" | "sonarr",
  integration?: RequestIntegration,
): IntegrationFormState {
  const options = integration?.options ?? {};
  return {
    id: integration?.id ?? "",
    name: integration?.name ?? "",
    kind,
    enabled: integration?.enabled ?? true,
    is_4k: integration?.is_4k ?? false,
    is_default: integration?.is_default ?? false,
    is_default_4k: integration?.is_default_4k ?? false,
    base_url: integration?.base_url ?? "",
    api_key_ref: "",
    root_folder: integration?.root_folder ?? "",
    quality_profile_id: integration?.quality_profile_id
      ? String(integration.quality_profile_id)
      : "",
    tags: integration?.tags?.join(", ") ?? "",
    anime_enabled: integration?.anime_enabled ?? false,
    anime_quality_profile_id: integration?.anime_quality_profile_id
      ? String(integration.anime_quality_profile_id)
      : "",
    anime_root_folder: integration?.anime_root_folder ?? "",
    anime_tags: integration?.anime_tags?.join(", ") ?? "",
    search_on_add: boolOption(options, "search_on_add", true),
    minimum_availability: stringOption(options, "minimum_availability", "released"),
    series_type: stringOption(options, "series_type", "standard"),
    season_folder: boolOption(options, "season_folder", true),
    has_api_key: integration?.has_api_key ?? false,
  };
}

function formToIntegration(form: IntegrationFormState): RequestIntegration {
  const qualityProfileID = Number(form.quality_profile_id);
  const animeQualityProfileID = Number(form.anime_quality_profile_id);
  const options: Record<string, unknown> = {
    search_on_add: form.search_on_add,
  };
  if (form.kind === "sonarr") {
    options.series_type = form.series_type.trim() || "standard";
    options.season_folder = form.season_folder;
  } else {
    options.minimum_availability = form.minimum_availability.trim() || "released";
  }

  return {
    id: form.id,
    name: form.name.trim(),
    kind: form.kind,
    enabled: form.enabled,
    is_4k: form.is_4k,
    is_default: form.is_default,
    is_default_4k: form.is_default_4k,
    base_url: form.base_url.trim(),
    api_key_ref: form.api_key_ref.trim() || undefined,
    root_folder: form.root_folder.trim(),
    quality_profile_id:
      Number.isFinite(qualityProfileID) && qualityProfileID > 0 ? qualityProfileID : undefined,
    tags: parseTags(form.tags),
    anime_enabled: form.anime_enabled,
    anime_quality_profile_id:
      Number.isFinite(animeQualityProfileID) && animeQualityProfileID > 0
        ? animeQualityProfileID
        : undefined,
    anime_root_folder: form.anime_root_folder.trim() || undefined,
    anime_tags: parseTags(form.anime_tags),
    options,
  };
}

function isIntegrationDirty(form: IntegrationFormState, source: RequestIntegration): boolean {
  // A pending API key entry always counts as an unsaved change.
  if (form.api_key_ref.trim().length > 0) return true;

  const next = formToIntegration(form);
  const seeded = integrationToForm(form.kind, source);
  const original = formToIntegration(seeded);

  return (
    JSON.stringify(stripIntegrationForCompare(next)) !==
    JSON.stringify(stripIntegrationForCompare(original))
  );
}

function stripIntegrationForCompare(integration: RequestIntegration): Record<string, unknown> {
  // api_key_ref is write-only (never round-trips from the source) and is handled
  // separately above, so exclude it from the structural comparison.
  const { api_key_ref: _apiKeyRef, ...rest } = integration;
  return rest;
}

function rootFolderChoices(options: RequestIntegrationOptions | null, currentPath: string) {
  const folders = options?.root_folders ?? [];
  if (!currentPath || folders.some((folder) => folder.path === currentPath)) {
    return folders;
  }
  return [{ path: currentPath, accessible: true }, ...folders];
}

function qualityProfileChoices(options: RequestIntegrationOptions | null, currentID: string) {
  const profiles = options?.quality_profiles ?? [];
  const id = Number(currentID);
  if (!Number.isInteger(id) || id <= 0 || profiles.some((profile) => profile.id === id)) {
    return profiles;
  }
  return [{ id, name: "Current profile" }, ...profiles];
}

function minimumAvailabilityChoices(currentValue: string) {
  if (
    !currentValue ||
    MINIMUM_AVAILABILITY_OPTIONS.some((option) => option.value === currentValue)
  ) {
    return MINIMUM_AVAILABILITY_OPTIONS;
  }
  return [
    { value: currentValue, label: `Current value (${currentValue})` },
    ...MINIMUM_AVAILABILITY_OPTIONS,
  ];
}

function rootFolderLabel(folder: RequestIntegrationOptions["root_folders"][number]): string {
  const freeSpace = folder.free_space ? `, ${formatBytes(folder.free_space)} free` : "";
  const access = folder.accessible ? "" : ", inaccessible";
  return `${folder.path}${freeSpace}${access}`;
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 || unit === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

function toggleTag(current: string, tagID: number): string {
  const tags = parseTags(current);
  const next = tags.includes(tagID) ? tags.filter((tag) => tag !== tagID) : [...tags, tagID];
  return next.join(", ");
}

type AutoscanSettingsFormState = {
  enabled: boolean;
  poll_interval_minutes: string;
  debounce_seconds: string;
  updated_at: string;
};

function AutoscanTab() {
  return (
    <div className="space-y-8">
      <AutoscanSettingsCard />
      <AutoscanSourcesSection />
    </div>
  );
}

function AutoscanSettingsCard() {
  const settings = useAutoscanSettings();

  if (settings.isLoading) return <RowsSkeleton />;
  if (settings.isError) {
    return <EmptyPanel title="Settings failed" detail="Autoscan settings could not be loaded." />;
  }
  if (!settings.data) {
    return <EmptyPanel title="No settings" detail="Autoscan settings are not available." />;
  }

  return (
    <AutoscanSettingsForm key={settings.data.updated_at ?? "settings"} settings={settings.data} />
  );
}

function AutoscanSettingsForm({ settings }: { settings: AutoscanSettings }) {
  const updateSettings = useUpdateAutoscanSettings();
  const triggerAutoscan = useTriggerAutoscan();
  const [form, setForm] = useState<AutoscanSettingsFormState>(() => ({
    enabled: settings.enabled,
    poll_interval_minutes: String(settings.poll_interval_minutes),
    debounce_seconds: String(settings.debounce_seconds),
    updated_at: settings.updated_at ?? "",
  }));

  function saveSettings() {
    updateSettings.mutate({
      enabled: form.enabled,
      poll_interval_minutes: Math.max(1, Number(form.poll_interval_minutes) || 10),
      debounce_seconds: Math.max(0, Number(form.debounce_seconds) || 0),
      updated_at: form.updated_at || undefined,
    });
  }

  return (
    <div className="border-border bg-card max-w-3xl space-y-5 rounded-lg border p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <RefreshCw className="text-primary h-4 w-4" />
          <h2 className="text-lg font-semibold tracking-normal">Autoscan</h2>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => triggerAutoscan.mutate()}
          disabled={triggerAutoscan.isPending}
        >
          <RefreshCw className="h-4 w-4" />
          Poll now
        </Button>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="sm:col-span-2">
          <SwitchField
            label="Autoscan enabled"
            description="Poll Radarr and Sonarr instances and rescan affected library paths automatically."
            checked={form.enabled}
            onCheckedChange={(enabled) => setForm((current) => ({ ...current, enabled }))}
          />
        </div>
        <Field label="Poll interval (minutes)">
          <Input
            type="number"
            min={1}
            value={form.poll_interval_minutes}
            onChange={(event) =>
              setForm((current) => ({ ...current, poll_interval_minutes: event.target.value }))
            }
          />
        </Field>
        <Field label="Debounce (seconds)">
          <Input
            type="number"
            min={0}
            value={form.debounce_seconds}
            onChange={(event) =>
              setForm((current) => ({ ...current, debounce_seconds: event.target.value }))
            }
          />
        </Field>
      </div>

      <Button onClick={saveSettings} disabled={updateSettings.isPending}>
        <Save className="h-4 w-4" />
        Save Settings
      </Button>
    </div>
  );
}

function AutoscanSourcesSection() {
  const sources = useAutoscanSources();

  if (sources.isLoading) return <RowsSkeleton />;
  if (sources.isError) {
    return <EmptyPanel title="Sources failed" detail="Autoscan sources could not be loaded." />;
  }

  const list = sources.data ?? [];
  if (list.length === 0) {
    return (
      <EmptyPanel
        title="No instances"
        detail="Add a Radarr or Sonarr instance in the Integrations tab first."
      />
    );
  }

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold tracking-normal">Instances</h2>
      <div className="grid gap-4 xl:grid-cols-2">
        {list.map((source) => (
          <AutoscanSourceEditor key={source.integration_id} source={source} />
        ))}
      </div>
    </div>
  );
}

function AutoscanSourceEditor({ source }: { source: AutoscanSource }) {
  const updateSource = useUpdateAutoscanSource();
  const [enabled, setEnabled] = useState(source.enabled);
  const [rewrites, setRewrites] = useState<AutoscanPathRewrite[]>(() =>
    source.path_rewrites.map((rewrite) => ({ ...rewrite })),
  );
  const [rewritesOpen, setRewritesOpen] = useState(source.path_rewrites.length > 0);
  const rewritesPanelID = useId();

  function updateRewrite(index: number, patch: Partial<AutoscanPathRewrite>) {
    setRewrites((current) =>
      current.map((rewrite, i) => (i === index ? { ...rewrite, ...patch } : rewrite)),
    );
  }

  function addRewrite() {
    setRewrites((current) => [...current, { from: "", to: "" }]);
  }

  function removeRewrite(index: number) {
    setRewrites((current) => current.filter((_, i) => i !== index));
  }

  function handleSave() {
    const path_rewrites = rewrites
      .map((rewrite) => ({ from: rewrite.from.trim(), to: rewrite.to.trim() }))
      .filter((rewrite) => rewrite.from.length > 0 || rewrite.to.length > 0);
    updateSource.mutate({ id: source.integration_id, body: { enabled, path_rewrites } });
  }

  return (
    <div className="border-border bg-card space-y-5 rounded-lg border p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Plug className="text-primary h-4 w-4" />
          <h2 className="text-lg font-semibold tracking-normal">{source.name}</h2>
          <Badge variant="secondary">{source.kind}</Badge>
        </div>
      </div>

      <SwitchField label="Autoscan" checked={enabled} onCheckedChange={setEnabled} />

      <Field label="Last polled">
        <p className="text-muted-foreground text-sm">
          {formatAutoscanPollDate(source.last_poll_at)}
        </p>
      </Field>

      <div className="border-border space-y-4 rounded-lg border p-3">
        <button
          type="button"
          className="flex w-full items-center justify-between gap-2 text-left"
          onClick={() => setRewritesOpen((open) => !open)}
          aria-expanded={rewritesOpen}
          aria-controls={rewritesPanelID}
        >
          <div className="flex items-center gap-2">
            {rewritesOpen ? (
              <ChevronDown className="h-4 w-4" />
            ) : (
              <ChevronRight className="h-4 w-4" />
            )}
            <span className="text-sm font-medium">Path rewrites</span>
            {rewrites.length > 0 ? <Badge variant="secondary">{rewrites.length}</Badge> : null}
          </div>
        </button>
        {rewritesOpen ? (
          <div id={rewritesPanelID} className="space-y-3" role="region" aria-label="Path rewrites">
            {rewrites.length === 0 ? (
              <p className="text-muted-foreground text-xs">
                No path rewrites. Map remote paths to local library paths.
              </p>
            ) : (
              rewrites.map((rewrite, index) => (
                <div key={index} className="flex items-end gap-2">
                  <Field label="From">
                    <Input
                      value={rewrite.from}
                      onChange={(event) => updateRewrite(index, { from: event.target.value })}
                      placeholder="/remote/media"
                    />
                  </Field>
                  <Field label="To">
                    <Input
                      value={rewrite.to}
                      onChange={(event) => updateRewrite(index, { to: event.target.value })}
                      placeholder="/media"
                    />
                  </Field>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    onClick={() => removeRewrite(index)}
                    aria-label="Remove rewrite"
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ))
            )}
            <Button type="button" variant="outline" size="sm" onClick={addRewrite}>
              <Plus className="h-4 w-4" />
              Add rewrite
            </Button>
          </div>
        ) : null}
      </div>

      <Button type="button" onClick={handleSave} disabled={updateSource.isPending}>
        <Save className="h-4 w-4" />
        Save
      </Button>
    </div>
  );
}

function formatAutoscanPollDate(value: string | null | undefined): string {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
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

function boolOption(options: Record<string, unknown>, key: string, fallback: boolean): boolean {
  return typeof options[key] === "boolean" ? Boolean(options[key]) : fallback;
}

function stringOption(options: Record<string, unknown>, key: string, fallback: string): string {
  return typeof options[key] === "string" ? String(options[key]) : fallback;
}

function parseTags(value: string): number[] {
  return value
    .split(",")
    .map((part) => Number(part.trim()))
    .filter((tag) => Number.isInteger(tag) && tag > 0);
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
