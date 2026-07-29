import { useState, useEffect } from "react";
import {
  useSubtitleProviders,
  useUpdateSubtitleProvider,
  useTestSubtitleProvider,
} from "@/hooks/queries/admin/subtitles";
import type { SubtitleProviderConfig } from "@/api/types";

import { Button } from "@/components/ui/button";
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
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Eye, EyeOff } from "lucide-react";
import { CredentialStatus } from "./CredentialStatus";

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

function subtitleProviderDraft(form: SubtitleProviderFormState, withAccount: boolean) {
  const user = form.username;
  const pass = form.password;
  const key = form.api_key;
  return {
    enabled: form.enabled,
    ...(withAccount ? { username: user, password: pass } : { api_key: key }),
  };
}

function SubtitleProviderCard({ config }: { config: SubtitleProviderConfig }) {
  const [form, setForm] = useState<SubtitleProviderFormState>(() =>
    defaultSubtitleFormState(config),
  );
  const [testResult, setTestResult] = useState<SubtitleTestResult | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);

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
  const credentialsConfigured =
    (isOpenSubtitles && config.has_credentials) || (!isOpenSubtitles && config.has_api_key);

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
    testProvider.mutate(
      {
        provider: providerName,
        config: subtitleProviderDraft(form, isOpenSubtitles),
      },
      {
        onSuccess: (result) => {
          setTestResult({ success: result.success, error: result.error });
        },
        onError: (err) => {
          setTestResult({
            success: false,
            error: err instanceof Error ? err.message : "Test failed",
          });
        },
      },
    );
  }

  return (
    <fieldset
      disabled={updateProvider.isPending || testProvider.isPending}
      className="border-border bg-surface space-y-4 rounded-lg border px-5 py-4"
    >
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
        {credentialsConfigured && (
          <Button
            variant="ghost"
            onClick={() => setConfirmClear(true)}
            disabled={updateProvider.isPending}
          >
            Clear credentials
          </Button>
        )}
        {testResult !== null && (
          <span className={`text-sm ${testResult.success ? "text-green-500" : "text-red-500"}`}>
            {testResult.success
              ? "Connection successful"
              : (testResult.error ?? "Connection failed")}
          </span>
        )}
      </div>
      <p className="text-muted-foreground text-xs">
        Test Connection uses the values currently entered above. Saving applies provider changes
        live to new searches.
      </p>
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clear {displayName} credentials?</AlertDialogTitle>
            <AlertDialogDescription>
              The provider will be disabled and removed from live subtitle searches immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={updateProvider.isPending}
              onClick={() =>
                updateProvider.mutate(
                  { provider: providerName, config: { enabled: false, clear_credentials: true } },
                  {
                    onSuccess: () => {
                      setForm(defaultSubtitleFormState({ ...config, enabled: false }));
                      setTestResult(null);
                      setConfirmClear(false);
                    },
                  },
                )
              }
            >
              {updateProvider.isPending ? "Clearing..." : "Clear and disable"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </fieldset>
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

export default function SubtitlesSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Subtitles</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Search providers for downloading subtitles. AI translation and transcription live under AI
          Services.
        </p>
      </div>

      <div className="space-y-8">
        <SearchProvidersContent />
      </div>
    </div>
  );
}
