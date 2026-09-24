import { useMemo, useRef, useState } from "react";
import { AlertTriangle, Plus, RotateCcw, Trash2 } from "lucide-react";

import type {
  ConnectionCheckResponse,
  StorageTransitionFailureCategory,
  StorageTransitionJobResult,
  StorageTransitionPhase,
} from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAdminServerStatus,
  useCancelStorageTransition,
  useCheckAdminSettingsConnection,
  useCreateStorageTransition,
  useStorageTransitionCapabilities,
  useStorageTransitionSourceHealth,
  type StorageTransitionPolicy,
} from "@/hooks/queries/admin/settings";
import { useAdminTaskJobs } from "@/hooks/queries/admin/taskJobs";
import { useRestartKeys, type RestartKeyMatcher } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { toast } from "sonner";

import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";
import { USER_DATABASE_BACKEND_OPTIONS } from "./databaseSettingOptions";
import { cleanPath } from "./settingsPathDefaults";
import {
  LOG_LEVEL_OPTIONS,
  OPSLOG_BUCKET_POLICIES_KEY,
  OPSLOG_MAX_ROWS_KEY,
  OPSLOG_MAX_SIZE_MB_KEY,
  OPSLOG_RETENTION_DAYS_KEY,
  appendBucketRow,
  bucketRowsFromRaw,
  recommendedBucketRows,
  removeBucketRow,
  serializeBucketRows,
  updateBucketRow,
  type LogRetentionBucketPolicy,
  type LogRetentionBucketRow,
} from "./logRetentionPolicy";

type SettingsForm = ReturnType<typeof useSettingsForm>;

const REDIS_KEYS = ["redis.url"];

const DATABASE_KEYS = [
  "database.max_connections",
  "userdb.backend",
  "userdb.pool_max_open",
  "userdb.idle_timeout",
];

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
];

// Changing any of these moves where cached artwork objects live. Once artwork
// has been stored in S3, direct settings writes are rejected and this page
// routes the edit through the managed storage transition flow instead.
const PUBLIC_S3_IDENTITY_KEYS = ["s3.public_endpoint", "s3.public_bucket", "s3.public_key_prefix"];

const PRIVATE_S3_KEYS = [
  "s3.private_endpoint",
  "s3.private_region",
  "s3.private_path_style",
  "s3.private_bucket",
  "s3.private_key_prefix",
  "s3.private_access_key",
  "s3.private_secret_key",
];

const PRIVATE_S3_IDENTITY_KEYS = [
  "s3.private_endpoint",
  "s3.private_bucket",
  "s3.private_key_prefix",
];
const S3_IDENTITY_KEYS = [...PUBLIC_S3_IDENTITY_KEYS, ...PRIVATE_S3_IDENTITY_KEYS];
const STORAGE_LOCATION_KEYS = new Set([
  "artwork.storage_backend",
  "artwork.local_path",
  ...S3_IDENTITY_KEYS,
]);
const STORAGE_TRANSITION_KEYS = new Set([
  "artwork.storage_backend",
  "artwork.local_path",
  ...PUBLIC_S3_KEYS,
  ...PRIVATE_S3_KEYS,
]);

// The overall trim limits are what an admin comes here to change; the policy
// decision log and the per-area rules are debugging tools behind Advanced.
const LOG_ESSENTIAL_KEYS = [OPSLOG_RETENTION_DAYS_KEY, OPSLOG_MAX_ROWS_KEY, OPSLOG_MAX_SIZE_MB_KEY];

const LOG_ADVANCED_KEYS = [
  "policy.decision_log_retention_days",
  "policy.decision_log_verbosity",
  "policy.decision_log_scope_sample_rate",
  OPSLOG_BUCKET_POLICIES_KEY,
];

const LOG_KEYS = [...LOG_ESSENTIAL_KEYS, ...LOG_ADVANCED_KEYS];

const KEYS = [
  "artwork.storage_backend",
  "artwork.local_path",
  ...REDIS_KEYS,
  ...DATABASE_KEYS,
  ...PUBLIC_S3_KEYS,
  ...PRIVATE_S3_KEYS,
  ...LOG_KEYS,
];

function countDirty(form: SettingsForm, keys: string[]): number {
  return keys.filter((key) => form.isDirty(key)).length;
}

// Whether a group may claim "changes apply after a restart" for all of its
// fields at once. Computed from the server's restart registry rather than
// asserted, so converting one key to hot-reload silently demotes its group to
// per-field chips instead of leaving a false blanket claim — the Logs group
// below is exactly that case today.
function allRestart(restartKeys: RestartKeyMatcher, keys: string[]): boolean {
  return keys.every((key) => restartKeys.has(key));
}

function transitionPhaseLabel(phase: StorageTransitionPhase | undefined): string {
  switch (phase) {
    case "queued":
      return "Waiting to start";
    case "checking_target":
      return "Checking target storage";
    case "copying":
      return "Copying storage objects";
    case "verifying":
      return "Verifying copied objects";
    case "committing":
      return "Saving storage settings";
    case "restart_pending":
      return "Restart required to activate storage";
    case "completed":
      return "Storage transition completed";
    case "failed":
      return "Storage transition failed";
    case "canceled":
      return "Storage transition canceled";
    default:
      return "Preparing storage transition";
  }
}

function transitionFailureLabel(category: StorageTransitionFailureCategory | undefined): string {
  switch (category) {
    case "preparation_failed":
      return "Storage transition preparation failed.";
    case "target_check_failed":
      return "Target storage could not be verified.";
    case "copy_failed":
      return "Copying storage objects failed.";
    case "verification_failed":
      return "Storage verification failed.";
    case "commit_failed":
      return "Storage settings could not be saved.";
    default:
      return "The storage transition did not complete.";
  }
}

function verifiedObjectsLabel(count: number): string {
  return `${count} ${count === 1 ? "object" : "objects"} verified`;
}

function recoveryStateLabel(state: string | undefined): string {
  switch (state) {
    case "running":
      return "running";
    case "waiting_retry":
      return "waiting to retry";
    case "blocked":
      return "blocked";
    default:
      return "pending";
  }
}

function recoveryStateMessage(state: string | undefined): string {
  switch (state) {
    case "running":
      return "Reconciling committed artwork storage.";
    case "waiting_retry":
      return "Reconciliation paused; waiting to retry.";
    case "blocked":
      return "Recovery is blocked. Check administrator diagnostics for details.";
    default:
      return "Artwork reconciliation is pending after restart.";
  }
}

/**
 * Shared credential-draft helpers for every secret on the page. Every editor
 * is frozen while a save is in flight so a late keystroke cannot ride along
 * with it; emptying an input reverts to the saved value (`keepSaved`), and
 * erasing one for real takes the explicit `clearSaved` action.
 */
interface SecretEditors {
  keepSaved: (key: string) => void;
  clearSaved: (key: string) => void;
  setSecret: (key: string, value: string) => void;
  disabled: boolean;
}

