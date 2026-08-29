import { useMemo, useRef, useState } from "react";
import { AlertTriangle, Plus, RotateCcw, Trash2 } from "lucide-react";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import { useRestartKeys, type RestartKeyMatcher } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";
import { USER_DATABASE_BACKEND_OPTIONS } from "./databaseSettingOptions";
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

// Changing any of these moves where cached artwork objects live. Silo detects
// that change after restart but requires an explicit manual reconcile so an
// incomplete bucket migration cannot rewrite the artwork catalog.
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

const KEYS = [...REDIS_KEYS, ...DATABASE_KEYS, ...PUBLIC_S3_KEYS, ...PRIVATE_S3_KEYS, ...LOG_KEYS];

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

function S3Group({
  form,
  restartKeys,
  secrets,
  scope,
  label,
  description,
  checkKind,
}: {
  form: SettingsForm;
  restartKeys: RestartKeyMatcher;
  secrets: SecretEditors;
  scope: "public" | "private";
  label: string;
  description: string;
  checkKind: "s3_public" | "s3_private";
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
        restartRequired={restartKeys.has(key("endpoint"))}
      />
      <SettingField
        label="Bucket"
        value={form.getValue(key("bucket"))}
        onChange={(v) => form.setValue(key("bucket"), v)}
        restartRequired={restartKeys.has(key("bucket"))}
      />
      {scope === "public" && PUBLIC_S3_IDENTITY_KEYS.some((k) => form.isDirty(k)) && (
        <div className="my-3 flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-4">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <div className="text-[13px] leading-relaxed">
            <p className="font-medium text-amber-500">Storage location change</p>
            <p className="text-muted-foreground mt-1">
              Artwork is cached in this bucket. Silo will not change artwork cache records
              automatically after restart. Copy or migrate the existing bucket objects first, then
              manually run Reconcile Artwork Cache only if you intend every missing record to be
              reset or cleared. Re-downloading those reset provider images is a separate, manual
              Backfill Metadata Images action; normal scheduled caching only processes artwork
              queued by new or changed metadata. Uploaded images (custom posters, collection
              artwork, branding) cannot be re-downloaded.
            </p>
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
  const [saveInProgress, setSaveInProgress] = useState(false);
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

  async function handleSave() {
    if (saveInProgressRef.current) return;
    saveInProgressRef.current = true;
    setSaveInProgress(true);
    try {
      await form.save();
    } catch {
      // The mutation reports the error; staged credential drafts stay for retry.
    } finally {
      saveInProgressRef.current = false;
      setSaveInProgress(false);
    }
  }

  function handleDiscard() {
    if (saveInProgressRef.current) return;
    form.discard();
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

  if (form.isLoading || !form.sensitiveStatusReady)
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
        <RedisGroup form={form} restartKeys={restartKeys} secrets={secrets} />
        <S3Group
          form={form}
          restartKeys={restartKeys}
          secrets={secrets}
          scope="public"
          label="Public storage"
          description="Files clients download directly: cached artwork, uploaded posters, and branding images."
          checkKind="s3_public"
        />
        <S3Group
          form={form}
          restartKeys={restartKeys}
          secrets={secrets}
          scope="private"
          label="Private storage"
          description="Files only the server reads: profile avatars, diagnostics bundles, and catalog seed artifacts."
          checkKind="s3_private"
        />
        <DatabaseGroup form={form} restartKeys={restartKeys} />
        <LogsGroup form={form} restartKeys={restartKeys} />
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={handleSave}
        onDiscard={handleDiscard}
        isSaving={form.isSaving || saveInProgress}
      />
    </div>
  );
}
