import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { Check, Plug, RefreshCw, Save, Settings2, SlidersHorizontal, X } from "lucide-react";
import type {
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestApprovalMode,
  RequestIntegration,
  RequestIntegrationOptions,
  RequestLimitMode,
  RequestSettings,
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
  useAdminMediaRequests,
  useApproveMediaRequest,
  useDeclineMediaRequest,
  useLoadRequestIntegrationOptions,
  useRequestIntegrations,
  useRequestSettings,
  useRequestUserLimit,
  useRetryMediaRequest,
  useUpdateRequestIntegrations,
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
        {request.integration_kind || "Not submitted"}
        {request.external_status ? <span className="block">{request.external_status}</span> : null}
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

type SettingsFormState = {
  requests_enabled: boolean;
  global_max_requests: string;
  global_window_days: string;
  global_auto_approval_enabled: boolean;
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
    updated_at: settings.updated_at,
  }));

  function saveSettings() {
    const payload: RequestSettings = {
      requests_enabled: form.requests_enabled,
      global_max_requests: Math.max(0, Number(form.global_max_requests) || 0),
      global_window_days: Math.max(1, Number(form.global_window_days) || 1),
      global_auto_approval_enabled: form.global_auto_approval_enabled,
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
      </div>

      <Button onClick={saveSettings} disabled={updateSettings.isPending}>
        <Save className="h-4 w-4" />
        Save Settings
      </Button>
    </div>
  );
}

type IntegrationFormState = {
  kind: "radarr" | "sonarr";
  enabled: boolean;
  base_url: string;
  api_key_ref: string;
  root_folder: string;
  quality_profile_id: string;
  tags: string;
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

  return (
    <RequestIntegrationsForm
      key={integrationsFormKey(integrations.data ?? [])}
      integrations={integrations.data ?? []}
    />
  );
}

function RequestIntegrationsForm({ integrations }: { integrations: RequestIntegration[] }) {
  const updateIntegrations = useUpdateRequestIntegrations();
  const [forms, setForms] = useState<IntegrationFormState[]>(() =>
    INTEGRATION_KINDS.map((kind) =>
      integrationToForm(
        kind,
        integrations.find((integration) => integration.kind === kind),
      ),
    ),
  );

  function updateForm(kind: "radarr" | "sonarr", patch: Partial<IntegrationFormState>) {
    setForms((current) =>
      current.map((form) => (form.kind === kind ? { ...form, ...patch } : form)),
    );
  }

  function saveIntegrations() {
    updateIntegrations.mutate(forms.map(formToIntegration));
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-4 xl:grid-cols-2">
        {forms.map((form) => (
          <IntegrationEditor
            key={form.kind}
            form={form}
            onChange={(patch) => updateForm(form.kind, patch)}
          />
        ))}
      </div>
      <Button onClick={saveIntegrations} disabled={updateIntegrations.isPending}>
        <Save className="h-4 w-4" />
        Save Integrations
      </Button>
    </div>
  );
}

function integrationsFormKey(integrations: RequestIntegration[]): string {
  if (integrations.length === 0) return "empty";
  return integrations
    .map(
      (integration) =>
        `${integration.kind}:${integration.updated_at ?? ""}:${integration.enabled}:${integration.has_api_key}`,
    )
    .join("|");
}

function IntegrationEditor({
  form,
  onChange,
}: {
  form: IntegrationFormState;
  onChange: (patch: Partial<IntegrationFormState>) => void;
}) {
  const title = form.kind === "radarr" ? "Radarr" : "Sonarr";
  const loadOptions = useLoadRequestIntegrationOptions();
  const [options, setOptions] = useState<RequestIntegrationOptions | null>(null);
  const rootFolders = rootFolderChoices(options, form.root_folder);
  const qualityProfiles = qualityProfileChoices(options, form.quality_profile_id);
  const tags = options?.tags ?? [];
  const selectedTags = parseTags(form.tags);
  const canLoadOptions =
    form.base_url.trim().length > 0 && Boolean(form.api_key_ref.trim() || form.has_api_key);

  async function handleLoadOptions() {
    try {
      const loaded = await loadOptions.mutateAsync({
        kind: form.kind,
        body: {
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

  return (
    <div className="border-border bg-card space-y-5 rounded-lg border p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Plug className="text-primary h-4 w-4" />
          <h2 className="text-lg font-semibold tracking-normal">{title}</h2>
          {form.has_api_key ? <Badge variant="secondary">Key saved</Badge> : null}
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
            {loadOptions.isPending ? "Loading" : "Load Settings"}
          </Button>
          <Switch checked={form.enabled} onCheckedChange={(enabled) => onChange({ enabled })} />
        </div>
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
    </div>
  );
}

function integrationToForm(
  kind: "radarr" | "sonarr",
  integration?: RequestIntegration,
): IntegrationFormState {
  const options = integration?.options ?? {};
  return {
    kind,
    enabled: integration?.enabled ?? false,
    base_url: integration?.base_url ?? "",
    api_key_ref: "",
    root_folder: integration?.root_folder ?? "",
    quality_profile_id: integration?.quality_profile_id
      ? String(integration.quality_profile_id)
      : "",
    tags: integration?.tags?.join(", ") ?? "",
    search_on_add: boolOption(options, "search_on_add", true),
    minimum_availability: stringOption(options, "minimum_availability", "released"),
    series_type: stringOption(options, "series_type", "standard"),
    season_folder: boolOption(options, "season_folder", true),
    has_api_key: integration?.has_api_key ?? false,
  };
}

function formToIntegration(form: IntegrationFormState): RequestIntegration {
  const qualityProfileID = Number(form.quality_profile_id);
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
    kind: form.kind,
    enabled: form.enabled,
    base_url: form.base_url.trim(),
    api_key_ref: form.api_key_ref.trim() || undefined,
    root_folder: form.root_folder.trim(),
    quality_profile_id:
      Number.isFinite(qualityProfileID) && qualityProfileID > 0 ? qualityProfileID : undefined,
    tags: parseTags(form.tags),
    options,
  };
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
}: {
  label: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="border-border flex items-center justify-between gap-3 rounded-lg border p-3">
      <Label>{label}</Label>
      <Switch checked={checked} onCheckedChange={onCheckedChange} />
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
