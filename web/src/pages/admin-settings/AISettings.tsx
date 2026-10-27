import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { AudioLines, Languages, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { LimitField } from "@/components/settings/LimitField";
import { ProviderTile, ProviderTileGrid } from "@/components/settings/ProviderTile";
import type { ProviderTileState } from "@/components/settings/ProviderTile";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import {
  useAIProviders,
  useRefreshAIProviders,
  type AIProvider,
} from "@/hooks/queries/admin/aiProviders";
import { useDebounce } from "@/hooks/useDebounce";
import { useRestartKeys, type RestartKeyMatcher } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { QUOTA_PERIODS, QUOTA_PERIOD_WINDOW_LABELS } from "@/lib/quotaPeriods";
import { cn } from "@/lib/utils";
import { SaveBar } from "@/components/SaveBar";

import { providerOptions } from "@/lib/aiProviders";
import { AIProvidersList, ModelField } from "./AIProviders";
import { FieldGroup } from "./FieldGroup";
import { SettingField, SettingFieldStatus } from "./SettingField";
import {
  useAdminAICapabilities,
  useChatGPTStatus,
  useChatGPTModels,
} from "@/hooks/queries/admin/chatgpt";
import { ChatGPTConnection } from "./ChatGPTConnection";

// ---------------------------------------------------------------------------
// Setting keys
// ---------------------------------------------------------------------------

const TEXT_AI_KEYS = [
  "ai.text_provider_id",
  "ai.chat_model",
  "ai.auth_mode",
  "ai.reasoning_effort",
] as const;
const SPEECH_AI_KEYS = ["ai.speech_provider_id", "ai.asr_model"] as const;
/** Shown here, chosen on Recommendations next to the embedding lock. */
const EMBEDDING_KEYS = ["recommendations.embedding_provider_id", "recommendations.embedding_model"];
/**
 * Pre-`ai.*` keys still read as a fallback, so a server configured before the
 * rename keeps its model and job limit until the modern key is saved.
 */
const LEGACY_AI_KEYS = ["subtitle_ai.chat_model", "subtitle_ai.max_concurrent_jobs"] as const;

const AI_FEATURE_KEYS = [
  "subtitle_ai.enabled",
  "subtitle_ai.transcribe_enabled",
  "metadata_ai.enabled",
  "metadata_ai.on_view",
];

const AI_ADVANCED_KEYS = [
  "ai.max_concurrent_jobs",
  "subtitle_ai.batch_size",
  "subtitle_ai.context_neighbors",
  "subtitle_ai.asr_chunk_seconds",
  "subtitle_ai.transcribe_quota_jobs",
  "subtitle_ai.transcribe_quota_period",
];

const KEYS: string[] = Array.from(
  new Set([
    ...TEXT_AI_KEYS,
    ...SPEECH_AI_KEYS,
    ...EMBEDDING_KEYS,
    ...LEGACY_AI_KEYS,
    ...AI_FEATURE_KEYS,
    ...AI_ADVANCED_KEYS,
  ]),
);

/** The text provider select's value for the ChatGPT plan. */
const CHATGPT_PROVIDER = "chatgpt";
/**
 * A provider select's value for no provider. Choosing it releases the
 * provider, so it can be removed once no job uses it.
 */
const NO_PROVIDER = "none";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function parseStrictInteger(rawValue: string): number | null {
  const trimmed = rawValue.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

/** Result of the last connection check, kept in memory for the tile and strip. */
interface AITestState {
  ok: boolean;
  message: string;
  at: number;
  durationMs: number;
}

function testedLabel(test: AITestState): string {
  const seconds = Math.max(0, Math.round((Date.now() - test.at) / 1000));
  const ago = seconds < 60 ? `${seconds}s ago` : `${Math.round(seconds / 60)}m ago`;
  return `Tested ${ago} · ${test.durationMs} ms`;
}

/**
 * The action row shared by both model tiles. Both controls carry a border or a
 * fill at rest: a `ghost` button reads as plain text until it is hovered, which
 * hid Close from admins who never hovered it.
 */
function ModelPanelActions({
  testLabel,
  pendingLabel,
  onTest,
  isTesting,
  testDisabled,
  onCollapse,
  canCollapse,
  test,
}: {
  testLabel: string;
  pendingLabel: string;
  onTest: () => void;
  isTesting: boolean;
  testDisabled: boolean;
  onCollapse: () => void;
  /**
   * False while a staged edit holds the tile open. Collapsing then does
   * nothing, so the button is left out rather than shown as a dead control.
   */
  canCollapse: boolean;
  test: AITestState | undefined;
}) {
  return (
    // Buttons right-aligned to match the collapsed tile's Manage button (and
    // the shared ProviderPanelActions); the test status takes the left side.
    <div className="mt-3.5 flex flex-wrap items-center justify-end gap-2">
      {test ? (
        <span
          role="status"
          aria-live="polite"
          className={cn(
            "mr-auto text-xs",
            test.ok ? "text-muted-foreground" : "text-amber-600 dark:text-amber-400",
          )}
        >
          {test.ok ? `${test.message} · ${testedLabel(test)}` : test.message}
        </span>
      ) : null}
      <Button
        type="button"
        size="sm"
        variant="secondary"
        onClick={onTest}
        disabled={testDisabled || isTesting}
      >
        {isTesting ? pendingLabel : testLabel}
      </Button>
      {canCollapse ? (
        <Button type="button" size="sm" variant="outline" onClick={onCollapse}>
          Close
        </Button>
      ) : null}
    </div>
  );
}

/**
 * A labelled cluster inside the Advanced disclosure. The tuning fields mix two
 * scopes — server-wide dispatch/batching and a per-login-account quota — and
 * nothing on the row itself says which is which, so the scope is stated once
 * per cluster instead of being repeated (or omitted) field by field.
 *
 * One element per cluster also means the disclosure's child rule draws a single
 * hairline between the two, with the rows keeping their own inside each.
 */
function TuningScope({
  label,
  caption,
  children,
}: {
  label: string;
  caption: string;
  children: ReactNode;
}) {
  return (
    <div>
      <SettingsSubheading caption={caption}>{label}</SettingsSubheading>
      {children}
    </div>
  );
}

/** Note shown on a model tile whose values are staged in the page's save bar. */
function PendingSaveNote({ dirty }: { dirty: boolean }) {
  if (!dirty) return null;
  return (
    <p className="text-muted-foreground mt-2 text-xs">Unsaved. Test uses what is typed here.</p>
  );
}

// ---------------------------------------------------------------------------
// Model tiles
// ---------------------------------------------------------------------------

function TextModelTile({
  authMode,
  providerID,
  providers,
  reasoningEffort,
  reasoningLevels,
  reasoningAttention,
  chatGPTSupported,
  chatModel,
  ready,
  canTest,
  dirty,
  restartKeys,
  onChange,
  onProviderChange,
  onConnectionConfirmed,
  onSettingsTransitionStarted,
  onTest,
  isTesting,
  test,
  expanded,
  onExpand,
  onCollapse,
}: {
  authMode: string;
  providerID: string;
  providers: AIProvider[];
  reasoningEffort: string;
  reasoningLevels: string[];
  /**
   * The reasoning level has a staged edit, or one the selected model cannot
   * use; either holds the tile's Advanced section open.
   */
  reasoningAttention: boolean;
  chatGPTSupported: boolean;
  chatModel: string;
  ready: boolean;
  canTest: boolean;
  dirty: boolean;
  restartKeys: RestartKeyMatcher;
  onChange: (key: string, value: string) => void;
  onProviderChange: (value: string) => void;
  onConnectionConfirmed: () => void;
  onSettingsTransitionStarted: ReturnType<typeof useSettingsForm>["beginSettingsTransition"];
  onTest: () => void;
  isTesting: boolean;
  test: AITestState | undefined;
  expanded: boolean;
  onExpand: () => void;
  onCollapse: () => void;
}) {
  const failed = test != null && !test.ok;
  const state: ProviderTileState = expanded
    ? "editing"
    : failed
      ? "error"
      : ready
        ? "connected"
        : "not_connected";
  const chatGPT = authMode === CHATGPT_PROVIDER;
  const provider = providers.find((candidate) => candidate.id === providerID);
  const selected = chatGPT ? CHATGPT_PROVIDER : providerID;

  return (
    <ProviderTile
      name="Text model"
      tagline="Subtitle text, descriptions, and taglines"
      logo={<Languages className="text-muted-foreground size-4" aria-hidden="true" />}
      state={state}
      statePill={!expanded && ready && !test?.ok ? "Configured" : undefined}
      meta={
        expanded
          ? undefined
          : failed
            ? test.message
            : ready
              ? `${chatModel} · ${chatGPT ? "ChatGPT plan" : (provider?.name ?? "")}`
              : chatGPT
                ? "Connect an account and choose a model"
                : "Choose a provider and a model"
      }
      expanded={expanded}
      primaryAction={{ label: ready ? "Manage" : "Set up", onClick: onExpand }}
    >
      <SettingField
        label="Provider"
        type="select"
        value={selected || NO_PROVIDER}
        onChange={onProviderChange}
        description={
          providers.some((candidate) => candidate.capabilities.includes("chat"))
            ? undefined
            : "Add a provider that serves chat under Providers, or use a ChatGPT plan."
        }
        options={[
          { value: NO_PROVIDER, label: "None" },
          ...(chatGPTSupported || chatGPT
            ? [{ value: CHATGPT_PROVIDER, label: "ChatGPT plan", disabled: !chatGPTSupported }]
            : []),
          ...providerOptions(providers, "chat", providerID),
        ]}
      />
      {chatGPT ? (
        <ChatGPTConnection
          model={chatModel}
          onModelChange={(next) => onChange("ai.chat_model", next)}
          onConnectionConfirmed={onConnectionConfirmed}
          onSettingsTransitionStarted={onSettingsTransitionStarted}
        />
      ) : (
        <ModelField
          label="Model"
          providerID={provider ? providerID : ""}
          kind="chat"
          value={chatModel}
          onChange={(next) => onChange("ai.chat_model", next)}
          placeholder="gpt-4o-mini, gemini-2.5-flash, llama3.1"
        />
      )}
      <AdvancedSection id="ai.text-model" count={1} forceOpen={reasoningAttention}>
        <SettingField
          label="Reasoning level"
          type="select"
          value={reasoningEffort || "default"}
          description="Higher effort can improve complex translations and use more time and tokens."
          onChange={(next) => onChange("ai.reasoning_effort", next === "default" ? "" : next)}
          restartRequired={restartKeys.has("ai.reasoning_effort")}
          options={[
            { value: "default", label: "Provider default" },
            ...reasoningLevels.map((level) => ({
              value: level,
              label:
                level === "none"
                  ? "None"
                  : level === "xhigh"
                    ? "Extra high"
                    : level.charAt(0).toUpperCase() + level.slice(1),
            })),
            ...(reasoningEffort && !reasoningLevels.includes(reasoningEffort)
              ? [
                  {
                    value: reasoningEffort,
                    label: `${reasoningEffort} (unsupported by this model)`,
                    disabled: true,
                  },
                ]
              : []),
          ]}
        />
      </AdvancedSection>
      <ModelPanelActions
        testLabel="Test text model"
        pendingLabel="Testing text model..."
        onTest={onTest}
        isTesting={isTesting}
        testDisabled={!canTest}
        onCollapse={onCollapse}
        canCollapse={!dirty}
        test={test}
      />
      <PendingSaveNote dirty={dirty} />
    </ProviderTile>
  );
}

function SpeechModelTile({
  providerID,
  providers,
  asrModel,
  ready,
  dirty,
  onChange,
  onTest,
  isTesting,
  test,
  expanded,
  onExpand,
  onCollapse,
}: {
  providerID: string;
  providers: AIProvider[];
  asrModel: string;
  ready: boolean;
  dirty: boolean;
  onChange: (key: string, value: string) => void;
  onTest: () => void;
  isTesting: boolean;
  test: AITestState | undefined;
  expanded: boolean;
  onExpand: () => void;
  onCollapse: () => void;
}) {
  const failed = test != null && !test.ok;
  const provider = providers.find((candidate) => candidate.id === providerID);
  const state: ProviderTileState = expanded
    ? "editing"
    : failed
      ? "error"
      : ready
        ? "connected"
        : "not_connected";

  return (
    <ProviderTile
      name="Speech-to-text"
      tagline="Writes subtitles from an audio track"
      logo={<AudioLines className="text-muted-foreground size-4" aria-hidden="true" />}
      state={state}
      statePill={!expanded && ready && !test?.ok ? "Configured" : undefined}
      meta={
        expanded
          ? undefined
          : failed
            ? test.message
            : ready
              ? `${asrModel} · ${provider?.name ?? ""}`
              : "Choose a provider and a model"
      }
      expanded={expanded}
      primaryAction={{ label: ready ? "Manage" : "Set up", onClick: onExpand }}
    >
      <SettingField
        label="Provider"
        type="select"
        value={providerID || NO_PROVIDER}
        onChange={(next) => onChange("ai.speech_provider_id", next === NO_PROVIDER ? "" : next)}
        description={
          providers.some((candidate) => candidate.capabilities.includes("speech"))
            ? "A Whisper-compatible endpoint that returns timestamps."
            : "Add a provider that serves speech under Providers. ChatGPT plans do not include it."
        }
        options={[
          { value: NO_PROVIDER, label: "None" },
          ...providerOptions(providers, "speech", providerID),
        ]}
      />
      <ModelField
        label="Model"
        providerID={provider ? providerID : ""}
        kind="speech"
        value={asrModel}
        onChange={(next) => onChange("ai.asr_model", next)}
        placeholder="whisper-large-v3-turbo or whisper-1"
      />
      <ModelPanelActions
        testLabel="Test speech-to-text"
        pendingLabel="Testing speech-to-text..."
        onTest={onTest}
        isTesting={isTesting}
        testDisabled={!ready}
        onCollapse={onCollapse}
        canCollapse={!dirty}
        test={test}
      />
      <PendingSaveNote dirty={dirty} />
    </ProviderTile>
  );
}

/**
 * Recommendation embeddings as a read-only row. Changing the provider or model
 * can require resetting every stored embedding, so the choice is made on
 * Recommendations, beside the embedding lock and the reset.
 */
function EmbeddingsSummaryRow({ provider, model }: { provider?: AIProvider; model: string }) {
  const ready = provider !== undefined && model.trim() !== "";
  return (
    <ProviderTile
      name="Embeddings"
      tagline="Recommendations and semantic search"
      logo={<Sparkles className="text-muted-foreground size-4" aria-hidden="true" />}
      state={ready ? "connected" : "not_connected"}
      statePill={ready ? "Configured" : undefined}
      meta={
        ready ? `${model} · ${provider.name}` : "Choose a provider and a model on Recommendations"
      }
      headerActions={
        <Button asChild type="button" size="sm" variant="outline">
          <Link to="/admin/recommendations">Manage</Link>
        </Button>
      }
    />
  );
}

/**
 * The ChatGPT plan as a provider row. Its sign-in lives with the text model,
 * the only job a plan can serve, so Manage opens that tile.
 */
function ChatGPTProviderRow({
  email,
  inUse,
  onManage,
}: {
  email: string | undefined;
  inUse: boolean;
  onManage: () => void;
}) {
  let statePill: string | undefined;
  if (inUse) statePill = email ? "In use" : "Sign-in needed";
  else if (email) statePill = "Idle";
  return (
    <ProviderTile
      name="ChatGPT plan"
      monogram="GP"
      monogramClass="bg-emerald-500/10 text-emerald-700 dark:text-emerald-200"
      tagline={email ? `Signed in as ${email}` : "Sign in with a ChatGPT account"}
      meta={
        inUse
          ? "Used by Text model. Plans serve chat only, not speech or embeddings."
          : "Plans serve chat only, not speech or embeddings."
      }
      state={inUse && email ? "connected" : "not_connected"}
      statePill={statePill}
      primaryAction={{ label: email ? "Manage" : "Connect", onClick: onManage }}
    />
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function AISettings() {
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const textCheck = useCheckAdminSettingsConnection();
  const speechCheck = useCheckAdminSettingsConnection();
  const [textResult, setTextResult] = useState<AITestState | undefined>(undefined);
  const [speechResult, setSpeechResult] = useState<AITestState | undefined>(undefined);
  const [expandedTile, setExpandedTile] = useState<string | null>(null);

  const authMode = form.getValue("ai.auth_mode") || "api_key";
  const configuredChatModel =
    form.getValue("ai.chat_model") || form.getValue("subtitle_ai.chat_model");
  const currentModel = configuredChatModel || "gpt-4o-mini";
  const capabilityModel = useDebounce(currentModel, 300);
  const capabilities = useAdminAICapabilities(capabilityModel);
  const chatGPTSupported = capabilities.data?.chatgpt_sign_in === true;
  const providerList = useAIProviders();
  const refreshProviders = useRefreshAIProviders();
  const connection = useChatGPTStatus(false, authMode === "chatgpt" || chatGPTSupported);
  const authenticatedAccount = connection.data?.accounts.find(
    (account) =>
      account.client_id === connection.data?.active_client_id &&
      !account.plan_ineligible &&
      !account.needs_login &&
      (account.connected || account.usage_limited === true),
  );
  const chatGPTModels = useChatGPTModels(
    authenticatedAccount?.client_id ?? "",
    authMode === "chatgpt" && !!authenticatedAccount,
  );
  if (form.isLoading || providerList.isLoading) {
    return (
      <div className="max-w-5xl space-y-6" role="status" aria-label="Loading AI Services settings">
        <Skeleton className="h-9 w-48" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-64 w-full" />
        <span className="sr-only">Loading AI Services settings</span>
      </div>
    );
  }

  const value = (key: string, fallback = "") => form.getValue(key) || fallback;
  // Legacy `subtitle_ai.*` values stay authoritative until the modern `ai.*`
  // key holds something, exactly as the old AI Services tab read them.
  const effectiveValue = (key: string, legacyKey: string, fallback: string) =>
    value(key, value(legacyKey, fallback));

  const providers = providerList.data?.providers ?? [];
  const providerTypes = providerList.data?.types ?? [];
  const chatModel = effectiveValue("ai.chat_model", "subtitle_ai.chat_model", "gpt-4o-mini");
  const textProviderID = value("ai.text_provider_id");
  const speechProviderID = value("ai.speech_provider_id");
  const asrModel = value("ai.asr_model", "whisper-1");
  const serves = (id: string, capability: AIProvider["capabilities"][number]) =>
    providers.some((provider) => provider.id === id && provider.capabilities.includes(capability));
  const textReady =
    authMode === "chatgpt"
      ? authenticatedAccount?.connected === true &&
        !!chatGPTModels.data?.models.some((model) => model.id === chatModel)
      : serves(textProviderID, "chat") && chatModel.trim() !== "";
  const textCanTest =
    textReady ||
    (authMode === "chatgpt" &&
      authenticatedAccount?.usage_limited === true &&
      configuredChatModel.trim() !== "");
  const speechReady = serves(speechProviderID, "speech") && asrModel.trim() !== "";
  const subtitleTranslateEnabled = value("subtitle_ai.enabled", "false") === "true";
  const transcribeEnabled = value("subtitle_ai.transcribe_enabled", "false") === "true";
  const descriptionEnabled = value("metadata_ai.enabled", "false") === "true";
  // Only judged once the capabilities describe the model being saved; while
  // they are stale or loading, the server's own validation decides.
  const reasoningEffort = value("ai.reasoning_effort");
  const reasoningUnsupported =
    reasoningEffort !== "" &&
    capabilityModel === currentModel &&
    !capabilities.isFetching &&
    capabilities.isSuccess &&
    !!capabilities.data &&
    !capabilities.data.reasoning_levels.includes(reasoningEffort);
  const textDirty = TEXT_AI_KEYS.some((key) => form.isDirty(key));
  const speechDirty = SPEECH_AI_KEYS.some((key) => form.isDirty(key));
  const advancedChangedCount = AI_ADVANCED_KEYS.filter((key) => form.isDirty(key)).length;

  function setValue(key: string, nextValue: string) {
    form.setValue(key, nextValue);
    if (TEXT_AI_KEYS.includes(key as (typeof TEXT_AI_KEYS)[number])) {
      setTextResult(undefined);
    }
    if (SPEECH_AI_KEYS.includes(key as (typeof SPEECH_AI_KEYS)[number])) {
      setSpeechResult(undefined);
    }
  }

  // The ChatGPT plan is chosen by authentication mode; any other choice is an
  // API-key provider. The provider id is kept while the plan serves text, so
  // switching back restores it.
  function selectTextProvider(next: string) {
    if (next === CHATGPT_PROVIDER) {
      setValue("ai.auth_mode", "chatgpt");
      return;
    }
    setValue("ai.auth_mode", "api_key");
    setValue("ai.text_provider_id", next === NO_PROVIDER ? "" : next);
  }

  // The plan's sign-in lives in the text model tile, the only job it serves.
  function openChatGPTSetup() {
    if (authMode !== "chatgpt") selectTextProvider(CHATGPT_PROVIDER);
    setExpandedTile("text");
  }

  async function checkTextConnection() {
    const authority = captureProfileRequestContext();
    if (!authority || !isCapturedProfileAuthorityActive(authority)) return;
    const started = Date.now();
    try {
      const result = await textCheck.mutateAsync({
        kind: "ai_chat",
        body: form.buildConnectionCheckRequest([...TEXT_AI_KEYS]),
      });
      if (!isCapturedProfileAuthorityActive(authority)) return;
      setTextResult({
        ok: result.success,
        message: result.message,
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    } catch (error) {
      if (!isCapturedProfileAuthorityActive(authority)) return;
      setTextResult({
        ok: false,
        message: error instanceof Error ? error.message : "Text model connection check failed.",
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    } finally {
      if (authMode === "chatgpt" && isCapturedProfileAuthorityActive(authority)) {
        await Promise.all([connection.refetch(), chatGPTModels.refetch()]);
      }
    }
  }

  async function checkSpeechConnection() {
    const started = Date.now();
    try {
      const result = await speechCheck.mutateAsync({
        kind: "ai_transcription",
        body: form.buildConnectionCheckRequest([...SPEECH_AI_KEYS]),
      });
      setSpeechResult({
        ok: result.success,
        message: result.message,
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    } catch (error) {
      setSpeechResult({
        ok: false,
        message: error instanceof Error ? error.message : "Speech-to-text connection check failed.",
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    }
  }

  async function save() {
    const batchSize = parseStrictInteger(value("subtitle_ai.batch_size", "40"));
    const contextLines = parseStrictInteger(value("subtitle_ai.context_neighbors", "2"));
    const chunkSeconds = parseStrictInteger(value("subtitle_ai.asr_chunk_seconds", "600"));
    const quotaJobs = parseStrictInteger(value("subtitle_ai.transcribe_quota_jobs", "0"));
    const maxConcurrent = parseStrictInteger(
      effectiveValue("ai.max_concurrent_jobs", "subtitle_ai.max_concurrent_jobs", "2"),
    );

    // None releases the text provider; it saves while nothing translates.
    const textCleared = authMode !== "chatgpt" && textProviderID === "";
    const textFeaturesOn = subtitleTranslateEnabled || descriptionEnabled;
    const requiresTextConnection =
      (textDirty && (!textCleared || textFeaturesOn)) ||
      (form.isDirty("subtitle_ai.enabled") && subtitleTranslateEnabled) ||
      (form.isDirty("metadata_ai.enabled") && descriptionEnabled);
    if (requiresTextConnection && !textReady) {
      toast.error(
        authMode === "chatgpt"
          ? "Connect a ChatGPT account and choose an available text model."
          : textCleared && textDirty
            ? "Turn off subtitle and description translation before removing the text provider."
            : "Choose a text provider and a model.",
      );
      return;
    }
    if (requiresTextConnection && reasoningUnsupported) {
      toast.error("Choose a reasoning level supported by the selected model.");
      return;
    }
    if (maxConcurrent === null || maxConcurrent < 1) {
      toast.error("Max concurrent jobs must be a positive whole number.");
      return;
    }
    if (batchSize === null || batchSize < 1) {
      toast.error("Subtitle batch size must be a positive whole number.");
      return;
    }
    if (contextLines === null || contextLines < 0) {
      toast.error("Subtitle context lines must be zero or a positive whole number.");
      return;
    }
    if (chunkSeconds === null || chunkSeconds < 60 || chunkSeconds > 600) {
      toast.error("Transcription chunk length must be between 60 and 600 seconds.");
      return;
    }
    if (quotaJobs === null || quotaJobs < 0) {
      toast.error("Transcription limit must be zero or a positive whole number.");
      return;
    }
    await form.save();
    // A job's provider choice changes which providers report it in use.
    void refreshProviders();
  }

  function discard() {
    form.discard();
    setTextResult(undefined);
    setSpeechResult(undefined);
  }

  return (
    <div className="space-y-5">
      <SettingsPageHeader title="AI Services" />

      <p className="text-muted-foreground text-xs">
        Connect each AI provider once. Text, speech-to-text, and recommendation embeddings each
        choose a provider and a model from this list.
      </p>
      <FieldGroup label="Providers">
        <div className="py-3.5">
          <ProviderTileGrid>
            <AIProvidersList
              providers={providers}
              types={providerTypes}
              onChooseChatGPT={chatGPTSupported ? openChatGPTSetup : undefined}
              leading={
                chatGPTSupported || authMode === "chatgpt" ? (
                  <ChatGPTProviderRow
                    email={authenticatedAccount?.email}
                    inUse={authMode === "chatgpt"}
                    onManage={openChatGPTSetup}
                  />
                ) : null
              }
            />
          </ProviderTileGrid>
        </div>
      </FieldGroup>

      <FieldGroup label="Models">
        <div className="py-3.5">
          <ProviderTileGrid>
            <TextModelTile
              authMode={authMode}
              providerID={textProviderID}
              providers={providers}
              reasoningEffort={reasoningEffort}
              reasoningLevels={capabilities.data?.reasoning_levels ?? []}
              reasoningAttention={form.isDirty("ai.reasoning_effort") || reasoningUnsupported}
              chatGPTSupported={chatGPTSupported}
              chatModel={chatModel}
              ready={textReady}
              canTest={textCanTest}
              dirty={textDirty}
              restartKeys={restartKeys}
              onChange={setValue}
              onProviderChange={selectTextProvider}
              onConnectionConfirmed={() => void form.retryLoad()}
              onSettingsTransitionStarted={form.beginSettingsTransition}
              onTest={() => void checkTextConnection()}
              isTesting={textCheck.isPending}
              test={textResult}
              // A staged edit forces its tile open: the save bar must never
              // block on a field the admin cannot see.
              expanded={expandedTile === "text" || textDirty}
              onExpand={() => setExpandedTile("text")}
              onCollapse={() => setExpandedTile(null)}
            />
            <SpeechModelTile
              providerID={speechProviderID}
              providers={providers}
              asrModel={asrModel}
              ready={speechReady}
              dirty={speechDirty}
              onChange={setValue}
              onTest={() => void checkSpeechConnection()}
              isTesting={speechCheck.isPending}
              test={speechResult}
              expanded={expandedTile === "speech" || speechDirty}
              onExpand={() => setExpandedTile("speech")}
              onCollapse={() => setExpandedTile(null)}
            />
            <EmbeddingsSummaryRow
              provider={providers.find(
                (provider) =>
                  provider.id === value("recommendations.embedding_provider_id") &&
                  provider.capabilities.includes("embeddings"),
              )}
              model={value("recommendations.embedding_model")}
            />
          </ProviderTileGrid>
        </div>
      </FieldGroup>

      <FieldGroup label="Features">
        <p className="text-muted-foreground py-3.5 text-xs leading-relaxed">
          Nothing here runs on a schedule: subtitle work starts when a viewer or admin asks for a
          track, and description translation when an admin queues it or a viewer opens a detail
          page.
        </p>
        {/*
          A feature whose model is not configured only queues jobs that fail at
          the provider, so its switch is disabled until the model is ready. One
          that is already on stays switchable, so a degraded provider can be
          turned off without being fixed first.
        */}
        <SettingField
          label="Translate subtitles"
          type="toggle"
          value={value("subtitle_ai.enabled", "false")}
          onChange={(next) => setValue("subtitle_ai.enabled", next)}
          description="Turns an existing subtitle track into another language, on request."
          disabled={!textReady && !subtitleTranslateEnabled}
          status={
            textReady ? undefined : (
              <SettingFieldStatus tone="warn">Needs the text model</SettingFieldStatus>
            )
          }
          restartRequired={restartKeys.has("subtitle_ai.enabled")}
        />
        <SettingField
          label="Create subtitles from audio"
          type="toggle"
          value={value("subtitle_ai.transcribe_enabled", "false")}
          onChange={(next) => setValue("subtitle_ai.transcribe_enabled", next)}
          description="Writes timed subtitles from the audio track, on request."
          disabled={!speechReady && !transcribeEnabled}
          status={
            speechReady ? undefined : (
              <SettingFieldStatus tone="warn">Needs speech-to-text</SettingFieldStatus>
            )
          }
          restartRequired={restartKeys.has("subtitle_ai.transcribe_enabled")}
        />
        <SettingField
          label="Translate descriptions"
          type="toggle"
          value={value("metadata_ai.enabled", "false")}
          onChange={(next) => setValue("metadata_ai.enabled", next)}
          description="Translates overviews and taglines for the items an admin or viewer asks for."
          disabled={!textReady && !descriptionEnabled}
          status={
            textReady ? undefined : (
              <SettingFieldStatus tone="warn">Needs the text model</SettingFieldStatus>
            )
          }
          restartRequired={restartKeys.has("metadata_ai.enabled")}
        />
        <SettingField
          label="Description translation for viewers"
          type="select"
          value={value("metadata_ai.on_view", "off")}
          onChange={(next) => setValue("metadata_ai.on_view", next)}
          disabled={!descriptionEnabled}
          options={[
            { value: "off", label: "Off" },
            { value: "button", label: "Translate button on detail pages" },
            { value: "auto", label: "Automatic on view" },
          ]}
          description={
            descriptionEnabled ? undefined : "Inactive until Translate descriptions is on."
          }
          restartRequired={restartKeys.has("metadata_ai.on_view")}
        />
        <AdvancedSection
          id="ai.tuning"
          count={AI_ADVANCED_KEYS.length}
          forceOpen={advancedChangedCount > 0}
        >
          <TuningScope
            label="Server-wide tuning"
            caption="One setting for the whole server, whoever the job belongs to."
          >
            <SettingField
              label="Jobs running at once"
              type="number"
              value={effectiveValue(
                "ai.max_concurrent_jobs",
                "subtitle_ai.max_concurrent_jobs",
                "2",
              )}
              onChange={(next) => setValue("ai.max_concurrent_jobs", next)}
              // Each server process builds its own dispatch semaphore at start
              // (internal/api/router.go), so a cluster runs this many per process.
              description="Shared by every AI job in one server process; each process gets its own budget."
              restartRequired={restartKeys.has("ai.max_concurrent_jobs")}
            />
            <SettingField
              label="Subtitle lines per request"
              type="number"
              value={value("subtitle_ai.batch_size", "40")}
              onChange={(next) => setValue("subtitle_ai.batch_size", next)}
              restartRequired={restartKeys.has("subtitle_ai.batch_size")}
            />
            <SettingField
              label="Surrounding lines sent for context"
              type="number"
              value={value("subtitle_ai.context_neighbors", "2")}
              onChange={(next) => setValue("subtitle_ai.context_neighbors", next)}
              restartRequired={restartKeys.has("subtitle_ai.context_neighbors")}
            />
            <SettingField
              label="Audio per request"
              type="number"
              unit="seconds"
              value={value("subtitle_ai.asr_chunk_seconds", "600")}
              onChange={(next) => setValue("subtitle_ai.asr_chunk_seconds", next)}
              description="Between 60 and 600."
              restartRequired={restartKeys.has("subtitle_ai.asr_chunk_seconds")}
            />
          </TuningScope>
          <TuningScope
            label="Per-account limits"
            caption="Counted per login account, shared by every profile on it."
          >
            <LimitField
              label="Transcriptions per account"
              value={value("subtitle_ai.transcribe_quota_jobs", "0")}
              onChange={(next) => setValue("subtitle_ai.transcribe_quota_jobs", next)}
              fallbackValue="10"
              hint="Every profile on the account draws from this one allowance."
              restartRequired={restartKeys.has("subtitle_ai.transcribe_quota_jobs")}
            />
            <SettingField
              label="Allowance resets"
              type="select"
              value={value("subtitle_ai.transcribe_quota_period", "day")}
              onChange={(next) => setValue("subtitle_ai.transcribe_quota_period", next)}
              options={QUOTA_PERIODS.map((period) => ({
                value: period,
                label: `Per ${period} (rolling ${QUOTA_PERIOD_WINDOW_LABELS[period]})`,
              }))}
              description="Rolling window for the transcription allowance above."
              restartRequired={restartKeys.has("subtitle_ai.transcribe_quota_period")}
            />
          </TuningScope>
        </AdvancedSection>
      </FieldGroup>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={save}
        onDiscard={discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
