import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Link } from "react-router";

import type {
  RequestIntegration,
  RequestIntegrationOptions,
  LoadRequestIntegrationOptionsRequest,
} from "@/api/types";
import {
  getAdminRequestIntegrationV2,
  isRequestEditorConflict,
  requestValidationErrors,
  type RequestRoute,
  type RequestRouting,
} from "@/api/v2/adminRequests";
import { V2ProblemError } from "@/api/v2/request";
import { EditorConflict } from "@/components/admin/EditorConflict";
import { SchemaForm } from "@/components/admin/plugins/SchemaForm";
import { buildSchemaValues, parseFieldTypes } from "@/components/admin/plugins/schemaFormUtils";
import { ProviderTile, ProviderTileGrid } from "@/components/settings/ProviderTile";
import { providerMonogram } from "@/lib/monogram";
import { SecretField } from "@/components/settings/SecretField";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { useDebounce } from "@/hooks/useDebounce";
import {
  useCreateRequestIntegration,
  useDeleteRequestIntegration,
  useLoadRequestIntegrationOptions,
  useRequestRouting,
  useUpdateRequestIntegration,
} from "@/hooks/queries/admin/requests";
import { cn } from "@/lib/utils";

import { FieldGroup } from "./FieldGroup";
import { FieldError } from "./RequestRouteFields";
import { supportedMediaTypesForConfig } from "./requestIntegrationMediaTypes";
import {
  installationOptionLabel,
  installationOptionValue,
  mediaTypePlural,
  ROUTING_OWNED_CONFIG_KEYS,
  serverConfigSchema,
  serverInstallation,
  serverKind,
  serverDeleteBlockers,
  serverReady,
  serverRouteUsage,
  serverTypeLabel,
  serviceKindLabel,
  standardServerUsage,
  SERVICE_KIND_KEY,
  type RequestRouterInstallation,
} from "./requestServerModel";
import { SETTINGS_CONTROL_WIDTH, SettingFieldRow } from "./SettingField";

const KIND_TILE_CLASSES: Record<string, string> = {
  radarr: "bg-amber-500/20 text-amber-700 dark:text-amber-300",
  sonarr: "bg-sky-500/20 text-sky-700 dark:text-sky-300",
};

/** The plugin config key that marks a server as the 4K one. */
const FOUR_K_KEY = "is_4k";

/** The plugin's per-kind default switches, which routing replaced. */
const RETIRED_DEFAULT_KEYS = ["is_default", "is_default_4k"] as const;

function plural(count: number, word: string) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

/**
 * The service the plugin found at the address, e.g. Sonarr 4.0.14. Plugins
 * that detect it answer one `service_kind` option; older ones answer none.
 */
function detectedService(options: RequestIntegrationOptions) {
  const found = options[SERVICE_KIND_KEY];
  return found?.length === 1 ? found[0] : undefined;
}

/**
 * The Test result in words. Sonarr and Radarr answer with their quality
 * profiles and root folders; another plugin's options are just counted.
 */
function connectedMessage(options: RequestIntegrationOptions): string {
  const profiles = options.quality_profile_id?.length;
  const folders = options.root_folder?.length;
  const parts = [
    profiles !== undefined ? plural(profiles, "quality profile") : null,
    folders !== undefined ? plural(folders, "root folder") : null,
  ].filter(Boolean);
  const detected = detectedService(options);
  const lead = detected ? `Detected ${detected.label}` : "Connected";
  return parts.length > 0 ? `${lead} — ${parts.join(", ")}` : lead;
}

/**
 * Why a probe failed, in the editor's terms: messages for the URL and API
 * key fields, and otherwise the server's sentence for the whole connection.
 */
interface ProbeProblem {
  fields: Record<string, string>;
  message: string | null;
}

