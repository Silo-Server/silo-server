import { useState, useEffect } from "react";
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
import { Eye, EyeOff, CircleCheck, CircleAlert } from "lucide-react";
import { SettingField } from "./SettingField";

// ============================================================================
// Subtitles
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

function SubtitleCredentialStatus({ configured }: { configured: boolean }) {
  if (configured) {
    return (
      <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
        <CircleCheck className="h-3.5 w-3.5 text-green-500" />
        Configured
      </span>
    );
  }
  return (
    <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
      <CircleAlert className="h-3.5 w-3.5 text-yellow-500" />
      Not configured
    </span>
  );
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
          <SubtitleCredentialStatus
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

function SubtitlesContent() {
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

interface WatchProviderCredentials {
  key: string;
  displayName: string;
}

const WATCH_PROVIDER_CREDENTIALS: WatchProviderCredentials[] = [
  { key: "trakt", displayName: "Trakt" },
  { key: "simkl", displayName: "Simkl" },
];

function WatchProviderCredentialCard({ provider }: { provider: WatchProviderCredentials }) {
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const configured = new Set(sensitive?.configured ?? []);
  const clientIdKey = `watchsync.${provider.key}.client_id`;
  const clientSecretKey = `watchsync.${provider.key}.client_secret`;

  function save() {
    const updates = [];
    if (clientId.trim() !== "") {
      updates.push(updateSetting.mutateAsync({ key: clientIdKey, value: clientId }));
    }
    if (clientSecret.trim() !== "") {
      updates.push(
        updateSetting.mutateAsync({
          key: clientSecretKey,
          value: clientSecret,
        }),
      );
    }
    void Promise.all(updates).then(() => {
      setClientId("");
      setClientSecret("");
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{provider.displayName}</h3>
          <p className="text-muted-foreground text-xs">
            OAuth credentials for profile connections.
          </p>
        </div>
        <SubtitleCredentialStatus
          configured={configured.has(clientIdKey) && configured.has(clientSecretKey)}
        />
      </div>
      <SettingField
        label="Client ID"
        type="password"
        value={clientId}
        onChange={setClientId}
        sensitiveConfigured={configured.has(clientIdKey)}
        hint="Leave blank to keep the current value."
      />
      <SettingField
        label="Client Secret"
        type="password"
        value={clientSecret}
        onChange={setClientSecret}
        sensitiveConfigured={configured.has(clientSecretKey)}
        hint="Leave blank to keep the current value."
      />
      <Button type="button" onClick={save} disabled={updateSetting.isPending}>
        {updateSetting.isPending ? "Saving..." : `Save ${provider.displayName} Credentials`}
      </Button>
    </div>
  );
}

function WatchProviderCredentialsContent() {
  return (
    <div className="space-y-4">
      {WATCH_PROVIDER_CREDENTIALS.map((provider) => (
        <WatchProviderCredentialCard key={provider.key} provider={provider} />
      ))}
    </div>
  );
}

function MDBListCredentialCard() {
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();
  const [apiKey, setApiKey] = useState("");
  const configured = new Set(sensitive?.configured ?? []).has("mdblist.api_key");

  function save() {
    if (apiKey.trim() === "") return;
    void updateSetting.mutateAsync({ key: "mdblist.api_key", value: apiKey }).then(() => {
      setApiKey("");
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">MDBList</h3>
          <p className="text-muted-foreground text-xs">
            Enables list search/browse when users add MDBList collections. Importing a list by URL
            works without a key — only discovery requires one. Get a free key at{" "}
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
        </div>
        <SubtitleCredentialStatus configured={configured} />
      </div>
      <SettingField
        label="API Key"
        type="password"
        value={apiKey}
        onChange={setApiKey}
        sensitiveConfigured={configured}
        hint="Leave blank to keep the current value."
      />
      <Button type="button" onClick={save} disabled={updateSetting.isPending}>
        {updateSetting.isPending ? "Saving..." : "Save MDBList API Key"}
      </Button>
    </div>
  );
}

function IntroDBCredentialCard() {
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();
  const [apiKey, setApiKey] = useState("");
  const configured = new Set(sensitive?.configured ?? []).has("introdb.api_key");

  function save() {
    void updateSetting.mutateAsync({ key: "introdb.api_key", value: apiKey }).then(() => {
      setApiKey("");
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">TheIntroDB</h3>
          <p className="text-muted-foreground text-xs">
            Community-sourced intro, recap, credits, and preview timestamps. Read access is free and
            requires no key. Supplying your{" "}
            <a
              href="https://theintrodb.org/profile"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              TheIntroDB
            </a>{" "}
            API key lets the server use your pending submissions before they're community-verified.
          </p>
        </div>
        <SubtitleCredentialStatus configured={configured} />
      </div>
      <SettingField
        label="API Key (optional)"
        type="password"
        value={apiKey}
        onChange={setApiKey}
        sensitiveConfigured={configured}
        hint="Leave blank to use anonymous read access."
      />
      <Button type="button" onClick={save} disabled={updateSetting.isPending}>
        {updateSetting.isPending ? "Saving..." : "Save TheIntroDB API Key"}
      </Button>
    </div>
  );
}

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
    const updates = [
      updateSetting.mutateAsync({ key: "subtitle_ai.enabled", value: enabled }),
      updateSetting.mutateAsync({ key: "subtitle_ai.base_url", value: baseUrl }),
      updateSetting.mutateAsync({ key: "subtitle_ai.chat_model", value: chatModel }),
      updateSetting.mutateAsync({ key: "subtitle_ai.max_concurrent_jobs", value: maxConcurrent }),
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
        <SubtitleCredentialStatus configured={apiKeyConfigured} />
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
      </div>
    </div>
  );
}

export default function IntegrationsSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6">
        <h2 className="text-lg font-semibold">Integrations</h2>
        <p className="text-muted-foreground text-sm">External services and provider credentials</p>
      </div>

      <div className="mb-8">
        <WatchProviderCredentialsContent />
      </div>
      <div className="mb-8">
        <MDBListCredentialCard />
      </div>
      <div className="mb-8">
        <IntroDBCredentialCard />
      </div>
      <div className="mb-8">
        <AISubtitleTranslationCard />
      </div>
      <SubtitlesContent />
    </div>
  );
}
