import {
  captureProviderEditIntent,
  getProviderEditor,
  providerIntentActive,
  providerSaveMessage,
  type ProviderEditor,
} from "@/api/v2/adminSubtitleProviderConfiguration";
import { useRef, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";

import type { components } from "@/api/v2/schema";
type SubtitleProviderConfig = components["schemas"]["AdminSubtitleProvider"];
import {
  ProviderPanelActions,
  ProviderTile,
  ProviderTileGrid,
  resolveProviderTileState,
} from "@/components/settings/ProviderTile";
import type { ProviderTestState } from "@/components/settings/ProviderTile";
import { RestartBadge } from "@/components/settings/RestartBadge";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
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
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useCheckAdminSettingsConnection,
  useUpdateServerSettings,
} from "@/hooks/queries/admin/settings";
import {
  useSubtitleProviders,
  useTestSubtitleProvider,
  useUpdateSubtitleProvider,
} from "@/hooks/queries/admin/subtitles";
import { useRestartKeys, type RestartKeyMatcher } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { sortSubtitleProviders } from "@/lib/subtitleProviders";

import { FieldGroup } from "./FieldGroup";
import { MarkerProviderTiles } from "./MarkerProviderTiles";
import { SettingField } from "./SettingField";

/**
 * MDBList is the only provider on this page whose credential is a server
 * setting; the subtitle providers have their own endpoints. The form is still
 * mounted so the page reads one sensitive-status list for every tile.
 */
const KEYS = ["mdblist.api_key"];

// ---------------------------------------------------------------------------
// Shared tile plumbing
// ---------------------------------------------------------------------------

/** Marks the elapsed time of a request without each caller re-deriving it. */
async function timed<T>(run: () => Promise<T>): Promise<{ result: T; durationMs: number }> {
  const started = Date.now();
  const result = await run();
  return { result, durationMs: Date.now() - started };
}

