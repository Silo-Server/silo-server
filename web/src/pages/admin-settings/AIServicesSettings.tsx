import { useState } from "react";
import {
  AudioLines,
  ChevronDown,
  CircleAlert,
  CircleCheck,
  ExternalLink,
  Languages,
} from "lucide-react";
import { toast } from "sonner";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { QUOTA_PERIODS, QUOTA_PERIOD_WINDOW_LABELS } from "@/lib/quotaPeriods";
import { cn } from "@/lib/utils";

import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";

const TEXT_AI_KEYS = ["ai.base_url", "ai.chat_model", "ai.api_key"] as const;
const SPEECH_AI_KEYS = [
  "ai.base_url",
  "ai.api_key",
  "ai.asr_base_url",
  "ai.asr_model",
  "ai.asr_api_key",
] as const;
const LEGACY_AI_KEYS = [
  "subtitle_ai.base_url",
  "subtitle_ai.api_key",
  "subtitle_ai.chat_model",
  "subtitle_ai.max_concurrent_jobs",
] as const;
const KEYS: string[] = [
  ...TEXT_AI_KEYS,
  ...LEGACY_AI_KEYS,
  "ai.asr_base_url",
  "ai.asr_model",
  "ai.asr_api_key",
  "ai.max_concurrent_jobs",
  "subtitle_ai.enabled",
  "subtitle_ai.transcribe_enabled",
  "subtitle_ai.batch_size",
  "subtitle_ai.context_neighbors",
  "subtitle_ai.asr_chunk_seconds",
  "subtitle_ai.transcribe_quota_jobs",
  "subtitle_ai.transcribe_quota_period",
  "metadata_ai.enabled",
  "metadata_ai.on_view",
];

const TRANSCRIPTION_PRESETS = [
  {
    id: "self-hosted",
    label: "Self-hosted",
    description:
      "Speaches or faster-whisper on your network. Replace the hostname with one reachable from the Silo container.",
    baseUrl: "http://speaches:8000",
    model: "deepdml/faster-whisper-large-v3-turbo-ct2",
  },
  {
    id: "groq-turbo",
    label: "Groq - fast",
    description: "Hosted whisper-large-v3-turbo. Requires a Groq API key.",
    baseUrl: "https://api.groq.com/openai",
    model: "whisper-large-v3-turbo",
  },
  {
    id: "groq-accurate",
    label: "Groq - accurate",
    description: "Hosted whisper-large-v3. Requires a Groq API key.",
    baseUrl: "https://api.groq.com/openai",
    model: "whisper-large-v3",
  },
  {
    id: "openai",
    label: "OpenAI",
    description: "Hosted whisper-1. The transcription key can inherit the Text AI key.",
    baseUrl: "https://api.openai.com",
    model: "whisper-1",
  },
] as const;

const CHAT_ONLY_GATEWAY_HOSTS = ["openrouter.ai"];

function isChatOnlyGateway(rawURL: string): boolean {
  const trimmed = rawURL.trim();
  if (!trimmed) return false;
  try {
    const host = new URL(
      trimmed.includes("://") ? trimmed : `https://${trimmed}`,
    ).hostname.toLowerCase();
    return CHAT_ONLY_GATEWAY_HOSTS.some(
      (gateway) => host === gateway || host.endsWith(`.${gateway}`),
    );
  } catch {
    return false;
  }
}

function SectionHeading({
  icon: Icon,
  title,
  description,
  status,
  statusTone = "neutral",
}: {
  icon: typeof Languages;
  title: string;
  description: string;
  status: string;
  statusTone?: "ready" | "warning" | "neutral";
}) {
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div className="flex min-w-0 gap-3">
        <div className="bg-muted mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md">
          <Icon className="text-muted-foreground size-4" />
        </div>
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{title}</h3>
          <p className="text-muted-foreground mt-1 max-w-2xl text-xs leading-relaxed">
            {description}
          </p>
        </div>
      </div>
      <Badge
        variant="outline"
        className={cn(
          statusTone === "ready" && "border-green-500/30 text-green-600",
          statusTone === "warning" && "border-amber-500/30 text-amber-600",
        )}
      >
        {statusTone === "ready" ? (
          <CircleCheck />
        ) : statusTone === "warning" ? (
          <CircleAlert />
        ) : null}
        {status}
      </Badge>
    </div>
  );
}

