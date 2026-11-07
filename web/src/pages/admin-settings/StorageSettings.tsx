import { useMemo, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";

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
import { SaveBar } from "@/components/SaveBar";
import { useT, type Translate } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";
import { cleanPath } from "./settingsPathDefaults";

type SettingsForm = ReturnType<typeof useSettingsForm>;

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

// Shown only for the link modes that use them, behind public storage's
// Advanced disclosure.
const PUBLIC_S3_ADVANCED_KEYS = [
  "s3.public_read_endpoint",
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

// Whether provider artwork is copied into storage at all. It hot-reloads and
// names no location, so it saves like any other setting, never through a
// transition.
const CACHE_IMAGES_KEY = "metadata.cache_images";

const STORAGE_GROUP_KEYS = ["artwork.storage_backend", "artwork.local_path", CACHE_IMAGES_KEY];

const KEYS = [...STORAGE_GROUP_KEYS, ...PUBLIC_S3_KEYS, ...PRIVATE_S3_KEYS];

// An example absolute path, shown as the hint of the local path fields.
const LOCAL_PATH_EXAMPLE = "/var/lib/silo/artwork";

function countDirty(form: SettingsForm, keys: string[]): number {
  return keys.filter((key) => form.isDirty(key)).length;
}

// Whether a group may claim "changes apply after a restart" for all of its
// fields at once. Computed from the server's restart registry rather than
// asserted, so converting one key to hot-reload silently demotes its group to
// per-field chips instead of leaving a false blanket claim.
function allRestart(restartKeys: RestartKeyMatcher, keys: string[]): boolean {
  return keys.every((key) => restartKeys.has(key));
}

function transitionPhaseLabel(t: Translate, phase: StorageTransitionPhase | undefined): string {
  switch (phase) {
    case "queued":
      return t("web.admin.settings.storage.phase.queued");
    case "checking_target":
      return t("web.admin.settings.storage.phase.checkingTarget");
    case "copying":
      return t("web.admin.settings.storage.phase.copying");
    case "verifying":
      return t("web.admin.settings.storage.phase.verifying");
    case "committing":
      return t("web.admin.settings.storage.phase.committing");
    case "restart_pending":
      return t("web.admin.settings.storage.phase.restartPending");
    case "completed":
      return t("web.admin.settings.storage.phase.completed");
    case "failed":
      return t("web.admin.settings.storage.phase.failed");
    case "canceled":
      return t("web.admin.settings.storage.phase.canceled");
    default:
      return t("web.admin.settings.storage.phase.preparing");
  }
}

function transitionFailureLabel(
  t: Translate,
  category: StorageTransitionFailureCategory | undefined,
): string {
  switch (category) {
    case "preparation_failed":
      return t("web.admin.settings.storage.failure.preparation");
    case "target_check_failed":
      return t("web.admin.settings.storage.failure.targetCheck");
    case "copy_failed":
      return t("web.admin.settings.storage.failure.copy");
    case "verification_failed":
      return t("web.admin.settings.storage.failure.verification");
    case "commit_failed":
      return t("web.admin.settings.storage.failure.commit");
    default:
      return t("web.admin.settings.storage.failure.unknown");
  }
}

function verifiedObjectsLabel(t: Translate, count: number): string {
  return t("web.admin.settings.storage.verifiedObjects", { count, count_text: String(count) });
}

function recoveryStateTitle(t: Translate, state: string | undefined): string {
  switch (state) {
    case "running":
      return t("web.admin.settings.storage.recovery.titleRunning");
    case "waiting_retry":
      return t("web.admin.settings.storage.recovery.titleWaitingRetry");
    case "blocked":
      return t("web.admin.settings.storage.recovery.titleBlocked");
    default:
      return t("web.admin.settings.storage.recovery.titlePending");
  }
}

function recoveryStateMessage(t: Translate, state: string | undefined): string {
  switch (state) {
    case "running":
      return t("web.admin.settings.storage.recovery.messageRunning");
    case "waiting_retry":
      return t("web.admin.settings.storage.recovery.messageWaitingRetry");
    case "blocked":
      return t("web.admin.settings.storage.recovery.messageBlocked");
    default:
      return t("web.admin.settings.storage.recovery.messagePending");
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
  const t = useT();
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const keys = scope === "public" ? PUBLIC_S3_KEYS : PRIVATE_S3_KEYS;
  const key = (suffix: string) => `s3.${scope}_${suffix}`;
  const urlAuth = form.getValue("s3.public_url_auth") || "presigned";

  // Everything that reaches the bucket is shown. Only public storage has an
  // Advanced tier: the CDN address and the Cloudflare token settings, which
  // exist only for the link modes that use them.
  const advancedCount =
    scope === "public"
      ? (urlAuth !== "presigned" ? 1 : 0) + (urlAuth === "cloudflare_token" ? 3 : 0)
      : 0;
  const advancedChanged = countDirty(form, PUBLIC_S3_ADVANCED_KEYS);

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
        message:
          error instanceof Error ? error.message : t("web.admin.settings.storage.s3.checkFailed"),
      });
    }
  }

  return (
    <FieldGroup label={label} description={description} restartAll={allRestart(restartKeys, keys)}>
      <SettingField
        label={t("web.admin.settings.storage.s3.endpoint")}
        hint="https://s3.us-east-1.amazonaws.com"
        value={form.getValue(key("endpoint"))}
        onChange={(v) => form.setValue(key("endpoint"), v)}
        disabled={secrets.disabled || locationFieldsDisabled}
        restartRequired={restartKeys.has(key("endpoint"))}
      />
      <SettingField
        label={t("web.admin.settings.storage.s3.bucket")}
        value={form.getValue(key("bucket"))}
        onChange={(v) => form.setValue(key("bucket"), v)}
        disabled={secrets.disabled || locationFieldsDisabled}
        restartRequired={restartKeys.has(key("bucket"))}
      />
      <SettingField
        label={t("web.admin.settings.storage.s3.keyPrefix")}
        description={t("web.admin.settings.storage.s3.keyPrefixDescription")}
        value={form.getValue(key("key_prefix"))}
        onChange={(v) => form.setValue(key("key_prefix"), v)}
        disabled={secrets.disabled || locationFieldsDisabled}
        restartRequired={restartKeys.has(key("key_prefix"))}
      />
      {(scope === "public" ? PUBLIC_S3_IDENTITY_KEYS : PRIVATE_S3_IDENTITY_KEYS).some((k) =>
        form.isDirty(k),
      ) && (
        <div className="settings-field-note my-3 flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-4">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <div className="text-[13px] leading-relaxed">
            <p className="font-medium text-amber-500">
              {t("web.admin.settings.storage.s3.locationChange")}
            </p>
            {publicBackendChangePending ||
            artworkLockedBackend === "s3" ||
            (scope === "private" && artworkLockedBackend !== undefined) ? (
              <p className="text-muted-foreground mt-1">
                {publicBackendChangePending
                  ? t("web.admin.settings.storage.s3.locationChangeFromLocal")
                  : scope === "private" && artworkLockedBackend !== "s3"
                    ? t("web.admin.settings.storage.s3.locationChangePrivate")
                    : t("web.admin.settings.storage.s3.locationChangeS3")}
              </p>
            ) : (
              <p className="text-muted-foreground mt-1">
                {scope === "public"
                  ? t("web.admin.settings.storage.s3.locationUnlockedPublic")
                  : t("web.admin.settings.storage.s3.locationUnlockedPrivate")}
              </p>
            )}
          </div>
        </div>
      )}
      <SettingField
        label={t("web.admin.settings.storage.s3.region")}
        description={t("web.admin.settings.storage.s3.regionDescription")}
        value={form.getValue(key("region"))}
        onChange={(v) => form.setValue(key("region"), v)}
        restartRequired={restartKeys.has(key("region"))}
      />
      <SettingField
        label={t("web.admin.settings.storage.s3.pathStyle")}
        type="toggle"
        description={t("web.admin.settings.storage.s3.pathStyleDescription")}
        value={form.getValue(key("path_style"))}
        onChange={(v) => form.setValue(key("path_style"), v)}
        restartRequired={restartKeys.has(key("path_style"))}
      />
      {/*
        The access and secret keys must stay configured together, so clearing
        one alone is refused at save time. Both carry the action, which is what
        lets an admin stage the pair and hand this bucket back to anonymous or
        instance-role access.
      */}
      <SecretField
        label={t("web.admin.settings.storage.s3.accessKey")}
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
        label={t("web.admin.settings.storage.s3.secretKey")}
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
      {scope === "public" && (
        <>
          <SettingField
            label={t("web.admin.settings.storage.s3.urlAuth")}
            type="select"
            description={t("web.admin.settings.storage.s3.urlAuthDescription")}
            value={urlAuth}
            onChange={(v) => form.setValue("s3.public_url_auth", v)}
            options={[
              { value: "presigned", label: t("web.admin.settings.storage.s3.urlAuthPresigned") },
              { value: "public", label: t("web.admin.settings.storage.s3.urlAuthPublic") },
              {
                value: "cloudflare_token",
                label: t("web.admin.settings.storage.s3.urlAuthCloudflare"),
              },
            ]}
            restartRequired={restartKeys.has("s3.public_url_auth")}
          />
          {advancedCount > 0 && (
            <AdvancedSection
              id="storage.s3.public"
              count={advancedCount}
              forceOpen={advancedChanged > 0}
            >
              <SettingField
                label={t("web.admin.settings.storage.s3.readEndpoint")}
                hint="https://cdn.example.com"
                value={form.getValue("s3.public_read_endpoint")}
                onChange={(v) => form.setValue("s3.public_read_endpoint", v)}
                restartRequired={restartKeys.has("s3.public_read_endpoint")}
              />
              {urlAuth === "cloudflare_token" && (
                <>
                  {/*
                    Cloudflare token auth requires this secret, so a clear only
                    saves alongside a switch back to another mode — the one
                    order that works, since the field is hidden in those modes.
                  */}
                  <SecretField
                    label={t("web.admin.settings.storage.s3.tokenSecret")}
                    value={form.getValue("s3.public_token_secret")}
                    configured={form.sensitiveConfigured.includes("s3.public_token_secret")}
                    onKeep={() => secrets.keepSaved("s3.public_token_secret")}
                    onClear={() => secrets.clearSaved("s3.public_token_secret")}
                    cleared={form.isClearStaged("s3.public_token_secret")}
                    onChange={(v) => secrets.setSecret("s3.public_token_secret", v)}
                    hint={t("web.admin.settings.storage.s3.tokenSecretHint")}
                    disabled={secrets.disabled}
                    restartRequired={restartKeys.has("s3.public_token_secret")}
                  />
                  <SettingField
                    label={t("web.admin.settings.storage.s3.tokenParam")}
                    description={t("web.admin.settings.storage.s3.tokenParamDescription")}
                    value={form.getValue("s3.public_token_param") || "verify"}
                    onChange={(v) => form.setValue("s3.public_token_param", v)}
                    restartRequired={restartKeys.has("s3.public_token_param")}
                  />
                  <SettingField
                    label={t("web.admin.settings.storage.s3.tokenTtl")}
                    type="number"
                    unit={t("web.admin.settings.storage.unitSeconds")}
                    value={form.getValue("s3.public_token_ttl") || "10800"}
                    onChange={(v) => form.setValue("s3.public_token_ttl", v)}
                    restartRequired={restartKeys.has("s3.public_token_ttl")}
                  />
                </>
              )}
            </AdvancedSection>
          )}
        </>
      )}
    </FieldGroup>
  );
}