function RedisGroup({
  form,
  restartKeys,
  secrets,
}: {
  form: SettingsForm;
  restartKeys: RestartKeyMatcher;
  secrets: SecretEditors;
}) {
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const redisUrl = form.getValue("redis.url");
  const managedByEnv = form.sensitiveManagedByEnv.includes("redis.url");
  const configured = form.sensitiveConfigured.includes("redis.url");
  const [enabledOverride, setEnabledOverride] = useState<boolean | null>(null);
  // Saving or discarding clears the toggle override so it follows the stored
  // URL again. Adjusting during render (rather than in an effect) keeps the
  // override alive while the admin is still editing.
  const [lastDirtyCount, setLastDirtyCount] = useState(form.dirtyCount);
  if (lastDirtyCount !== form.dirtyCount) {
    setLastDirtyCount(form.dirtyCount);
    if (form.dirtyCount === 0) setEnabledOverride(null);
  }
  const enabled = enabledOverride ?? (redisUrl.trim() !== "" || configured);

  async function handleCheckConnection() {
    try {
      setConnectionResult(
        await checkConnection.mutateAsync({
          kind: "redis",
          body: form.buildConnectionCheckRequest(REDIS_KEYS),
        }),
      );
    } catch (error) {
      setConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  return (
    <FieldGroup label="Redis" restartAll={allRestart(restartKeys, REDIS_KEYS)}>
      <SettingField
        label="Use Redis"
        type="toggle"
        description={
          managedByEnv ? "Set by REDIS_URL" : "Needed when running more than one server."
        }
        value={enabled ? "true" : "false"}
        onChange={(value) => {
          if (value === "true") {
            setEnabledOverride(true);
            form.resetValue("redis.url");
            return;
          }
          setEnabledOverride(false);
          form.setValue("redis.url", "");
        }}
        disabled={managedByEnv}
        restartRequired={restartKeys.has("redis.url")}
      />
      {enabled && (
        <>
          {/*
            No `onClear` here: the Use Redis switch above already stages the
            empty URL, and one clear per surface is the rule.
          */}
          <SecretField
            label="Connection URL"
            value={redisUrl}
            configured={configured}
            onKeep={() => secrets.keepSaved("redis.url")}
            onChange={(v) => secrets.setSecret("redis.url", v)}
            hint={managedByEnv ? "Value supplied by REDIS_URL" : "redis://host:6379"}
            disabled={managedByEnv || secrets.disabled}
            restartRequired={restartKeys.has("redis.url")}
          />
          {/*
            Env-managed URLs stay checkable: the field is read-only so nothing
            is dirty, and the server checks the effective value it merged from
            REDIS_URL. Only writes are refused for env-managed keys.
          */}
          <ConnectionCheckAction
            onClick={handleCheckConnection}
            result={connectionResult}
            isPending={checkConnection.isPending}
            disabled={form.isSaving}
          />
        </>
      )}
    </FieldGroup>
  );
}

// normalizeLocationValue compares storage location fields the way the server
// names stores: endpoint scheme and host and the bucket are case-insensitive,
// and a key prefix ignores its slashes. Preserve explicit ports and endpoint
// paths, including a trailing slash, as the server's URL parser does.
function normalizeLocationValue(key: string, raw: string): string {
  const value = raw.trim();
  if (key === "artwork.local_path") return value.startsWith("/") ? cleanPath(value) : value;
  if (key.endsWith("_key_prefix")) return value.replace(/^\/+|\/+$/g, "");
  if (key.endsWith("_bucket")) return value.toLowerCase();
  if (key.endsWith("_endpoint")) {
    const parts = /^([a-z][a-z0-9+.-]*):\/\/([^/?#]+)(.*)$/i.exec(value);
    const scheme = parts?.[1];
    const authority = parts?.[2];
    const rest = parts?.[3];
    if (!scheme || !authority || rest === undefined) return value.toLowerCase();
    const hostStart = authority.lastIndexOf("@") + 1;
    return `${scheme.toLowerCase()}://${authority.slice(0, hostStart)}${authority.slice(hostStart).toLowerCase()}${rest}`;
  }
  return value;
}

function effectiveArtworkBackend(backend: string, publicBucket: string): "local" | "s3" {
  const selected = backend.trim().toLowerCase();
  return selected === "s3" || (selected !== "local" && publicBucket.trim() !== "") ? "s3" : "local";
}

function S3Group({
  form,
  restartKeys,
  secrets,
  scope,
  label,
  description,
  checkKind,
  artworkLockedBackend,
  publicBackendChangePending = false,
  locationFieldsDisabled = false,
}: {
  form: SettingsForm;
  restartKeys: RestartKeyMatcher;
  secrets: SecretEditors;
  scope: "public" | "private";
  label: string;
  description: string;
  checkKind: "s3_public" | "s3_private";
  artworkLockedBackend?: string;
  publicBackendChangePending?: boolean;
  locationFieldsDisabled?: boolean;
}) {
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const keys = scope === "public" ? PUBLIC_S3_KEYS : PRIVATE_S3_KEYS;
  const key = (suffix: string) => `s3.${scope}_${suffix}`;
  const urlAuth = form.getValue("s3.public_url_auth") || "presigned";

  const advancedKeys =
    scope === "public"
      ? [
          "s3.public_region",
          "s3.public_path_style",
          "s3.public_key_prefix",
          "s3.public_url_auth",
          "s3.public_read_endpoint",
          "s3.public_token_secret",
          "s3.public_token_param",
          "s3.public_token_ttl",
        ]
      : ["s3.private_region", "s3.private_path_style", "s3.private_key_prefix"];
  const advancedCount =
    scope === "public"
      ? 4 + (urlAuth !== "presigned" ? 1 : 0) + (urlAuth === "cloudflare_token" ? 3 : 0)
      : 3;
  const advancedChanged = countDirty(form, advancedKeys);

  async function handleCheckConnection() {
    try {
      setConnectionResult(
        await checkConnection.mutateAsync({
          kind: checkKind,
          body: form.buildConnectionCheckRequest(keys),
        }),
      );
    } catch (error) {
      setConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  return (
    <FieldGroup label={label} description={description} restartAll={allRestart(restartKeys, keys)}>
      <SettingField
        label="Endpoint"
        hint="https://s3.us-east-1.amazonaws.com"
        value={form.getValue(key("endpoint"))}
        onChange={(v) => form.setValue(key("endpoint"), v)}
        disabled={secrets.disabled || locationFieldsDisabled}
        restartRequired={restartKeys.has(key("endpoint"))}
      />
      <SettingField
        label="Bucket"
        value={form.getValue(key("bucket"))}
        onChange={(v) => form.setValue(key("bucket"), v)}
        disabled={secrets.disabled || locationFieldsDisabled}
        restartRequired={restartKeys.has(key("bucket"))}
      />
      {(scope === "public" ? PUBLIC_S3_IDENTITY_KEYS : PRIVATE_S3_IDENTITY_KEYS).some((k) =>
        form.isDirty(k),
      ) && (
        <div className="settings-field-note my-3 flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-4">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <div className="text-[13px] leading-relaxed">
            <p className="font-medium text-amber-500">Storage location change</p>
            {publicBackendChangePending ||
            artworkLockedBackend === "s3" ||
            (scope === "private" && artworkLockedBackend !== undefined) ? (
              <p className="text-muted-foreground mt-1">
                {publicBackendChangePending
                  ? "Saving this bucket opens a managed transition from local storage. Choose what to copy before switching."
                  : scope === "private" && artworkLockedBackend !== "s3"
                    ? "Saving this location opens a managed transition. You can start fresh or copy avatars, diagnostics, and catalog artifacts from where they are kept now; Silo verifies the old and new locations before switching."
                    : "Saving this location opens a managed transition. You can start fresh or copy data from the current S3 storage; Silo verifies the old and new locations before switching."}
              </p>
            ) : (
              <p className="text-muted-foreground mt-1">
                {scope === "public"
                  ? "The first artwork write records this location. Uploaded posters, collection artwork, and branding cannot be re-downloaded, so choose the bucket before scanning."
                  : "Silo records a configured private bucket at startup. Choose it before storing avatars, diagnostics, or catalog artifacts; changing it later requires a managed transition."}
              </p>
            )}
          </div>
        </div>
      )}
      {/*
        The access and secret keys must stay configured together, so clearing
        one alone is refused at save time. Both carry the action, which is what
        lets an admin stage the pair and hand this bucket back to anonymous or
        instance-role access.
      */}
      <SecretField
        label="Access Key"
        value={form.getValue(key("access_key"))}
        configured={form.sensitiveConfigured.includes(key("access_key"))}
        onKeep={() => secrets.keepSaved(key("access_key"))}
        onClear={() => secrets.clearSaved(key("access_key"))}
        cleared={form.isClearStaged(key("access_key"))}
        onChange={(v) => secrets.setSecret(key("access_key"), v)}
        disabled={secrets.disabled}
        restartRequired={restartKeys.has(key("access_key"))}
      />
      <SecretField
        label="Secret Key"
        value={form.getValue(key("secret_key"))}
        configured={form.sensitiveConfigured.includes(key("secret_key"))}
        onKeep={() => secrets.keepSaved(key("secret_key"))}
        onClear={() => secrets.clearSaved(key("secret_key"))}
        cleared={form.isClearStaged(key("secret_key"))}
        onChange={(v) => secrets.setSecret(key("secret_key"), v)}
        disabled={secrets.disabled}
        restartRequired={restartKeys.has(key("secret_key"))}
      />
      <ConnectionCheckAction
        onClick={handleCheckConnection}
        result={connectionResult}
        isPending={checkConnection.isPending}
        disabled={form.isSaving}
      />

      <AdvancedSection
        id={`infrastructure.s3.${scope}`}
        count={advancedCount}
        forceOpen={advancedChanged > 0}
      >
        <SettingField
          label="Region"
          description="Leave blank unless your provider requires one."
          value={form.getValue(key("region"))}
          onChange={(v) => form.setValue(key("region"), v)}
          restartRequired={restartKeys.has(key("region"))}
        />
        <SettingField
          label="Put the bucket name in the URL path"
          type="toggle"
          description="Needed by MinIO and some self-hosted storage."
          value={form.getValue(key("path_style"))}
          onChange={(v) => form.setValue(key("path_style"), v)}
          restartRequired={restartKeys.has(key("path_style"))}
        />
        <SettingField
          label="Folder inside the bucket"
          description="Leave blank to use the bucket root."
          value={form.getValue(key("key_prefix"))}
          onChange={(v) => form.setValue(key("key_prefix"), v)}
          disabled={secrets.disabled || locationFieldsDisabled}
          restartRequired={restartKeys.has(key("key_prefix"))}
        />
        {scope === "public" && (
          <>
            <SettingField
              label="How asset links are authorized"
              type="select"
              description="Signed links work with a private bucket and suit most installs."
              value={urlAuth}
              onChange={(v) => form.setValue("s3.public_url_auth", v)}
              options={[
                { value: "presigned", label: "Signed links (recommended)" },
                { value: "public", label: "Anyone with the link" },
                { value: "cloudflare_token", label: "Cloudflare signed token" },
              ]}
              restartRequired={restartKeys.has("s3.public_url_auth")}
            />
            {urlAuth !== "presigned" && (
              <SettingField
                label="Address clients download from"
                hint="https://cdn.example.com"
                value={form.getValue("s3.public_read_endpoint")}
                onChange={(v) => form.setValue("s3.public_read_endpoint", v)}
                restartRequired={restartKeys.has("s3.public_read_endpoint")}
              />
            )}
            {urlAuth === "cloudflare_token" && (
              <>
                {/*
                  Cloudflare token auth requires this secret, so a clear only
                  saves alongside a switch back to another mode — the one order
                  that works, since the field is hidden in those modes.
                */}
                <SecretField
                  label="Token Secret"
                  value={form.getValue("s3.public_token_secret")}
                  configured={form.sensitiveConfigured.includes("s3.public_token_secret")}
                  onKeep={() => secrets.keepSaved("s3.public_token_secret")}
                  onClear={() => secrets.clearSaved("s3.public_token_secret")}
                  cleared={form.isClearStaged("s3.public_token_secret")}
                  onChange={(v) => secrets.setSecret("s3.public_token_secret", v)}
                  hint="Signing key configured in Cloudflare"
                  disabled={secrets.disabled}
                  restartRequired={restartKeys.has("s3.public_token_secret")}
                />
                <SettingField
                  label="Token query parameter"
                  description="Usually verify."
                  value={form.getValue("s3.public_token_param") || "verify"}
                  onChange={(v) => form.setValue("s3.public_token_param", v)}
                  restartRequired={restartKeys.has("s3.public_token_param")}
                />
                <SettingField
                  label="Link lifetime"
                  type="number"
                  unit="seconds"
                  value={form.getValue("s3.public_token_ttl") || "10800"}
                  onChange={(v) => form.setValue("s3.public_token_ttl", v)}
                  restartRequired={restartKeys.has("s3.public_token_ttl")}
                />
              </>
            )}
          </>
        )}
      </AdvancedSection>
    </FieldGroup>
  );
}

function DatabaseGroup({
  form,
  restartKeys,
}: {
  form: SettingsForm;
  restartKeys: RestartKeyMatcher;
}) {
  const userDBBackend = form.getValue("userdb.backend");
  const sqlite = userDBBackend === "sqlite";
  const changed = countDirty(form, DATABASE_KEYS);

  return (
    <FieldGroup label="Database" restartAll={allRestart(restartKeys, DATABASE_KEYS)}>
      <AdvancedSection id="infrastructure.database" count={sqlite ? 4 : 2} forceOpen={changed > 0}>
        <SettingField
          label="Maximum Postgres connections"
          type="number"
          description="Raise only if the logs show connection-pool waits."
          value={form.getValue("database.max_connections")}
          onChange={(v) => form.setValue("database.max_connections", v)}
          restartRequired={restartKeys.has("database.max_connections")}
        />
        <SettingField
          label="Where per-user data is stored"
          type="select"
          description="PostgreSQL is the only supported option."
          options={USER_DATABASE_BACKEND_OPTIONS}
          value={userDBBackend}
          onChange={(v) => form.setValue("userdb.backend", v)}
          restartRequired={restartKeys.has("userdb.backend")}
        />
        {sqlite && (
          <>
            <SettingField
              label="Open files per user"
              type="number"
              description="SQLite connections one user database may hold open."
              value={form.getValue("userdb.pool_max_open")}
              onChange={(v) => form.setValue("userdb.pool_max_open", v)}
              restartRequired={restartKeys.has("userdb.pool_max_open")}
            />
            <SettingField
              label="Close idle user databases after"
              type="duration"
              description="For example 12h."
              value={form.getValue("userdb.idle_timeout")}
              onChange={(v) => form.setValue("userdb.idle_timeout", v)}
              restartRequired={restartKeys.has("userdb.idle_timeout")}
            />
          </>
        )}
      </AdvancedSection>
    </FieldGroup>
  );
}

function BucketOverridesEditor({
  rows,
  parseError,
  onChange,
  onRestore,
}: {
  rows: LogRetentionBucketRow[];
  parseError: string;
  onChange: (rows: LogRetentionBucketRow[]) => void;
  onRestore: () => void;
}) {
  function edit(id: string, field: keyof LogRetentionBucketPolicy, value: string) {
    onChange(updateBucketRow(rows, id, field, value));
  }

  return (
    <div className="space-y-4 py-3.5">
      <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
        <div className="min-w-0">
          <h4 className="text-sm font-medium">Per-area limits</h4>
          <p className="text-muted-foreground mt-1 text-xs">A limit of 0 turns that rule off.</p>
        </div>
        <div className="flex shrink-0 flex-col gap-2 sm:flex-row sm:items-center">
          <Button type="button" size="sm" variant="outline" onClick={onRestore}>
            <RotateCcw className="size-4" />
            Restore Recommended Rules
          </Button>
          <Button type="button" size="sm" onClick={() => onChange(appendBucketRow(rows))}>
            <Plus className="size-4" />
            Add Rule
          </Button>
        </div>
      </div>

      {parseError ? (
        <div className="border-warning/30 bg-warning/10 text-warning rounded-[1rem] border px-3 py-2 text-sm">
          The saved rules could not be read. The editor loaded the recommended rules so you can
          recover cleanly. Details: {parseError}
        </div>
      ) : null}

      <div className="border-border/70 overflow-x-auto rounded-[1rem] border">
        <table className="w-full border-collapse text-sm">
          <thead className="bg-muted/40 text-left">
            <tr>
              <th className="px-3 py-2 font-medium">Component</th>
              <th className="px-3 py-2 font-medium">Level</th>
              <th className="px-3 py-2 font-medium">Days</th>
              <th className="px-3 py-2 font-medium">Max rows</th>
              <th className="px-3 py-2 font-medium">Max size (MB)</th>
              <th className="w-[60px] px-3 py-2 font-medium"> </th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-muted-foreground px-3 py-6 text-center">
                  No per-area rules configured.
                </td>
              </tr>
            ) : (
              rows.map((row) => (
                <tr key={row.id} className="border-t">
                  <td className="px-3 py-2">
                    <Input
                      value={row.component}
                      onChange={(event) => edit(row.id, "component", event.target.value)}
                      placeholder="metadata"
                      aria-label={`Component for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Select
                      value={row.level}
                      onValueChange={(value) => edit(row.id, "level", value)}
                    >
                      <SelectTrigger className="w-[120px]">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {LOG_LEVEL_OPTIONS.map((level) => (
                          <SelectItem key={level} value={level}>
                            {level}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="number"
                      min="0"
                      value={String(row.retention_days)}
                      onChange={(event) => edit(row.id, "retention_days", event.target.value)}
                      className="w-[110px]"
                      aria-label={`Days for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="number"
                      min="0"
                      value={String(row.max_rows)}
                      onChange={(event) => edit(row.id, "max_rows", event.target.value)}
                      className="w-[140px]"
                      aria-label={`Max rows for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="number"
                      min="0"
                      value={String(row.max_size_mb)}
                      onChange={(event) => edit(row.id, "max_size_mb", event.target.value)}
                      className="w-[140px]"
                      aria-label={`Max size for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="outline"
                      onClick={() => onChange(removeBucketRow(rows, row.id))}
                      aria-label={`Remove ${row.component || "bucket"} rule`}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function LogsGroup({ form, restartKeys }: { form: SettingsForm; restartKeys: RestartKeyMatcher }) {
  // The bucket rules are one JSON setting edited as a table, so the rows live
  // here while they are dirty and re-hydrate from the saved value otherwise.
  const [draftRows, setDraftRows] = useState<LogRetentionBucketRow[] | null>(null);
  const raw = form.getValue(OPSLOG_BUCKET_POLICIES_KEY);
  const bucketDirty = form.isDirty(OPSLOG_BUCKET_POLICIES_KEY);
  const hydrated = useMemo(() => bucketRowsFromRaw(raw), [raw]);
  // A stale draft can never be shown: it is only reachable while the key is
  // dirty, and the only thing that marks it dirty also sets the draft.
  const rows = bucketDirty && draftRows ? draftRows : hydrated.rows;
  const parseError = bucketDirty ? "" : hydrated.error;
  const advancedChanged = countDirty(form, LOG_ADVANCED_KEYS);

  function commitRows(next: LogRetentionBucketRow[]) {
    setDraftRows(next);
    form.setValue(OPSLOG_BUCKET_POLICIES_KEY, serializeBucketRows(next));
  }

  return (
    <FieldGroup label="Logs">
      <SettingField
        label="Delete log entries older than"
        type="number"
        unit="days"
        value={form.getValue(OPSLOG_RETENTION_DAYS_KEY)}
        onChange={(v) => form.setValue(OPSLOG_RETENTION_DAYS_KEY, v)}
        restartRequired={restartKeys.has(OPSLOG_RETENTION_DAYS_KEY)}
      />
      <SettingField
        label="Maximum log entries"
        type="number"
        value={form.getValue(OPSLOG_MAX_ROWS_KEY)}
        onChange={(v) => form.setValue(OPSLOG_MAX_ROWS_KEY, v)}
        restartRequired={restartKeys.has(OPSLOG_MAX_ROWS_KEY)}
      />
      <SettingField
        label="Maximum log size"
        type="number"
        unit="MB"
        value={form.getValue(OPSLOG_MAX_SIZE_MB_KEY)}
        onChange={(v) => form.setValue(OPSLOG_MAX_SIZE_MB_KEY, v)}
        restartRequired={restartKeys.has(OPSLOG_MAX_SIZE_MB_KEY)}
      />

      <AdvancedSection
        id="infrastructure.logs"
        count={LOG_ADVANCED_KEYS.length}
        forceOpen={advancedChanged > 0}
      >
        <SettingsSubheading>Permission checks</SettingsSubheading>
        <SettingField
          label="Delete permission records older than"
          type="number"
          unit="days"
          value={form.getValue("policy.decision_log_retention_days")}
          onChange={(v) => form.setValue("policy.decision_log_retention_days", v)}
          restartRequired={restartKeys.has("policy.decision_log_retention_days")}
        />
        <SettingField
          label="How much to record"
          type="select"
          description="Full also stores a sample of each request."
          value={form.getValue("policy.decision_log_verbosity") || "digest"}
          onChange={(v) => form.setValue("policy.decision_log_verbosity", v)}
          options={[
            { value: "digest", label: "Summary" },
            { value: "verbose", label: "Full" },
          ]}
          restartRequired={restartKeys.has("policy.decision_log_verbosity")}
        />
        <SettingField
          label="Record one allowed check in every"
          type="number"
          description="Denials and errors are always recorded."
          value={form.getValue("policy.decision_log_scope_sample_rate")}
          onChange={(v) => form.setValue("policy.decision_log_scope_sample_rate", v)}
          restartRequired={restartKeys.has("policy.decision_log_scope_sample_rate")}
        />

        <BucketOverridesEditor
          rows={rows}
          parseError={parseError}
          onChange={commitRows}
          onRestore={() => commitRows(recommendedBucketRows())}
        />
      </AdvancedSection>
    </FieldGroup>
  );
}

export default function InfrastructureSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();
  const serverStatus = useAdminServerStatus();
  const artworkStorage = serverStatus.data?.artwork_storage;
  const storageStatusKnown =
    artworkStorage != null &&
    "status_known" in artworkStorage &&
    artworkStorage.status_known === true &&
    !serverStatus.isError;
  const artworkLocked = artworkStorage?.locked === true;
  // A configured private bucket locks at startup, before any artwork, and
  // the private location also locks once artwork is recorded.
  const privateLocked = artworkLocked || artworkStorage?.private_locked === true;
  const [saveInProgress, setSaveInProgress] = useState(false);
  const [transitionOpen, setTransitionOpen] = useState(false);
  const [transitionBackend, setTransitionBackend] = useState<"local" | "s3">(
    artworkStorage?.backend === "s3" ? "local" : "s3",
  );
  const [transitionPolicy, setTransitionPolicy] =
    useState<StorageTransitionPolicy>("preserve_uploads");
  const [transitionLocalPath, setTransitionLocalPath] = useState(
    form.getValue("artwork.local_path"),
  );
  const [dismissedTransitionId, setDismissedTransitionId] = useState<string>();
  const createTransition = useCreateStorageTransition();
  const transitionCapabilities = useStorageTransitionCapabilities();
  const managedTransitionsAvailable =
    transitionCapabilities.data?.state === "available" &&
    transitionCapabilities.data.allowed !== false;
  const currentSourceIsS3 = artworkStorage?.backend === "s3";
  const transitionJobs = useAdminTaskJobs("storage_transition", 5, true);
  const latestTransition = transitionJobs.data?.[0];
  const latestTransitionResult = latestTransition?.result_payload as
    | StorageTransitionJobResult
    | undefined;
  const draftEffectiveBackend = effectiveArtworkBackend(
    form.getValue("artwork.storage_backend"),
    form.getValue("s3.public_bucket"),
  );
  // The lock compares edits with the recorded location. That is the running
  // backend, except after a committed transition and before the restart, when
  // the saved settings already name the committed location.
  const lockedEffectiveBackend =
    latestTransitionResult?.phase === "restart_pending"
      ? effectiveArtworkBackend(
          form.getPersistedValue("artwork.storage_backend"),
          form.getPersistedValue("s3.public_bucket"),
        )
      : currentSourceIsS3
        ? "s3"
        : "local";
  const effectiveBackendChanging =
    artworkLocked && draftEffectiveBackend !== lockedEffectiveBackend;
  const publicBackendChangePending = effectiveBackendChanging && draftEffectiveBackend === "s3";
  // A local install can keep its operational data in a private bucket, which a
  // copy policy has to read just like an S3 source.
  const persistedPrivateBucket = form.getPersistedValue("s3.private_bucket").trim();
  const currentSourceUsesS3 = currentSourceIsS3 || Boolean(persistedPrivateBucket);
  const recoveryHealth = useStorageTransitionSourceHealth(false, managedTransitionsAvailable);
  const sourceHealth = useStorageTransitionSourceHealth(
    true,
    managedTransitionsAvailable && transitionOpen && currentSourceUsesS3,
  );
  const sourceHealthUnavailable =
    currentSourceUsesS3 && (sourceHealth.data?.reachable === false || sourceHealth.isError);
  const selectedPolicyNeedsSource = transitionPolicy !== "start_fresh";
  const sourceMayHavePrivateAvatars =
    !currentSourceIsS3 ||
    sourceHealth.data?.private_configured === true ||
    Boolean(persistedPrivateBucket);
  const targetS3MissingPrivateBucket =
    transitionBackend === "s3" &&
    sourceMayHavePrivateAvatars &&
    !form.getValue("s3.private_bucket").trim();
  const copyPolicyUnavailable = sourceHealthUnavailable || targetS3MissingPrivateBucket;
  const cancelTransition = useCancelStorageTransition();
  const verifiedObjects = latestTransitionResult?.verified_objects;
  const verifiedObjectCount =
    typeof verifiedObjects === "number" &&
    Number.isSafeInteger(verifiedObjects) &&
    verifiedObjects >= 0
      ? verifiedObjects
      : undefined;
  const activeTransition =
    latestTransition?.status === "queued" || latestTransition?.status === "running"
      ? latestTransition
      : undefined;
  const failedTransition =
    (latestTransition?.status === "failed" || latestTransition?.status === "cancelled") &&
    latestTransition.id !== dismissedTransitionId
      ? latestTransition
      : undefined;
  const manualRestartTransition =
    latestTransitionResult?.manual_restart_required === true &&
    latestTransition?.id !== dismissedTransitionId
      ? latestTransition
      : undefined;
  // A value typed and then reverted stays dirty in the form, so compare it
  // with what is stored before treating it as a new location.
  const locationKeyChanged = (key: string) => {
    if (!form.isDirty(key)) return false;
    // With no private bucket before or after, a leftover endpoint or prefix
    // names no location, and the server saves it directly.
    if (
      key !== "s3.private_bucket" &&
      key.startsWith("s3.private_") &&
      !form.getValue("s3.private_bucket").trim() &&
      !form.getPersistedValue("s3.private_bucket").trim()
    ) {
      return false;
    }
    return (
      normalizeLocationValue(key, form.getValue(key)) !==
      normalizeLocationValue(key, form.getPersistedValue(key))
    );
  };
  const publicLocationChanging =
    transitionBackend !== (currentSourceIsS3 ? "s3" : "local") ||
    (transitionBackend === "s3" && PUBLIC_S3_IDENTITY_KEYS.some(locationKeyChanged)) ||
    (transitionBackend === "local" && locationKeyChanged("artwork.local_path"));
  // Avatars, diagnostics, and job artifacts move when the private bucket
  // changes, when S3 is disabled while a private bucket holds them, and when a
  // local install without one moves to S3.
  const privateLocationChanging =
    PRIVATE_S3_IDENTITY_KEYS.some(locationKeyChanged) ||
    (transitionBackend === "local" && currentSourceIsS3 && Boolean(persistedPrivateBucket)) ||
    (transitionBackend === "local" &&
      !currentSourceIsS3 &&
      !persistedPrivateBucket &&
      locationKeyChanged("artwork.local_path")) ||
    (transitionBackend === "s3" && !currentSourceIsS3 && !persistedPrivateBucket);
  const privateOnlyTransition = privateLocationChanging && !publicLocationChanging;
  // The private bucket owns avatars, diagnostics, and job artifacts on either
  // backend, so once storage is locked a change to it is a managed transition.
  const storageLocationChangePending = artworkLocked
    ? effectiveBackendChanging ||
      (lockedEffectiveBackend === "s3"
        ? S3_IDENTITY_KEYS
        : ["artwork.local_path", ...PRIVATE_S3_IDENTITY_KEYS]
      ).some(locationKeyChanged)
    : privateLocked && PRIVATE_S3_IDENTITY_KEYS.some(locationKeyChanged);
  const privateBucketValue = form.getValue("s3.private_bucket").trim();
  const privateEndpointMissing =
    Boolean(privateBucketValue) && !form.getValue("s3.private_endpoint").trim();
  const privateTargetLabel = privateBucketValue
    ? `Private bucket: ${privateBucketValue} · ${
        form.getValue("s3.private_endpoint").trim() || "endpoint not set"
      }`
    : transitionBackend === "local"
      ? "No private bucket: avatars, diagnostics, and catalog artifacts are kept on local disk."
      : "No private bucket: avatars, diagnostics, and catalog artifacts become unavailable.";
  const saveInProgressRef = useRef(false);

  const secrets: SecretEditors = {
    keepSaved: (key) => {
      if (saveInProgressRef.current) return;
      form.resetValue(key);
    },
    clearSaved: (key) => {
      if (saveInProgressRef.current) return;
      form.setValue(key, "");
    },
    setSecret: (key, value) => {
      if (saveInProgressRef.current) return;
      form.setValue(key, value);
    },
    disabled: form.isSaving || saveInProgress,
  };

  async function saveSettings(keys?: string[]): Promise<boolean> {
    saveInProgressRef.current = true;
    setSaveInProgress(true);
    try {
      await form.save(keys);
      return true;
    } catch {
      // The mutation reports the error; staged credential drafts stay for retry.
      return false;
    } finally {
      saveInProgressRef.current = false;
      setSaveInProgress(false);
    }
  }

  async function handleSave() {
    if (saveInProgressRef.current) return;
    if (!storageStatusKnown) {
      // A location edit may need a transition, which also carries the storage
      // credentials that go with it; hold all of them back together.
      const held = form.dirtyKeys.some((key) => STORAGE_LOCATION_KEYS.has(key))
        ? STORAGE_TRANSITION_KEYS
        : STORAGE_LOCATION_KEYS;
      const safeKeys = form.dirtyKeys.filter((key) => !held.has(key));
      if (safeKeys.length === 0) {
        toast.error(
          "Storage lock status is unavailable. Retry the status check before saving this location.",
        );
        return;
      }
      await saveSettings(safeKeys);
      return;
    }
    if (storageLocationChangePending) {
      if (!managedTransitionsAvailable) {
        toast.error("Managed storage transitions are not available on this server.");
        return;
      }
      const nonTransitionKeys = form.dirtyKeys.filter((key) => !STORAGE_TRANSITION_KEYS.has(key));
      if (nonTransitionKeys.length > 0 && !(await saveSettings(nonTransitionKeys))) return;
      setTransitionBackend(draftEffectiveBackend);
      setTransitionLocalPath(form.getValue("artwork.local_path"));
      setTransitionOpen(true);
      return;
    }
    await saveSettings();
  }

  function handleDiscard() {
    if (saveInProgressRef.current) return;
    form.discard();
  }

  async function handleStorageTransition() {
    if (!storageStatusKnown) {
      toast.error(
        "Storage lock status is unavailable. Retry the status check before moving storage.",
      );
      return;
    }
    if (!managedTransitionsAvailable) {
      toast.error("Managed storage transitions are not available on this server.");
      return;
    }
    const values: Record<string, string> = {
      "artwork.storage_backend": transitionBackend,
    };
    if (transitionBackend === "local") {
      values["artwork.local_path"] = transitionLocalPath;
      // Leaving S3 clears the private bucket on the server. A local install
      // keeps it unless the administrator changed it.
      if (!currentSourceIsS3) {
        for (const key of PRIVATE_S3_KEYS) {
          if (form.isDirty(key)) values[key] = form.getValue(key);
        }
      }
    } else {
      for (const key of [...PUBLIC_S3_KEYS, ...PRIVATE_S3_KEYS]) {
        if (form.isDirty(key)) values[key] = form.getValue(key);
      }
    }
    try {
      const accepted = await createTransition.mutateAsync({ policy: transitionPolicy, values });
      setTransitionOpen(false);
      for (const key of Object.keys(values)) form.resetValue(key);
      if (transitionBackend === "local" && currentSourceIsS3) {
        for (const key of [...PUBLIC_S3_KEYS, ...PRIVATE_S3_KEYS]) {
          if (form.isDirty(key)) form.resetValue(key);
        }
      }
      toast.success(`Storage transition queued (${accepted.job.id}).`);
    } catch {
      toast.error("Failed to queue storage transition. Check the storage settings and try again.");
    }
  }

  function handleArtworkBackendChange(value: string) {
    if (!storageStatusKnown) return;
    const currentBackend = artworkStorage?.backend;
    const requestedBackend = effectiveArtworkBackend(value, form.getValue("s3.public_bucket"));
    if (artworkLocked && currentBackend && requestedBackend !== currentBackend) {
      if (!managedTransitionsAvailable) {
        toast.error("Managed storage transitions are not available on this server.");
        return;
      }
      setTransitionBackend(requestedBackend);
      setTransitionLocalPath(form.getValue("artwork.local_path"));
      setTransitionOpen(true);
      return;
    }
    form.setValue("artwork.storage_backend", value);
  }

  if (form.sensitiveStatusError) {
    return (
      <div
        className="flex items-start gap-3 rounded-xl border border-red-500/20 bg-red-500/5 p-4"
        role="alert"
      >
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-500" />
        <div>
          <p className="text-sm font-medium">Protected credential status is unavailable</p>
          <p className="text-muted-foreground mt-1 text-xs">
            Reload this page before editing infrastructure settings.
          </p>
        </div>
      </div>
    );
  }

  if (!artworkStorage && (serverStatus.isError || serverStatus.data)) {
    return (
      <div
        className="flex items-start gap-3 rounded-xl border border-red-500/20 bg-red-500/5 p-4"
        role="alert"
      >
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-500" />
        <div>
          <p className="text-sm font-medium">Storage lock status is unavailable</p>
          <p className="text-muted-foreground mt-1 text-xs">
            Reload this page before editing infrastructure settings.
          </p>
        </div>
      </div>
    );
  }

  if (form.isLoading || !form.sensitiveStatusReady || !artworkStorage)
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

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Storage & Database" className="mb-8" />

      <div className="flex-1 space-y-5">
        {!storageStatusKnown ? (
          <div
            className="flex items-start justify-between gap-3 rounded-xl border border-red-500/20 bg-red-500/5 p-4"
            role="alert"
          >
            <div>
              <p className="text-sm font-medium">Storage lock status is unavailable</p>
              <p className="text-muted-foreground mt-1 text-xs">
                Retry the status check before changing storage locations. Other settings remain
                editable.
              </p>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void serverStatus.refetch()}
            >
              Retry status
            </Button>
          </div>
        ) : null}
        <FieldGroup label="Storage" restartAll={restartKeys.has("artwork.storage_backend")}>
          <SettingField
            label="Backend"
            type="select"
            value={
              artworkLocked && artworkStorage?.backend
                ? artworkStorage.backend
                : form.getValue("artwork.storage_backend") || "auto"
            }
            onChange={handleArtworkBackendChange}
            disabled={!storageStatusKnown}
            options={[
              { value: "auto", label: "Automatic" },
              { value: "local", label: "Local disk" },
              { value: "s3", label: "S3" },
            ]}
            description={
              artworkLocked
                ? artworkStorage?.backend === "s3"
                  ? "Choose Automatic or Local disk to review a managed transition from S3."
                  : "Edit the local path or choose S3 to review a managed transition."
                : "Where Silo keeps artwork, subtitles, and other library assets."
            }
            restartRequired={restartKeys.has("artwork.storage_backend")}
          />
          <SettingField
            label="Local storage path"
            hint="/var/lib/silo/artwork"
            value={form.getValue("artwork.local_path")}
            onChange={(value) => form.setValue("artwork.local_path", value)}
            disabled={
              !storageStatusKnown ||
              (artworkLocked && currentSourceIsS3) ||
              form.isSaving ||
              saveInProgress
            }
            description={
              artworkLocked
                ? artworkStorage?.backend === "s3"
                  ? "Used when Local disk is selected. Mount this path as a volume in Docker."
                  : "Changing this path opens a managed transition. Mount the new path as a volume in Docker."
                : "Absolute path on the server. Mount a volume here in Docker."
            }
            restartRequired={restartKeys.has("artwork.local_path")}
          />
        </FieldGroup>
        {activeTransition ? (
          <div className="border-border/60 bg-card/40 rounded-xl border p-4">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0 space-y-1">
                <p className="text-sm font-medium">Storage transition: {activeTransition.status}</p>
                <p className="text-muted-foreground text-xs">
                  {transitionPhaseLabel(latestTransitionResult?.phase)}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => transitionJobs.refetch()}
                >
                  Refresh
                </Button>
                {activeTransition ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={async () => {
                      try {
                        await cancelTransition.mutateAsync(activeTransition.id);
                        toast.success("Storage transition cancellation requested.");
                      } catch {
                        toast.error("Failed to cancel storage transition. Try again.");
                      }
                    }}
                    disabled={cancelTransition.isPending}
                  >
                    {cancelTransition.isPending ? "Cancelling…" : "Cancel transition"}
                  </Button>
                ) : null}
              </div>
            </div>
            {verifiedObjectCount !== undefined ? (
              <p
                className="text-muted-foreground mt-3 text-xs"
                aria-label="Storage objects verified"
              >
                {verifiedObjectsLabel(verifiedObjectCount)}
              </p>
            ) : null}
          </div>
        ) : null}
        {recoveryHealth.data?.recovery_pending ? (
          <div className="border-border/60 bg-card/40 rounded-xl border p-4" role="status">
            <div className="space-y-1">
              <p className="text-sm font-medium">
                Storage recovery: {recoveryStateLabel(recoveryHealth.data.recovery_state)}
              </p>
              <p className="text-muted-foreground text-xs">
                {recoveryStateMessage(recoveryHealth.data.recovery_state)}
              </p>
              {recoveryHealth.data.recovery_error ? (
                <p className="text-destructive text-xs">
                  Recovery needs attention. Check administrator diagnostics for details.
                </p>
              ) : null}
            </div>
            <div className="bg-muted mt-3 h-1.5 overflow-hidden rounded-full">
              <div
                className="bg-primary h-full rounded-full"
                style={{ width: `${recoveryHealth.data.recovery_progress_percent ?? 0}%` }}
              />
            </div>
          </div>
        ) : null}
        {failedTransition ? (
          <div className="rounded-xl border border-red-500/25 bg-red-500/5 p-4" role="alert">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0 space-y-1">
                <p className="text-sm font-medium">Storage transition {failedTransition.status}</p>
                <p className="text-muted-foreground text-xs">
                  {failedTransition.status === "cancelled"
                    ? "The storage transition was canceled."
                    : transitionFailureLabel(latestTransitionResult?.failure_category)}{" "}
                  Check administrator diagnostics for details.
                </p>
                {verifiedObjectCount !== undefined ? (
                  <p className="text-muted-foreground text-xs">
                    {verifiedObjectsLabel(verifiedObjectCount)}
                  </p>
                ) : null}
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="shrink-0"
                onClick={() => setDismissedTransitionId(failedTransition.id)}
              >
                Dismiss
              </Button>
            </div>
          </div>
        ) : null}
        {manualRestartTransition ? (
          <div className="rounded-xl border border-amber-500/25 bg-amber-500/5 p-4" role="alert">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0 space-y-1">
                <p className="text-sm font-medium">Manual restart required</p>
                <p className="text-muted-foreground text-xs">
                  The storage switch is committed, but this host could not restart Silo
                  automatically. Restart the server to activate the new storage safely.
                </p>
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="shrink-0"
                onClick={() => setDismissedTransitionId(manualRestartTransition.id)}
              >
                Dismiss
              </Button>
            </div>
          </div>
        ) : null}
        <Dialog open={transitionOpen} onOpenChange={setTransitionOpen}>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>
                {privateOnlyTransition ? "Change private storage" : "Change artwork storage"}
              </DialogTitle>
              <DialogDescription>
                Silo verifies and copies the selected data before switching. Public catalog
                references are reconciled in the background after restart when the public location
                changes. The old storage is never deleted automatically.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-5">
              {currentSourceUsesS3 ? (
                sourceHealth.isPending && !sourceHealth.data ? (
                  <div
                    className="border-border/60 bg-muted/20 space-y-2 rounded-lg border p-4"
                    role="status"
                    aria-label="Checking current S3 storage"
                  >
                    <Skeleton className="h-4 w-48" />
                    <Skeleton className="h-3 w-full max-w-md" />
                  </div>
                ) : sourceHealthUnavailable ? (
                  <div
                    className="border-destructive/30 bg-destructive/5 flex items-start gap-3 rounded-lg border p-4"
                    role="alert"
                  >
                    <AlertTriangle className="text-destructive mt-0.5 h-4 w-4 shrink-0" />
                    <div className="min-w-0 flex-1">
                      <p className="text-sm font-medium">
                        {sourceHealth.data?.message ?? "Current S3 storage could not be checked."}
                      </p>
                      <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                        Reconnect the existing bucket to copy data, or select Start fresh to switch
                        without reading S3. The old bucket will not be deleted.
                      </p>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="mt-3"
                        onClick={() => void sourceHealth.refetch()}
                        disabled={sourceHealth.isFetching}
                      >
                        {sourceHealth.isFetching ? "Checking…" : "Retry check"}
                      </Button>
                    </div>
                  </div>
                ) : sourceHealth.data ? (
                  <div
                    className="rounded-lg border border-emerald-500/25 bg-emerald-500/5 px-4 py-3"
                    role="status"
                  >
                    <p className="text-sm font-medium text-emerald-600 dark:text-emerald-400">
                      Current S3 storage is reachable
                    </p>
                    <p className="text-muted-foreground mt-1 text-xs">
                      Copy-based transition options are available.
                    </p>
                  </div>
                ) : null
              ) : null}

              <div className="space-y-2">
                <p className="text-sm font-medium" id="storage-transition-target-label">
                  Target
                </p>
                <div
                  className="border-border bg-muted/20 rounded-md border px-3 py-2.5"
                  role="group"
                  aria-labelledby="storage-transition-target-label"
                >
                  <p className="text-sm font-medium">
                    {privateOnlyTransition
                      ? "New private storage location"
                      : transitionBackend === "local"
                        ? "Local disk (default Silo behavior)"
                        : "New S3 location"}
                  </p>
                  <p className="text-muted-foreground mt-0.5 text-xs">
                    {privateOnlyTransition
                      ? privateTargetLabel
                      : transitionBackend === "local"
                        ? "Selected from the Backend setting."
                        : `Public bucket: ${form.getValue("s3.public_bucket") || "not set"} · ${
                            form.getValue("s3.public_endpoint").trim() || "endpoint not set"
                          }`}
                  </p>
                  {transitionBackend === "s3" &&
                  privateLocationChanging &&
                  !privateOnlyTransition ? (
                    <p className="text-muted-foreground mt-0.5 text-xs">{privateTargetLabel}</p>
                  ) : null}
                </div>
              </div>

              {privateEndpointMissing ? (
                <p className="text-destructive text-xs font-medium" role="alert">
                  Enter the private storage endpoint to use this bucket.
                </p>
              ) : null}

              {privateOnlyTransition ? null : transitionBackend === "local" ? (
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="storage-transition-local-path">
                    Local artwork path
                  </label>
                  <Input
                    id="storage-transition-local-path"
                    value={transitionLocalPath}
                    onChange={(event) => setTransitionLocalPath(event.target.value)}
                    placeholder="/var/lib/silo/artwork"
                  />
                </div>
              ) : (
                <p className="text-muted-foreground text-xs">
                  The public and private S3 values currently entered on this page will be used.
                  Check both connections before starting.
                </p>
              )}

              <fieldset className="space-y-2">
                <legend className="mb-2 text-sm font-medium">What should move?</legend>
                {(
                  (privateOnlyTransition
                    ? [
                        [
                          "preserve_uploads",
                          "Preserve profile avatars",
                          "Copies profile avatars to the new location. Diagnostic bundles and catalog artifacts stay in the old location and are unavailable after the switch.",
                        ],
                        [
                          "start_fresh",
                          "Start fresh",
                          "Does not read the old location. Existing profile avatars will appear missing after the switch; their files, diagnostic bundles, and catalog artifacts stay in the old location.",
                        ],
                        [
                          "migrate_all",
                          "Migrate all private data",
                          "Copies profile avatars, diagnostic bundles, and catalog job artifacts to the new location.",
                        ],
                      ]
                    : [
                        [
                          "preserve_uploads",
                          "Preserve personal uploads (recommended)",
                          privateLocationChanging
                            ? "Copies branding, collection and library posters, profile avatars, and downloaded subtitles. Provider artwork is rebuilt from its saved source URLs."
                            : "Copies branding, collection and library posters, and downloaded subtitles. Provider artwork is rebuilt from its saved source URLs.",
                        ],
                        [
                          "start_fresh",
                          "Start fresh",
                          `Does not read the old artwork store. Provider paths return to TMDB/TVDB URLs; custom images must be uploaded again. Downloaded subtitles stay in the old store and are unavailable after the switch.${
                            privateLocationChanging
                              ? " Profile avatars also remain in the old storage and are unavailable after the switch."
                              : ""
                          }`,
                        ],
                        [
                          "migrate_all",
                          "Migrate everything",
                          "Copies the complete artwork tree, including downloaded subtitles. When private data changes location, avatars, diagnostic bundles, and catalog job artifacts are copied too. This can take a long time for very large caches.",
                        ],
                      ]) as readonly (readonly [StorageTransitionPolicy, string, string])[]
                ).map(([value, title, description]) => (
                  <label
                    key={value}
                    className={`border-border flex gap-3 rounded-lg border p-3 transition-[border-color,background-color,opacity] ${
                      value !== "start_fresh" && copyPolicyUnavailable
                        ? "cursor-not-allowed opacity-50"
                        : "hover:bg-muted/30 cursor-pointer"
                    }`}
                  >
                    <input
                      type="radio"
                      name="storage-transition-policy"
                      value={value}
                      checked={transitionPolicy === value}
                      onChange={() => setTransitionPolicy(value)}
                      disabled={value !== "start_fresh" && copyPolicyUnavailable}
                      className="mt-1"
                    />
                    <span>
                      <span className="block text-sm font-medium">{title}</span>
                      <span className="text-muted-foreground mt-1 block text-xs leading-relaxed">
                        {description}
                      </span>
                      {value !== "start_fresh" && copyPolicyUnavailable ? (
                        <span className="text-destructive mt-1.5 block text-xs font-medium">
                          {sourceHealthUnavailable
                            ? "Unavailable while the current S3 storage cannot be reached."
                            : "Configure a private S3 bucket to preserve profile avatars."}
                        </span>
                      ) : null}
                    </span>
                  </label>
                ))}
              </fieldset>

              <div className="rounded-lg border border-amber-500/25 bg-amber-500/5 p-4 text-xs leading-relaxed">
                <p className="font-medium text-amber-500">Before you continue</p>
                <ul className="text-muted-foreground mt-2 list-disc space-y-1 pl-4">
                  <li>Core metadata in PostgreSQL is retained.</li>
                  {!privateOnlyTransition ? (
                    <>
                      <li>
                        Backfill Metadata Images downloads from saved provider URLs, not old S3.
                      </li>
                      <li>NFO/sidecar artwork that is not copied needs a metadata refresh.</li>
                    </>
                  ) : null}
                  {transitionBackend === "local" && currentSourceIsS3 ? (
                    <li>
                      {transitionPolicy === "start_fresh"
                        ? "Start fresh copies nothing: uploads, downloaded subtitles, profile avatars, diagnostic bundles, and catalog job artifacts stay in S3 and are unavailable after the switch."
                        : transitionPolicy === "preserve_uploads"
                          ? "Diagnostic bundles and catalog job artifacts stay in the old private bucket and are unavailable after the switch."
                          : "Everything, including diagnostic bundles and catalog job artifacts, is copied to local disk."}
                    </li>
                  ) : privateOnlyTransition ? (
                    <li>
                      Migrate all private data copies diagnostic bundles and catalog job artifacts
                      to the new location; Preserve profile avatars and Start fresh leave them in
                      the old location.
                    </li>
                  ) : privateLocationChanging ? (
                    <li>
                      Migrate everything copies diagnostic bundles and catalog job artifacts to the
                      new location; Preserve personal uploads and Start fresh leave them in the old
                      location.
                    </li>
                  ) : (
                    <li>
                      Profile avatars, diagnostic bundles, and catalog job artifacts stay in their
                      current storage.
                    </li>
                  )}
                  <li>A restart is required after the transition completes.</li>
                </ul>
              </div>
            </div>

            <DialogFooter>
              <Button variant="outline" onClick={() => setTransitionOpen(false)}>
                Cancel
              </Button>
              <Button
                onClick={handleStorageTransition}
                disabled={
                  !storageStatusKnown ||
                  createTransition.isPending ||
                  (currentSourceUsesS3 && sourceHealth.isPending) ||
                  (copyPolicyUnavailable && selectedPolicyNeedsSource) ||
                  (transitionBackend === "local" && !transitionLocalPath.trim()) ||
                  privateEndpointMissing
                }
              >
                {createTransition.isPending ? "Queuing…" : "Queue transition"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        <RedisGroup form={form} restartKeys={restartKeys} secrets={secrets} />
        <S3Group
          form={form}
          restartKeys={restartKeys}
          secrets={secrets}
          scope="public"
          label="Public storage"
          description="Files clients download directly: cached artwork, uploaded posters, and branding images."
          checkKind="s3_public"
          artworkLockedBackend={artworkLocked ? artworkStorage?.backend : undefined}
          publicBackendChangePending={publicBackendChangePending}
          locationFieldsDisabled={!storageStatusKnown}
        />
        <S3Group
          form={form}
          restartKeys={restartKeys}
          secrets={secrets}
          scope="private"
          label="Private storage"
          description="Files only the server reads: profile avatars, diagnostics bundles, and catalog seed artifacts."
          checkKind="s3_private"
          artworkLockedBackend={privateLocked ? artworkStorage?.backend : undefined}
          locationFieldsDisabled={!storageStatusKnown}
        />
        <DatabaseGroup form={form} restartKeys={restartKeys} />
        <LogsGroup form={form} restartKeys={restartKeys} />
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={handleSave}
        onDiscard={handleDiscard}
        isSaving={form.isSaving || saveInProgress}
        saveLabel={
          storageStatusKnown && storageLocationChangePending ? "Review transition" : "Save"
        }
      />
    </div>
  );
}