function RequirementNote({
  label,
  ready,
  detail,
}: {
  label: string;
  ready: boolean;
  detail: string;
}) {
  return (
    <div className="text-muted-foreground flex items-start gap-2 text-xs leading-relaxed">
      {ready ? (
        <CircleCheck className="mt-0.5 size-3.5 shrink-0 text-green-600" />
      ) : (
        <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-amber-600" />
      )}
      <span>
        <span className="text-foreground font-medium">{label}</span> - {detail}
      </span>
    </div>
  );
}

export default function AIServicesSettings() {
  const form = useSettingsForm({ keys: KEYS });
  const textCheck = useCheckAdminSettingsConnection();
  const speechCheck = useCheckAdminSettingsConnection();
  const [textResult, setTextResult] = useState<ConnectionCheckResponse | null>(null);
  const [speechResult, setSpeechResult] = useState<ConnectionCheckResponse | null>(null);

  if (form.isLoading) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading AI settings">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-36 w-full max-w-3xl" />
        <Skeleton className="h-48 w-full max-w-3xl" />
        <Skeleton className="h-52 w-full max-w-3xl" />
        <span className="sr-only">Loading AI settings</span>
      </div>
    );
  }

  const value = (key: string, fallback = "") => form.getValue(key) || fallback;
  const effectiveValue = (key: string, legacyKey: string, fallback: string) =>
    value(key, value(legacyKey, fallback));
  const textBaseURL = effectiveValue(
    "ai.base_url",
    "subtitle_ai.base_url",
    "https://api.openai.com",
  );
  const chatModel = effectiveValue("ai.chat_model", "subtitle_ai.chat_model", "gpt-4o-mini");
  const asrBaseURL = value("ai.asr_base_url");
  const asrModel = value("ai.asr_model", "whisper-1");
  const textReady = textBaseURL.trim() !== "" && chatModel.trim() !== "";
  const speechUsesTextEndpoint = asrBaseURL.trim() === "";
  const speechCheckable =
    (asrBaseURL.trim() !== "" || textBaseURL.trim() !== "") && asrModel.trim() !== "";
  const speechCompatible = !isChatOnlyGateway(speechUsesTextEndpoint ? textBaseURL : asrBaseURL);
  const speechReady = speechCheckable && speechCompatible;
  const descriptionEnabled = value("metadata_ai.enabled", "false") === "true";

  function setValue(key: string, nextValue: string) {
    form.setValue(key, nextValue);
    if (TEXT_AI_KEYS.includes(key as (typeof TEXT_AI_KEYS)[number])) {
      setTextResult(null);
    }
    if (SPEECH_AI_KEYS.includes(key as (typeof SPEECH_AI_KEYS)[number])) {
      setSpeechResult(null);
    }
  }

  async function checkTextConnection() {
    try {
      setTextResult(
        await textCheck.mutateAsync({
          kind: "ai_chat",
          body: form.buildConnectionCheckRequest([...TEXT_AI_KEYS]),
        }),
      );
    } catch (error) {
      setTextResult({
        success: false,
        message: error instanceof Error ? error.message : "Text AI connection check failed.",
      });
    }
  }

  async function checkSpeechConnection() {
    try {
      setSpeechResult(
        await speechCheck.mutateAsync({
          kind: "ai_transcription",
          body: form.buildConnectionCheckRequest([...SPEECH_AI_KEYS]),
        }),
      );
    } catch (error) {
      setSpeechResult({
        success: false,
        message: error instanceof Error ? error.message : "Speech-to-text connection check failed.",
      });
    }
  }

  async function save() {
    const batchSize = Number.parseInt(value("subtitle_ai.batch_size", "40"), 10);
    const contextLines = Number.parseInt(value("subtitle_ai.context_neighbors", "2"), 10);
    const chunkSeconds = Number.parseInt(value("subtitle_ai.asr_chunk_seconds", "600"), 10);
    const quotaJobs = Number.parseInt(value("subtitle_ai.transcribe_quota_jobs", "0"), 10);
    const maxConcurrent = Number.parseInt(
      effectiveValue("ai.max_concurrent_jobs", "subtitle_ai.max_concurrent_jobs", "2"),
      10,
    );

    if (!textReady) {
      toast.error("Text AI base URL and chat model are required.");
      return;
    }
    if (!Number.isInteger(maxConcurrent) || maxConcurrent < 1) {
      toast.error("Max concurrent jobs must be a positive whole number.");
      return;
    }
    if (!Number.isInteger(batchSize) || batchSize < 1) {
      toast.error("Subtitle batch size must be a positive whole number.");
      return;
    }
    if (!Number.isInteger(contextLines) || contextLines < 0) {
      toast.error("Subtitle context lines must be zero or a positive whole number.");
      return;
    }
    if (!Number.isInteger(chunkSeconds) || chunkSeconds < 60 || chunkSeconds > 600) {
      toast.error("Transcription chunk length must be between 60 and 600 seconds.");
      return;
    }
    if (!Number.isInteger(quotaJobs) || quotaJobs < 0) {
      toast.error("Transcription limit must be zero or a positive whole number.");
      return;
    }
    await form.save();
  }

  return (
    <div className="flex h-full max-w-4xl flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">AI Services</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure text translation and speech-to-text independently, then enable only the features
          that use them.
        </p>
      </div>

      <div className="divide-border border-border divide-y border-y">
        <section className="space-y-5 py-6">
          <SectionHeading
            icon={Languages}
            title="Text translation"
            description="An OpenAI-compatible chat endpoint used to translate existing text subtitles, descriptions, and taglines. Whisper is not required."
            status={textReady ? "Configured" : "Not configured"}
            statusTone={textReady ? "ready" : "warning"}
          />
          <div className="ml-0 space-y-1 sm:ml-11">
            <SettingField
              label="Base URL"
              value={textBaseURL}
              onChange={(next) => setValue("ai.base_url", next)}
              hint="https://api.openai.com"
            />
            <SettingField
              label="Chat model"
              value={chatModel}
              onChange={(next) => setValue("ai.chat_model", next)}
              hint="gpt-4o-mini, gemini-flash-latest, llama3.1"
            />
            <SettingField
              label="API key"
              type="password"
              value={value("ai.api_key")}
              onChange={(next) => setValue("ai.api_key", next)}
              sensitiveConfigured={
                form.sensitiveConfigured.includes("ai.api_key") ||
                form.sensitiveConfigured.includes("subtitle_ai.api_key")
              }
              hint="Optional for keyless local endpoints. Saved keys are reused for tests only when the endpoint host is unchanged."
            />
            <ConnectionCheckAction
              onClick={checkTextConnection}
              result={textResult}
              isPending={textCheck.isPending}
              disabled={form.isSaving || !textReady}
              label="Test Text AI"
              pendingLabel="Testing Text AI..."
            />
          </div>
        </section>

        <section className="space-y-5 py-6">
          <SectionHeading
            icon={AudioLines}
            title="Speech-to-text"
            description="A Whisper-compatible transcription endpoint that returns segment timestamps. Only required when Silo generates subtitles from audio."
            status={
              !speechCompatible
                ? "Incompatible endpoint"
                : speechResult?.success
                  ? "Verified"
                  : speechUsesTextEndpoint
                    ? "Using text endpoint"
                    : speechReady
                      ? "Configured separately"
                      : "Not configured"
            }
            statusTone={speechUsesTextEndpoint ? "warning" : speechReady ? "ready" : "warning"}
          />
          <div className="ml-0 space-y-3 sm:ml-11">
            <div className="flex flex-wrap gap-2">
              {TRANSCRIPTION_PRESETS.map((preset) => {
                const active = asrBaseURL === preset.baseUrl && asrModel === preset.model;
                return (
                  <button
                    key={preset.id}
                    type="button"
                    title={preset.description}
                    aria-pressed={active}
                    onClick={() => {
                      setValue("ai.asr_base_url", preset.baseUrl);
                      setValue("ai.asr_model", preset.model);
                    }}
                    className={cn(
                      "border-border hover:bg-accent rounded-md border px-3 py-1.5 text-xs transition-colors",
                      active && "border-primary bg-primary/5 text-primary",
                    )}
                  >
                    {preset.label}
                  </button>
                );
              })}
            </div>
            <SettingField
              label="Transcription base URL"
              value={asrBaseURL}
              onChange={(next) => setValue("ai.asr_base_url", next)}
              hint="http://speaches:8000 or https://api.groq.com/openai"
            />
            {speechUsesTextEndpoint && (
              <div className="flex max-w-2xl gap-2 rounded-md border border-amber-500/25 bg-amber-500/5 px-3 py-2 text-xs leading-relaxed">
                <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-amber-600" />
                <span>
                  Uses the Text translation endpoint and API key. This only works when that provider
                  implements OpenAI-compatible <code>/audio/transcriptions</code> with timestamped
                  segments. Test it before enabling audio generation.
                </span>
              </div>
            )}
            <SettingField
              label="Transcription model"
              value={asrModel}
              onChange={(next) => setValue("ai.asr_model", next)}
              hint="whisper-large-v3-turbo or whisper-1"
            />
            <SettingField
              label="Transcription API key"
              type="password"
              value={value("ai.asr_api_key")}
              onChange={(next) => setValue("ai.asr_api_key", next)}
              sensitiveConfigured={form.sensitiveConfigured.includes("ai.asr_api_key")}
              hint="Optional. A saved or inherited key is reused for tests only when the endpoint host is unchanged."
            />
            <p className="text-muted-foreground max-w-2xl text-xs leading-relaxed">
              For self-hosted services, use a hostname or IP reachable from the Silo container.
              <code className="mx-1">localhost</code>
              points back to Silo itself.
            </p>
            <ConnectionCheckAction
              onClick={checkSpeechConnection}
              result={speechResult}
              isPending={speechCheck.isPending}
              disabled={form.isSaving || !speechCheckable}
              label="Test Speech-to-Text"
              pendingLabel="Testing Speech-to-Text..."
            />
          </div>
        </section>

        <section className="space-y-5 py-6">
          <div>
            <h3 className="text-sm font-semibold">Features</h3>
            <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
              Generated subtitles and translated metadata are saved once and served to every client
              through Silo&apos;s normal pipelines.
            </p>
          </div>

          <div className="max-w-3xl divide-y">
            <div className="py-1">
              <SettingField
                label="Subtitle translation"
                type="toggle"
                value={value("subtitle_ai.enabled", "false")}
                onChange={(next) => setValue("subtitle_ai.enabled", next)}
                hint="Text AI required - Translates an existing text subtitle track. Whisper is not used."
              />
              <RequirementNote
                label="Text AI required"
                ready={textReady}
                detail="Uses the chat endpoint above."
              />
            </div>
            <div className="py-1">
              <SettingField
                label="Subtitle generation from audio"
                type="toggle"
                value={value("subtitle_ai.transcribe_enabled", "false")}
                onChange={(next) => setValue("subtitle_ai.transcribe_enabled", next)}
                hint="Speech-to-text required - Uses Whisper to create timed subtitles from the selected audio track."
              />
              <RequirementNote
                label="Speech-to-text required"
                ready={speechReady}
                detail="Also uses Text AI when the requested subtitle language differs from the audio language."
              />
            </div>
            <div className="py-1">
              <SettingField
                label="Description translation"
                type="toggle"
                value={value("metadata_ai.enabled", "false")}
                onChange={(next) => setValue("metadata_ai.enabled", next)}
                hint="Text AI required - Translates overviews and taglines from the metadata editor or library refresh."
              />
              <RequirementNote
                label="Text AI required"
                ready={textReady}
                detail="Whisper is not used."
              />
            </div>
            <div className="py-2">
              <SettingField
                label="On-view description translation"
                type="select"
                value={value("metadata_ai.on_view", "off")}
                onChange={(next) => setValue("metadata_ai.on_view", next)}
                disabled={!descriptionEnabled}
                options={[
                  { value: "off", label: "Off" },
                  { value: "button", label: "Translate button on detail pages" },
                  { value: "auto", label: "Automatic on view" },
                ]}
                hint={
                  descriptionEnabled
                    ? "Controls viewer-triggered description translation."
                    : "Inactive until Description translation is enabled."
                }
              />
            </div>
          </div>
        </section>

        <section className="py-6">
          <details className="group">
            <summary className="flex cursor-pointer list-none items-center justify-between gap-3">
              <div>
                <h3 className="text-sm font-semibold">Advanced</h3>
                <p className="text-muted-foreground mt-1 text-xs">
                  Job concurrency, translation batching, transcription chunks, and account quotas.
                </p>
              </div>
              <ChevronDown className="text-muted-foreground size-4 transition-transform group-open:rotate-180" />
            </summary>
            <div className="mt-5 max-w-3xl space-y-1 border-t pt-4">
              <SettingField
                label="Max concurrent AI jobs"
                type="number"
                value={effectiveValue(
                  "ai.max_concurrent_jobs",
                  "subtitle_ai.max_concurrent_jobs",
                  "2",
                )}
                onChange={(next) => setValue("ai.max_concurrent_jobs", next)}
                hint="Shared by subtitle translation, speech-to-text, and description translation. Changing this value requires a server restart."
              />
              <SettingField
                label="Subtitle batch size"
                type="number"
                value={value("subtitle_ai.batch_size", "40")}
                onChange={(next) => setValue("subtitle_ai.batch_size", next)}
                hint="Text cues sent in each translation request."
              />
              <SettingField
                label="Subtitle context lines"
                type="number"
                value={value("subtitle_ai.context_neighbors", "2")}
                onChange={(next) => setValue("subtitle_ai.context_neighbors", next)}
                hint="Previous source cues included for scene continuity."
              />
              <SettingField
                label="Transcription chunk length (seconds)"
                type="number"
                value={value("subtitle_ai.asr_chunk_seconds", "600")}
                onChange={(next) => setValue("subtitle_ai.asr_chunk_seconds", next)}
                hint="60-600. Shorter chunks reduce timestamp drift but make more requests."
              />
              <SettingField
                label="Transcription limit per account"
                type="number"
                value={value("subtitle_ai.transcribe_quota_jobs", "0")}
                onChange={(next) => setValue("subtitle_ai.transcribe_quota_jobs", next)}
                hint="0 = unlimited. Profiles share their account's limit."
              />
              <SettingField
                label="Transcription limit period"
                type="select"
                value={value("subtitle_ai.transcribe_quota_period", "day")}
                onChange={(next) => setValue("subtitle_ai.transcribe_quota_period", next)}
                options={QUOTA_PERIODS.map((period) => ({
                  value: period,
                  label: `Per ${period} (rolling ${QUOTA_PERIOD_WINDOW_LABELS[period]})`,
                }))}
                hint="Rolling window used for the account limit."
              />
            </div>
          </details>
        </section>
      </div>

      <div className="bg-muted/30 mt-6 flex flex-col gap-3 rounded-md px-4 py-3 text-sm sm:flex-row sm:items-center sm:justify-between">
        <div>
          <p className="font-medium">Recommendation embeddings are configured separately</p>
          <p className="text-muted-foreground mt-0.5 text-xs">
            Search vectors and recommendations do not use the translation or speech endpoints above.
          </p>
        </div>
        <a
          href="/admin/recommendations"
          className="text-primary inline-flex shrink-0 items-center gap-1 text-xs font-medium hover:underline"
        >
          Open Recommendations
          <ExternalLink className="size-3.5" />
        </a>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={() => void save()}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}
