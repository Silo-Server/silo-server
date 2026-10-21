import { useCallback, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AlertTriangle, RefreshCw, Trash2, Webhook } from "lucide-react";
import { Link } from "react-router";

import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import type {
  AutoscanDeliveryMode,
  AutoscanPathRewrite,
  AutoscanSource,
  AutoscanWebhookProvider,
} from "@/api/types";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAutoscanSourceBusy,
  useAvailableScanSources,
  useCreateAutoscanSource,
  useCreateAutoscanWebhook,
  useUpdateAutoscanSource,
} from "@/hooks/queries/useAutoscan";
import { cn } from "@/lib/utils";

import { ChoiceCard, StepTrail, type Step } from "./ChoiceCard";
import InlineConnectionPicker, { type ConnectionOption } from "./InlineConnectionPicker";
import { PathMappingEditor } from "./PathMappingEditor";
import { RewriteSuggestions } from "./RewriteSuggestions";
import SourceConfigForm from "./SourceConfigForm";
import { formatInterval, providerName } from "./sourceDisplay";
import {
  configFields,
  connectionIsMandatory,
  connectionMatchesKinds,
  defaultDeliveryMode,
  descriptorFor,
  initialConfigValues,
  needsConnectionStep,
  needsDeliveryChoice,
  parseConfigValues,
  serializeConfigValues,
} from "./sourceDescriptor";
import {
  BLANK_SOURCE_DRAFT,
  MAX_POLL_INTERVAL_SECONDS,
  WEBHOOK_PROVIDER_KEY,
  draftConnectionId,
  draftFromSource,
  editedSourceBody,
  incompleteMappings,
  normalizeSourceConfig,
  parseIntervalInput,
  pluginKey,
  sourceConfigForEdit,
  webhookProviderOf,
  type SourceDraft,
} from "./sourceForm";
import { WebhookEndpointSection } from "./WebhookEndpoint";
import { absoluteWebhookURL } from "./webhookURL";
import { WebhookInstructions, WebhookProviderSelect, WebhookTriggerHint } from "./WebhookSetupStep";
import { hasUsableMapping, newMapping, seedMappings, usableMappings } from "./webhookSetup";

// ---------------------------------------------------------------------------
// Add and edit share one dialog.
//
// The flow is built from the source's descriptor rather than from its plugin
// id: a source declaring one delivery mode is never asked to choose one, a
// source needing no credentials never sees the connection step, and its own
// config fields come from its manifest. Adding a scan-source plugin therefore
// needs no change here.
//
// Add stacks the questions on one page under a step trail. Edit puts the same
// sections on tabs, with the plugin and delivery mode fixed.
// ---------------------------------------------------------------------------

/** Human wording for a delivery mode, used on the choice cards. */
const DELIVERY_MODE_COPY: Record<AutoscanDeliveryMode, { title: string; description: string }> = {
  webhook: {
    title: "The service tells Silo",
    description:
      "Instant. Paste one URL into the service's webhook settings. No credentials stored here.",
  },
  poll: {
    title: "Silo checks the service",
    description: "Silo asks on a schedule. Works without changing anything upstream.",
  },
};

type TabId = "connect" | "connection" | "settings" | "paths" | "general";

interface Problem {
  tab: TabId;
  message: string;
}

type SourceDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connectionOptions: ConnectionOption[];
  globalPollInterval: number | null;
} & (
  | { mode: "add" }
  | {
      mode: "edit";
      /** The live source; the dialog's draft is taken from it when it mounts. */
      source: AutoscanSource;
      title: string;
      description: string;
      onRequestDelete: (source: AutoscanSource) => void;
    }
);