export default function StorageSettings() {
  const t = useT();
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
  // What Backend shows: the running backend once storage is locked, unless a
  // different choice is staged.
  const selectedBackend =
    artworkLocked && artworkStorage?.backend && !form.isDirty("artwork.storage_backend")
      ? artworkStorage.backend
      : form.getValue("artwork.storage_backend") || "auto";
  const localDiskSelected = selectedBackend === "local";
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
  // Public storage only serves S3 and Automatic, so Local disk hides it unless
  // one of its fields holds an unsaved edit; choosing S3 stages the backend,
  // which brings it back before any transition review. Local disk keeps
  // avatars, diagnostics, and catalog artifacts in the local path too, so
  // private storage shows there only while a saved or staged bucket holds them.
  const showPublicStorage = !localDiskSelected || countDirty(form, PUBLIC_S3_KEYS) > 0;
  const showPrivateStorage =
    !localDiskSelected || Boolean(persistedPrivateBucket) || countDirty(form, PRIVATE_S3_KEYS) > 0;
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
  const privateEndpointValue = form.getValue("s3.private_endpoint").trim();
  const privateTargetLabel = privateBucketValue
    ? privateEndpointValue
      ? t("web.admin.settings.storage.target.privateBucket", {
          bucket: privateBucketValue,
          endpoint: privateEndpointValue,
        })
      : t("web.admin.settings.storage.target.privateBucketNoEndpoint", {
          bucket: privateBucketValue,
        })
    : transitionBackend === "local"
      ? t("web.admin.settings.storage.target.noPrivateBucketLocal")
      : t("web.admin.settings.storage.target.noPrivateBucketS3");
  const publicBucketValue = form.getValue("s3.public_bucket");
  const publicEndpointValue = form.getValue("s3.public_endpoint").trim();
  const publicTargetLabel = publicBucketValue
    ? publicEndpointValue
      ? t("web.admin.settings.storage.target.publicBucket", {
          bucket: publicBucketValue,
          endpoint: publicEndpointValue,
        })
      : t("web.admin.settings.storage.target.publicBucketNoEndpoint", { bucket: publicBucketValue })
    : publicEndpointValue
      ? t("web.admin.settings.storage.target.publicNoBucket", { endpoint: publicEndpointValue })
      : t("web.admin.settings.storage.target.publicNoBucketNoEndpoint");
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
        toast.error(t("web.admin.settings.storage.lockUnknownSave"));
        return;
      }
      await saveSettings(safeKeys);
      return;
    }
    if (storageLocationChangePending) {
      if (!managedTransitionsAvailable) {
        toast.error(t("web.admin.settings.storage.transitionsUnavailable"));
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
      toast.error(t("web.admin.settings.storage.lockUnknownMove"));
      return;
    }
    if (!managedTransitionsAvailable) {
      toast.error(t("web.admin.settings.storage.transitionsUnavailable"));
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
      toast.success(t("web.admin.settings.storage.transitionQueued", { id: accepted.job.id }));
    } catch {
      toast.error(t("web.admin.settings.storage.transitionQueueFailed"));
    }
  }

  function handleArtworkBackendChange(value: string) {
    if (!storageStatusKnown) return;
    const currentBackend = artworkStorage?.backend;
    const requestedBackend = effectiveArtworkBackend(value, form.getValue("s3.public_bucket"));
    if (artworkLocked && currentBackend && requestedBackend !== currentBackend) {
      if (!managedTransitionsAvailable) {
        toast.error(t("web.admin.settings.storage.transitionsUnavailable"));
        return;
      }
      // A move to S3 is staged, which shows Public storage so the bucket can
      // be filled in; Save then reads Review transition. A move back to local
      // disk needs nothing more, so its review opens now.
      if (requestedBackend === "s3") {
        form.setValue("artwork.storage_backend", value);
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
          <p className="text-sm font-medium">
            {t("web.admin.settings.storage.credentialStatusUnavailable")}
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {t("web.admin.settings.storage.reloadBeforeEditing")}
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
          <p className="text-sm font-medium">
            {t("web.admin.settings.storage.lockStatusUnavailable")}
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {t("web.admin.settings.storage.reloadBeforeEditing")}
          </p>
        </div>
      </div>
    );
  }

  if (form.isLoading || !form.sensitiveStatusReady || !artworkStorage)
    return (
      <div className="space-y-6" role="status" aria-label={t("web.admin.settings.storage.loading")}>
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">{t("web.admin.settings.storage.loading")}</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.storage.title")} />

      <div className="flex-1 space-y-5">
        {!storageStatusKnown ? (
          <div
            className="flex items-start justify-between gap-3 rounded-xl border border-red-500/20 bg-red-500/5 p-4"
            role="alert"
          >
            <div>
              <p className="text-sm font-medium">
                {t("web.admin.settings.storage.lockStatusUnavailable")}
              </p>
              <p className="text-muted-foreground mt-1 text-xs">
                {t("web.admin.settings.storage.lockStatusRetryHint")}
              </p>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void serverStatus.refetch()}
            >
              {t("web.admin.settings.storage.retryStatus")}
            </Button>
          </div>
        ) : null}
        <FieldGroup
          label={t("web.admin.settings.storage.group")}
          restartAll={allRestart(restartKeys, STORAGE_GROUP_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.storage.backend")}
            type="select"
            value={selectedBackend}
            onChange={handleArtworkBackendChange}
            disabled={!storageStatusKnown}
            options={[
              { value: "auto", label: t("web.admin.settings.storage.backendAuto") },
              { value: "local", label: t("web.admin.settings.storage.backendLocal") },
              { value: "s3", label: "S3" },
            ]}
            description={
              artworkLocked
                ? artworkStorage?.backend === "s3"
                  ? t("web.admin.settings.storage.backendLockedS3")
                  : t("web.admin.settings.storage.backendLockedLocal")
                : t("web.admin.settings.storage.backendDescription")
            }
            restartRequired={restartKeys.has("artwork.storage_backend")}
          />
          <SettingField
            label={t("web.admin.settings.storage.localPath")}
            hint={LOCAL_PATH_EXAMPLE}
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
                  ? t("web.admin.settings.storage.localPathLockedS3")
                  : t("web.admin.settings.storage.localPathLockedLocal")
                : t("web.admin.settings.storage.localPathDescription")
            }
            restartRequired={restartKeys.has("artwork.local_path")}
          />
          <SettingField
            label={t("web.admin.settings.storage.cacheImages")}
            type="toggle"
            description={t("web.admin.settings.storage.cacheImagesDescription")}
            value={form.getValue(CACHE_IMAGES_KEY)}
            onChange={(value) => form.setValue(CACHE_IMAGES_KEY, value)}
            restartRequired={restartKeys.has(CACHE_IMAGES_KEY)}
          />
        </FieldGroup>
        {activeTransition ? (
          <div className="border-border/60 bg-card/40 rounded-xl border p-4">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0 space-y-1">
                <p className="text-sm font-medium">
                  {activeTransition.status === "queued"
                    ? t("web.admin.settings.storage.active.queued")
                    : t("web.admin.settings.storage.active.running")}
                </p>
                <p className="text-muted-foreground text-xs">
                  {transitionPhaseLabel(t, latestTransitionResult?.phase)}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => transitionJobs.refetch()}
                >
                  {t("web.admin.settings.storage.active.refresh")}
                </Button>
                {activeTransition ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={async () => {
                      try {
                        await cancelTransition.mutateAsync(activeTransition.id);
                        toast.success(t("web.admin.settings.storage.active.cancelRequested"));
                      } catch {
                        toast.error(t("web.admin.settings.storage.active.cancelFailed"));
                      }
                    }}
                    disabled={cancelTransition.isPending}
                  >
                    {cancelTransition.isPending
                      ? t("web.admin.settings.storage.active.cancelling")
                      : t("web.admin.settings.storage.active.cancel")}
                  </Button>
                ) : null}
              </div>
            </div>
            {verifiedObjectCount !== undefined ? (
              <p
                className="text-muted-foreground mt-3 text-xs"
                aria-label={t("web.admin.settings.storage.active.verifiedLabel")}
              >
                {verifiedObjectsLabel(t, verifiedObjectCount)}
              </p>
            ) : null}
          </div>
        ) : null}
        {recoveryHealth.data?.recovery_pending ? (
          <div className="border-border/60 bg-card/40 rounded-xl border p-4" role="status">
            <div className="space-y-1">
              <p className="text-sm font-medium">
                {recoveryStateTitle(t, recoveryHealth.data.recovery_state)}
              </p>
              <p className="text-muted-foreground text-xs">
                {recoveryStateMessage(t, recoveryHealth.data.recovery_state)}
              </p>
              {recoveryHealth.data.recovery_error ? (
                <p className="text-destructive text-xs">
                  {t("web.admin.settings.storage.recovery.needsAttention")}
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
                <p className="text-sm font-medium">
                  {failedTransition.status === "cancelled"
                    ? t("web.admin.settings.storage.failed.titleCancelled")
                    : t("web.admin.settings.storage.failed.titleFailed")}
                </p>
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.settings.storage.failed.body", {
                    reason:
                      failedTransition.status === "cancelled"
                        ? t("web.admin.settings.storage.failed.canceledReason")
                        : transitionFailureLabel(t, latestTransitionResult?.failure_category),
                  })}
                </p>
                {verifiedObjectCount !== undefined ? (
                  <p className="text-muted-foreground text-xs">
                    {verifiedObjectsLabel(t, verifiedObjectCount)}
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
                {t("web.admin.settings.storage.dismiss")}
              </Button>
            </div>
          </div>
        ) : null}
        {manualRestartTransition ? (
          <div className="rounded-xl border border-amber-500/25 bg-amber-500/5 p-4" role="alert">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0 space-y-1">
                <p className="text-sm font-medium">
                  {t("web.admin.settings.storage.manualRestart.title")}
                </p>
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.settings.storage.manualRestart.body")}
                </p>
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="shrink-0"
                onClick={() => setDismissedTransitionId(manualRestartTransition.id)}
              >
                {t("web.admin.settings.storage.dismiss")}
              </Button>
            </div>
          </div>
        ) : null}
        <Dialog open={transitionOpen} onOpenChange={setTransitionOpen}>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>
                {privateOnlyTransition
                  ? t("web.admin.settings.storage.dialog.titlePrivate")
                  : t("web.admin.settings.storage.dialog.titleArtwork")}
              </DialogTitle>
              <DialogDescription>
                {t("web.admin.settings.storage.dialog.description")}
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-5">
              {currentSourceUsesS3 ? (
                sourceHealth.isPending && !sourceHealth.data ? (
                  <div
                    className="border-border/60 bg-muted/20 space-y-2 rounded-lg border p-4"
                    role="status"
                    aria-label={t("web.admin.settings.storage.dialog.checkingSource")}
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
                        {sourceHealth.data?.message ??
                          t("web.admin.settings.storage.dialog.sourceCheckFailed")}
                      </p>
                      <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                        {t("web.admin.settings.storage.dialog.sourceUnavailableHint")}
                      </p>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="mt-3"
                        onClick={() => void sourceHealth.refetch()}
                        disabled={sourceHealth.isFetching}
                      >
                        {sourceHealth.isFetching
                          ? t("web.admin.settings.storage.dialog.checking")
                          : t("web.admin.settings.storage.dialog.retryCheck")}
                      </Button>
                    </div>
                  </div>
                ) : sourceHealth.data ? (
                  <div
                    className="rounded-lg border border-emerald-500/25 bg-emerald-500/5 px-4 py-3"
                    role="status"
                  >
                    <p className="text-sm font-medium text-emerald-600 dark:text-emerald-400">
                      {t("web.admin.settings.storage.dialog.sourceReachable")}
                    </p>
                    <p className="text-muted-foreground mt-1 text-xs">
                      {t("web.admin.settings.storage.dialog.copyOptionsAvailable")}
                    </p>
                  </div>
                ) : null
              ) : null}

              <div className="space-y-2">
                <p className="text-sm font-medium" id="storage-transition-target-label">
                  {t("web.admin.settings.storage.dialog.target")}
                </p>
                <div
                  className="border-border bg-muted/20 rounded-md border px-3 py-2.5"
                  role="group"
                  aria-labelledby="storage-transition-target-label"
                >
                  <p className="text-sm font-medium">
                    {privateOnlyTransition
                      ? t("web.admin.settings.storage.dialog.targetPrivate")
                      : transitionBackend === "local"
                        ? t("web.admin.settings.storage.dialog.targetLocal")
                        : t("web.admin.settings.storage.dialog.targetS3")}
                  </p>
                  <p className="text-muted-foreground mt-0.5 text-xs">
                    {privateOnlyTransition
                      ? privateTargetLabel
                      : transitionBackend === "local"
                        ? t("web.admin.settings.storage.dialog.targetLocalSource")
                        : publicTargetLabel}
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
                  {t("web.admin.settings.storage.dialog.privateEndpointMissing")}
                </p>
              ) : null}

              {privateOnlyTransition ? null : transitionBackend === "local" ? (
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="storage-transition-local-path">
                    {t("web.admin.settings.storage.dialog.localPath")}
                  </label>
                  <Input
                    id="storage-transition-local-path"
                    value={transitionLocalPath}
                    onChange={(event) => setTransitionLocalPath(event.target.value)}
                    placeholder={LOCAL_PATH_EXAMPLE}
                  />
                </div>
              ) : (
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.settings.storage.dialog.s3ValuesNote")}
                </p>
              )}

              <fieldset className="space-y-2">
                <legend className="mb-2 text-sm font-medium">
                  {t("web.admin.settings.storage.policy.legend")}
                </legend>
                {(
                  (privateOnlyTransition
                    ? [
                        [
                          "preserve_uploads",
                          t("web.admin.settings.storage.policy.privatePreserve"),
                          t("web.admin.settings.storage.policy.privatePreserveDescription"),
                        ],
                        [
                          "start_fresh",
                          t("web.admin.settings.storage.policy.startFresh"),
                          t("web.admin.settings.storage.policy.privateStartFreshDescription"),
                        ],
                        [
                          "migrate_all",
                          t("web.admin.settings.storage.policy.privateMigrateAll"),
                          t("web.admin.settings.storage.policy.privateMigrateAllDescription"),
                        ],
                      ]
                    : [
                        [
                          "preserve_uploads",
                          t("web.admin.settings.storage.policy.preserve"),
                          privateLocationChanging
                            ? t("web.admin.settings.storage.policy.preserveWithAvatarsDescription")
                            : t("web.admin.settings.storage.policy.preserveDescription"),
                        ],
                        [
                          "start_fresh",
                          t("web.admin.settings.storage.policy.startFresh"),
                          privateLocationChanging
                            ? t(
                                "web.admin.settings.storage.policy.startFreshWithAvatarsDescription",
                              )
                            : t("web.admin.settings.storage.policy.startFreshDescription"),
                        ],
                        [
                          "migrate_all",
                          t("web.admin.settings.storage.policy.migrateAll"),
                          t("web.admin.settings.storage.policy.migrateAllDescription"),
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
                            ? t("web.admin.settings.storage.policy.unavailableSource")
                            : t("web.admin.settings.storage.policy.unavailableNoPrivateBucket")}
                        </span>
                      ) : null}
                    </span>
                  </label>
                ))}
              </fieldset>

              <div className="rounded-lg border border-amber-500/25 bg-amber-500/5 p-4 text-xs leading-relaxed">
                <p className="font-medium text-amber-500">
                  {t("web.admin.settings.storage.notes.title")}
                </p>
                <ul className="text-muted-foreground mt-2 list-disc space-y-1 ps-4">
                  <li>{t("web.admin.settings.storage.notes.metadataRetained")}</li>
                  {!privateOnlyTransition ? (
                    <>
                      <li>{t("web.admin.settings.storage.notes.backfill")}</li>
                      <li>{t("web.admin.settings.storage.notes.sidecar")}</li>
                    </>
                  ) : null}
                  {transitionBackend === "local" && currentSourceIsS3 ? (
                    <li>
                      {transitionPolicy === "start_fresh"
                        ? t("web.admin.settings.storage.notes.toLocalStartFresh")
                        : transitionPolicy === "preserve_uploads"
                          ? t("web.admin.settings.storage.notes.toLocalPreserve")
                          : t("web.admin.settings.storage.notes.toLocalMigrateAll")}
                    </li>
                  ) : privateOnlyTransition ? (
                    <li>{t("web.admin.settings.storage.notes.privateOnly")}</li>
                  ) : privateLocationChanging ? (
                    <li>{t("web.admin.settings.storage.notes.privateChanging")}</li>
                  ) : (
                    <li>{t("web.admin.settings.storage.notes.privateUnchanged")}</li>
                  )}
                  <li>{t("web.admin.settings.storage.notes.restartRequired")}</li>
                </ul>
              </div>
            </div>

            <DialogFooter>
              <Button variant="outline" onClick={() => setTransitionOpen(false)}>
                {t("common.cancel")}
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
                {createTransition.isPending
                  ? t("web.admin.settings.storage.dialog.queuing")
                  : t("web.admin.settings.storage.dialog.queue")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        {showPublicStorage && (
          <S3Group
            form={form}
            restartKeys={restartKeys}
            secrets={secrets}
            scope="public"
            label={t("web.admin.settings.storage.publicGroup")}
            description={t("web.admin.settings.storage.publicGroupDescription")}
            checkKind="s3_public"
            artworkLockedBackend={artworkLocked ? artworkStorage?.backend : undefined}
            publicBackendChangePending={publicBackendChangePending}
            locationFieldsDisabled={!storageStatusKnown}
          />
        )}
        {showPrivateStorage && (
          <S3Group
            form={form}
            restartKeys={restartKeys}
            secrets={secrets}
            scope="private"
            label={t("web.admin.settings.storage.privateGroup")}
            description={t("web.admin.settings.storage.privateGroupDescription")}
            checkKind="s3_private"
            artworkLockedBackend={privateLocked ? artworkStorage?.backend : undefined}
            locationFieldsDisabled={!storageStatusKnown}
          />
        )}
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={handleSave}
        onDiscard={handleDiscard}
        isSaving={form.isSaving || saveInProgress}
        saveLabel={
          storageStatusKnown && storageLocationChangePending
            ? t("web.admin.settings.storage.reviewTransition")
            : t("common.save")
        }
      />
    </div>
  );
}
