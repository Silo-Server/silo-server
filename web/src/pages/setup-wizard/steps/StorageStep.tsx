import { useMemo, useState } from "react";
import type { ReactNode } from "react";

import {
  ConnectionCheckAction,
  useConnectionCheck,
} from "@/components/admin/ConnectionCheckAction";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { useAdminServerStatus } from "@/hooks/queries/admin/settings";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import {
  SettingField,
  SettingFieldRow,
  SettingFieldStatus,
} from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const REDIS_KEYS = ["redis.url"];

const PUBLIC_S3_KEYS = [
  "s3.public_endpoint",
  "s3.public_bucket",
  "s3.public_key_prefix",
  "s3.public_access_key",
  "s3.public_secret_key",
  "s3.public_url_auth",
  "s3.public_read_endpoint",
  "s3.public_token_secret",
  "s3.public_token_param",
  "s3.public_token_ttl",
];

const PRIVATE_S3_KEYS = [
  "s3.private_endpoint",
  "s3.private_bucket",
  "s3.private_key_prefix",
  "s3.private_access_key",
  "s3.private_secret_key",
];

const META_KEYS = ["metadata.cache_images"];

const ALL_KEYS = [...REDIS_KEYS, ...PUBLIC_S3_KEYS, ...PRIVATE_S3_KEYS, ...META_KEYS];

const S3_URL_AUTH_OPTIONS = [
  { value: "presigned", label: "Signed links (recommended)" },
  { value: "public", label: "Anyone with the link" },
  { value: "cloudflare_token", label: "Cloudflare signed token" },
];

type Form = ReturnType<typeof useSettingsForm>;
type Status = { tone: "ok" | "warn" | "muted"; text: string };

/**
 * The Redis URL is a protected value: the settings snapshot never carries it,
 * so "configured" comes from the sensitive-status list (a stored row or a
 * REDIS_URL from the environment) and "reachable" from the server's health
 * probe. Undefined leaves the row on the generic Configured / Not set up line.
 */
function redisStatusFor(
  saved: boolean,
  managed: boolean,
  health: { ok?: boolean } | undefined,
): Status | undefined {
  if (managed) {
    if (health?.ok === false)
      return { tone: "warn", text: "Set by the environment, but not reachable" };
    return {
      tone: "ok",
      text: `${health?.ok ? "Connected" : "Configured"} · set by the environment`,
    };
  }
  if (saved && health?.ok) return { tone: "ok", text: "Connected" };
  if (saved && health?.ok === false) return { tone: "warn", text: "Configured, but not reachable" };
  return undefined;
}

/**
 * One storage backend: a summary row that says whether it is configured, and a
 * disclosure with the fields. A backend an admin does not touch stays folded
 * and reads as one line, which is what most single-node installs want.
 */
function Backend({
  title,
  description,
  configured,
  status,
  open,
  onToggle,
  editable = true,
  children,
}: {
  title: string;
  description: ReactNode;
  configured: boolean;
  /** Overrides the default Configured / Not set up line. */
  status?: ReactNode;
  open: boolean;
  onToggle: () => void;
  /** False when the value is owned by the environment and cannot be edited here. */
  editable?: boolean;
  children: ReactNode;
}) {
  return (
    <>
      <SettingFieldRow
        label={title}
        description={description}
        status={
          status ??
          (configured ? (
            <SettingFieldStatus tone="ok">Configured</SettingFieldStatus>
          ) : (
            <SettingFieldStatus tone="muted">Not set up</SettingFieldStatus>
          ))
        }
      >
        {editable ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={onToggle}
            aria-expanded={open}
          >
            {open ? "Hide" : configured ? "Edit" : "Set up"}
          </Button>
        ) : null}
      </SettingFieldRow>
      {open && editable ? <div className="setup-backend-fields">{children}</div> : null}
    </>
  );
}

function S3Fields({
  form,
  prefix,
  check,
  disabled,
}: {
  form: Form;
  prefix: "public" | "private";
  check: ReturnType<typeof useConnectionCheck>;
  disabled: boolean;
}) {
  const key = (name: string) => `s3.${prefix}_${name}`;
  const urlAuth = form.getValue(key("url_auth")) || "presigned";
  return (
    <>
      <SettingField
        label="Endpoint"
        hint="https://s3.amazonaws.com"
        value={form.getValue(key("endpoint"))}
        onChange={(v) => form.setValue(key("endpoint"), v)}
      />
      <SettingField
        label="Bucket"
        value={form.getValue(key("bucket"))}
        onChange={(v) => form.setValue(key("bucket"), v)}
      />
      <SettingField
        label="Access key"
        type="password"
        value={form.getValue(key("access_key"))}
        onChange={(v) => form.setValue(key("access_key"), v)}
        sensitiveConfigured={form.sensitiveConfigured.includes(key("access_key"))}
      />
      <SettingField
        label="Secret key"
        type="password"
        value={form.getValue(key("secret_key"))}
        onChange={(v) => form.setValue(key("secret_key"), v)}
        sensitiveConfigured={form.sensitiveConfigured.includes(key("secret_key"))}
      />
      <SettingField
        label="Folder inside the bucket"
        description="Optional. Leave blank to use the bucket root."
        hint="silo"
        value={form.getValue(key("key_prefix"))}
        onChange={(v) => form.setValue(key("key_prefix"), v)}
      />
      {prefix === "public" ? (
        <>
          <SettingField
            label="How asset links are authorized"
            type="select"
            value={urlAuth}
            onChange={(v) => form.setValue("s3.public_url_auth", v)}
            options={S3_URL_AUTH_OPTIONS}
          />
          {urlAuth !== "presigned" ? (
            <SettingField
              label="Address clients download from"
              hint="https://cdn.example.com"
              value={form.getValue("s3.public_read_endpoint")}
              onChange={(v) => form.setValue("s3.public_read_endpoint", v)}
            />
          ) : null}
          {urlAuth === "cloudflare_token" ? (
            <>
              <SettingField
                label="Token secret"
                type="password"
                hint="Signing key configured in Cloudflare"
                value={form.getValue("s3.public_token_secret")}
                onChange={(v) => form.setValue("s3.public_token_secret", v)}
                sensitiveConfigured={form.sensitiveConfigured.includes("s3.public_token_secret")}
              />
              <SettingField
                label="Token query parameter"
                description="Usually verify."
                value={form.getValue("s3.public_token_param") || "verify"}
                onChange={(v) => form.setValue("s3.public_token_param", v)}
              />
              <SettingField
                label="Link lifetime"
                type="number"
                unit="seconds"
                value={form.getValue("s3.public_token_ttl") || "10800"}
                onChange={(v) => form.setValue("s3.public_token_ttl", v)}
              />
            </>
          ) : null}
        </>
      ) : null}
      <ConnectionCheckAction
        onClick={check.run}
        result={check.result}
        isPending={check.isPending}
        disabled={disabled}
        label="Check connection"
      />
    </>
  );
}