export function SourceDialog(props: SourceDialogProps) {
  const { open, onOpenChange, connectionOptions, globalPollInterval } = props;
  const editSource = props.mode === "edit" ? props.source : null;
  const isEdit = editSource !== null;

  const available = useAvailableScanSources();
  const libraries = useAdminLibraries();
  const [draftAuthority, setDraftAuthority] = useState(captureProfileRequestContext);
  const createSource = useCreateAutoscanSource(draftAuthority);
  const createWebhook = useCreateAutoscanWebhook(draftAuthority);
  const updateSource = useUpdateAutoscanSource(draftAuthority);

  const plugins = available.data ?? [];
  const findPlugin = (key: string) =>
    plugins.find((p) => pluginKey(p.plugin_id, p.capability_id) === key);

  const [form, setForm] = useState<SourceDraft>(() =>
    editSource
      ? draftFromSource(
          editSource,
          descriptorFor(findPlugin(pluginKey(editSource.plugin_id, editSource.capability_id))),
        )
      : BLANK_SOURCE_DRAFT,
  );
  const currentForm = useRef(form);
  useLayoutEffect(() => {
    currentForm.current = form;
  }, [form]);
  // Identifies the add submission whose result may still drive the dialog.
  // Closing the dialog starts a new one; typing while a create is pending
  // does not, so the created source still gets its endpoint and Connect step.
  const submission = useRef(0);

  const selectedPlugin = findPlugin(form.pluginKey);
  const descriptor = descriptorFor(selectedPlugin);
  const hasPlugin = isEdit || Boolean(selectedPlugin);

  // /sources and /scan-source-plugins are independent queries. If the edit
  // dialog mounts before the descriptor arrives it parses with the defaults,
  // leaving switch and multi-select values as raw strings — "false" would
  // render as an enabled switch. Re-parse during render once the real
  // descriptor arrives, and only while the operator has not edited the config.
  const [parsedWith, setParsedWith] = useState(descriptor);
  if (parsedWith !== descriptor) {
    setParsedWith(descriptor);
    if (editSource && !form.configDirty) {
      setForm((f) => {
        const sourceConfig = parseConfigValues(descriptor, sourceConfigForEdit(editSource));
        // The provider has its own control and does not mark the config dirty;
        // keep a choice made before the descriptor arrived.
        const chosenProvider = f.sourceConfig[WEBHOOK_PROVIDER_KEY];
        if (chosenProvider !== undefined) sourceConfig[WEBHOOK_PROVIDER_KEY] = chosenProvider;
        return { ...f, sourceConfig };
      });
    }
  }

  // Set once a webhook source exists and its endpoint has been generated. The
  // dialog then shows the paste-this-into-your-arr instructions rather than
  // closing, so setup finishes in one place.
  const [createdWebhookReceipt, setCreatedWebhookSource] = useState<AutoscanSource | null>(null);
  const createdWebhookSource =
    draftAuthority && isCapturedProfileAuthorityActive(draftAuthority)
      ? createdWebhookReceipt
      : null;

  // The chosen mode, or the descriptor's default while the operator has not
  // been asked (single-mode sources are never asked at all).
  const deliveryMode: AutoscanDeliveryMode = editSource
    ? editSource.delivery_mode
    : form.deliveryMode || defaultDeliveryMode(descriptor);
  const isWebhook = hasPlugin && deliveryMode === "webhook";

  const showDeliveryChoice = !isEdit && Boolean(selectedPlugin) && needsDeliveryChoice(descriptor);
  const showConnection = hasPlugin && needsConnectionStep(descriptor, deliveryMode);
  const connectionRequired = connectionIsMandatory(descriptor, deliveryMode);
  // Poll interval only means something when Silo is the one asking.
  const showInterval = hasPlugin && deliveryMode === "poll";

  // Only offer connections this source can actually talk to.
  const eligibleConnections = connectionOptions.filter((c) =>
    connectionMatchesKinds(descriptor, c.kind),
  );

  // The arr webhook's provider has its own control next to the URL, so it is
  // left out of the generic settings form rather than shown twice.
  const showProvider =
    isWebhook &&
    (configFields(descriptor).some((field) => field.key === WEBHOOK_PROVIDER_KEY) ||
      (editSource?.source_config ?? {})[WEBHOOK_PROVIDER_KEY] !== undefined);
  const provider =
    (form.sourceConfig[WEBHOOK_PROVIDER_KEY] as AutoscanWebhookProvider | undefined) ?? "auto";

  // Stable identity matters: SchemaForm reports validity from an effect keyed
  // on its descriptor and callback, so rebuilding either inline re-fires it
  // every render and loops through setForm (React error #185).
  const settingsFields = useMemo(
    () =>
      configFields(descriptor).filter(
        (field) => !(showProvider && field.key === WEBHOOK_PROVIDER_KEY),
      ),
    [descriptor, showProvider],
  );
  const settingsDescriptor = useMemo(
    () => ({
      ...descriptor,
      config_form: { ...(descriptor.config_form ?? { fields: [] }), fields: settingsFields },
    }),
    [descriptor, settingsFields],
  );
  const handleConfigValidity = useCallback((configValid: boolean) => {
    setForm((f) => (f.configValid === configValid ? f : { ...f, configValid }));
  }, []);
  const hasSettings = showInterval || settingsFields.length > 0;

  const libraryPaths = (libraries.data ?? []).flatMap((library) => library.paths ?? []);
  const idBase = isEdit ? `edit-source-${editSource.id}` : "add-source";

  // --- Validation ---------------------------------------------------------

  const interval = parseIntervalInput(form.intervalStr);
  const intervalInvalid = showInterval && !interval.valid;
  // Seeded webhook rows start with only the Silo side filled; until the
  // operator touches them they are suggestions, not unfinished input.
  const checkIncomplete = isEdit || !isWebhook;
  const incomplete = checkIncomplete ? incompleteMappings(form.mappings) : [];

  const problems: Problem[] = [];
  if (hasPlugin) {
    if (connectionRequired && !draftConnectionId(form))
      problems.push({ tab: "connection", message: "Choose the server this source reads from." });
    if (intervalInvalid)
      problems.push({
        tab: "settings",
        message: "The check interval must be a whole number of seconds.",
      });
    if (!form.configValid)
      problems.push({ tab: "settings", message: "Some settings are missing or invalid." });
    if (incomplete.length > 0)
      problems.push({ tab: "paths", message: "Each path mapping needs both paths." });
    // A new webhook source needs a mapping, and an edit may not remove the last
    // one: without it deliveries usually match no library. A source saved
    // earlier without mappings (both sides use the same paths) stays editable.
    // A polling source may have none: its paths are used as reported.
    if (
      isWebhook &&
      !hasUsableMapping(form.mappings) &&
      (!editSource || editSource.path_rewrites.length > 0)
    )
      problems.push({
        tab: "paths",
        message: "Add at least one complete path mapping, or deliveries will match no library.",
      });
  }
  const [attempted, setAttempted] = useState(false);
  const problemTabs = new Set(attempted ? problems.map((p) => p.tab) : []);

  // --- Tabs (edit) ----------------------------------------------------------

  const tabs: Array<{ id: TabId; label: string }> = isWebhook
    ? [
        { id: "connect", label: "Connect" },
        { id: "paths", label: "Match paths" },
        ...(hasSettings ? [{ id: "settings" as const, label: "Settings" }] : []),
        { id: "general", label: "General" },
      ]
    : [
        ...(showConnection ? [{ id: "connection" as const, label: "Connection" }] : []),
        ...(hasSettings ? [{ id: "settings" as const, label: "Settings" }] : []),
        { id: "paths", label: "Match paths" },
        { id: "general", label: "General" },
      ];
  const [chosenTab, setActiveTab] = useState<TabId>(tabs[0]!.id);
  // The tab set follows the descriptor, which can arrive after the dialog opens.
  const activeTab = tabs.some((tab) => tab.id === chosenTab) ? chosenTab : tabs[0]!.id;

  // --- Actions ------------------------------------------------------------

  function close() {
    if (!isEdit) {
      submission.current += 1;
      setDraftAuthority(captureProfileRequestContext());
      setForm(BLANK_SOURCE_DRAFT);
      setCreatedWebhookSource(null);
      setAttempted(false);
    }
    onOpenChange(false);
  }

  function update(patch: Partial<SourceDraft>) {
    setForm((f) => ({ ...f, ...patch }));
  }

  function selectPlugin(value: string) {
    const next = descriptorFor(findPlugin(value));
    // Reset per-source state on every change: config keys, delivery modes and
    // eligible connections all belong to the previously selected source.
    const nextMode = defaultDeliveryMode(next);
    setAttempted(false);
    setForm({
      ...BLANK_SOURCE_DRAFT,
      label: form.label,
      pluginKey: value,
      sourceConfig: initialConfigValues(next),
      mappings:
        nextMode === "webhook" ? seedMappings(webhookProviderOf(next), libraries.data ?? []) : [],
    });
  }

  /** Seed mappings when the operator switches delivery to webhook. */
  function selectDeliveryMode(mode: AutoscanDeliveryMode) {
    setForm((f) => ({
      ...f,
      deliveryMode: mode,
      mappings:
        mode === "webhook"
          ? f.mappings.length === 0
            ? seedMappings(webhookProviderOf(descriptor), libraries.data ?? [])
            : f.mappings
          : // Seeded webhook rows have no meaning for polling; keep only real input.
            f.mappings.filter((m) => m.from.trim() !== ""),
    }));
  }

  function applySuggestions(additions: AutoscanPathRewrite[]) {
    setForm((f) => {
      const existing = new Set(f.mappings.map((m) => m.from.trim()));
      const added = additions
        .filter((a) => !existing.has(a.from))
        .map((a) => newMapping(a.to, a.from));
      return { ...f, mappings: [...f.mappings, ...added], mappingsDirty: true };
    });
  }

  /**
   * Seeded rows follow the provider: Sonarr feeds TV libraries and Radarr movie
   * libraries. Re-seed while adding, until the operator edits the rows.
   */
  function selectProvider(next: AutoscanWebhookProvider) {
    setForm((f) => ({
      ...f,
      sourceConfig: { ...f.sourceConfig, [WEBHOOK_PROVIDER_KEY]: next },
      mappings:
        !isEdit && !f.mappingsDirty
          ? seedMappings(
              webhookProviderOf(descriptor, { [WEBHOOK_PROVIDER_KEY]: next }),
              libraries.data ?? [],
            )
          : f.mappings,
    }));
  }

  function serializedConfig(): Record<string, string> {
    const out = serializeConfigValues(form.sourceConfig, descriptor);
    // The provider is edited outside the schema form; keep it even when the
    // descriptor does not declare it.
    const chosen = form.sourceConfig[WEBHOOK_PROVIDER_KEY];
    if (showProvider && typeof chosen === "string") out[WEBHOOK_PROVIDER_KEY] = chosen;
    return out;
  }

  function handleSubmit() {
    if (!hasPlugin || !draftAuthority || !isCapturedProfileAuthorityActive(draftAuthority)) return;
    if (problems.length > 0) {
      setAttempted(true);
      if (isEdit) setActiveTab(problems[0]!.tab);
      return;
    }

    if (editSource) {
      updateSource.mutate(
        { id: editSource.id, body: editedSourceBody(editSource, form, serializedConfig()) },
        {
          onSuccess: () => {
            if (currentForm.current === form) onOpenChange(false);
          },
        },
      );
      return;
    }

    if (!selectedPlugin) return;
    const thisSubmission = ++submission.current;
    const current = () => submission.current === thisSubmission;
    createSource.mutate(
      {
        plugin_id: selectedPlugin.plugin_id,
        capability_id: selectedPlugin.capability_id,
        connection_id: showConnection ? draftConnectionId(form) : null,
        // Created enabled: this dialog collects everything a source needs
        // (server, settings, interval, mappings, label), so there is nothing
        // left to configure afterwards.
        enabled: true,
        delivery_mode: deliveryMode,
        poll_interval_seconds: showInterval && interval.valid ? interval.seconds : null,
        path_rewrites: usableMappings(form.mappings),
        source_config: normalizeSourceConfig(serializedConfig()),
        label: form.label.trim(),
      },
      {
        onSuccess: (created) => {
          if (!current()) return;
          if (!isWebhook) {
            close();
            return;
          }
          // Generate the endpoint immediately and stay open: the operator still
          // needs the URL, and closing here is what previously left them to
          // hunt for a "Generate webhook URL" button.
          //
          // On failure, stay open holding the created source rather than
          // closing. The source exists and is enabled at this point, so
          // dismissing the dialog would leave an enabled webhook source with no
          // endpoint and no visible explanation. The instructions panel renders
          // a retry when the URL is missing.
          createWebhook.mutate(created.id, {
            onSuccess: (withWebhook) => {
              if (isCapturedProfileAuthorityActive(draftAuthority) && current())
                setCreatedWebhookSource(withWebhook);
            },
            onError: () => {
              if (isCapturedProfileAuthorityActive(draftAuthority) && current())
                setCreatedWebhookSource(created);
            },
          });
        },
      },
    );
  }

  const busy = createSource.isPending || createWebhook.isPending || updateSource.isPending;
  // Edit saves a complete body built from the source as it was when the dialog
  // opened, so it waits while another write or re-read for this source is in
  // flight. Add has no stored source to race.
  const sourceBusy = useAutoscanSourceBusy(editSource?.id ?? "");
  const saveBlocked = busy || (isEdit && sourceBusy);
  // Dismissing mid-request would drop the result: an add would leave a
  // webhook source without its endpoint, and a save's outcome would go unseen.
  const handleOpenChange = (next: boolean) => {
    if (next) onOpenChange(true);
    else if (!busy) close();
  };
  const firstProblem = attempted ? problems[0] : undefined;

  // --- Sections shared by both layouts --------------------------------------

  const providerSelect = showProvider ? (
    <WebhookProviderSelect id={`${idBase}-provider`} value={provider} onChange={selectProvider} />
  ) : null;

  const connectionSection = showConnection ? (
    <InlineConnectionPicker
      // Remount per source: the picker holds its own draft (URL, key, reuse
      // id, test result), and switching plugins mid-add would otherwise submit
      // stale credentials under the new descriptor's kind.
      key={form.pluginKey}
      value={form.connectionId}
      onChange={(connectionId) => update({ connectionId })}
      options={eligibleConnections}
      required={connectionRequired}
      connectionKinds={descriptor.connection_kinds ?? []}
      idPrefix={`${idBase}-conn`}
    />
  ) : null;

  const defaultIntervalText =
    globalPollInterval != null ? ` (${formatInterval(globalPollInterval)})` : "";
  const intervalSection = showInterval ? (
    <div className="space-y-1.5">
      <Label htmlFor={`${idBase}-interval`}>Check interval (seconds)</Label>
      <div className="flex items-center gap-2">
        <Input
          id={`${idBase}-interval`}
          className="w-32"
          placeholder="Default"
          inputMode="numeric"
          value={form.intervalStr}
          aria-invalid={intervalInvalid}
          onChange={(e) => update({ intervalStr: e.target.value })}
        />
        <span className="text-muted-foreground text-sm">sec</span>
      </div>
      {intervalInvalid && (
        <p className="text-destructive text-xs">
          Must be a whole number from 1 to {MAX_POLL_INTERVAL_SECONDS.toLocaleString()}.
        </p>
      )}
      <p className="text-muted-foreground text-xs">
        Optional — leave blank to use the global default{defaultIntervalText}. A longer interval
        checks this source less often; values below the default have no effect.
      </p>
    </div>
  ) : null;

  const configSection =
    hasPlugin && settingsFields.length > 0 ? (
      <SourceConfigForm
        descriptor={settingsDescriptor}
        values={form.sourceConfig}
        onChange={(sourceConfig) => update({ sourceConfig, configDirty: true })}
        onValidityChange={handleConfigValidity}
        idPrefix={`${idBase}-config`}
      />
    ) : null;

  const invalidIds = attempted ? new Set(incomplete.map((m) => m.id)) : undefined;
  const syncDisabledReason = !editSource
    ? null
    : !editSource.connection_id
      ? "Choose a server on the Connection tab and save to sync from it."
      : draftConnectionId(form) !== editSource.connection_id
        ? "Save the new server choice first, then sync from it."
        : null;
  const mappingSection = hasPlugin ? (
    <div className="space-y-2">
      <PathMappingEditor
        mappings={form.mappings}
        onChange={(mappings) => update({ mappings, mappingsDirty: true })}
        variant={isWebhook ? "webhook" : "poll"}
        libraryPaths={libraryPaths}
        invalidIds={invalidIds}
        idPrefix={`${idBase}-map`}
        actions={
          editSource && showConnection ? (
            <RewriteSuggestions
              sourceId={editSource.id}
              disabledReason={syncDisabledReason}
              onApply={applySuggestions}
            />
          ) : null
        }
      />
      {!isEdit && showConnection && (
        <p className="text-muted-foreground text-xs">
          Once the source is saved, Edit → Match paths can read these from the server with{" "}
          <span className="font-medium">Sync from server</span>.
        </p>
      )}
    </div>
  ) : null;

  const typeName = selectedPlugin?.display_name ?? editSource?.capability_id ?? "";
  // What the list calls the source while it has no label of its own (see
  // sourceTitle): the arr a webhook receives from, else the type.
  const unlabelledName = (isWebhook && providerName(provider)) || typeName;
  const labelSection = hasPlugin ? (
    <div className="space-y-1.5">
      <Label htmlFor={`${idBase}-label`}>
        Custom label <span className="text-muted-foreground font-normal">(optional)</span>
      </Label>
      <Input
        id={`${idBase}-label`}
        value={form.label}
        maxLength={1024}
        onChange={(e) => update({ label: e.target.value })}
      />
      <p className="text-muted-foreground text-xs">
        Leave empty to show &ldquo;{unlabelledName}&rdquo;. A label tells sources of the same kind
        apart in this list and in Activity.
      </p>
    </div>
  ) : null;

  const problemMessage = firstProblem ? (
    <p
      className="text-destructive flex items-center gap-1.5 text-xs sm:mr-auto sm:self-center"
      role="alert"
    >
      <AlertTriangle className="size-3.5 shrink-0" />
      {firstProblem.message}
    </p>
  ) : null;

  // --- Edit layout --------------------------------------------------------

  if (props.mode === "edit" && editSource) {
    const tabBody: Record<TabId, ReactNode> = {
      connect: (
        <div className="space-y-5">
          <div className="space-y-2">
            <WebhookEndpointSection source={editSource} />
            <WebhookTriggerHint
              provider={showProvider ? provider : webhookProviderOf(descriptor)}
            />
          </div>
          {providerSelect}
        </div>
      ),
      connection: connectionSection,
      settings: (
        <div className="space-y-5">
          {intervalSection}
          {configSection}
        </div>
      ),
      paths: mappingSection,
      general: (
        <div className="space-y-5">
          {labelSection}
          <div className="text-muted-foreground space-y-1 text-xs">
            <p>
              Type: <span className="text-foreground">{typeName}</span>
              {" · "}
              {isWebhook ? "webhook delivery" : "polled on a schedule"}
            </p>
            <p className="font-mono break-all">{editSource.plugin_id}</p>
            <p>To use a different type, add a new source.</p>
          </div>
        </div>
      ),
    };

    return (
      <Dialog open={open} onOpenChange={handleOpenChange}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] grid-cols-[minmax(0,1fr)] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="pr-6 break-words">Edit source · {props.title}</DialogTitle>
            <DialogDescription>{props.description}</DialogDescription>
          </DialogHeader>

          <Tabs
            value={activeTab}
            onValueChange={(v) => setActiveTab(v as TabId)}
            className="gap-5 sm:min-h-80"
          >
            {/* Every tab stays visible on a phone: compact type below sm, and
                the strip wraps rather than scrolling a tab out of view. */}
            <TabsList
              variant="line"
              className="border-border h-auto w-full flex-wrap justify-start gap-y-2 border-b pb-1"
            >
              {tabs.map((tab) => (
                <TabsTrigger
                  key={tab.id}
                  value={tab.id}
                  className="flex-none px-1.5 text-[13px] sm:px-3 sm:text-sm"
                >
                  {tab.label}
                  {problemTabs.has(tab.id) && (
                    <span
                      className="bg-destructive size-1.5 rounded-full"
                      aria-label="needs attention"
                    />
                  )}
                </TabsTrigger>
              ))}
            </TabsList>
            {/* Kept mounted so drafts in inactive tabs (an inline connection being
                created, schema form state) survive switching, and the settings
                form keeps reporting its validity. */}
            {tabs.map((tab) => (
              <TabsContent
                key={tab.id}
                value={tab.id}
                forceMount
                className="data-[state=inactive]:hidden"
              >
                {tabBody[tab.id]}
              </TabsContent>
            ))}
          </Tabs>

          <DialogFooter className="border-border border-t pt-4 sm:items-center">
            <Button
              type="button"
              variant="ghost"
              className="text-destructive hover:text-destructive hover:bg-destructive/10 sm:mr-auto"
              onClick={() => props.onRequestDelete(editSource)}
            >
              <Trash2 />
              Delete source
            </Button>
            {problemMessage}
            <Button variant="outline" onClick={close} disabled={busy}>
              Cancel
            </Button>
            <Button onClick={handleSubmit} disabled={saveBlocked}>
              {updateSource.isPending ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  // --- Add layout ---------------------------------------------------------

  // Steps vary per source: a single-mode credential-free watcher genuinely has
  // fewer questions than a pollable arr, so the trail is built from what this
  // descriptor actually asks rather than being a fixed 1-2-3.
  //
  // A step is done once it is answered: filled in, or — for a step that needs
  // nothing — once the operator has touched it or moved on to a later one.
  const connectionChosen = draftConnectionId(form) !== null;
  const stepStates: Array<{ label: string; satisfied: boolean; touched: boolean }> = [
    {
      label: "What changes?",
      satisfied: Boolean(selectedPlugin),
      touched: Boolean(selectedPlugin),
    },
    ...(showDeliveryChoice
      ? [
          {
            label: "How do we hear about it?",
            satisfied: Boolean(form.deliveryMode),
            touched: Boolean(form.deliveryMode),
          },
        ]
      : []),
    ...(showConnection
      ? [
          {
            label: "Which server?",
            satisfied: connectionChosen || !connectionRequired,
            touched: connectionChosen,
          },
        ]
      : []),
    ...(!selectedPlugin
      ? []
      : isWebhook
        ? [
            {
              label: "Match paths",
              satisfied: hasUsableMapping(form.mappings),
              touched: form.mappingsDirty,
            },
            { label: "Connect it", satisfied: false, touched: false },
          ]
        : [
            ...(hasSettings
              ? [
                  {
                    label: "Set it up",
                    satisfied: !intervalInvalid && form.configValid,
                    touched: form.configDirty || form.intervalStr.trim() !== "",
                  },
                ]
              : []),
            {
              label: "Match paths",
              satisfied: incomplete.length === 0,
              touched: form.mappingsDirty,
            },
          ]),
  ];
  const steps: Step[] = stepStates.map((step, index) => ({
    label: step.label,
    done:
      step.satisfied &&
      (step.touched || stepStates.slice(index + 1).some((later) => later.touched)),
  }));

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] grid-cols-[minmax(0,1fr)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {createdWebhookSource ? "Almost done — connect your service" : "Add scan source"}
          </DialogTitle>
          <DialogDescription>
            {createdWebhookSource
              ? "The source is created and listening. Paste this into your download manager to finish."
              : "Pick what you want Silo to watch. Each source only asks for what it actually needs."}
          </DialogDescription>
        </DialogHeader>

        {createdWebhookSource ? (
          <div className="space-y-4">
            <StepTrail
              steps={steps.map((step, index) => ({ ...step, done: index < steps.length - 1 }))}
            />
            {createdWebhookSource.webhook_url ? (
              <WebhookInstructions
                url={absoluteWebhookURL(createdWebhookSource.webhook_url)}
                provider={webhookProviderOf(descriptor, createdWebhookSource.source_config)}
              />
            ) : (
              <div className="border-destructive/30 bg-destructive/10 space-y-3 rounded-md border p-3">
                <p className="text-destructive flex items-center gap-1.5 text-sm font-medium">
                  <AlertTriangle className="size-4 shrink-0" />
                  Couldn&apos;t generate the webhook URL
                </p>
                <p className="text-muted-foreground text-xs">
                  The source was created, but has no endpoint yet — it can&apos;t receive anything
                  until one exists.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={createWebhook.isPending}
                  onClick={() =>
                    createWebhook.mutate(createdWebhookSource.id, {
                      onSuccess: (withWebhook) => setCreatedWebhookSource(withWebhook),
                    })
                  }
                >
                  <RefreshCw />
                  {createWebhook.isPending ? "Retrying…" : "Try again"}
                </Button>
              </div>
            )}
          </div>
        ) : available.isLoading ? (
          <p className="text-muted-foreground py-4 text-sm">Loading available sources…</p>
        ) : plugins.length === 0 ? (
          <p className="text-muted-foreground py-4 text-sm">
            No scan-source plugins installed. Install one from the{" "}
            <Link to="/admin/plugins" className="text-primary underline-offset-4 hover:underline">
              Plugins page
            </Link>{" "}
            to add sources here.
          </p>
        ) : (
          <div className="space-y-5">
            <StepTrail steps={steps} />

            <div className="space-y-2">
              <Label>What should Silo watch?</Label>
              <div className="grid gap-2 sm:grid-cols-2">
                {plugins.map((p) => {
                  const key = pluginKey(p.plugin_id, p.capability_id);
                  const pluginDescriptor = descriptorFor(p);
                  return (
                    <ChoiceCard
                      key={key}
                      title={p.display_name}
                      description={pluginDescriptor.summary || p.description}
                      icon={
                        pluginDescriptor.delivery_modes.includes("webhook") &&
                        pluginDescriptor.delivery_modes.length === 1 ? (
                          <Webhook className="size-3.5" />
                        ) : (
                          <RefreshCw className="size-3.5" />
                        )
                      }
                      selected={form.pluginKey === key}
                      onSelect={() => selectPlugin(key)}
                    />
                  );
                })}
              </div>
            </div>

            {showDeliveryChoice && (
              <div className="space-y-2">
                <Label>How should Silo hear about changes?</Label>
                <div className="grid gap-2 sm:grid-cols-2">
                  {descriptor.delivery_modes.map((mode) => {
                    const copy = DELIVERY_MODE_COPY[mode];
                    return (
                      <ChoiceCard
                        key={mode}
                        title={copy?.title ?? mode}
                        description={copy?.description}
                        icon={mode === "webhook" ? <Webhook className="size-3.5" /> : undefined}
                        badge={mode === "webhook" ? "Recommended" : undefined}
                        selected={deliveryMode === mode}
                        onSelect={() => selectDeliveryMode(mode)}
                      />
                    );
                  })}
                </div>
              </div>
            )}

            {isWebhook ? (
              <>
                {providerSelect}
                {mappingSection}
                {configSection}
              </>
            ) : (
              <>
                {connectionSection}
                {intervalSection}
                {configSection}
                {mappingSection}
              </>
            )}

            {labelSection}
          </div>
        )}

        <DialogFooter className={cn(firstProblem && "sm:items-center")}>
          {createdWebhookSource ? (
            // The source already exists at this point, so there is nothing to
            // cancel — only an acknowledgement that the URL has been copied.
            <Button onClick={close}>Done</Button>
          ) : (
            <>
              {problemMessage}
              <Button variant="outline" onClick={close} disabled={busy}>
                Cancel
              </Button>
              {plugins.length > 0 && (
                <Button onClick={handleSubmit} disabled={!selectedPlugin || busy}>
                  {busy ? "Adding…" : isWebhook ? "Create and continue" : "Add source"}
                </Button>
              )}
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
