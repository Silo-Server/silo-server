import { useMemo, useState } from "react";
import { AlertTriangle } from "lucide-react";
import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const PUBLIC_S3_KEYS = [
  "s3.public_endpoint",
  "s3.public_region",
  "s3.public_path_style",
  "s3.public_bucket",
  "s3.public_key_prefix",
  "s3.public_access_key",
  "s3.public_secret_key",
  "s3.public_read_endpoint",
  "s3.public_url_auth",
  "s3.public_token_secret",
  "s3.public_token_param",
  "s3.public_token_ttl",
] as const;

// Changing any of these moves where cached artwork objects live; the server
// reconciles the artwork cache after a restart (see reconcile_artwork_cache).
const PUBLIC_S3_IDENTITY_KEYS = [
  "s3.public_endpoint",
  "s3.public_bucket",
  "s3.public_key_prefix",
] as const;

const PRIVATE_S3_KEYS = [
  "s3.private_endpoint",
  "s3.private_region",
  "s3.private_path_style",
  "s3.private_bucket",
  "s3.private_key_prefix",
  "s3.private_access_key",
  "s3.private_secret_key",
] as const;

const KEYS = [
  ...PUBLIC_S3_KEYS,
  ...PRIVATE_S3_KEYS,
  "s3.user_db_endpoint",
  "s3.user_db_region",
  "s3.user_db_path_style",
  "s3.user_db_bucket",
  "s3.user_db_key_prefix",
  "s3.user_db_access_key",
  "s3.user_db_secret_key",
];

function KeyPrefixField({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="space-y-1 py-2">
      <Label htmlFor={id} className="text-sm font-medium">
        Key Prefix
      </Label>
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="max-w-md"
        placeholder="silo/dev"
      />
      <p className="text-muted-foreground text-xs">
        Optional. Stores all Silo objects under this folder inside the bucket. Leave blank to use
        the bucket root.
      </p>
    </div>
  );
}

