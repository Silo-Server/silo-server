import { useMemo, useState } from "react";
import type { FormEvent, ReactNode } from "react";

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

const ALL_KEYS = [
  "artwork.storage_backend",
  "artwork.local_path",
  ...REDIS_KEYS,
  ...PUBLIC_S3_KEYS,
  ...PRIVATE_S3_KEYS,
  ...META_KEYS,
];

const STORAGE_LOCATION_KEYS = [
  "artwork.storage_backend",
  "artwork.local_path",
  "s3.public_endpoint",
  "s3.public_bucket",
  "s3.public_key_prefix",
  "s3.private_endpoint",
  "s3.private_bucket",
  "s3.private_key_prefix",
];

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
  locationLocked = false,
  locationStatusUnavailable = false,
  lockedDescription,
}: {
  form: Form;
  prefix: "public" | "private";
  check: ReturnType<typeof useConnectionCheck>;
  disabled: boolean;
  // The server refuses direct location changes after an identity is recorded.
  // A configured private bucket is recorded at startup, even when empty.
  locationLocked?: boolean;
  locationStatusUnavailable?: boolean;
  lockedDescription?: string;
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
        disabled={locationLocked}
        description={
          locationLocked
            ? locationStatusUnavailable
              ? "Storage lock status is unavailable. Reload before changing this location."
              : lockedDescription
            : undefined
        }
      />
      <SettingField
        label="Bucket"
        value={form.getValue(key("bucket"))}
        onChange={(v) => form.setValue(key("bucket"), v)}
        disabled={locationLocked}
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
        disabled={locationLocked}
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
  const serverStatus = useAdminServerStatus();
  const artworkStorage = serverStatus.data?.artwork_storage;
  const storageLockStatusKnown =
    !serverStatus.isError &&
    artworkStorage != null &&
    "status_known" in artworkStorage &&
    artworkStorage.status_known === true;
  const locationStatusUnavailable = !storageLockStatusKnown;
  const artworkLocked = storageLockStatusKnown && artworkStorage.locked === true;
  const privateLocked =
    storageLockStatusKnown && (artworkLocked || artworkStorage.private_locked === true);
  const pendingLocationEdit = STORAGE_LOCATION_KEYS.some(form.isDirty);
  const holdSubmit = locationStatusUnavailable && pendingLocationEdit;
  const redisConfigured = form.getValue("redis.url").trim() !== "" || redisSaved;
  const redisStatus = redisStatusFor(redisSaved, redisManaged, serverStatus.data?.health?.redis);
  const publicConfigured = form.getValue("s3.public_bucket").trim() !== "";
  const privateConfigured =
    form.getValue("s3.private_bucket").trim() !== "" &&
    form.getValue("s3.private_endpoint").trim() !== "";

  const artworkBackend = form.getValue("artwork.storage_backend") || "auto";
  const usesS3 = artworkBackend === "s3" || (artworkBackend === "auto" && publicConfigured);
  const storageParts = [
    redisConfigured ? "Redis" : null,
    usesS3 ? "S3 storage" : "Local storage",
  ].filter(Boolean);
  useStepSummary("storage", storageParts.join(" + "));

  if (!artworkStorage && (serverStatus.isError || serverStatus.data)) {
    return (
      <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4" role="alert">
        <p className="text-sm font-medium">Storage lock status is unavailable</p>
        <p className="text-muted-foreground mt-1 text-xs">
          Reload this page before editing storage settings.
        </p>
      </div>
    );
  }

  if (form.isPending || !artworkStorage) return <StepSkeleton rows={3} />;

  return (
    <StepFrame
      title="Storage and cache"
      lede="A single server works without any of this. Add Redis to run more than one node, and an S3 bucket if you want stored files kept somewhere other than this server."
      onSubmit={(event: FormEvent<HTMLFormElement>) => {
        if (holdSubmit) {
          event.preventDefault();
          return;
        }
        void handleSubmit(event);
      }}
      busy={busy}
      disabled={holdSubmit}
      onSkip={skip}
      footnote="Storage, Redis, and database settings are in Admin › Settings › Storage & Database."
    >
      {locationStatusUnavailable ? (
        <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4" role="alert">
          <p className="text-sm font-medium">Storage lock status is unavailable</p>
          <p className="text-muted-foreground mt-1 text-xs">
            Reload before changing storage locations.{" "}
            {pendingLocationEdit
              ? "Continue is unavailable until lock status returns."
              : "Other settings can still be saved."}
          </p>
        </div>
      ) : null}
      <StepSection>
        <SettingField
          label="Storage"
          type="select"
          value={artworkBackend}
          options={[
            { value: "auto", label: "Automatic" },
            { value: "local", label: "Local disk" },
            { value: "s3", label: "S3" },
          ]}
          disabled={locationStatusUnavailable || artworkLocked}
          description={
            locationStatusUnavailable
              ? "Storage lock status is unavailable. Reload before changing the backend."
              : artworkLocked
                ? "Locked: files have already been stored on this backend. Change it from Admin › Settings › Storage & Database, which moves them."
                : "Where Silo keeps artwork, subtitles, and other library assets."
          }
          onChange={(value) => {
            form.setValue("artwork.storage_backend", value);
            if (value === "s3") setOpen((o) => ({ ...o, public: true }));
          }}
        />
        <SettingField
          label="Local storage path"
          hint="/var/lib/silo/artwork"
          value={form.getValue("artwork.local_path")}
          onChange={(value) => form.setValue("artwork.local_path", value)}
          disabled={locationStatusUnavailable || artworkLocked}
          description={
            locationStatusUnavailable
              ? "Storage lock status is unavailable. Reload before changing this path."
              : "Absolute path on the server. Mount a volume here in Docker."
          }
        />
        <SettingFieldRow
          label="Keep provider artwork"
          description="Copies posters and backdrops into your artwork storage."
        >
          <Switch
            checked={form.getValue("metadata.cache_images") !== "false"}
            onCheckedChange={(v) => form.setValue("metadata.cache_images", v ? "true" : "false")}
            aria-label="Keep provider artwork"
          />
        </SettingFieldRow>
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
          title="Use S3 object storage for artwork (recommended for multi-node)"
          description="Public S3 storage for posters, backdrops, chapter thumbnails, and subtitles."
          configured={publicConfigured}
          open={open.public}
          onToggle={() => setOpen((o) => ({ ...o, public: !o.public }))}
        >
          <S3Fields
            form={form}
            prefix="public"
            check={publicCheck}
            disabled={busy}
            locationLocked={
              locationStatusUnavailable || (artworkLocked && artworkBackend !== "local")
            }
            locationStatusUnavailable={locationStatusUnavailable}
            lockedDescription={
              usesS3
                ? "Locked: files are stored here. Change it from Admin › Settings › Storage & Database, which moves them."
                : "Locked: files are stored on local disk, and adding a public bucket would switch Automatic storage to S3. Change it from Admin › Settings › Storage & Database, which moves them."
            }
          />
        </Backend>
        <Backend
          title="Private bucket"
          description="S3 storage for imports, exports, and other files that are never served to clients."
          configured={privateConfigured}
          open={open.private}
          onToggle={() => setOpen((o) => ({ ...o, private: !o.private }))}
        >
          <S3Fields
            form={form}
            prefix="private"
            check={privateCheck}
            disabled={busy}
            locationLocked={locationStatusUnavailable || privateLocked}
            locationStatusUnavailable={locationStatusUnavailable}
            lockedDescription="Locked: Silo records a configured private bucket at startup, even when empty. Stored library assets also lock this location. Change it from Admin › Settings › Storage & Database."
          />
        </Backend>
      </StepSection>
    </StepFrame>
  );
}
