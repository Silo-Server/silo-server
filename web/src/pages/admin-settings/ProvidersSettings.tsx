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
import { useT, type MessageKey } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { MarkerProviderTiles } from "./MarkerProviderTiles";
import { SettingField } from "./SettingField";
import { WatchProviderTiles } from "./WatchProviderTiles";

/**
 * MDBList is the only provider on this page whose credential is a server
 * setting; the subtitle and marker providers have their own endpoints, and
 * watch providers are configured on their plugin pages. Every tile saves
 * itself, so the page has no save bar: the form only reports which
 * secrets are configured and reads the marker detection mode.
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
  label,
  title,
  description,
  actionLabel,
  onConfirm,
}: {
  label?: string;
  title: string;
  description: string;
  actionLabel?: string;
  onConfirm: () => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button type="button" size="sm" variant="outline" onClick={() => setOpen(true)}>
        {label ?? t("web.admin.settings.providers.disconnect")}
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{title}</AlertDialogTitle>
            <AlertDialogDescription>{description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                onConfirm();
                setOpen(false);
              }}
            >
              {actionLabel ?? t("web.admin.settings.providers.clear")}
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
  tagline: MessageKey;
  monogram: string;
  monogramClass: string;
}

const SUBTITLE_PROVIDERS: Record<string, SubtitleProviderPresentation> = {
  opensubtitles: {
    name: "OpenSubtitles",
    tagline: "web.admin.settings.providers.subtitles.taglineCommunity",
    monogram: "OS",
    monogramClass: "bg-sky-500/20 text-sky-700 dark:text-sky-300",
  },
  subdl: {
    name: "SubDL",
    tagline: "web.admin.settings.providers.subtitles.taglineApiKey",
    monogram: "SD",
    monogramClass: "bg-orange-500/20 text-orange-700 dark:text-orange-300",
  },
  subsource: {
    name: "SubSource",
    tagline: "web.admin.settings.providers.subtitles.taglineApiKey",
    monogram: "SS",
    monogramClass: "bg-violet-500/20 text-violet-700 dark:text-violet-300",
  },
};

function presentationFor(providerName: string): SubtitleProviderPresentation {
  return (
    SUBTITLE_PROVIDERS[providerName] ?? {
      name: providerName,
      tagline: "web.admin.settings.providers.subtitles.taglineGeneric",
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
  const t = useT();
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
  const providerName = config.provider_name;
  const { name, tagline, monogram, monogramClass } = presentationFor(providerName);
  const usesAccount = providerName === "opensubtitles";
  const configured = usesAccount ? canonical.has_credentials : canonical.has_api_key;
  const credentialTyped = usesAccount
    ? username.trim() !== "" && password !== ""
    : apiKey.trim() !== "";
  // Typing the first credentials into a provider that has none is connecting
  // it, so the switch follows unless the admin set it themselves; otherwise
  // the save lands as "Connected · off" and nothing downloads.
  const enabled = enabledDraft ?? (!configured && credentialTyped ? true : canonical.enabled);

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
        setFeedback(t("web.admin.settings.providers.subtitles.loadFailed"));
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
          setFeedback(t("web.admin.settings.providers.subtitles.saveNotConfirmed"));
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
              ? t("web.admin.settings.providers.subtitles.testSucceeded")
              : (result.error ?? t("web.admin.settings.providers.subtitles.testFailed")),
            at: Date.now(),
            durationMs: Date.now() - started,
          }),
        onError: (err) =>
          onTested({
            ok: false,
            message:
              err instanceof Error
                ? err.message
                : t("web.admin.settings.providers.subtitles.testFailed"),
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
  const statePill =
    !expanded && configured && !enabled
      ? t("web.admin.settings.providers.subtitles.connectedOff")
      : undefined;
  // Only a failure earns the extra line: "connected" is already in the header.
  const meta = !expanded && test && !test.ok ? test.message : undefined;

  return (
    <ProviderTile
      name={name}
      tagline={t(tagline)}
      monogram={monogram}
      monogramClass={monogramClass}
      state={state}
      statePill={statePill}
      meta={meta}
      busy={updateProvider.isPending || testProvider.isPending}
      expanded={expanded}
      primaryAction={{
        label:
          test && !test.ok
            ? t("web.admin.settings.providers.subtitles.fix")
            : configured
              ? t("web.admin.settings.providers.subtitles.manage")
              : t("web.admin.settings.providers.subtitles.connect"),
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
            aria-label={t("web.admin.settings.providers.subtitles.enable", { name })}
          />
        ) : undefined
      }
    >
      {feedback && <p role="status">{feedback}</p>}
      {loadingEditor && (
        <p role="status">{t("web.admin.settings.providers.subtitles.loadingEditor")}</p>
      )}
      <fieldset disabled={!editor || loadingEditor || saveAttempted || updateProvider.isPending}>
        {usesAccount ? (
          <>
            <SettingField
              label={t("web.admin.settings.providers.subtitles.username")}
              value={username}
              onChange={handleUsernameChange}
              description={
                canonical.has_credentials
                  ? t("web.admin.settings.providers.subtitles.usernameKeep")
                  : undefined
              }
            />
            <SecretField
              label={t("web.admin.settings.providers.subtitles.password")}
              value={password}
              configured={canonical.has_credentials}
              onChange={handlePasswordChange}
            />
          </>
        ) : (
          <SecretField
            label={t("web.admin.settings.providers.subtitles.apiKey")}
            value={apiKey}
            configured={canonical.has_api_key}
            onChange={handleApiKeyChange}
          />
        )}
        <ProviderPanelActions test={test}>
          <Button type="button" size="sm" onClick={handleSave} disabled={updateProvider.isPending}>
            {updateProvider.isPending
              ? t("web.admin.settings.providers.subtitles.saving")
              : t("common.save")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            onClick={handleTest}
            disabled={testProvider.isPending}
          >
            {testProvider.isPending
              ? t("web.admin.settings.providers.subtitles.testing")
              : t("web.admin.settings.providers.subtitles.testConnection")}
          </Button>
          {configured ? (
            <DisconnectButton
              title={t("web.admin.settings.providers.subtitles.clearTitle", { name })}
              description={t("web.admin.settings.providers.subtitles.clearDescription", { name })}
              actionLabel={t("web.admin.settings.providers.subtitles.clearAction")}
              onConfirm={handleClear}
            />
          ) : null}
        </ProviderPanelActions>
      </fieldset>
      <Button type="button" size="sm" variant="outline" onClick={onCollapse}>
        {t("web.admin.settings.providers.subtitles.close")}
      </Button>
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={loadingEditor || updateProvider.isPending}
        onClick={() => void loadEditor()}
      >
        {t("web.admin.settings.providers.subtitles.reload")}
      </Button>
      <p className="text-muted-foreground mt-2 text-xs">
        {t("web.admin.settings.providers.subtitles.testNote")}
      </p>
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
  const t = useT();
  const updateSettings = useUpdateServerSettings();
  const checkConnection = useCheckAdminSettingsConnection();
  const [apiKey, setApiKey] = useState("");
  const [testing, setTesting] = useState(false);

  const configured = sensitiveConfigured.includes("mdblist.api_key");
  const hasDraft = apiKey.trim() !== "";
  useReportUnsavedChanges(hasDraft);

  async function save() {
    if (!hasDraft) {
      toast.info(t("web.admin.settings.providers.mdblist.nothingToSave"));
      return;
    }
    try {
      await updateSettings.mutateAsync({ "mdblist.api_key": apiKey });
      setApiKey("");
      onTested(undefined);
      toast.success(t("web.admin.settings.providers.mdblist.saved"));
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function clearAll() {
    try {
      await updateSettings.mutateAsync({ "mdblist.api_key": "" });
      setApiKey("");
      onTested(undefined);
      toast.success(t("web.admin.settings.providers.mdblist.cleared"));
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
        message:
          error instanceof Error
            ? error.message
            : t("web.admin.settings.providers.mdblist.testFailed"),
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
      tagline={t("web.admin.settings.providers.mdblist.tagline")}
      monogram="MD"
      monogramClass="bg-emerald-500/20 text-emerald-700 dark:text-emerald-300"
      state={state}
      meta={meta}
      busy={updateSettings.isPending || testing}
      expanded={expanded}
      // A pending restart has to be visible without opening the tile.
      badge={restartKeys.has("mdblist.api_key") ? <RestartBadge /> : undefined}
      primaryAction={{
        label:
          test && !test.ok
            ? t("web.admin.settings.providers.mdblist.fix")
            : configured
              ? t("web.admin.settings.providers.mdblist.manage")
              : t("web.admin.settings.providers.mdblist.connect"),
        onClick: onExpand,
      }}
    >
      <p className="text-muted-foreground mb-1 text-xs">
        {t("web.admin.settings.providers.mdblist.intro", {
          link: (chunks) => (
            <a
              href="https://mdblist.com/preferences/#api"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              {chunks}
            </a>
          ),
        })}
      </p>
      <SecretField
        label={t("web.admin.settings.providers.mdblist.apiKey")}
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
          {updateSettings.isPending
            ? t("web.admin.settings.providers.mdblist.saving")
            : t("common.save")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          onClick={() => void runTest()}
          disabled={testing || (!configured && !hasDraft)}
        >
          {testing
            ? t("web.admin.settings.providers.mdblist.testing")
            : t("web.admin.settings.providers.mdblist.testConnection")}
        </Button>
        {configured ? (
          <DisconnectButton
            title={t("web.admin.settings.providers.mdblist.clearTitle")}
            description={t("web.admin.settings.providers.mdblist.clearDescription")}
            onConfirm={() => void clearAll()}
          />
        ) : null}
        <Button type="button" size="sm" variant="outline" onClick={onCollapse}>
          {t("web.admin.settings.providers.mdblist.close")}
        </Button>
      </ProviderPanelActions>
      <p className="text-muted-foreground mt-2 text-xs">
        {t("web.admin.settings.providers.mdblist.testNote")}
      </p>
    </ProviderTile>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function ProvidersSettings() {
  const t = useT();
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
  // mode on Playback decides whether Silo looks online at all. The key is
  // read, not staged — this page never saves it.
  const markerMode = form.getValue("markers.mode") || "both";
  const onlineMarkerLookupEnabled = markerMode === "online" || markerMode === "both";

  const providers = sortSubtitleProviders(data?.providers ?? []);

  if (form.isLoading || isLoading) {
    return (
      <div
        className="max-w-5xl space-y-6"
        role="status"
        aria-label={t("web.admin.settings.providers.loading")}
      >
        <Skeleton className="h-9 w-64" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full" />
        <span className="sr-only">{t("web.admin.settings.providers.loading")}</span>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <SettingsPageHeader title={t("web.admin.settings.providers.title")} />

      <FieldGroup label={t("web.admin.settings.providers.watchProvidersGroup")}>
        <div className="py-3.5">
          <WatchProviderTiles />
        </div>
      </FieldGroup>

      {/*
        The server's MDBList key only powers list search and browse in
        collection imports; MDBList ratings come from its metadata plugin,
        configured under Plugins like TMDB and TheTVDB.
      */}
      <FieldGroup label={t("web.admin.settings.providers.listsGroup")}>
        <div className="py-3.5">
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
        </div>
      </FieldGroup>

      <FieldGroup label={t("web.admin.settings.providers.subtitlesGroup")}>
        <div className="py-3.5">
          {providers.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("web.admin.settings.providers.subtitlesEmpty")}
            </p>
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

      {/*
        Marker providers are the online half of "Marker source". The
        detection mode itself stays on Playback; what each provider does —
        lookup order, whether this server contributes back — is provider
        configuration and belongs beside the other providers.
      */}
      <FieldGroup label={t("web.admin.settings.providers.markersGroup")}>
        <div className="space-y-3 py-3.5">
          <MarkerProviderTiles
            expandedTile={expandedTile}
            onExpand={setExpandedTile}
            onCollapse={() => setExpandedTile(null)}
          />
          <p className="text-muted-foreground text-xs">
            {t(
              onlineMarkerLookupEnabled
                ? "web.admin.settings.providers.markerLookupEnabled"
                : "web.admin.settings.providers.markerLookupDisabled",
              {
                link: (chunks) => (
                  <Link
                    to="/admin/settings/playback"
                    className="hover:text-foreground font-medium underline underline-offset-2 transition-colors"
                  >
                    {chunks}
                  </Link>
                ),
              },
            )}
          </p>
        </div>
      </FieldGroup>

      {/* An admin looking for metadata providers on a page called Providers
          lands here, so say where they are. */}
      <p className="text-muted-foreground text-xs">
        {t("web.admin.settings.providers.metadataNote", {
          link: (chunks) => (
            <Link to="/admin/plugins" className="underline underline-offset-2">
              {chunks}
            </Link>
          ),
        })}
      </p>
    </div>
  );
}