export default function StorageSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const publicCheckConnection = useCheckAdminSettingsConnection();
  const privateCheckConnection = useCheckAdminSettingsConnection();
  const [publicConnectionResult, setPublicConnectionResult] =
    useState<ConnectionCheckResponse | null>(null);
  const [privateConnectionResult, setPrivateConnectionResult] =
    useState<ConnectionCheckResponse | null>(null);

  async function handleCheckPublicConnection() {
    try {
      setPublicConnectionResult(
        await publicCheckConnection.mutateAsync({
          kind: "s3_public",
          body: form.buildConnectionCheckRequest([...PUBLIC_S3_KEYS]),
        }),
      );
    } catch (error) {
      setPublicConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  async function handleCheckPrivateConnection() {
    try {
      setPrivateConnectionResult(
        await privateCheckConnection.mutateAsync({
          kind: "s3_private",
          body: form.buildConnectionCheckRequest([...PRIVATE_S3_KEYS]),
        }),
      );
    } catch (error) {
      setPrivateConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  const publicURLAuth = form.getValue("s3.public_url_auth") || "presigned";

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Storage</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure separate S3-compatible storage for client-facing assets and private internal
          Silo artifacts.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <Tabs defaultValue="public">
          <TabsList className="surface-panel-subtle h-auto gap-1 rounded-[1.1rem] border-0 bg-transparent p-1">
            <TabsTrigger value="public">Public Assets</TabsTrigger>
            <TabsTrigger value="private">Private Internal</TabsTrigger>
            <TabsTrigger value="userdb" disabled title="Reserved for future Litestream replication">
              User DB
            </TabsTrigger>
          </TabsList>

          <TabsContent value="public" className="space-y-1 pt-4">
            <p className="text-muted-foreground mb-2 text-sm">
              Stores client-facing assets such as artwork, chapter thumbnails, and subtitle files.
            </p>
            <p className="text-muted-foreground mb-4 text-xs leading-relaxed">
              This bucket does not need to be public. Most installs should keep it private and use
              presigned URLs. Only use Public or Cloudflare Token modes if you want direct
              CDN/object access.
            </p>
            <SettingField
              label="Endpoint"
              value={form.getValue("s3.public_endpoint")}
              onChange={(v) => form.setValue("s3.public_endpoint", v)}
            />
            <SettingField
              label="Region"
              value={form.getValue("s3.public_region")}
              onChange={(v) => form.setValue("s3.public_region", v)}
            />
            <SettingField
              label="Path Style"
              type="toggle"
              value={form.getValue("s3.public_path_style")}
              onChange={(v) => form.setValue("s3.public_path_style", v)}
            />
            <SettingField
              label="Bucket"
              value={form.getValue("s3.public_bucket")}
              onChange={(v) => form.setValue("s3.public_bucket", v)}
            />
            <KeyPrefixField
              id="s3-public-key-prefix"
              value={form.getValue("s3.public_key_prefix")}
              onChange={(v) => form.setValue("s3.public_key_prefix", v)}
            />
            {PUBLIC_S3_IDENTITY_KEYS.some((key) => form.isDirty(key)) && (
              <div className="my-3 flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-4">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
                <div className="text-[13px] leading-relaxed">
                  <p className="font-medium text-amber-500">Storage location change</p>
                  <p className="text-muted-foreground mt-1">
                    Artwork is cached in this bucket. After the server restarts, Silo verifies the
                    cache against the new storage and automatically re-caches anything missing.
                    Uploaded images (custom posters, collection artwork, branding) cannot be
                    re-downloaded — migrate your bucket contents if you want to keep them.
                  </p>
                </div>
              </div>
            )}
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

            <ConnectionCheckAction
              onClick={handleCheckPublicConnection}
              result={publicConnectionResult}
              isPending={publicCheckConnection.isPending}
              disabled={form.isSaving}
            />

            <div className="border-border mt-6 border-t pt-6">
              <h3 className="mb-1 text-sm font-medium">Asset URL Authentication</h3>
              <p className="text-muted-foreground mb-4 text-xs leading-relaxed">
                Controls how client-facing asset URLs are generated. Presigned URLs are recommended
                and work with private buckets.
              </p>
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
              {publicURLAuth === "cloudflare_token" && (
                <>
                  <SettingField
                    label="Token Secret"
                    type="password"
                    value={form.getValue("s3.public_token_secret")}
                    onChange={(v) => form.setValue("s3.public_token_secret", v)}
                    sensitiveConfigured={form.sensitiveConfigured.includes(
                      "s3.public_token_secret",
                    )}
                  />
                  <SettingField
                    label="Token Param"
                    value={form.getValue("s3.public_token_param") || "verify"}
                    onChange={(v) => form.setValue("s3.public_token_param", v)}
                    hint="verify"
                  />
                  <SettingField
                    label="Token TTL (seconds)"
                    type="number"
                    value={form.getValue("s3.public_token_ttl") || "10800"}
                    onChange={(v) => form.setValue("s3.public_token_ttl", v)}
                  />
                </>
              )}
            </div>
          </TabsContent>

          <TabsContent value="private" className="space-y-1 pt-4">
            <p className="text-muted-foreground mb-4 text-sm">
              Stores non-public Silo objects such as imports, exports, and internal artifacts.
            </p>
            <SettingField
              label="Endpoint"
              value={form.getValue("s3.private_endpoint")}
              onChange={(v) => form.setValue("s3.private_endpoint", v)}
            />
            <SettingField
              label="Region"
              value={form.getValue("s3.private_region")}
              onChange={(v) => form.setValue("s3.private_region", v)}
            />
            <SettingField
              label="Path Style"
              type="toggle"
              value={form.getValue("s3.private_path_style")}
              onChange={(v) => form.setValue("s3.private_path_style", v)}
            />
            <SettingField
              label="Bucket"
              value={form.getValue("s3.private_bucket")}
              onChange={(v) => form.setValue("s3.private_bucket", v)}
            />
            <KeyPrefixField
              id="s3-private-key-prefix"
              value={form.getValue("s3.private_key_prefix")}
              onChange={(v) => form.setValue("s3.private_key_prefix", v)}
            />
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

            <ConnectionCheckAction
              onClick={handleCheckPrivateConnection}
              result={privateConnectionResult}
              isPending={privateCheckConnection.isPending}
              disabled={form.isSaving}
            />
          </TabsContent>

          <TabsContent value="userdb" className="space-y-1 pt-4 opacity-50">
            <p className="text-muted-foreground mb-4 text-sm">
              Reserved for Litestream user database replication. Not currently in use.
            </p>
            <SettingField
              label="Endpoint"
              value={form.getValue("s3.user_db_endpoint")}
              onChange={(v) => form.setValue("s3.user_db_endpoint", v)}
              disabled
            />
            <SettingField
              label="Region"
              value={form.getValue("s3.user_db_region")}
              onChange={(v) => form.setValue("s3.user_db_region", v)}
              disabled
            />
            <SettingField
              label="Path Style"
              type="toggle"
              value={form.getValue("s3.user_db_path_style")}
              onChange={(v) => form.setValue("s3.user_db_path_style", v)}
              disabled
            />
            <SettingField
              label="Bucket"
              value={form.getValue("s3.user_db_bucket")}
              onChange={(v) => form.setValue("s3.user_db_bucket", v)}
              disabled
            />
            <SettingField
              label="Access Key"
              type="password"
              value={form.getValue("s3.user_db_access_key")}
              onChange={(v) => form.setValue("s3.user_db_access_key", v)}
              sensitiveConfigured={form.sensitiveConfigured.includes("s3.user_db_access_key")}
              disabled
            />
            <SettingField
              label="Secret Key"
              type="password"
              value={form.getValue("s3.user_db_secret_key")}
              onChange={(v) => form.setValue("s3.user_db_secret_key", v)}
              sensitiveConfigured={form.sensitiveConfigured.includes("s3.user_db_secret_key")}
              disabled
            />
          </TabsContent>
        </Tabs>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}
