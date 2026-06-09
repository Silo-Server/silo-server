import { useState, useEffect } from "react";
import { toast } from "sonner";
import {
  useSubtitleProviders,
  useUpdateSubtitleProvider,
  useTestSubtitleProvider,
} from "@/hooks/queries/admin/subtitles";
import {
  useAdminSensitiveStatus,
  useAdminServerSettings,
  useUpdateServerSetting,
} from "@/hooks/queries/admin/settings";
import type { SubtitleProviderConfig } from "@/api/types";

import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Eye, EyeOff } from "lucide-react";
import { CredentialStatus } from "./CredentialStatus";
import { SettingField } from "./SettingField";

// ============================================================================
// Search providers
// ============================================================================

const SUBTITLE_PROVIDER_NAMES: Record<string, string> = {
  opensubtitles: "OpenSubtitles",
  subdl: "SubDL",
  subsource: "SubSource",
};

interface SubtitleProviderFormState {
  enabled: boolean;
  api_key: string;
  username: string;
  password: string;
  showApiKey: boolean;
}

interface SubtitleTestResult {
  success: boolean;
  error?: string;
}

function defaultSubtitleFormState(config: SubtitleProviderConfig): SubtitleProviderFormState {
  return {
    enabled: config.enabled,
    api_key: "",
    username: "",
    password: "",
    showApiKey: false,
  };
}