export function StorageStep() {
  const form = useSettingsForm({ keys: useMemo(() => ALL_KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit(
    "storage",
    form,
    "Failed to save storage settings",
  );
  const [open, setOpen] = useState<{ redis: boolean; public: boolean; private: boolean }>({
    redis: false,
    public: false,
    private: false,
  });
  const redisCheck = useConnectionCheck("redis", form, REDIS_KEYS);
  const publicCheck = useConnectionCheck("s3_public", form, PUBLIC_S3_KEYS);
  const privateCheck = useConnectionCheck("s3_private", form, PRIVATE_S3_KEYS);

  const redisSaved = form.sensitiveConfigured.includes("redis.url");
  const redisManaged = form.sensitiveManagedByEnv.includes("redis.url");
  // The health probe pings Redis and Postgres; only worth it once there is a
  // Redis to report on.
  const serverStatus = useAdminServerStatus(redisSaved || redisManaged);
  const redisConfigured = form.getValue("redis.url").trim() !== "" || redisSaved;
  const redisStatus = redisStatusFor(redisSaved, redisManaged, serverStatus.data?.health?.redis);
  const publicConfigured =
    form.getValue("s3.public_bucket").trim() !== "" &&
    form.getValue("s3.public_endpoint").trim() !== "";
  const privateConfigured =
    form.getValue("s3.private_bucket").trim() !== "" &&
    form.getValue("s3.private_endpoint").trim() !== "";

  const storageParts = [
    redisConfigured ? "Redis" : null,
    publicConfigured || privateConfigured ? "S3" : null,
  ].filter(Boolean);
  useStepSummary("storage", storageParts.length ? storageParts.join(" + ") : "Built-in storage");

  if (form.isPending) return <StepSkeleton rows={3} />;

  return (
    <StepFrame
      title="Storage and cache"
      lede="A single server works without any of this. Add Redis to run more than one node, and an S3 bucket if you want artwork and thumbnails kept somewhere other than this server."
      onSubmit={handleSubmit}
      busy={busy}
      onSkip={skip}
      footnote="All of this is in Admin › Settings › Infrastructure."
    >
      <StepSection>
        <Backend
          title="Redis"
          description={
            redisManaged
              ? "Shared queue and cache. This server reads REDIS_URL from its environment."
              : "Shared queue and cache. Required once you add transcode or proxy nodes."
          }
          configured={redisConfigured}
          status={
            redisStatus ? (
              <SettingFieldStatus tone={redisStatus.tone}>{redisStatus.text}</SettingFieldStatus>
            ) : undefined
          }
          open={open.redis}
          onToggle={() => setOpen((o) => ({ ...o, redis: !o.redis }))}
          editable={!redisManaged}
        >
          <SettingField
            label="Connection URL"
            type="password"
            hint="redis://localhost:6379"
            value={form.getValue("redis.url")}
            onChange={(v) => form.setValue("redis.url", v)}
            sensitiveConfigured={redisSaved && form.getValue("redis.url") === ""}
          />
          <ConnectionCheckAction
            onClick={redisCheck.run}
            result={redisCheck.result}
            isPending={redisCheck.isPending}
            disabled={busy}
            label="Check connection"
          />
        </Backend>
        <Backend
          title="Artwork bucket"
          description="Public S3 storage for posters, backdrops, chapter thumbnails, and subtitles."
          configured={publicConfigured}
          open={open.public}
          onToggle={() => setOpen((o) => ({ ...o, public: !o.public }))}
        >
          <S3Fields form={form} prefix="public" check={publicCheck} disabled={busy} />
          <SettingFieldRow
            label="Keep provider artwork in the bucket"
            description="Copies posters and backdrops from metadata providers into your bucket instead of linking to them remotely."
          >
            <Switch
              checked={form.getValue("metadata.cache_images") === "true"}
              onCheckedChange={(v) => form.setValue("metadata.cache_images", v ? "true" : "false")}
              aria-label="Keep provider artwork in the bucket"
            />
          </SettingFieldRow>
        </Backend>
        <Backend
          title="Private bucket"
          description="S3 storage for imports, exports, and other files that are never served to clients."
          configured={privateConfigured}
          open={open.private}
          onToggle={() => setOpen((o) => ({ ...o, private: !o.private }))}
        >
          <S3Fields form={form} prefix="private" check={privateCheck} disabled={busy} />
        </Backend>
      </StepSection>
    </StepFrame>
  );
}
