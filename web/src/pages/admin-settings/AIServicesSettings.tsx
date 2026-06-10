import { useState, useEffect } from "react";
import { toast } from "sonner";
import {
  useAdminSensitiveStatus,
  useAdminServerSettings,
  useUpdateServerSetting,
} from "@/hooks/queries/admin/settings";

import { Button } from "@/components/ui/button";
import { CredentialStatus } from "./CredentialStatus";
import { SettingField } from "./SettingField";

// Connection settings live under the ai.* keys; reads fall back to the legacy
// subtitle_ai.* rows (mirroring the server's loader) so an existing setup
// shows its effective values, while saves always write the new keys.

function AIConnectionCard() {
  const { data: settings } = useAdminServerSettings();
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();

  const configuredKeys = new Set(sensitive?.configured ?? []);
  const apiKeyConfigured =
    configuredKeys.has("ai.api_key") || configuredKeys.has("subtitle_ai.api_key");
  const asrApiKeyConfigured = configuredKeys.has("ai.asr_api_key");

  const [baseUrl, setBaseUrl] = useState("");
  const [chatModel, setChatModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [asrModel, setAsrModel] = useState("");
  const [asrBaseUrl, setAsrBaseUrl] = useState("");
  const [asrApiKey, setAsrApiKey] = useState("");
  const [maxConcurrent, setMaxConcurrent] = useState("2");

  useEffect(() => {
    if (!settings) return;
    setBaseUrl(
      settings["ai.base_url"] ?? settings["subtitle_ai.base_url"] ?? "https://api.openai.com",
    );
    setChatModel(settings["ai.chat_model"] ?? settings["subtitle_ai.chat_model"] ?? "gpt-4o-mini");
    setAsrModel(settings["ai.asr_model"] ?? "whisper-1");
    setAsrBaseUrl(settings["ai.asr_base_url"] ?? "");
    setMaxConcurrent(
      settings["ai.max_concurrent_jobs"] ?? settings["subtitle_ai.max_concurrent_jobs"] ?? "2",
    );
  }, [settings]);

  function save() {
    const trimmedBaseUrl = baseUrl.trim();
    const trimmedChatModel = chatModel.trim();
    const parsedMaxConcurrent = Number.parseInt(maxConcurrent, 10);

    if (trimmedBaseUrl === "" || trimmedChatModel === "") {
      toast.error("Base URL and chat model are required.");
      return;
    }
    if (!Number.isInteger(parsedMaxConcurrent) || parsedMaxConcurrent < 1) {
      toast.error("Max concurrent jobs must be a positive whole number.");
      return;
    }

    const updates = [
      updateSetting.mutateAsync({ key: "ai.base_url", value: trimmedBaseUrl }),
      updateSetting.mutateAsync({ key: "ai.chat_model", value: trimmedChatModel }),
      updateSetting.mutateAsync({ key: "ai.asr_model", value: asrModel.trim() }),
      updateSetting.mutateAsync({ key: "ai.asr_base_url", value: asrBaseUrl.trim() }),
      updateSetting.mutateAsync({
        key: "ai.max_concurrent_jobs",
        value: String(parsedMaxConcurrent),
      }),
    ];
    if (apiKey.trim() !== "") {
      updates.push(updateSetting.mutateAsync({ key: "ai.api_key", value: apiKey }));
    }
    if (asrApiKey.trim() !== "") {
      updates.push(updateSetting.mutateAsync({ key: "ai.asr_api_key", value: asrApiKey }));
    }
    void Promise.all(updates).then(() => {
      setApiKey("");
      setAsrApiKey("");
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Endpoint</h3>
          <p className="text-muted-foreground text-xs">
            One OpenAI-compatible endpoint shared by every AI feature (OpenAI, Groq, a local Ollama
            server, …). Transcription can use a separate Whisper-compatible server.
          </p>
        </div>
        <CredentialStatus configured={apiKeyConfigured} />
      </div>
      <SettingField
        label="Base URL"
        type="text"
        value={baseUrl}
        onChange={setBaseUrl}
        hint="https://api.openai.com"
      />
      <SettingField
        label="Chat model"
        type="text"
        value={chatModel}
        onChange={setChatModel}
        hint="Used for subtitle and description translation, e.g. gpt-4o-mini, llama3.1"
      />
      <SettingField
        label="API Key"
        type="password"
        value={apiKey}
        onChange={setApiKey}
        sensitiveConfigured={apiKeyConfigured}
        hint="Leave blank to keep current. Empty is fine for keyless local servers."
      />
      <SettingField
        label="Transcription model"
        type="text"
        value={asrModel}
        onChange={setAsrModel}
        hint="Whisper model for subtitle generation, e.g. whisper-1"
      />
      <SettingField
        label="Transcription base URL"
        type="text"
        value={asrBaseUrl}
        onChange={setAsrBaseUrl}
        hint="Optional separate Whisper server (faster-whisper, speaches, …). Blank uses the base URL."
      />
      <SettingField
        label="Transcription API key"
        type="password"
        value={asrApiKey}
        onChange={setAsrApiKey}
        sensitiveConfigured={asrApiKeyConfigured}
        hint="Optional; blank uses the main API key."
      />
      <SettingField
        label="Max concurrent jobs"
        type="number"
        value={maxConcurrent}
        onChange={setMaxConcurrent}
        hint="One shared cap across subtitle translation, transcription, and description translation."
      />
      <div className="pt-2">
        <Button type="button" onClick={save} disabled={updateSetting.isPending}>
          {updateSetting.isPending ? "Saving..." : "Save Endpoint Settings"}
        </Button>
        <p className="text-muted-foreground mt-2 text-xs">
          Changes take effect after a server restart.
        </p>
      </div>
    </div>
  );
}

function AIFeaturesCard() {
  const { data: settings } = useAdminServerSettings();
  const updateSetting = useUpdateServerSetting();

  const [subtitleTranslate, setSubtitleTranslate] = useState("false");
  const [transcribe, setTranscribe] = useState("false");
  const [metadataTranslate, setMetadataTranslate] = useState("false");
  const [batchSize, setBatchSize] = useState("40");
  const [contextNeighbors, setContextNeighbors] = useState("2");

  useEffect(() => {
    if (!settings) return;
    setSubtitleTranslate(settings["subtitle_ai.enabled"] ?? "false");
    setTranscribe(settings["subtitle_ai.transcribe_enabled"] ?? "false");
    setMetadataTranslate(settings["metadata_ai.enabled"] ?? "false");
    setBatchSize(settings["subtitle_ai.batch_size"] ?? "40");
    setContextNeighbors(settings["subtitle_ai.context_neighbors"] ?? "2");
  }, [settings]);

  function save() {
    const parsedBatch = Number.parseInt(batchSize, 10);
    const parsedNeighbors = Number.parseInt(contextNeighbors, 10);
    if (!Number.isInteger(parsedBatch) || parsedBatch < 1) {
      toast.error("Batch size must be a positive whole number.");
      return;
    }
    if (!Number.isInteger(parsedNeighbors) || parsedNeighbors < 0) {
      toast.error("Context lines must be zero or a positive whole number.");
      return;
    }
    void Promise.all([
      updateSetting.mutateAsync({ key: "subtitle_ai.enabled", value: subtitleTranslate }),
      updateSetting.mutateAsync({ key: "subtitle_ai.transcribe_enabled", value: transcribe }),
      updateSetting.mutateAsync({ key: "metadata_ai.enabled", value: metadataTranslate }),
      updateSetting.mutateAsync({ key: "subtitle_ai.batch_size", value: String(parsedBatch) }),
      updateSetting.mutateAsync({
        key: "subtitle_ai.context_neighbors",
        value: String(parsedNeighbors),
      }),
    ]);
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-2">
        <h3 className="text-sm font-semibold">Features</h3>
        <p className="text-muted-foreground text-xs">
          Everything runs once on the server and is served to every client through the normal
          subtitle and metadata pipelines.
        </p>
      </div>
      <SettingField
        label="Subtitle translation"
        type="toggle"
        value={subtitleTranslate}
        onChange={setSubtitleTranslate}
        hint="Show the “Translate with AI” action in the player."
      />
      <SettingField
        label="Subtitle generation from audio"
        type="toggle"
        value={transcribe}
        onChange={setTranscribe}
        hint="Whisper transcription — generates subtitle tracks for media with no usable text subtitles."
      />
      <SettingField
        label="Description translation"
        type="toggle"
        value={metadataTranslate}
        onChange={setMetadataTranslate}
        hint="Translate overviews and taglines from the metadata editor, plus the per-library auto-translate option."
      />
      <SettingField
        label="Subtitle batch size"
        type="number"
        value={batchSize}
        onChange={setBatchSize}
        hint="Cues per translation request."
      />
      <SettingField
        label="Subtitle context lines"
        type="number"
        value={contextNeighbors}
        onChange={setContextNeighbors}
        hint="Preceding source cues sent for scene continuity across batches."
      />
      <div className="pt-2">
        <Button type="button" onClick={save} disabled={updateSetting.isPending}>
          {updateSetting.isPending ? "Saving..." : "Save Feature Settings"}
        </Button>
        <p className="text-muted-foreground mt-2 text-xs">
          Changes take effect after a server restart.
        </p>
      </div>
    </div>
  );
}

export default function AIServicesSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">AI Services</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Shared AI endpoint and feature toggles for subtitle translation, subtitle generation from
          audio, and description translation.
        </p>
      </div>

      <div className="space-y-8">
        <AIConnectionCard />
        <AIFeaturesCard />
      </div>
    </div>
  );
}
