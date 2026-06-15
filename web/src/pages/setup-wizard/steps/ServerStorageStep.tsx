import { useState, useEffect, useMemo } from "react";
import type { FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiClientError, api } from "@/api/client";
import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { AlertCircle, CheckCircle2, ChevronRight, Download } from "lucide-react";
import { toast } from "sonner";
import {
  useCheckAdminSettingsConnection,
  useInstallJellyfinCompatWeb,
  useJellyfinCompatStatus,
} from "@/hooks/queries/admin/settings";
import { hasPinnedJellyfinWebInstalled } from "@/lib/jellyfinCompat";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "@/pages/admin-settings/SettingField";
import { useWizardContext } from "../WizardContext";

const SERVER_KEYS = [
  "redis.url",
  "playback.ffmpeg_path",
  "playback.transcode_dir",
  "playback.hw_accel",
  "playback.transcode_enabled",
  "jellyfin_compat.enabled",
  "jellyfin_compat.public_url",
  "jellyfin_compat.server_name",
  "jellyfin_compat.web_version",
  "jellyfin_compat.web_install_dir",
];

const PUBLIC_S3_KEYS = [
  "s3.public_endpoint",
  "s3.public_bucket",
  "s3.public_key_prefix",
  "s3.public_access_key",
  "s3.public_secret_key",
  "s3.public_url_auth",
  "s3.public_read_endpoint",
];

const PRIVATE_S3_KEYS = [
  "s3.private_endpoint",
  "s3.private_bucket",
  "s3.private_key_prefix",
  "s3.private_access_key",
  "s3.private_secret_key",
];

const META_KEYS = ["metadata.cache_images"];

const ALL_KEYS = [...SERVER_KEYS, ...PUBLIC_S3_KEYS, ...PRIVATE_S3_KEYS, ...META_KEYS];

async function fetchSettingValue(key: string): Promise<string | null> {
  try {
    const result = await api<{ key: string; value: string }>(
      `/admin/settings/${encodeURIComponent(key)}`,
    );
    return result?.value ?? null;
  } catch (err) {
    if (err instanceof ApiClientError && err.status === 404) return null;
    throw err;
  }
}

function Section({
  label,
  description,
  children,
}: {
  label: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border-foreground/[0.07] bg-foreground/[0.03] space-y-4 rounded-xl border px-4 py-4">
      <div>
        <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.1em] uppercase">
          {label}
        </p>
        {description && <p className="text-muted-foreground/70 mt-0.5 text-xs">{description}</p>}
      </div>
      {children}
    </div>
  );
}

function StorageBlock({
  title,
  expanded,
  onToggle,
  children,
}: {
  title: string;
  expanded: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="border-foreground/[0.07] bg-foreground/[0.03] rounded-xl border px-4 py-4">
      <button
        type="button"
        onClick={onToggle}
        className="text-muted-foreground hover:text-foreground flex w-full items-center gap-2 text-left transition-colors"
      >
        <ChevronRight
          className={`h-3.5 w-3.5 transition-transform duration-200 ${expanded ? "rotate-90" : ""}`}
        />
        <span className="text-[11px] font-semibold tracking-[0.1em] uppercase">{title}</span>
      </button>

      {expanded && (
        <div className="mt-4 animate-[fade-in_0.15s_ease-out] space-y-4">{children}</div>
      )}
    </div>
  );
}

function KeyPrefixField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs">Key Prefix</Label>
      <Input value={value} onChange={(e) => onChange(e.target.value)} placeholder="silo/dev" />
      <p className="text-muted-foreground/70 text-xs">
        Optional. Stores all Silo objects under this folder inside the bucket. Leave blank to use
        the bucket root.
      </p>
    </div>
  );
}