function DisconnectButton({
  label = "Disconnect",
  title,
  description,
  actionLabel = "Clear",
  onConfirm,
}: {
  label?: string;
  title: string;
  description: string;
  actionLabel?: string;
  onConfirm: () => void;
}) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button type="button" size="sm" variant="outline" onClick={() => setOpen(true)}>
        {label}
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{title}</AlertDialogTitle>
            <AlertDialogDescription>{description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                onConfirm();
                setOpen(false);
              }}
            >
              {actionLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// ---------------------------------------------------------------------------
// Subtitle providers
// ---------------------------------------------------------------------------

interface SubtitleProviderPresentation {
  name: string;
  tagline: string;
  monogram: string;
  monogramClass: string;
}

const SUBTITLE_PROVIDERS: Record<string, SubtitleProviderPresentation> = {
  opensubtitles: {
    name: "OpenSubtitles",
    tagline: "Community subtitles",
    monogram: "OS",
    monogramClass: "bg-sky-500/20 text-sky-700 dark:text-sky-300",
  },
  subdl: {
    name: "SubDL",
    tagline: "API key provider",
    monogram: "SD",
    monogramClass: "bg-orange-500/20 text-orange-700 dark:text-orange-300",
  },
  subsource: {
    name: "SubSource",
    tagline: "API key provider",
    monogram: "SS",
    monogramClass: "bg-violet-500/20 text-violet-700 dark:text-violet-300",
  },
};

function presentationFor(providerName: string): SubtitleProviderPresentation {
  return (
    SUBTITLE_PROVIDERS[providerName] ?? {
      name: providerName,
      tagline: "Subtitle provider",
      monogram: providerName.slice(0, 2),
      monogramClass: "bg-muted text-muted-foreground",
    }
  );
}

function SubtitleProviderTile({
  config,
  scope,
  expanded,
  onExpand,
  onCollapse,
  test,
  onTested,
}: {
  config: SubtitleProviderConfig;
  scope: string;
  expanded: boolean;
  onExpand: () => void;
  onCollapse: () => void;
  test: ProviderTestState | undefined;
  onTested: (test: ProviderTestState | undefined) => void;
}) {
  // `null` means "follow the server"; the switch only pins a value while the
  // admin has an unsaved change, so a refetch can't silently flip it back.
  const [enabledDraft, setEnabledDraft] = useState<boolean | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [apiKey, setApiKey] = useState("");

  const [editor, setEditor] = useState<ProviderEditor | null>(null);
  const [loadingEditor, setLoadingEditor] = useState(false);
  const [saveAttempted, setSaveAttempted] = useState(false);
  const [feedback, setFeedback] = useState("");
  const loadGeneration = useRef(0);
  const updateProvider = useUpdateSubtitleProvider();
  const testProvider = useTestSubtitleProvider();

  const canonical = editor?.body ?? config;
  const enabled = enabledDraft ?? canonical.enabled;
  const providerName = config.provider_name;
  const { name, tagline, monogram, monogramClass } = presentationFor(providerName);
  const usesAccount = providerName === "opensubtitles";
  const configured = usesAccount ? canonical.has_credentials : canonical.has_api_key;

  const draft = usesAccount ? { username, password } : { api_key: apiKey };
  // Tile drafts live outside useSettingsForm, so the navigation guard and
  // the reload prompt only see them if the tile reports them itself.
  useReportUnsavedChanges(
    enabledDraft !== null || username !== "" || password !== "" || apiKey !== "",
  );

  function resetDrafts() {
    setEnabledDraft(null);
    setUsername("");
    setPassword("");
    setApiKey("");
  }

  // A failure describes the values that were tested, so it stops being true the
  // moment they are edited; leaving it up would keep the tile in error while
  // the admin retypes.
  function clearStaleTest() {
    if (test) onTested(undefined);
  }

  function handleUsernameChange(next: string) {
    clearStaleTest();
    setUsername(next);
  }

  function handlePasswordChange(next: string) {
    clearStaleTest();
    setPassword(next);
  }

  function handleApiKeyChange(next: string) {
    clearStaleTest();
    setApiKey(next);
  }

  async function loadEditor() {
    const generation = ++loadGeneration.current;
    setLoadingEditor(true);
    setFeedback("");
    try {
      const intent = captureProviderEditIntent(providerName, scope);
      const loaded = await getProviderEditor(intent);
      if (generation !== loadGeneration.current || !providerIntentActive(intent)) return;
      setEditor(loaded);
      resetDrafts();
      setSaveAttempted(false);
    } catch {
      if (generation === loadGeneration.current) {
        setEditor(null);
        setFeedback("Unable to load saved configuration. Reload before editing.");
      }
    } finally {
      if (generation === loadGeneration.current) setLoadingEditor(false);
    }
  }

  function save(clear: boolean) {
    if (!editor || saveAttempted || loadingEditor || updateProvider.isPending) return;
    setSaveAttempted(true);
    updateProvider.mutate(
      {
        editor,
        config: clear ? { enabled: false, clear_credentials: true } : { enabled, ...draft },
      },
      {
        onSuccess: (saved) => {
          if (!providerIntentActive(editor.intent)) return;
          resetDrafts();
          onTested(undefined);
          setFeedback(providerSaveMessage(saved));
        },
        onError: () => {
          if (!providerIntentActive(editor.intent)) return;
          setFeedback(
            "Save not confirmed. Your draft is retained. Reload saved configuration to review it before making another change.",
          );
        },
      },
    );
  }

  function handleSave() {
    save(false);
  }

  function handleTest() {
    onTested(undefined);
    const started = Date.now();
    testProvider.mutate(
      { provider: providerName, config: { enabled, ...draft } },
      {
        onSuccess: (result) =>
          onTested({
            ok: result.success,
            message: result.success
              ? "Connection successful."
              : (result.error ?? "Connection failed."),
            at: Date.now(),
            durationMs: Date.now() - started,
          }),
        onError: (err) =>
          onTested({
            ok: false,
            message: err instanceof Error ? err.message : "Connection failed.",
            at: Date.now(),
            durationMs: Date.now() - started,
          }),
      },
    );
  }

  function handleClear() {
    save(true);
  }

  const connected = configured && enabled;
  const state = resolveProviderTileState({ expanded, test, connected });
  const statePill = !expanded && configured && !enabled ? "Connected · off" : undefined;
  // Only a failure earns the extra line: "connected" is already in the header.
  const meta = !expanded && test && !test.ok ? test.message : undefined;

  return (
    <ProviderTile
      name={name}
      tagline={tagline}
      monogram={monogram}
      monogramClass={monogramClass}
      state={state}
      statePill={statePill}
      meta={meta}
      busy={updateProvider.isPending || testProvider.isPending}
      expanded={expanded}
      primaryAction={{
        label: test && !test.ok ? "Fix" : configured ? "Manage" : "Connect",
        onClick: () => {
          onExpand();
          if (!editor && !loadingEditor) void loadEditor();
        },
      }}
      headerActions={
        expanded ? (
          <Switch
            disabled={!editor || loadingEditor || saveAttempted || updateProvider.isPending}
            checked={enabled}
            onCheckedChange={setEnabledDraft}
            aria-label={`Enable ${name}`}
          />
        ) : undefined
      }
    >
      {feedback && <p role="status">{feedback}</p>}
      {loadingEditor && <p role="status">Loading saved configuration...</p>}
      <fieldset disabled={!editor || loadingEditor || saveAttempted || updateProvider.isPending}>
        {usesAccount ? (
          <>
            <SettingField
              label="Username"
              value={username}
              onChange={handleUsernameChange}
              description={
                canonical.has_credentials ? "Leave blank to keep the saved username." : undefined
              }
            />
            <SecretField
              label="Password"
              value={password}
              configured={canonical.has_credentials}
              onChange={handlePasswordChange}
            />
          </>
        ) : (
          <SecretField
            label="API key"
            value={apiKey}
            configured={canonical.has_api_key}
            onChange={handleApiKeyChange}
          />
        )}
        <ProviderPanelActions test={test}>
          <Button type="button" size="sm" onClick={handleSave} disabled={updateProvider.isPending}>
            {updateProvider.isPending ? "Saving..." : "Save"}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            onClick={handleTest}
            disabled={testProvider.isPending}
          >
            {testProvider.isPending ? "Testing..." : "Test connection"}
          </Button>
          {configured ? (
            <DisconnectButton
              title={`Clear ${name} credentials?`}
              description={`Save ${name} as disabled and clear its stored credentials. Applying this change on this server is reported separately; other servers may still use older settings.`}
              actionLabel="Clear and turn off"
              onConfirm={handleClear}
            />
          ) : null}
        </ProviderPanelActions>
      </fieldset>
      <Button type="button" size="sm" variant="outline" onClick={onCollapse}>
        Close
      </Button>
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={loadingEditor || updateProvider.isPending}
        onClick={() => void loadEditor()}
      >
        Reload saved configuration (discard draft)
      </Button>
      <p className="text-muted-foreground mt-2 text-xs">Test uses the values typed here.</p>
    </ProviderTile>
  );
}

// ---------------------------------------------------------------------------
// MDBList
// ---------------------------------------------------------------------------

function MDBListTile({
  sensitiveConfigured,
  restartKeys,
  expanded,
  onExpand,
  onCollapse,
  test,
  onTested,
}: {
  sensitiveConfigured: string[];
  restartKeys: RestartKeyMatcher;
  expanded: boolean;
  onExpand: () => void;
  onCollapse: () => void;
  test: ProviderTestState | undefined;
  onTested: (test: ProviderTestState | undefined) => void;
}) {
  const updateSettings = useUpdateServerSettings();
  const checkConnection = useCheckAdminSettingsConnection();
  const [apiKey, setApiKey] = useState("");
  const [testing, setTesting] = useState(false);

  const configured = sensitiveConfigured.includes("mdblist.api_key");
  const hasDraft = apiKey.trim() !== "";
  useReportUnsavedChanges(hasDraft);

  async function save() {
    if (!hasDraft) {
      toast.info("Nothing to save for MDBList.");
      return;
    }
    try {
      await updateSettings.mutateAsync({ "mdblist.api_key": apiKey });
      setApiKey("");
      onTested(undefined);
      toast.success("MDBList credentials saved");
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function clearAll() {
    try {
      await updateSettings.mutateAsync({ "mdblist.api_key": "" });
      setApiKey("");
      onTested(undefined);
      toast.success("MDBList credentials cleared");
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function runTest() {
    setTesting(true);
    try {
      const { result, durationMs } = await timed(() =>
        checkConnection.mutateAsync({
          kind: "mdblist",
          body: {
            values: { "mdblist.api_key": apiKey },
            dirty_keys: hasDraft ? ["mdblist.api_key"] : [],
          },
        }),
      );
      onTested({ ok: result.success, message: result.message, at: Date.now(), durationMs });
    } catch (error) {
      onTested({
        ok: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
        at: Date.now(),
        durationMs: 0,
      });
    } finally {
      setTesting(false);
    }
  }

  const state = resolveProviderTileState({ expanded, test, connected: configured });
  const meta = !expanded && test && !test.ok ? test.message : undefined;

  return (
    <ProviderTile
      name="MDBList"
      tagline="Ratings and lists"
      monogram="MD"
      monogramClass="bg-emerald-500/20 text-emerald-700 dark:text-emerald-300"
      state={state}
      meta={meta}
      busy={updateSettings.isPending || testing}
      expanded={expanded}
      // A pending restart has to be visible without opening the tile.
      badge={restartKeys.has("mdblist.api_key") ? <RestartBadge /> : undefined}
      primaryAction={{
        label: test && !test.ok ? "Fix" : configured ? "Manage" : "Connect",
        onClick: onExpand,
      }}
    >
      <p className="text-muted-foreground mb-1 text-xs">
        A key adds search and browse; list URLs import without one. Get a key at{" "}
        <a
          href="https://mdblist.com/preferences/#api"
          target="_blank"
          rel="noreferrer"
          className="underline"
        >
          mdblist.com/preferences
        </a>
        .
      </p>
      <SecretField
        label="API key"
        value={apiKey}
        configured={configured}
        onChange={(next) => {
          // The failure describes the key that was tested, not the new one.
          if (test) onTested(undefined);
          setApiKey(next);
        }}
        restartRequired={restartKeys.has("mdblist.api_key")}
      />
      <ProviderPanelActions test={test}>
        <Button
          type="button"
          size="sm"
          onClick={() => void save()}
          disabled={updateSettings.isPending}
        >
          {updateSettings.isPending ? "Saving..." : "Save"}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          onClick={() => void runTest()}
          disabled={testing || (!configured && !hasDraft)}
        >
          {testing ? "Testing..." : "Test connection"}
        </Button>
        {configured ? (
          <DisconnectButton
            title="Clear MDBList credentials?"
            description="MDBList search and browse stop working. Lists already imported by URL are unaffected."
            onConfirm={() => void clearAll()}
          />
        ) : null}
        <Button type="button" size="sm" variant="outline" onClick={onCollapse}>
          Close
        </Button>
      </ProviderPanelActions>
      <p className="text-muted-foreground mt-2 text-xs">
        Test uses the key typed here, or the saved one.
      </p>
    </ProviderTile>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function ProvidersSettings() {
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const { data, isLoading, scope } = useSubtitleProviders();
  // One expanded tile at a time: the panel is the page's focus while it is
  // open, and two open panels lose the list the admin is working through.
  const [expandedTile, setExpandedTile] = useState<string | null>(null);
  const [tests, setTests] = useState<Record<string, ProviderTestState | undefined>>({});

  function recordTest(id: string, test: ProviderTestState | undefined) {
    setTests((current) => ({ ...current, [id]: test }));
  }

  // Marker tiles can be perfectly set up and still never run: the detection
  // mode on Library & Metadata decides whether Silo looks online at all. The
  // key is read, not staged — this page never saves it.
  const markerMode = form.getValue("markers.mode") || "both";
  const onlineMarkerLookupEnabled = markerMode === "online" || markerMode === "both";

  const providers = sortSubtitleProviders(data?.providers ?? []);

  if (form.isLoading || isLoading) {
    return (
      <div className="max-w-5xl space-y-6" role="status" aria-label="Loading providers">
        <Skeleton className="h-9 w-64" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full" />
        <span className="sr-only">Loading providers</span>
      </div>
    );
  }

  return (
    <div className="flex h-full max-w-5xl flex-col gap-7">
      <SettingsPageHeader title="Subtitles & Metadata" />

      <FieldGroup label="Subtitle providers">
        <div className="py-3.5">
          {providers.length === 0 ? (
            <p className="text-muted-foreground text-sm">No subtitle providers are available.</p>
          ) : (
            <ProviderTileGrid>
              {providers.map((provider) => (
                <SubtitleProviderTile
                  scope={scope}
                  key={`${scope}:${provider.provider_name}`}
                  config={provider}
                  expanded={expandedTile === provider.provider_name}
                  onExpand={() => setExpandedTile(provider.provider_name)}
                  onCollapse={() => setExpandedTile(null)}
                  test={tests[provider.provider_name]}
                  onTested={(test) => recordTest(provider.provider_name, test)}
                />
              ))}
            </ProviderTileGrid>
          )}
        </div>
      </FieldGroup>

      <FieldGroup label="Metadata providers">
        <div className="space-y-3 py-3.5">
          <ProviderTileGrid>
            <MDBListTile
              sensitiveConfigured={form.sensitiveConfigured}
              restartKeys={restartKeys}
              expanded={expandedTile === "mdblist"}
              onExpand={() => setExpandedTile("mdblist")}
              onCollapse={() => setExpandedTile(null)}
              test={tests.mdblist}
              onTested={(test) => recordTest("mdblist", test)}
            />
          </ProviderTileGrid>
          <p className="text-muted-foreground text-xs">
            TMDB and TheTVDB connect from{" "}
            <Link to="/admin/plugins" className="underline underline-offset-2">
              Plugins
            </Link>
            .
          </p>
        </div>
      </FieldGroup>

      {/*
        Marker providers are the online half of "Marker source". The
        detection mode itself stays on Library & Metadata; what each provider
        does — lookup order, whether this server contributes back — is provider
        configuration and belongs beside the other providers.
      */}
      <FieldGroup label="Marker providers">
        <div className="space-y-3 py-3.5">
          <MarkerProviderTiles
            expandedTile={expandedTile}
            onExpand={setExpandedTile}
            onCollapse={() => setExpandedTile(null)}
          />
          <p className="text-muted-foreground text-xs">
            Online marker lookup is {onlineMarkerLookupEnabled ? "enabled" : "disabled"}. Change
            this under{" "}
            <Link
              to="/admin/settings/library"
              className="hover:text-foreground font-medium underline underline-offset-2 transition-colors"
            >
              Marker source
            </Link>
            .
          </p>
        </div>
      </FieldGroup>
    </div>
  );
}
