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
import { QUOTA_PERIODS, quotaPeriodWindowLabel } from "@/lib/quotaPeriods";
import { useT, type Translate } from "@/i18n";
import { cn } from "@/lib/utils";
import { SaveBar } from "@/components/SaveBar";

import { providerOptions } from "@/lib/aiProviders";
import { AIProvidersList } from "./AIProviders";
import { ModelField } from "./AIModelField";
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
  "ai.service_tier",
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

/** Example model ids for the text model field; they read the same in every language. */
const TEXT_MODEL_PLACEHOLDER = "gpt-6-luna, gemini-2.5-flash, llama3.1";

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

/** A passed test's message with how long ago it ran and how long it took. */
function testedLabel(t: Translate, test: AITestState): string {
  const seconds = Math.max(0, Math.round((Date.now() - test.at) / 1000));
  const values = { message: test.message, duration_ms: String(test.durationMs) };
  if (seconds < 60) return t("web.admin.settings.ai.test.testedSeconds", { ...values, seconds });
  const minutes = Math.round(seconds / 60);
  return t("web.admin.settings.ai.test.testedMinutes", {
    ...values,
    minutes,
    minutes_text: String(minutes),
  });
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
  const t = useT();
  return (
    // Buttons right-aligned to match the collapsed tile's Manage button (and
    // the shared ProviderPanelActions); the test status takes the left side.
    <div className="mt-3.5 flex flex-wrap items-center justify-end gap-2">
      {test ? (
        <span
          role="status"
          aria-live="polite"
          className={cn(
            "me-auto text-xs",
            test.ok ? "text-muted-foreground" : "text-amber-600 dark:text-amber-400",
          )}
        >
          {test.ok ? testedLabel(t, test) : test.message}
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
          {t("web.admin.settings.ai.tile.close")}
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
  const t = useT();
  if (!dirty) return null;
  return (
    <p className="text-muted-foreground mt-2 text-xs">{t("web.admin.settings.ai.unsavedNote")}</p>
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
  serviceTier,
  serviceTiers,
  serviceTierDirty,
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
  serviceTier: string;
  serviceTiers: string[];
  serviceTierDirty: boolean;
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
  const t = useT();
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
  const supportsServiceTier = !chatGPT && provider?.type === "openai" && serviceTiers.length > 0;

  return (
    <ProviderTile
      name={t("web.admin.settings.ai.jobs.text")}
      tagline={t("web.admin.settings.ai.text.tagline")}
      logo={<Languages className="text-muted-foreground size-4" aria-hidden="true" />}
      state={state}
      statePill={
        !expanded && ready && !test?.ok ? t("web.admin.settings.ai.tile.configured") : undefined
      }
      meta={
        expanded
          ? undefined
          : failed
            ? test.message
            : ready
              ? `${chatModel} · ${chatGPT ? t("web.admin.settings.ai.chatgpt.plan") : (provider?.name ?? "")}`
              : chatGPT
                ? t("web.admin.settings.ai.text.connectAccount")
                : t("web.admin.settings.ai.tile.chooseProviderAndModel")
      }
      expanded={expanded}
      primaryAction={{
        label: ready
          ? t("web.admin.settings.ai.tile.manage")
          : t("web.admin.settings.ai.tile.setUp"),
        onClick: onExpand,
      }}
    >
      <SettingField
        label={t("web.admin.settings.ai.provider.label")}
        type="select"
        value={selected || NO_PROVIDER}
        onChange={onProviderChange}
        description={
          providers.some((candidate) => candidate.capabilities.includes("chat"))
            ? undefined
            : t("web.admin.settings.ai.text.noChatProvider")
        }
        options={[
          { value: NO_PROVIDER, label: t("web.admin.settings.ai.provider.none") },
          ...(chatGPTSupported || chatGPT
            ? [
                {
                  value: CHATGPT_PROVIDER,
                  label: t("web.admin.settings.ai.chatgpt.plan"),
                  disabled: !chatGPTSupported,
                },
              ]
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
          label={t("web.admin.settings.ai.model.label")}
          providerID={provider ? providerID : ""}
          kind="chat"
          value={chatModel}
          onChange={(next) => onChange("ai.chat_model", next)}
          placeholder={TEXT_MODEL_PLACEHOLDER}
        />
      )}
      <AdvancedSection
        id="ai.text-model"
        count={supportsServiceTier ? 2 : 1}
        forceOpen={reasoningAttention || (supportsServiceTier && serviceTierDirty)}
      >
        <SettingField
          label={t("web.admin.settings.ai.text.reasoningLevel")}
          type="select"
          value={reasoningEffort || "default"}
          description={t("web.admin.settings.ai.text.reasoningDescription")}
          onChange={(next) => onChange("ai.reasoning_effort", next === "default" ? "" : next)}
          restartRequired={restartKeys.has("ai.reasoning_effort")}
          options={[
            { value: "default", label: t("web.admin.settings.ai.text.reasoningDefault") },
            ...reasoningLevels.map((level) => ({
              value: level,
              label: t("web.admin.settings.ai.text.reasoningOption", {
                level,
                fallback: level.charAt(0).toUpperCase() + level.slice(1),
              }),
            })),
            ...(reasoningEffort && !reasoningLevels.includes(reasoningEffort)
              ? [
                  {
                    value: reasoningEffort,
                    label: t("web.admin.settings.ai.text.reasoningUnsupported", {
                      level: reasoningEffort,
                    }),
                    disabled: true,
                  },
                ]
              : []),
          ]}
        />
        {supportsServiceTier && (
          <SettingField
            label={t("web.admin.settings.ai.text.serviceTier")}
            type="select"
            value={serviceTier || "default"}
            description={t("web.admin.settings.ai.text.serviceTierDescription")}
            onChange={(next) => onChange("ai.service_tier", next === "default" ? "" : next)}
            options={serviceTiers.map((tier) => ({
              value: tier,
              label: t("web.admin.settings.ai.text.serviceTierOption", { tier }),
            }))}
          />
        )}
      </AdvancedSection>
      <ModelPanelActions
        testLabel={t("web.admin.settings.ai.text.test")}
        pendingLabel={t("web.admin.settings.ai.text.testing")}
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
  const t = useT();
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
      name={t("web.admin.settings.ai.jobs.speech")}
      tagline={t("web.admin.settings.ai.speech.tagline")}
      logo={<AudioLines className="text-muted-foreground size-4" aria-hidden="true" />}
      state={state}
      statePill={
        !expanded && ready && !test?.ok ? t("web.admin.settings.ai.tile.configured") : undefined
      }
      meta={
        expanded
          ? undefined
          : failed
            ? test.message
            : ready
              ? `${asrModel} · ${provider?.name ?? ""}`
              : t("web.admin.settings.ai.tile.chooseProviderAndModel")
      }
      expanded={expanded}
      primaryAction={{
        label: ready
          ? t("web.admin.settings.ai.tile.manage")
          : t("web.admin.settings.ai.tile.setUp"),
        onClick: onExpand,
      }}
    >
      <SettingField
        label={t("web.admin.settings.ai.provider.label")}
        type="select"
        value={providerID || NO_PROVIDER}
        onChange={(next) => onChange("ai.speech_provider_id", next === NO_PROVIDER ? "" : next)}
        description={
          providers.some((candidate) => candidate.capabilities.includes("speech"))
            ? t("web.admin.settings.ai.speech.providerDescription")
            : t("web.admin.settings.ai.speech.noProvider")
        }
        options={[
          { value: NO_PROVIDER, label: t("web.admin.settings.ai.provider.none") },
          ...providerOptions(providers, "speech", providerID),
        ]}
      />
      <ModelField
        label={t("web.admin.settings.ai.model.label")}
        providerID={provider ? providerID : ""}
        kind="speech"
        value={asrModel}
        onChange={(next) => onChange("ai.asr_model", next)}
        placeholder={t("web.admin.settings.ai.speech.modelPlaceholder")}
      />
      <ModelPanelActions
        testLabel={t("web.admin.settings.ai.speech.test")}
        pendingLabel={t("web.admin.settings.ai.speech.testing")}
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
  const t = useT();
  const ready = provider !== undefined && model.trim() !== "";
  return (
    <ProviderTile
      name={t("web.admin.settings.ai.jobs.embeddings")}
      tagline={t("web.admin.settings.ai.embeddings.tagline")}
      logo={<Sparkles className="text-muted-foreground size-4" aria-hidden="true" />}
      state={ready ? "connected" : "not_connected"}
      statePill={ready ? t("web.admin.settings.ai.tile.configured") : undefined}
      meta={
        ready
          ? `${model} · ${provider.name}`
          : t("web.admin.settings.ai.embeddings.chooseOnRecommendations")
      }
      headerActions={
        <Button asChild type="button" size="sm" variant="outline">
          <Link to="/admin/recommendations">{t("web.admin.settings.ai.tile.manage")}</Link>
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
  const t = useT();
  let statePill: string | undefined;
  if (inUse)
    statePill = email
      ? t("web.admin.settings.ai.tile.inUse")
      : t("web.admin.settings.ai.chatgptRow.signInNeeded");
  else if (email) statePill = t("web.admin.settings.ai.tile.idle");
  return (
    <ProviderTile
      name={t("web.admin.settings.ai.chatgpt.plan")}
      monogram="GP"
      monogramClass="bg-emerald-500/10 text-emerald-700 dark:text-emerald-200"
      tagline={
        email
          ? t("web.admin.settings.ai.chatgptRow.signedInAs", { email })
          : t("web.admin.settings.ai.chatgpt.signInTagline")
      }
      meta={
        inUse
          ? t("web.admin.settings.ai.chatgptRow.usedByText")
          : t("web.admin.settings.ai.chatgptRow.chatOnly")
      }
      state={inUse && email ? "connected" : "not_connected"}
      statePill={statePill}
      primaryAction={{
        label: email
          ? t("web.admin.settings.ai.tile.manage")
          : t("web.admin.settings.ai.tile.connect"),
        onClick: onManage,
      }}
    />
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function AISettings() {
  const t = useT();
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const textCheck = useCheckAdminSettingsConnection();
  const speechCheck = useCheckAdminSettingsConnection();
  const [textResult, setTextResult] = useState<AITestState | undefined>(undefined);
  const [speechResult, setSpeechResult] = useState<AITestState | undefined>(undefined);
  const [expandedTile, setExpandedTile] = useState<string | null>(null);

  const authMode = form.getValue("ai.auth_mode") || "api_key";
  const chatModelEdited = form.isDirty("ai.chat_model");
  const configuredChatModel = chatModelEdited
    ? form.getValue("ai.chat_model")
    : form.getValue("ai.chat_model") || form.getValue("subtitle_ai.chat_model");
  // A cleared draft must stay empty; only untouched settings inherit defaults.
  const currentModel = chatModelEdited ? configuredChatModel : configuredChatModel || "gpt-6-luna";
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
      <div
        className="max-w-5xl space-y-6"
        role="status"
        aria-label={t("web.admin.settings.ai.page.loading")}
      >
        <Skeleton className="h-9 w-48" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-64 w-full" />
        <span className="sr-only">{t("web.admin.settings.ai.page.loading")}</span>
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
  const chatModel = currentModel;
  const textProviderID = value("ai.text_provider_id");
  const speechProviderID = value("ai.speech_provider_id");
  const asrModel = form.isDirty("ai.asr_model")
    ? form.getValue("ai.asr_model")
    : value("ai.asr_model", "whisper-1");
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
        message:
          error instanceof Error ? error.message : t("web.admin.settings.ai.page.textCheckFailed"),
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
        message:
          error instanceof Error
            ? error.message
            : t("web.admin.settings.ai.page.speechCheckFailed"),
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
          ? t("web.admin.settings.ai.page.connectChatgpt")
          : textCleared && textDirty
            ? t("web.admin.settings.ai.page.turnOffBeforeRemove")
            : t("web.admin.settings.ai.page.chooseTextProvider"),
      );
      return;
    }
    if (requiresTextConnection && reasoningUnsupported) {
      toast.error(t("web.admin.settings.ai.page.chooseReasoning"));
      return;
    }
    if (maxConcurrent === null || maxConcurrent < 1) {
      toast.error(t("web.admin.settings.ai.page.maxConcurrentInvalid"));
      return;
    }
    if (batchSize === null || batchSize < 1) {
      toast.error(t("web.admin.settings.ai.page.batchSizeInvalid"));
      return;
    }
    if (contextLines === null || contextLines < 0) {
      toast.error(t("web.admin.settings.ai.page.contextLinesInvalid"));
      return;
    }
    if (chunkSeconds === null || chunkSeconds < 60 || chunkSeconds > 600) {
      toast.error(t("web.admin.settings.ai.page.chunkInvalid"));
      return;
    }
    if (quotaJobs === null || quotaJobs < 0) {
      toast.error(t("web.admin.settings.ai.page.quotaInvalid"));
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
      <SettingsPageHeader title={t("web.admin.settings.ai.page.title")} />

      <p className="text-muted-foreground text-xs">{t("web.admin.settings.ai.page.intro")}</p>
      <FieldGroup label={t("web.admin.settings.ai.page.providers")}>
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

      <FieldGroup label={t("web.admin.settings.ai.page.models")}>
        <div className="py-3.5">
          <ProviderTileGrid>
            <TextModelTile
              authMode={authMode}
              providerID={textProviderID}
              providers={providers}
              reasoningEffort={reasoningEffort}
              reasoningLevels={capabilities.data?.reasoning_levels ?? []}
              reasoningAttention={form.isDirty("ai.reasoning_effort") || reasoningUnsupported}
              serviceTier={value("ai.service_tier")}
              serviceTiers={capabilities.data?.openai_service_tiers ?? []}
              serviceTierDirty={form.isDirty("ai.service_tier")}
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

      <FieldGroup label={t("web.admin.settings.ai.page.features")}>
        <p className="text-muted-foreground py-3.5 text-xs leading-relaxed">
          {t("web.admin.settings.ai.page.featuresIntro")}
        </p>
        {/*
          A feature whose model is not configured only queues jobs that fail at
          the provider, so its switch is disabled until the model is ready. One
          that is already on stays switchable, so a degraded provider can be
          turned off without being fixed first.
        */}
        <SettingField
          label={t("web.admin.settings.ai.features.translateSubtitles")}
          type="toggle"
          value={value("subtitle_ai.enabled", "false")}
          onChange={(next) => setValue("subtitle_ai.enabled", next)}
          description={t("web.admin.settings.ai.features.translateSubtitlesDescription")}
          disabled={!textReady && !subtitleTranslateEnabled}
          status={
            textReady ? undefined : (
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.ai.features.needsTextModel")}
              </SettingFieldStatus>
            )
          }
          restartRequired={restartKeys.has("subtitle_ai.enabled")}
        />
        <SettingField
          label={t("web.admin.settings.ai.features.createSubtitles")}
          type="toggle"
          value={value("subtitle_ai.transcribe_enabled", "false")}
          onChange={(next) => setValue("subtitle_ai.transcribe_enabled", next)}
          description={t("web.admin.settings.ai.features.createSubtitlesDescription")}
          disabled={!speechReady && !transcribeEnabled}
          status={
            speechReady ? undefined : (
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.ai.features.needsSpeech")}
              </SettingFieldStatus>
            )
          }
          restartRequired={restartKeys.has("subtitle_ai.transcribe_enabled")}
        />
        <SettingField
          label={t("web.admin.settings.ai.features.translateDescriptions")}
          type="toggle"
          value={value("metadata_ai.enabled", "false")}
          onChange={(next) => setValue("metadata_ai.enabled", next)}
          description={t("web.admin.settings.ai.features.translateDescriptionsDescription")}
          disabled={!textReady && !descriptionEnabled}
          status={
            textReady ? undefined : (
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.ai.features.needsTextModel")}
              </SettingFieldStatus>
            )
          }
          restartRequired={restartKeys.has("metadata_ai.enabled")}
        />
        <SettingField
          label={t("web.admin.settings.ai.features.viewerTranslation")}
          type="select"
          value={value("metadata_ai.on_view", "off")}
          onChange={(next) => setValue("metadata_ai.on_view", next)}
          disabled={!descriptionEnabled}
          options={[
            { value: "off", label: t("web.admin.settings.ai.features.viewerTranslationOff") },
            { value: "button", label: t("web.admin.settings.ai.features.viewerTranslationButton") },
            { value: "auto", label: t("web.admin.settings.ai.features.viewerTranslationAuto") },
          ]}
          description={
            descriptionEnabled
              ? undefined
              : t("web.admin.settings.ai.features.viewerTranslationInactive")
          }
          restartRequired={restartKeys.has("metadata_ai.on_view")}
        />
        <AdvancedSection
          id="ai.tuning"
          count={AI_ADVANCED_KEYS.length}
          forceOpen={advancedChangedCount > 0}
        >
          <TuningScope
            label={t("web.admin.settings.ai.tuning.serverWide")}
            caption={t("web.admin.settings.ai.tuning.serverWideCaption")}
          >
            <SettingField
              label={t("web.admin.settings.ai.tuning.jobsAtOnce")}
              type="number"
              value={effectiveValue(
                "ai.max_concurrent_jobs",
                "subtitle_ai.max_concurrent_jobs",
                "2",
              )}
              onChange={(next) => setValue("ai.max_concurrent_jobs", next)}
              // Each server process builds its own dispatch semaphore at start
              // (internal/api/router.go), so a cluster runs this many per process.
              description={t("web.admin.settings.ai.tuning.jobsAtOnceDescription")}
              restartRequired={restartKeys.has("ai.max_concurrent_jobs")}
            />
            <SettingField
              label={t("web.admin.settings.ai.tuning.batchSize")}
              type="number"
              value={value("subtitle_ai.batch_size", "40")}
              onChange={(next) => setValue("subtitle_ai.batch_size", next)}
              restartRequired={restartKeys.has("subtitle_ai.batch_size")}
            />
            <SettingField
              label={t("web.admin.settings.ai.tuning.contextLines")}
              type="number"
              value={value("subtitle_ai.context_neighbors", "2")}
              onChange={(next) => setValue("subtitle_ai.context_neighbors", next)}
              restartRequired={restartKeys.has("subtitle_ai.context_neighbors")}
            />
            <SettingField
              label={t("web.admin.settings.ai.tuning.audioPerRequest")}
              type="number"
              unit={t("web.admin.settings.ai.unit.seconds")}
              value={value("subtitle_ai.asr_chunk_seconds", "600")}
              onChange={(next) => setValue("subtitle_ai.asr_chunk_seconds", next)}
              description={t("web.admin.settings.ai.tuning.audioPerRequestRange")}
              restartRequired={restartKeys.has("subtitle_ai.asr_chunk_seconds")}
            />
          </TuningScope>
          <TuningScope
            label={t("web.admin.settings.ai.tuning.perAccount")}
            caption={t("web.admin.settings.ai.tuning.perAccountCaption")}
          >
            <LimitField
              label={t("web.admin.settings.ai.tuning.transcriptions")}
              value={value("subtitle_ai.transcribe_quota_jobs", "0")}
              onChange={(next) => setValue("subtitle_ai.transcribe_quota_jobs", next)}
              fallbackValue="10"
              hint={t("web.admin.settings.ai.tuning.transcriptionsHint")}
              restartRequired={restartKeys.has("subtitle_ai.transcribe_quota_jobs")}
            />
            <SettingField
              label={t("web.admin.settings.ai.tuning.allowanceResets")}
              type="select"
              value={value("subtitle_ai.transcribe_quota_period", "day")}
              onChange={(next) => setValue("subtitle_ai.transcribe_quota_period", next)}
              options={QUOTA_PERIODS.map((period) => ({
                value: period,
                label: t("web.admin.settings.ai.tuning.quotaPeriodOption", {
                  period,
                  window: quotaPeriodWindowLabel(period) ?? period,
                }),
              }))}
              description={t("web.admin.settings.ai.tuning.allowanceDescription")}
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