function probeProblem(error: unknown): ProbeProblem {
  const validation = requestValidationErrors(error);
  if (validation) {
    const hasFields = Object.keys(validation.fields).length > 0;
    return { fields: validation.fields, message: hasFields ? null : validation.message };
  }
  return { fields: {}, message: error instanceof V2ProblemError ? error.message : null };
}

/** The address as it will be probed and saved: http:// when none is given. */
function withScheme(url: string): string {
  const trimmed = url.trim();
  return trimmed && !trimmed.includes("://") ? `http://${trimmed}` : url;
}

function ServerTile({
  server,
  installations,
  routes,
  routing,
  onEdit,
}: {
  server: RequestIntegration;
  installations: RequestRouterInstallation[];
  routes: RequestRoute[];
  routing: RequestRouting | undefined;
  onEdit: () => void;
}) {
  const type = serverTypeLabel(server, installations);
  const ready = serverReady(server);
  const usage =
    routing?.mode === "standard"
      ? standardServerUsage(server.id, routing)
      : serverRouteUsage(server.id, routes);
  const failing = ready && Boolean(server.last_check_error);
  return (
    <ProviderTile
      name={server.name}
      tagline={type}
      monogram={providerMonogram(type)}
      monogramClass={
        KIND_TILE_CLASSES[serverKind(server)] ??
        "bg-violet-500/20 text-violet-700 dark:text-violet-300"
      }
      state={failing ? "error" : ready ? "connected" : "not_connected"}
      statePill={
        !server.enabled ? "Disabled" : !ready ? "Needs setup" : failing ? "Unreachable" : undefined
      }
      meta={failing ? server.last_check_error : usage || undefined}
      primaryAction={{ label: "Edit", onClick: onEdit }}
    />
  );
}

/**
 * The request servers: one tile each, and one editor for adding or changing
 * a server. Which requests go to which server is decided by routing below.
 */