function SubtitleProviderCard({ config }: { config: SubtitleProviderConfig }) {
  const [form, setForm] = useState<SubtitleProviderFormState>(() =>
    defaultSubtitleFormState(config),
  );
  const [testResult, setTestResult] = useState<SubtitleTestResult | null>(null);

  const updateProvider = useUpdateSubtitleProvider();
  const testProvider = useTestSubtitleProvider();

  useEffect(() => {
    setForm((prev) => ({
      ...prev,
      enabled: config.enabled,
    }));
  }, [config.enabled]);

  const providerName = config.provider_name;
  const displayName = SUBTITLE_PROVIDER_NAMES[providerName] ?? providerName;
  const isOpenSubtitles = providerName === "opensubtitles";

  function handleSave() {
    updateProvider.mutate({
      provider: providerName,
      config: {
        enabled: form.enabled,
        ...(isOpenSubtitles
          ? { username: form.username, password: form.password }
          : { api_key: form.api_key }),
      },
    });
  }

  function handleTest() {
    setTestResult(null);
    testProvider.mutate(providerName, {
      onSuccess: (result) => {
        setTestResult({ success: result.success, error: result.error });
      },
      onError: (err) => {
        setTestResult({
          success: false,
          error: err instanceof Error ? err.message : "Test failed",
        });
      },
    });
  }

  return (
    <div className="border-border bg-surface space-y-4 rounded-lg border px-5 py-4">
      {/* Header row */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span className="text-sm font-semibold">{displayName}</span>
          <CredentialStatus
            configured={isOpenSubtitles ? config.has_credentials : config.has_api_key}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${providerName}-enabled`} className="text-sm font-medium">
            {form.enabled ? "Enabled" : "Disabled"}
          </Label>
          <Switch
            id={`${providerName}-enabled`}
            checked={form.enabled}
            onCheckedChange={(checked) => setForm((prev) => ({ ...prev, enabled: checked }))}
          />
        </div>
      </div>

      {/* Credentials: username/password for OpenSubtitles, API key for others */}
      {isOpenSubtitles ? (
        <>
          <div className="space-y-1">
            <Label htmlFor={`${providerName}-username`} className="text-sm font-medium">
              Username
            </Label>
            <Input
              id={`${providerName}-username`}
              type="text"
              placeholder={
                config.has_credentials ? "Leave blank to keep current" : "OpenSubtitles username"
              }
              value={form.username}
              onChange={(e) => setForm((prev) => ({ ...prev, username: e.target.value }))}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor={`${providerName}-password`} className="text-sm font-medium">
              Password
            </Label>
            <Input
              id={`${providerName}-password`}
              type="password"
              placeholder={
                config.has_credentials ? "Leave blank to keep current" : "OpenSubtitles password"
              }
              value={form.password}
              onChange={(e) => setForm((prev) => ({ ...prev, password: e.target.value }))}
            />
          </div>
        </>
      ) : (
        <div className="space-y-1">
          <Label htmlFor={`${providerName}-api-key`} className="text-sm font-medium">
            API Key
          </Label>
          <div className="flex items-center gap-2">
            <Input
              id={`${providerName}-api-key`}
              type={form.showApiKey ? "text" : "password"}
              placeholder={config.has_api_key ? "Leave blank to keep current" : "Enter API key"}
              value={form.api_key}
              onChange={(e) => setForm((prev) => ({ ...prev, api_key: e.target.value }))}
              className="flex-1"
            />
            <Button
              variant="ghost"
              size="icon"
              type="button"
              onClick={() => setForm((prev) => ({ ...prev, showApiKey: !prev.showApiKey }))}
            >
              {form.showApiKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
            </Button>
          </div>
        </div>
      )}

      {/* Actions */}
      <div className="flex items-center gap-3 pt-1">
        <Button variant="outline" onClick={handleTest} disabled={testProvider.isPending}>
          {testProvider.isPending ? "Testing..." : "Test Connection"}
        </Button>
        <Button onClick={handleSave} disabled={updateProvider.isPending}>
          {updateProvider.isPending ? "Saving..." : "Save"}
        </Button>
        {testResult !== null && (
          <span className={`text-sm ${testResult.success ? "text-green-500" : "text-red-500"}`}>
            {testResult.success
              ? "Connection successful"
              : (testResult.error ?? "Connection failed")}
          </span>
        )}
      </div>
    </div>
  );
}

const SUBTITLE_PROVIDER_ORDER = ["opensubtitles", "subdl", "subsource"];

function SearchProvidersContent() {
  const { data, isLoading } = useSubtitleProviders();

  if (isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  const providers = data?.providers ?? [];

  // Sort by known order, putting unknown providers at end
  const sorted = [...providers].sort((a, b) => {
    const ai = SUBTITLE_PROVIDER_ORDER.indexOf(a.provider_name);
    const bi = SUBTITLE_PROVIDER_ORDER.indexOf(b.provider_name);
    if (ai === -1 && bi === -1) return 0;
    if (ai === -1) return 1;
    if (bi === -1) return -1;
    return ai - bi;
  });

  return (
    <div className="space-y-4">
      <p className="text-muted-foreground max-w-3xl text-sm">
        Configure external subtitle search providers. Credentials are stored securely and never
        returned by the API.
      </p>

      <div className="max-w-2xl space-y-4">
        {sorted.map((provider) => (
          <SubtitleProviderCard key={provider.provider_name} config={provider} />
        ))}
        {sorted.length === 0 && (
          <div className="border-border bg-surface rounded-lg border px-5 py-4">
            <p className="text-muted-foreground text-sm">No subtitle providers configured.</p>
          </div>
        )}
      </div>
    </div>
  );
}

// ============================================================================
// AI translation
// ============================================================================

function AISubtitleTranslationCard() {
  const { data: settings } = useAdminServerSettings();
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();

  const apiKeyConfigured = new Set(sensitive?.configured ?? []).has("subtitle_ai.api_key");

  const [enabled, setEnabled] = useState("false");
  const [baseUrl, setBaseUrl] = useState("");
  const [chatModel, setChatModel] = useState("");
  const [maxConcurrent, setMaxConcurrent] = useState("2");
  const [apiKey, setApiKey] = useState("");

  // Hydrate the form from current server settings once loaded.
  useEffect(() => {
    if (!settings) return;
    setEnabled(settings["subtitle_ai.enabled"] ?? "false");
    setBaseUrl(settings["subtitle_ai.base_url"] ?? "https://api.openai.com");
    setChatModel(settings["subtitle_ai.chat_model"] ?? "gpt-4o-mini");
    setMaxConcurrent(settings["subtitle_ai.max_concurrent_jobs"] ?? "2");
  }, [settings]);

  function save() {
    const trimmedBaseUrl = baseUrl.trim();
    const trimmedChatModel = chatModel.trim();
    const parsedMaxConcurrent = Number.parseInt(maxConcurrent, 10);

    // Don't let an admin persist a config that would break translation for
    // everyone (a blank endpoint/model when enabled, or a bad concurrency value).
    if (enabled === "true" && (trimmedBaseUrl === "" || trimmedChatModel === "")) {
      toast.error("Base URL and chat model are required to enable AI translation.");
      return;
    }
    if (!Number.isInteger(parsedMaxConcurrent) || parsedMaxConcurrent < 1) {
      toast.error("Max concurrent jobs must be a positive whole number.");
      return;
    }

    const updates = [
      updateSetting.mutateAsync({ key: "subtitle_ai.enabled", value: enabled }),
      updateSetting.mutateAsync({ key: "subtitle_ai.base_url", value: trimmedBaseUrl }),
      updateSetting.mutateAsync({ key: "subtitle_ai.chat_model", value: trimmedChatModel }),
      updateSetting.mutateAsync({
        key: "subtitle_ai.max_concurrent_jobs",
        value: String(parsedMaxConcurrent),
      }),
    ];
    if (apiKey.trim() !== "") {
      updates.push(updateSetting.mutateAsync({ key: "subtitle_ai.api_key", value: apiKey }));
    }
    void Promise.all(updates).then(() => setApiKey(""));
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">AI Subtitle Translation</h3>
          <p className="text-muted-foreground text-xs">
            On-demand subtitle translation via any OpenAI-compatible chat API (OpenAI, Groq, a local
            Ollama server, …). Translated tracks are generated once on the server and served to
            every client.
          </p>
        </div>
        <CredentialStatus configured={apiKeyConfigured} />
      </div>
      <SettingField
        label="Enabled"
        type="toggle"
        value={enabled}
        onChange={setEnabled}
        hint="Show the “Translate with AI” action in the player."
      />
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
        hint="e.g. gpt-4o-mini, llama3.1"
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
        label="Max concurrent jobs"
        type="number"
        value={maxConcurrent}
        onChange={setMaxConcurrent}
        hint="Caps simultaneous translations so they don't starve transcodes."
      />
      <div className="pt-2">
        <Button type="button" onClick={save} disabled={updateSetting.isPending}>
          {updateSetting.isPending ? "Saving..." : "Save AI Translation Settings"}
        </Button>
        <p className="text-muted-foreground mt-2 text-xs">
          Changes take effect after a server restart.
        </p>
      </div>
    </div>
  );
}

export default function SubtitlesSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Subtitles</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Search providers for downloading subtitles and AI translation for generating new language
          tracks.
        </p>
      </div>

      <div className="space-y-8">
        <SearchProvidersContent />
        <AISubtitleTranslationCard />
      </div>
    </div>
  );
}