function statusLabel(value?: string): string {
  if (!value) return "Unknown";
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

export function ServerStorageStep() {
  const { markDone } = useWizardContext();
  const form = useSettingsForm({ keys: useMemo(() => ALL_KEYS, []) });
  const redisConnectionCheck = useCheckAdminSettingsConnection();
  const publicS3ConnectionCheck = useCheckAdminSettingsConnection();
  const privateS3ConnectionCheck = useCheckAdminSettingsConnection();
  const jellyfinStatusQuery = useJellyfinCompatStatus();
  const installJellyfinWeb = useInstallJellyfinCompatWeb();
  const [submitting, setSubmitting] = useState(false);
  const [jellyfinWebInstallRequested, setJellyfinWebInstallRequested] = useState(false);
  const [publicExpanded, setPublicExpanded] = useState(true);
  const [privateExpanded, setPrivateExpanded] = useState(false);
  const [redisHydrated, setRedisHydrated] = useState(false);
  const [redisConnectionResult, setRedisConnectionResult] =
    useState<ConnectionCheckResponse | null>(null);
  const [publicS3ConnectionResult, setPublicS3ConnectionResult] =
    useState<ConnectionCheckResponse | null>(null);
  const [privateS3ConnectionResult, setPrivateS3ConnectionResult] =
    useState<ConnectionCheckResponse | null>(null);
  const redisQuery = useQuery({
    queryKey: ["setup-wizard", "setting", "redis.url"],
    queryFn: () => fetchSettingValue("redis.url"),
  });

  useEffect(() => {
    if (redisHydrated || !redisQuery.data) return;
    setRedisHydrated(true);
    form.setValue("redis.url", redisQuery.data);
  }, [redisQuery.data, redisHydrated, form]);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const shouldInstallJellyfinWeb = jellyfinWebInstallRequested && !pinnedJellyfinWebInstalled;
    if (form.dirtyCount === 0 && !shouldInstallJellyfinWeb) {
      markDone("server");
      return;
    }

    setSubmitting(true);
    try {
      if (form.dirtyCount > 0) {
        await form.save();
        toast.success("Server settings saved");
      }
      if (shouldInstallJellyfinWeb) {
        const version = form.getValue("jellyfin_compat.web_version").trim();
        await installJellyfinWeb.mutateAsync(version ? { version } : {});
      }
      markDone("server");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save server settings");
    } finally {
      setSubmitting(false);
    }
  }

  function handleSkip() {
    markDone("server");
  }

  async function handleRedisCheck() {
    try {
      setRedisConnectionResult(
        await redisConnectionCheck.mutateAsync({
          kind: "redis",
          body: form.buildConnectionCheckRequest(["redis.url"]),
        }),
      );
    } catch (error) {
      setRedisConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  async function handlePublicS3Check() {
    try {
      setPublicS3ConnectionResult(
        await publicS3ConnectionCheck.mutateAsync({
          kind: "s3_public",
          body: form.buildConnectionCheckRequest(PUBLIC_S3_KEYS),
        }),
      );
    } catch (error) {
      setPublicS3ConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  async function handlePrivateS3Check() {
    try {
      setPrivateS3ConnectionResult(
        await privateS3ConnectionCheck.mutateAsync({
          kind: "s3_private",
          body: form.buildConnectionCheckRequest(PRIVATE_S3_KEYS),
        }),
      );
    } catch (error) {
      setPrivateS3ConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  if (form.isLoading || jellyfinStatusQuery.isLoading) {
    return (
      <div className="space-y-3">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-24 w-full rounded-xl" />
        ))}
      </div>
    );
  }

  const publicURLAuth = form.getValue("s3.public_url_auth") || "presigned";
  const jellyfinEnabledValue = form.getValue("jellyfin_compat.enabled");
  const jellyfinStatus = jellyfinStatusQuery.data;
  const jellyfinAPIEnabled =
    jellyfinEnabledValue === ""
      ? Boolean(jellyfinStatus?.enabled)
      : jellyfinEnabledValue === "true";
  const jellyfinOperationRunning =
    jellyfinStatus?.operation?.state === "running" ||
    jellyfinStatus?.web_state === "installing" ||
    jellyfinStatus?.web_state === "removing";
  const jellyfinMissingPrerequisites =
    jellyfinStatus?.prerequisites?.filter((item) => !item.available) ?? [];
  const jellyfinSettingsDirty = form.dirtyKeys.some((key) => key.startsWith("jellyfin_compat."));
  const pinnedJellyfinWebInstalled = hasPinnedJellyfinWebInstalled(jellyfinStatus);

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <Section label="Redis" description="Required for multi-node deployments.">
        <Input
          id="setup-redis-url"
          type="password"
          value={form.getValue("redis.url")}
          onChange={(e) => form.setValue("redis.url", e.target.value)}
          placeholder="redis://localhost:6379"
        />
        <ConnectionCheckAction
          onClick={handleRedisCheck}
          result={redisConnectionResult}
          isPending={redisConnectionCheck.isPending}
          disabled={submitting || form.isSaving}
        />
      </Section>

      <Section label="Playback">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="setup-ffmpeg-path" className="text-xs">
              FFmpeg path
            </Label>
            <Input
              id="setup-ffmpeg-path"
              value={form.getValue("playback.ffmpeg_path")}
              onChange={(e) => form.setValue("playback.ffmpeg_path", e.target.value)}
              placeholder="/usr/lib/jellyfin-ffmpeg/ffmpeg"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="setup-transcode-dir" className="text-xs">
              Transcode directory
            </Label>
            <Input
              id="setup-transcode-dir"
              value={form.getValue("playback.transcode_dir")}
              onChange={(e) => form.setValue("playback.transcode_dir", e.target.value)}
              placeholder="/tmp/silo-transcode"
            />
          </div>
        </div>
        <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="setup-hw-accel" className="text-xs">
              Hardware accel
            </Label>
            <Select
              value={form.getValue("playback.hw_accel") || "auto"}
              onValueChange={(v) => form.setValue("playback.hw_accel", v)}
            >
              <SelectTrigger id="setup-hw-accel" className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">Auto</SelectItem>
                <SelectItem value="vaapi">VAAPI</SelectItem>
                <SelectItem value="nvenc">NVENC</SelectItem>
                <SelectItem value="qsv">QSV</SelectItem>
                <SelectItem value="none">None</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex items-center gap-2 pb-1.5">
            <Switch
              id="setup-transcode-enabled"
              checked={form.getValue("playback.transcode_enabled") !== "false"}
              onCheckedChange={(v) =>
                form.setValue("playback.transcode_enabled", v ? "true" : "false")
              }
            />
            <Label htmlFor="setup-transcode-enabled" className="text-xs">
              Transcoding
            </Label>
          </div>
        </div>
      </Section>

      <Section
        label="Jellyfin-compatible app support"
        description="For VidHub, Findroid, Infuse, and other Jellyfin clients."
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="border-foreground/[0.06] bg-background/40 rounded-lg border px-3 py-3">
            <p className="text-xs font-medium">API layer</p>
            <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
              Lets Jellyfin-compatible apps discover Silo, sign in, browse libraries, fetch
              metadata, and start playback through Silo's compatibility API.
            </p>
          </div>
          <div className="border-foreground/[0.06] bg-background/40 rounded-lg border px-3 py-3">
            <p className="text-xs font-medium">Web UI layer</p>
            <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
              Downloads and builds Jellyfin Web assets for clients that expect Jellyfin's web route.
            </p>
          </div>
        </div>
        <div className="mb-4 flex items-center gap-2 pb-1">
          <Switch
            id="setup-jellyfin-enabled"
            checked={jellyfinAPIEnabled}
            onCheckedChange={(v) => {
              form.setValue("jellyfin_compat.enabled", v ? "true" : "false");
              if (!v) setJellyfinWebInstallRequested(false);
            }}
          />
          <Label htmlFor="setup-jellyfin-enabled" className="text-xs">
            Enable Jellyfin-compatible API
          </Label>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="setup-jellyfin-url" className="text-xs">
              Public URL
            </Label>
            <Input
              id="setup-jellyfin-url"
              value={form.getValue("jellyfin_compat.public_url")}
              onChange={(e) => form.setValue("jellyfin_compat.public_url", e.target.value)}
              placeholder="http://your-server:8096"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="setup-jellyfin-name" className="text-xs">
              Server name
            </Label>
            <Input
              id="setup-jellyfin-name"
              value={form.getValue("jellyfin_compat.server_name")}
              onChange={(e) => form.setValue("jellyfin_compat.server_name", e.target.value)}
              placeholder="Silo"
            />
          </div>
        </div>
        <div className="border-foreground/[0.07] mt-4 space-y-3 border-t pt-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="setup-jellyfin-web-version" className="text-xs">
                Pinned Web version
              </Label>
              <Input
                id="setup-jellyfin-web-version"
                value={form.getValue("jellyfin_compat.web_version")}
                onChange={(e) => form.setValue("jellyfin_compat.web_version", e.target.value)}
                placeholder="Auto-select compatible release"
              />
              <p className="text-muted-foreground/70 text-xs">
                Optional. Leave blank to use the latest compatible released Jellyfin Web patch.
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="setup-jellyfin-web-install-dir" className="text-xs">
                Web install directory
              </Label>
              <Input
                id="setup-jellyfin-web-install-dir"
                value={form.getValue("jellyfin_compat.web_install_dir")}
                onChange={(e) => form.setValue("jellyfin_compat.web_install_dir", e.target.value)}
                placeholder="Use Silo managed directory"
              />
              <p className="text-muted-foreground/70 text-xs">
                Optional. Defaults to{" "}
                <span className="font-mono">/var/lib/silo/compat/jellyfin-web</span>.
              </p>
            </div>
          </div>

          <div className="grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2">
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">Web UI status</span>
              <span>{statusLabel(jellyfinStatus?.web_state)}</span>
            </div>
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">Pinned version</span>
              <span>{jellyfinStatus?.pinned_version || "Not set"}</span>
            </div>
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">Installed version</span>
              <span>{jellyfinStatus?.installed_version || "Not installed"}</span>
            </div>
            <div className="space-y-0.5 sm:col-span-2">
              <span className="text-muted-foreground">Install path</span>
              <div className="truncate font-mono">{jellyfinStatus?.install_path || "Not set"}</div>
            </div>
          </div>

          {jellyfinStatus?.last_error && (
            <div className="text-destructive flex items-start gap-2 text-xs">
              <AlertCircle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
              <span>{jellyfinStatus.last_error}</span>
            </div>
          )}

          {jellyfinStatus?.operation?.state === "running" && (
            <div className="border-border/70 bg-muted/30 flex items-start gap-2 rounded-lg border px-3 py-2 text-xs">
              <CheckCircle2 className="text-muted-foreground mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
              <span className="text-muted-foreground leading-relaxed">
                Jellyfin Web install is running.
              </span>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {!pinnedJellyfinWebInstalled && (
              <Button
                type="button"
                size="sm"
                variant="default"
                disabled={
                  !jellyfinAPIEnabled ||
                  installJellyfinWeb.isPending ||
                  jellyfinOperationRunning ||
                  jellyfinStatus?.installer_ready === false
                }
                onClick={() => setJellyfinWebInstallRequested((requested) => !requested)}
              >
                {jellyfinWebInstallRequested ? (
                  <CheckCircle2 className="mr-2 h-4 w-4" />
                ) : (
                  <Download className="mr-2 h-4 w-4" />
                )}
                {jellyfinWebInstallRequested
                  ? "Web UI will be installed"
                  : jellyfinStatus?.web_state === "update_available"
                    ? "Update Web UI"
                    : jellyfinOperationRunning || installJellyfinWeb.isPending
                      ? "Web UI Busy"
                      : "Install Web UI"}
              </Button>
            )}
            {pinnedJellyfinWebInstalled && (
              <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
                <CheckCircle2 className="h-3.5 w-3.5" />
                Pinned Web UI version installed
              </span>
            )}
            {jellyfinStatus?.license_present && jellyfinStatus?.provenance_present && (
              <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
                <CheckCircle2 className="h-3.5 w-3.5" />
                License and provenance files found
              </span>
            )}
            {!jellyfinAPIEnabled && !pinnedJellyfinWebInstalled && (
              <span className="text-muted-foreground text-xs">
                Enable the Jellyfin-compatible API before installing Web UI.
              </span>
            )}
            {jellyfinSettingsDirty && !jellyfinWebInstallRequested && (
              <span className="text-muted-foreground text-xs">
                Pending Jellyfin settings will be saved when you continue.
              </span>
            )}
            {jellyfinMissingPrerequisites.length > 0 && (
              <span className="text-muted-foreground text-xs">
                Missing installer prerequisites:{" "}
                {jellyfinMissingPrerequisites.map((item) => item.command).join(", ")}
              </span>
            )}
          </div>
        </div>
      </Section>

      <StorageBlock
        title="Public Assets Storage (S3)"
        expanded={publicExpanded}
        onToggle={() => setPublicExpanded((value) => !value)}
      >
        <p className="text-muted-foreground/80 text-xs leading-relaxed">
          Stores client-facing assets such as artwork, chapter thumbnails, and subtitle files.
          Private bucket + presigned URLs is fully supported. A public bucket is optional.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label className="text-xs">Endpoint</Label>
            <Input
              value={form.getValue("s3.public_endpoint")}
              onChange={(e) => form.setValue("s3.public_endpoint", e.target.value)}
              placeholder="https://s3.amazonaws.com"
            />
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Bucket</Label>
            <Input
              value={form.getValue("s3.public_bucket")}
              onChange={(e) => form.setValue("s3.public_bucket", e.target.value)}
            />
          </div>
        </div>
        <KeyPrefixField
          value={form.getValue("s3.public_key_prefix")}
          onChange={(v) => form.setValue("s3.public_key_prefix", v)}
        />
        <div className="grid gap-3 sm:grid-cols-2">
          <SettingField
            label="Access Key"
            type="password"
            value={form.getValue("s3.public_access_key")}
            onChange={(v) => form.setValue("s3.public_access_key", v)}
            sensitiveConfigured={form.sensitiveConfigured.includes("s3.public_access_key")}
          />
          <SettingField
            label="Secret Key"
            type="password"
            value={form.getValue("s3.public_secret_key")}
            onChange={(v) => form.setValue("s3.public_secret_key", v)}
            sensitiveConfigured={form.sensitiveConfigured.includes("s3.public_secret_key")}
          />
        </div>
        <div className="border-foreground/[0.06] border-t pt-3">
          <SettingField
            label="URL Auth Method"
            type="select"
            value={publicURLAuth}
            onChange={(v) => form.setValue("s3.public_url_auth", v)}
            options={[
              { value: "presigned", label: "S3 Presigned URLs (Recommended)" },
              { value: "public", label: "Public (no auth)" },
              { value: "cloudflare_token", label: "Cloudflare Token Auth" },
            ]}
          />
          {publicURLAuth !== "presigned" && (
            <SettingField
              label="Read Endpoint"
              value={form.getValue("s3.public_read_endpoint")}
              onChange={(v) => form.setValue("s3.public_read_endpoint", v)}
              hint="https://cdn.example.com"
            />
          )}
        </div>
        <ConnectionCheckAction
          onClick={handlePublicS3Check}
          result={publicS3ConnectionResult}
          isPending={publicS3ConnectionCheck.isPending}
          disabled={submitting || form.isSaving}
        />
      </StorageBlock>

      <StorageBlock
        title="Private Internal Storage (S3)"
        expanded={privateExpanded}
        onToggle={() => setPrivateExpanded((value) => !value)}
      >
        <p className="text-muted-foreground/80 text-xs leading-relaxed">
          Stores non-public Silo objects such as imports, exports, and internal artifacts.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label className="text-xs">Endpoint</Label>
            <Input
              value={form.getValue("s3.private_endpoint")}
              onChange={(e) => form.setValue("s3.private_endpoint", e.target.value)}
              placeholder="https://s3.amazonaws.com"
            />
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Bucket</Label>
            <Input
              value={form.getValue("s3.private_bucket")}
              onChange={(e) => form.setValue("s3.private_bucket", e.target.value)}
            />
          </div>
        </div>
        <KeyPrefixField
          value={form.getValue("s3.private_key_prefix")}
          onChange={(v) => form.setValue("s3.private_key_prefix", v)}
        />
        <div className="grid gap-3 sm:grid-cols-2">
          <SettingField
            label="Access Key"
            type="password"
            value={form.getValue("s3.private_access_key")}
            onChange={(v) => form.setValue("s3.private_access_key", v)}
            sensitiveConfigured={form.sensitiveConfigured.includes("s3.private_access_key")}
          />
          <SettingField
            label="Secret Key"
            type="password"
            value={form.getValue("s3.private_secret_key")}
            onChange={(v) => form.setValue("s3.private_secret_key", v)}
            sensitiveConfigured={form.sensitiveConfigured.includes("s3.private_secret_key")}
          />
        </div>
        <ConnectionCheckAction
          onClick={handlePrivateS3Check}
          result={privateS3ConnectionResult}
          isPending={privateS3ConnectionCheck.isPending}
          disabled={submitting || form.isSaving}
        />
      </StorageBlock>

      <div className="border-foreground/[0.07] bg-foreground/[0.03] rounded-xl border px-4 py-4">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.1em] uppercase">
              Image caching
            </p>
            <p className="text-muted-foreground/70 mt-0.5 text-xs">
              Store artwork in public asset storage instead of proxying external URLs.
            </p>
          </div>
          <Switch
            id="setup-cache-images"
            checked={form.getValue("metadata.cache_images") === "true"}
            onCheckedChange={(v) => form.setValue("metadata.cache_images", v ? "true" : "false")}
            className="ml-4 shrink-0"
          />
        </div>
      </div>

      <div className="flex gap-3 pt-4">
        <Button type="submit" disabled={submitting || form.isSaving}>
          {submitting || form.isSaving ? "Saving..." : "Save & continue"}
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={handleSkip}
          disabled={submitting || form.isSaving}
        >
          Skip
        </Button>
      </div>
    </form>
  );
}