export function RequestServersGroup({
  servers,
  serversLoading,
  serversError,
  installations,
  installationsLoading,
  routes,
  routing: routingAvailable,
}: {
  servers: RequestIntegration[];
  serversLoading: boolean;
  serversError: boolean;
  installations: RequestRouterInstallation[];
  installationsLoading: boolean;
  routes: RequestRoute[];
  /** Whether the server offers routing; without it the routing mode is not read. */
  routing: boolean;
}) {
  // null: closed; "new": adding; otherwise the id of the server being edited.
  const [editing, setEditing] = useState<string | null>(null);
  const [newKey, setNewKey] = useState(0);
  const routing = useRequestRouting(routingAvailable);
  const noRouterPlugin = !installationsLoading && installations.length === 0;
  const editingServer =
    editing && editing !== "new" ? servers.find((server) => server.id === editing) : undefined;

  return (
    <FieldGroup
      label="Servers"
      description="Where approved requests are sent."
      actions={
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => {
            setNewKey((key) => key + 1);
            setEditing("new");
          }}
          disabled={noRouterPlugin || installationsLoading}
        >
          <Plus aria-hidden="true" />
          Add server
        </Button>
      }
    >
      <div className="py-3.5">
        {serversLoading ? (
          <ProviderTileGrid>
            <Skeleton className="h-28 rounded-2xl" />
            <Skeleton className="h-28 rounded-2xl" />
          </ProviderTileGrid>
        ) : serversError ? (
          <p className="text-destructive text-sm">Request servers could not be loaded.</p>
        ) : noRouterPlugin ? (
          <p className="text-muted-foreground text-sm">
            Requests go to Sonarr, Radarr, or another request server through a plugin. Install one
            from{" "}
            <Link to="/admin/plugins" className="text-foreground underline underline-offset-2">
              Plugins
            </Link>{" "}
            first.
          </p>
        ) : servers.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            No servers yet. Without one, approved requests wait until the title shows up in a
            library.
          </p>
        ) : (
          <ProviderTileGrid>
            {servers.map((server) => (
              <ServerTile
                key={server.id}
                server={server}
                installations={installations}
                routes={routes}
                routing={routing.data}
                onEdit={() => setEditing(server.id)}
              />
            ))}
          </ProviderTileGrid>
        )}
      </div>

      <Dialog
        open={editing !== null && (editing === "new" || editingServer !== undefined)}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
      >
        <DialogContent className="sm:max-w-2xl">
          {editing === "new" ? (
            <RequestServerEditor
              key={`new-${newKey}`}
              source={null}
              installations={installations}
              routes={routes}
              servers={servers}
              standard={routing.data?.mode === "standard"}
              onDone={() => setEditing(null)}
            />
          ) : editingServer ? (
            <RequestServerEditor
              // Keyed by id alone: a refresh that brings a newer revision
              // must not throw away edits; the save's 412 reports it instead.
              key={editingServer.id}
              source={editingServer}
              installations={installations}
              routes={routes}
              servers={servers}
              standard={routing.data?.mode === "standard"}
              onDone={() => setEditing(null)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
    </FieldGroup>
  );
}

// Host-owned connection chrome; everything *arr-specific is the plugin's
// config, rendered from its form descriptor by SchemaForm.
interface ServerFormState {
  id: string;
  name: string;
  enabled: boolean;
  base_url: string;
  /** A newly typed key; empty keeps the saved one. */
  api_key_ref: string;
  has_api_key: boolean;
  installation_id: string;
  capability_id: string;
}

function serverForm(
  server: RequestIntegration | null,
  sole: RequestRouterInstallation | undefined,
): ServerFormState {
  return {
    id: server?.id ?? "",
    name: server?.name ?? "",
    enabled: server?.enabled ?? true,
    base_url: server?.base_url ?? "",
    api_key_ref: "",
    has_api_key: server?.has_api_key ?? false,
    installation_id: server?.installation_id
      ? String(server.installation_id)
      : sole
        ? String(sole.installationID)
        : "",
    capability_id: server?.capability_id ?? sole?.capability.id ?? "",
  };
}

type OptionsStatus = "idle" | "loading" | "error";

/**
 * Keeps the plugin form's dynamic choices (root folders, quality profiles,
 * tags) loaded for the connection being edited. The probe is keyed on the
 * connection itself (URL, key, plugin), not on the rest of the config, so
 * choosing a quality profile does not call the server again; the latest probe
 * wins. `probe` runs one now, for the Test button.
 */
function useServerOptions(
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
  const [status, setStatus] = useState<OptionsStatus>("idle");
  const [problem, setProblem] = useState<ProbeProblem | null>(null);
  // Bumped whenever the connection changes. A probe that finishes under a
  // later value answered for an address or key no longer in the form. Probes
  // of the same connection (the debounced one and a Test) are interchangeable.
  const connRef = useRef(0);

  const canLoad =
    draft.base_url.trim().length > 0 &&
    Boolean(draft.installation_id) &&
    draft.capability_id.trim().length > 0 &&
    Boolean(draft.api_key_ref.trim() || draft.has_api_key);

  const sig = JSON.stringify({
    u: draft.base_url,
    k: draft.api_key_ref,
    i: draft.installation_id,
    c: draft.capability_id,
  });
  const debouncedSig = useDebounce(sig, 400);
  const draftRef = useRef(draft);
  draftRef.current = draft;

  function body(): LoadRequestIntegrationOptionsRequest {
    const current = draftRef.current;
    return {
      base_url: current.base_url,
      api_key_ref: current.api_key_ref.trim() || undefined,
      capability_id: current.capability_id,
      installation_id: current.installation_id,
      plugin_config: current.plugin_config,
    };
  }

  /**
   * Probes the connection now. Resolves to null, whatever the answer, when
   * the connection changed while the probe ran: that answer is for an
   * address or key no longer in the form.
   */
  async function probe(): Promise<RequestIntegrationOptions | null> {
    const conn = connRef.current;
    setStatus("loading");
    try {
      const loaded = await load.mutateAsync({ id: connectionID || "new", body: body() });
      if (conn !== connRef.current) return null;
      setOptions(loaded);
      setStatus("idle");
      setProblem(null);
      return loaded;
    } catch (error) {
      if (conn !== connRef.current) return null;
      setOptions({});
      setStatus("error");
      setProblem(probeProblem(error));
      throw error;
    }
  }

  // A changed connection makes the last probe stale: an answer still in
  // flight is dropped, and its complaint is cleared while the next probe
  // waits out the debounce.
  useEffect(() => {
    connRef.current += 1;
    setProblem(null);
    setStatus("idle");
  }, [sig]);

  useEffect(() => {
    if (!canLoad) {
      connRef.current += 1;
      setOptions({});
      setStatus("idle");
      setProblem(null);
      return;
    }
    probe().catch(() => {
      // Shown inline through `status`; the Test button reports the reason.
    });
    // Only the connection identity decides when to probe again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedSig, canLoad]);

  return { options, status, problem, canLoad, probe };
}

interface TestResult {
  ok: boolean;
  message: string;
}

/**
 * Adds or edits one request server: which plugin serves it, how to reach it,
 * and the plugin's own settings. The keys routing owns (default and 4K
 * switches, the anime overlay) stay hidden; routing decides those now.
 */
export function RequestServerEditor({
  source,
  installations,
  routes,
  servers,
  standard,
  onDone,
}: {
  source: RequestIntegration | null;
  installations: RequestRouterInstallation[];
  routes: RequestRoute[];
  /** Every server, to tell whether this is the last of its kind. */
  servers: readonly RequestIntegration[];
  /** Whether Standard routing is on, which hides Everything else. */
  standard: boolean;
  onDone: () => void;
}) {
  const sole = installations.length === 1 ? installations[0] : undefined;
  const [form, setForm] = useState<ServerFormState>(() => serverForm(source, sole));
  const [pluginConfig, setPluginConfig] = useState<Record<string, unknown>>(() => ({
    ...(source?.plugin_config ?? {}),
  }));
  const [etag, setETag] = useState(source?.etag);
  const [conflict, setConflict] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [schemaValid, setSchemaValid] = useState(true);
  const [test, setTest] = useState<TestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const routedBy = source ? serverRouteUsage(source.id, routes) : "";
  // The server refuses to delete a server routing still sends to, except the
  // last of its kind when only Everything else uses it; that goes with it.
  // Under Standard, Everything else never keeps a server.
  const deleteBlockedBy = source ? serverDeleteBlockers(source.id, routes, servers, standard) : "";
  const clearsFallback =
    source && routedBy && !deleteBlockedBy && !standard
      ? routes.find(
          (route) =>
            route.is_fallback &&
            (route.hd.integration_id === source.id || route.uhd.integration_id === source.id),
        )?.media_type
      : undefined;
  // Nor does it let a routed server change type (Sonarr ↔ Radarr): the routes
  // pointing at it are for one media type.
  const typeLock = routedBy
    ? `Routing sends requests here (${routedBy}). Change that first to switch the type.`
    : undefined;
  const deleteBlockedId = useId();
  const createServer = useCreateRequestIntegration();
  const updateServer = useUpdateRequestIntegration();
  const deleteServer = useDeleteRequestIntegration();
  const isNew = form.id === "";
  const nameId = useId();
  const urlId = useId();
  const typeId = useId();

  const installationID = Number(form.installation_id);
  const hasInstallation = Number.isInteger(installationID) && installationID > 0;
  const selected = serverInstallation(
    installations,
    hasInstallation ? installationID : undefined,
    form.capability_id,
  );
  const { descriptor, jsonSchema } = serverConfigSchema(selected);
  const fieldTypes = useMemo(() => parseFieldTypes(jsonSchema), [jsonSchema]);
  // The Sonarr/Radarr plugin's "4K instance" switch. Routing owns the
  // plugin's other default switches, so the editor shows this one itself.
  const offers4K = Boolean(descriptor?.fields?.some((field) => field.key === FOUR_K_KEY));
  const is4K = [FOUR_K_KEY, "is_default_4k"].some(
    (key) => pluginConfig[key] === true || pluginConfig[key] === "true",
  );

  const {
    options,
    status: optionsStatus,
    problem: probeError,
    canLoad,
    probe,
  } = useServerOptions(form.id, {
    base_url: form.base_url,
    api_key_ref: form.api_key_ref,
    has_api_key: form.has_api_key,
    installation_id: hasInstallation ? installationID : undefined,
    capability_id: selected?.capability.id ?? "",
    plugin_config: pluginConfig,
  });

  // Once the server's choices arrive, fill each empty single choice with the
  // first one (the root folder and quality profile a new server needs). A
  // choice the admin already made is never replaced.
  // Both this and detection below update from the latest config, so the
  // first probe's defaults and detected type land together.
  useEffect(() => {
    if (!descriptor) return;
    setPluginConfig((current) => {
      const patch: Record<string, unknown> = {};
      for (const field of descriptor.fields) {
        if (field.control !== "SELECT" || !field.dynamic_options) continue;
        if (ROUTING_OWNED_CONFIG_KEYS.includes(field.key)) continue;
        const first = options[field.key]?.[0];
        const value = current[field.key];
        if (first && (value === undefined || value === null || value === "")) {
          patch[field.key] = first.value;
        }
      }
      return Object.keys(patch).length > 0 ? { ...current, ...patch } : current;
    });
  }, [options, descriptor]);

  // A plugin that tells Sonarr from Radarr sets the type from what answered,
  // and names an unnamed server after it. A server routing sends requests to
  // keeps its type; a different answer is only shown as a warning.
  const detected = detectedService(options);
  const detectedKind = detected?.value ?? "";
  const kindField = descriptor?.fields.find((field) => field.key === SERVICE_KIND_KEY);
  const detectsKind = Boolean(
    detectedKind && kindField?.options?.some((option) => option.value === detectedKind),
  );
  const currentKind = serverKind({ plugin_config: pluginConfig });
  const kindMismatch =
    detectsKind && typeLock !== undefined && currentKind !== "" && currentKind !== detectedKind
      ? `This address answers as ${detected?.label}, not ${serviceKindLabel(currentKind) || currentKind}. ${typeLock}`
      : undefined;
  // The name detection last filled in: a later detection may replace it, but
  // never a name the admin typed.
  const autoNameRef = useRef("");
  useEffect(() => {
    if (!detectsKind || typeLock !== undefined) return;
    setPluginConfig((current) =>
      serverKind({ plugin_config: current }) === detectedKind
        ? current
        : { ...current, [SERVICE_KIND_KEY]: detectedKind },
    );
    const label = serviceKindLabel(detectedKind);
    if (!label) return;
    const name = is4K ? `${label} 4K` : label;
    setForm((current) => {
      if (current.name.trim() && current.name !== autoNameRef.current) return current;
      autoNameRef.current = name;
      return { ...current, name };
    });
  }, [detectsKind, detectedKind, typeLock, is4K]);
  const schemaErrors = useMemo(
    () =>
      kindMismatch && !fieldErrors[SERVICE_KIND_KEY]
        ? { ...fieldErrors, [SERVICE_KIND_KEY]: kindMismatch }
        : fieldErrors,
    [fieldErrors, kindMismatch],
  );

  // Any edit makes a failed save's answer stale.
  function clearSaveErrors() {
    setFieldErrors((current) => (Object.keys(current).length === 0 ? current : {}));
    setFormError(null);
  }

  function patch(next: Partial<ServerFormState>) {
    clearSaveErrors();
    setTest(null);
    setForm((current) => ({ ...current, ...next }));
  }

  function patchConfig(next: Record<string, unknown>) {
    clearSaveErrors();
    setPluginConfig(next);
  }

  // A different plugin gets a clean config: the old one's keys mean nothing
  // to it.
  function changeType(value: string) {
    const entry = installations.find((candidate) => installationOptionValue(candidate) === value);
    if (!entry || (selected && installationOptionValue(selected) === value)) return;
    patch({ installation_id: String(entry.installationID), capability_id: entry.capability.id });
    setPluginConfig({});
  }

  async function reload() {
    try {
      const latest = await getAdminRequestIntegrationV2(form.id);
      setForm(serverForm(latest, sole));
      setPluginConfig({ ...(latest.plugin_config ?? {}) });
      setETag(latest.etag);
      setConflict(false);
      clearSaveErrors();
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "Reload failed");
    }
  }

  async function runTest() {
    setTesting(true);
    try {
      const loaded = await probe();
      // A stale answer says nothing about the connection now in the form.
      if (loaded) setTest({ ok: true, message: connectedMessage(loaded) });
    } catch (error) {
      const problem = probeProblem(error);
      setTest({
        ok: false,
        message:
          problem.message ??
          (Object.keys(problem.fields).length > 0
            ? "Check the fields marked above."
            : error instanceof Error
              ? error.message
              : "The server could not be reached."),
      });
    } finally {
      setTesting(false);
    }
  }

  const hasKey = form.api_key_ref.trim().length > 0 || form.has_api_key;
  const saving = createServer.isPending || updateServer.isPending;
  const canSave =
    !conflict &&
    (isNew || Boolean(etag)) &&
    form.name.trim().length > 0 &&
    form.base_url.trim().length > 0 &&
    hasKey &&
    hasInstallation &&
    (!descriptor || schemaValid);

  function save() {
    const nextConfig = descriptor
      ? buildSchemaValues(descriptor, pluginConfig, fieldTypes)
      : { ...pluginConfig };
    // The plugin still allows one default per kind and checks it on save. A
    // server that changes kind would carry its hidden default switches into
    // the other kind, where they can clash with a server the admin cannot fix
    // from here. Routing decides defaults now, so they are dropped.
    if (source && serverKind(source) !== serverKind({ plugin_config: nextConfig })) {
      for (const key of RETIRED_DEFAULT_KEYS) {
        if (nextConfig[key] === true || nextConfig[key] === "true") nextConfig[key] = false;
      }
    }
    const payload: RequestIntegration = {
      id: form.id,
      etag,
      name: form.name.trim(),
      enabled: form.enabled,
      base_url: form.base_url.trim(),
      api_key_ref: form.api_key_ref.trim() || undefined,
      capability_id: selected?.capability.id ?? "",
      installation_id: hasInstallation ? installationID : undefined,
      supported_media_types: supportedMediaTypesForConfig(nextConfig, source),
      plugin_config: nextConfig,
    };
    setFieldErrors({});
    setFormError(null);
    (isNew ? createServer : updateServer).mutate(payload, {
      onSuccess: onDone,
      onError: (error) => {
        if (isRequestEditorConflict(error)) setConflict(true);
        const validation = requestValidationErrors(error);
        if (validation) {
          setFieldErrors(validation.fields);
          setFormError(validation.message);
        }
      },
    });
  }

  const title = isNew ? "Add server" : `Edit ${source?.name || "server"}`;
  const typeLabel = selected ? installationOptionLabel(selected) : "";
  // Errors for a field the editor does not show (a hidden routing key the
  // plugin still validated, or the server type when there is only one) are
  // listed at the top instead of going unseen.
  const shownKeys = new Set([
    "name",
    "base_url",
    "api_key_ref",
    ...(installations.length > 1 ? ["installation_id", "capability_id"] : []),
    ...(descriptor?.fields ?? [])
      .map((field) => field.key)
      .filter((key) => !ROUTING_OWNED_CONFIG_KEYS.includes(key) || key === FOUR_K_KEY),
  ]);
  const unshownErrors = Object.entries(fieldErrors).filter(([key]) => !shownKeys.has(key));
  const urlError = fieldErrors.base_url ?? probeError?.fields.base_url;
  const keyError = fieldErrors.api_key_ref ?? probeError?.fields.api_key_ref;

  return (
    <>
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>
          Approved requests are sent here. Routing decides which requests.
        </DialogDescription>
      </DialogHeader>

      {formError || unshownErrors.length > 0 ? (
        <div
          role="alert"
          className="border-destructive/40 bg-destructive/10 text-destructive space-y-1 rounded-md border px-3 py-2 text-sm"
        >
          {formError ? <p>{formError}</p> : null}
          {unshownErrors.map(([key, detail]) => (
            <p key={key}>{detail}</p>
          ))}
        </div>
      ) : null}

      <div className="settings-field-list">
        {installations.length > 1 ? (
          <SettingFieldRow
            label="Server type"
            htmlFor={typeId}
            description={typeLock}
            status={
              <FieldError>
                {[fieldErrors.installation_id, fieldErrors.capability_id].filter(Boolean).join(" ")}
              </FieldError>
            }
          >
            <Select
              value={selected ? installationOptionValue(selected) : ""}
              onValueChange={changeType}
              disabled={typeLock !== undefined}
            >
              <SelectTrigger id={typeId} className={SETTINGS_CONTROL_WIDTH}>
                <SelectValue placeholder="Choose a type" />
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
          </SettingFieldRow>
        ) : null}
        <SettingFieldRow
          label="Name"
          htmlFor={nameId}
          status={<FieldError>{fieldErrors.name}</FieldError>}
        >
          <Input
            id={nameId}
            value={form.name}
            onChange={(event) => patch({ name: event.target.value })}
            placeholder={typeLabel ? `e.g. ${typeLabel} 4K` : "e.g. Radarr 4K"}
            aria-invalid={Boolean(fieldErrors.name)}
            className={SETTINGS_CONTROL_WIDTH}
          />
        </SettingFieldRow>
        <SettingFieldRow label="URL" htmlFor={urlId} status={<FieldError>{urlError}</FieldError>}>
          <Input
            id={urlId}
            value={form.base_url}
            onChange={(event) => patch({ base_url: event.target.value })}
            onBlur={() => {
              const next = withScheme(form.base_url);
              if (next !== form.base_url) patch({ base_url: next });
            }}
            placeholder="http://192.168.1.10:8989"
            aria-invalid={Boolean(urlError)}
            className={SETTINGS_CONTROL_WIDTH}
          />
        </SettingFieldRow>
        <SecretField
          label="API key"
          value={form.api_key_ref}
          configured={form.has_api_key}
          onChange={(value) => patch({ api_key_ref: value })}
          hint={
            form.has_api_key
              ? "Saved. Type a new key to replace it; leave blank to keep it."
              : "From the server's Settings › General."
          }
          status={<FieldError>{keyError}</FieldError>}
        />
        <SettingFieldRow label="Enabled" htmlFor={`${nameId}-enabled`}>
          <Switch
            id={`${nameId}-enabled`}
            checked={form.enabled}
            onCheckedChange={(enabled) => patch({ enabled })}
          />
        </SettingFieldRow>
        {offers4K ? (
          <SettingFieldRow
            label="4K server"
            htmlFor={`${nameId}-4k`}
            description="With Standard routing, 4K versions go here and everything else goes to the other server of this type."
            status={<FieldError>{fieldErrors[FOUR_K_KEY]}</FieldError>}
          >
            <Switch
              id={`${nameId}-4k`}
              checked={is4K}
              onCheckedChange={(on) =>
                // The plugin's older "4K default" switch meant the same; it
                // goes off with this one.
                patchConfig({
                  ...pluginConfig,
                  [FOUR_K_KEY]: on,
                  ...(on ? {} : { is_default_4k: false }),
                })
              }
            />
          </SettingFieldRow>
        ) : null}
      </div>

      {descriptor ? (
        <div className="space-y-2">
          <SchemaForm
            descriptor={descriptor}
            values={pluginConfig}
            onChange={patchConfig}
            dynamicOptions={options}
            optionsLoading={optionsStatus === "loading"}
            errors={schemaErrors}
            onValidityChange={setSchemaValid}
            idPrefix={`server-${form.id || "new"}`}
            hiddenKeys={ROUTING_OWNED_CONFIG_KEYS}
            lockedKeys={typeLock ? { [SERVICE_KIND_KEY]: typeLock } : undefined}
            expandSections
          />
          {optionsStatus === "error" &&
          !test &&
          (probeError?.message || Object.keys(probeError?.fields ?? {}).length === 0) ? (
            <p className="text-xs text-amber-600 dark:text-amber-400">
              {probeError?.message ||
                "Couldn't read root folders and profiles from the server. Check the URL and API key, then Test."}
            </p>
          ) : null}
          {optionsStatus === "idle" && detected && !test && !kindMismatch ? (
            <p className="text-muted-foreground text-xs">Detected {detected.label}.</p>
          ) : null}
        </div>
      ) : hasInstallation ? (
        <p className="text-muted-foreground text-sm">This plugin has no settings of its own.</p>
      ) : (
        <p className="text-muted-foreground text-sm">Choose a server type to continue.</p>
      )}

      {conflict ? <EditorConflict onReload={reload} /> : null}

      <DialogFooter className="flex-wrap items-center gap-2 sm:justify-between">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          {!isNew ? (
            <Button
              type="button"
              variant="outline"
              className="text-destructive"
              onClick={() => {
                setDeleteError(null);
                setConfirmDelete(true);
              }}
              disabled={deleteServer.isPending || conflict || !etag || deleteBlockedBy !== ""}
              aria-describedby={deleteBlockedBy ? deleteBlockedId : undefined}
            >
              <Trash2 aria-hidden="true" />
              Delete
            </Button>
          ) : null}
          {deleteBlockedBy ? (
            <span id={deleteBlockedId} className="text-muted-foreground min-w-0 text-xs">
              Routing still sends requests here ({deleteBlockedBy}); send them elsewhere first.
            </span>
          ) : null}
          {test ? (
            <span
              role="status"
              className={cn(
                "min-w-0 text-xs",
                test.ok ? "text-muted-foreground" : "text-amber-600 dark:text-amber-400",
              )}
            >
              {test.message}
            </span>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={() => void runTest()}
            disabled={!canLoad || testing}
          >
            {testing ? "Testing…" : "Test"}
          </Button>
          <Button type="button" variant="outline" onClick={onDone}>
            Cancel
          </Button>
          <Button type="button" onClick={save} disabled={!canSave || saving}>
            {saving ? "Saving…" : isNew ? "Add server" : "Save"}
          </Button>
        </div>
      </DialogFooter>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {source?.name || "server"}?</AlertDialogTitle>
            <AlertDialogDescription>
              Silo stops sending requests to it, and Autoscan connections that reuse it lose their
              connection details.
              {clearsFallback
                ? ` Everything else for ${mediaTypePlural(clearsFallback)} goes with it, so those requests have nowhere to go until you add another server.`
                : null}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {deleteError ? (
            <p role="alert" className="text-destructive text-sm">
              {deleteError}
            </p>
          ) : null}
          {conflict ? <EditorConflict onReload={reload} /> : null}
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={deleteServer.isPending || conflict || !etag}
              onClick={(event) => {
                // Stay open until the server answers, so a refused delete can
                // say why right here.
                event.preventDefault();
                deleteServer.mutate(
                  { id: form.id, etag },
                  {
                    onSuccess: () => {
                      setConfirmDelete(false);
                      onDone();
                    },
                    onError: (error) => {
                      if (isRequestEditorConflict(error)) setConflict(true);
                      else
                        setDeleteError(error instanceof Error ? error.message : "Delete failed.");
                    },
                  },
                );
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
