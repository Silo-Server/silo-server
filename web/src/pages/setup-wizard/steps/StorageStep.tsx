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
import { useT, type MessageKey, type Translate } from "@/i18n";
import { stageRedisUrl } from "@/pages/admin-settings/redisDraft";
import {
  SettingField,
  SettingFieldRow,
  SettingFieldStatus,
} from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const REDIS_KEYS = ["redis.url", "redis.db"];

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

const S3_URL_AUTH_OPTIONS: { value: string; label: MessageKey }[] = [
  { value: "presigned", label: "web.setup.storage.authPresigned" },
  { value: "public", label: "web.setup.storage.authPublic" },
  { value: "cloudflare_token", label: "web.setup.storage.authCloudflare" },
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
  t: Translate,
  saved: boolean,
  managed: boolean,
  health: { ok?: boolean } | undefined,
): Status | undefined {
  if (managed) {
    if (health?.ok === false)
      return { tone: "warn", text: t("web.setup.storage.redisEnvUnreachable") };
    return {
      tone: "ok",
      text: health?.ok
        ? t("web.setup.storage.redisEnvConnected")
        : t("web.setup.storage.redisEnvConfigured"),
    };
  }
  if (saved && health?.ok) return { tone: "ok", text: t("web.setup.wizard.connected") };
  if (saved && health?.ok === false)
    return { tone: "warn", text: t("web.setup.storage.redisUnreachable") };
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
  readOnly = false,
  children,
}: {
  title: string;
  description: ReactNode;
  configured: boolean;
  /** Overrides the default Configured / Not set up line. */
  status?: ReactNode;
  open: boolean;
  onToggle: () => void;
  /** Show the environment-owned values for inspection without offering edits. */
  readOnly?: boolean;
  children: ReactNode;
}) {
  const t = useT();
  return (
    <>
      <SettingFieldRow
        label={title}
        description={description}
        status={
          status ??
          (configured ? (
            <SettingFieldStatus tone="ok">{t("web.setup.storage.configured")}</SettingFieldStatus>
          ) : (
            <SettingFieldStatus tone="muted">{t("web.setup.storage.notSetUp")}</SettingFieldStatus>
          ))
        }
      >
        <Button type="button" variant="secondary" size="sm" onClick={onToggle} aria-expanded={open}>
          {open
            ? t("web.setup.storage.hide")
            : readOnly
              ? t("web.setup.storage.view")
              : configured
                ? t("web.setup.storage.edit")
                : t("web.setup.storage.setUp")}
        </Button>
      </SettingFieldRow>
      {open ? <div className="setup-backend-fields">{children}</div> : null}
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
  const t = useT();
  const key = (name: string) => `s3.${prefix}_${name}`;
  const urlAuth = form.getValue(key("url_auth")) || "presigned";
  return (
    <>
      <SettingField
        label={t("web.setup.storage.endpointLabel")}
        // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
        hint="https://s3.amazonaws.com"
        value={form.getValue(key("endpoint"))}
        onChange={(v) => form.setValue(key("endpoint"), v)}
        disabled={locationLocked}
        description={
          locationLocked
            ? locationStatusUnavailable
              ? t("web.setup.storage.lockUnknownLocation")
              : lockedDescription
            : undefined
        }
      />
      <SettingField
        label={t("web.setup.storage.bucketLabel")}
        value={form.getValue(key("bucket"))}
        onChange={(v) => form.setValue(key("bucket"), v)}
        disabled={locationLocked}
      />
      <SettingField
        label={t("web.setup.storage.accessKeyLabel")}
        type="password"
        value={form.getValue(key("access_key"))}
        onChange={(v) => form.setValue(key("access_key"), v)}
        sensitiveConfigured={form.sensitiveConfigured.includes(key("access_key"))}
      />
      <SettingField
        label={t("web.setup.storage.secretKeyLabel")}
        type="password"
        value={form.getValue(key("secret_key"))}
        onChange={(v) => form.setValue(key("secret_key"), v)}
        sensitiveConfigured={form.sensitiveConfigured.includes(key("secret_key"))}
      />
      <SettingField
        label={t("web.setup.storage.prefixLabel")}
        description={t("web.setup.storage.prefixDescription")}
        // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
        hint="silo"
        value={form.getValue(key("key_prefix"))}
        onChange={(v) => form.setValue(key("key_prefix"), v)}
        disabled={locationLocked}
      />
      {prefix === "public" ? (
        <>
          <SettingField
            label={t("web.setup.storage.urlAuthLabel")}
            type="select"
            value={urlAuth}
            onChange={(v) => form.setValue("s3.public_url_auth", v)}
            options={S3_URL_AUTH_OPTIONS.map((o) => ({ value: o.value, label: t(o.label) }))}
          />
          {urlAuth !== "presigned" ? (
            <SettingField
              label={t("web.setup.storage.readEndpointLabel")}
              // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
              hint="https://cdn.example.com"
              value={form.getValue("s3.public_read_endpoint")}
              onChange={(v) => form.setValue("s3.public_read_endpoint", v)}
            />
          ) : null}
          {urlAuth === "cloudflare_token" ? (
            <>
              <SettingField
                label={t("web.setup.storage.tokenSecretLabel")}
                type="password"
                hint={t("web.setup.storage.tokenSecretHint")}
                value={form.getValue("s3.public_token_secret")}
                onChange={(v) => form.setValue("s3.public_token_secret", v)}
                sensitiveConfigured={form.sensitiveConfigured.includes("s3.public_token_secret")}
              />
              <SettingField
                label={t("web.setup.storage.tokenParamLabel")}
                description={t("web.setup.storage.tokenParamDescription")}
                value={form.getValue("s3.public_token_param") || "verify"}
                onChange={(v) => form.setValue("s3.public_token_param", v)}
              />
              <SettingField
                label={t("web.setup.storage.tokenTtlLabel")}
                type="number"
                unit={t("web.setup.storage.seconds")}
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
        label={t("web.setup.wizard.checkConnection")}
      />
    </>
  );
}

export function StorageStep() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => ALL_KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit(
    "storage",
    form,
    t("web.setup.storage.saveFailed"),
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
  const redisStatus = redisStatusFor(t, redisSaved, redisManaged, serverStatus.data?.health?.redis);
  const publicConfigured = form.getValue("s3.public_bucket").trim() !== "";
  const privateConfigured =
    form.getValue("s3.private_bucket").trim() !== "" &&
    form.getValue("s3.private_endpoint").trim() !== "";

  const artworkBackend = form.getValue("artwork.storage_backend") || "auto";
  const usesS3 = artworkBackend === "s3" || (artworkBackend === "auto" && publicConfigured);
  const storageParts = [
    redisConfigured ? "Redis" : null,
    usesS3 ? t("web.setup.storage.summaryS3") : t("web.setup.storage.summaryLocal"),
  ].filter(Boolean);
  useStepSummary("storage", storageParts.join(" + "));

  if (!artworkStorage && (serverStatus.isError || serverStatus.data)) {
    return (
      <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4" role="alert">
        <p className="text-sm font-medium">{t("web.setup.storage.lockUnknownTitle")}</p>
        <p className="text-muted-foreground mt-1 text-xs">
          {t("web.setup.storage.lockUnknownReloadPage")}
        </p>
      </div>
    );
  }

  if (form.isPending || !artworkStorage) return <StepSkeleton rows={3} />;

  return (
    <StepFrame
      title={t("web.setup.storage.title")}
      lede={t("web.setup.storage.lede")}
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
      footnote={t("web.setup.storage.footnote")}
    >
      {locationStatusUnavailable ? (
        <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4" role="alert">
          <p className="text-sm font-medium">{t("web.setup.storage.lockUnknownTitle")}</p>
          <p className="text-muted-foreground mt-1 text-xs">
            {pendingLocationEdit
              ? t("web.setup.storage.lockUnknownNoteHeld")
              : t("web.setup.storage.lockUnknownNote")}
          </p>
        </div>
      ) : null}
      <StepSection>
        <SettingField
          label={t("web.setup.storage.backendLabel")}
          type="select"
          value={artworkBackend}
          options={[
            { value: "auto", label: t("web.setup.storage.backendAuto") },
            { value: "local", label: t("web.setup.storage.backendLocal") },
            { value: "s3", label: "S3" },
          ]}
          disabled={locationStatusUnavailable || artworkLocked}
          description={
            locationStatusUnavailable
              ? t("web.setup.storage.lockUnknownBackend")
              : artworkLocked
                ? t("web.setup.storage.backendLocked")
                : t("web.setup.storage.backendDescription")
          }
          onChange={(value) => {
            form.setValue("artwork.storage_backend", value);
            if (value === "s3") setOpen((o) => ({ ...o, public: true }));
          }}
        />
        <SettingField
          label={t("web.setup.storage.localPathLabel")}
          // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
          hint="/var/lib/silo/artwork"
          value={form.getValue("artwork.local_path")}
          onChange={(value) => form.setValue("artwork.local_path", value)}
          disabled={locationStatusUnavailable || artworkLocked}
          description={
            locationStatusUnavailable
              ? t("web.setup.storage.lockUnknownPath")
              : t("web.setup.storage.localPathDescription")
          }
        />
        <SettingFieldRow
          label={t("web.setup.storage.keepArtworkLabel")}
          description={t("web.setup.storage.keepArtworkDescription")}
        >
          <Switch
            checked={form.getValue("metadata.cache_images") !== "false"}
            onCheckedChange={(v) => form.setValue("metadata.cache_images", v ? "true" : "false")}
            aria-label={t("web.setup.storage.keepArtworkLabel")}
          />
        </SettingFieldRow>
        <Backend
          // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- product name
          title="Redis"
          description={
            redisManaged
              ? t("web.setup.storage.redisManagedDescription")
              : t("web.setup.storage.redisDescription")
          }
          configured={redisConfigured}
          status={
            redisStatus ? (
              <SettingFieldStatus tone={redisStatus.tone}>{redisStatus.text}</SettingFieldStatus>
            ) : undefined
          }
          open={open.redis}
          onToggle={() => setOpen((o) => ({ ...o, redis: !o.redis }))}
          readOnly={redisManaged}
        >
          <SettingField
            label={t("web.setup.storage.redisUrlLabel")}
            type="password"
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="redis://localhost:6379"
            value={form.getValue("redis.url")}
            onChange={(v) => stageRedisUrl(form, v)}
            sensitiveConfigured={redisSaved && form.getValue("redis.url") === ""}
            disabled={redisManaged}
          />
          <SettingField
            label={t("web.setup.storage.redisDbLabel")}
            type="number"
            description={
              redisManaged
                ? t("web.setup.storage.redisDbManaged")
                : t("web.setup.storage.redisDbDescription")
            }
            value={form.getValue("redis.db")}
            onChange={(v) => form.setValue("redis.db", v)}
            disabled={redisManaged}
          />
          <ConnectionCheckAction
            onClick={redisCheck.run}
            result={redisCheck.result}
            isPending={redisCheck.isPending}
            disabled={busy}
            label={t("web.setup.wizard.checkConnection")}
          />
        </Backend>
        <Backend
          title={t("web.setup.storage.publicTitle")}
          description={t("web.setup.storage.publicDescription")}
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
                ? t("web.setup.storage.publicLockedS3")
                : t("web.setup.storage.publicLockedLocal")
            }
          />
        </Backend>
        <Backend
          title={t("web.setup.storage.privateTitle")}
          description={t("web.setup.storage.privateDescription")}
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
            lockedDescription={t("web.setup.storage.privateLocked")}
          />
        </Backend>
      </StepSection>
    </StepFrame>
  );
}
