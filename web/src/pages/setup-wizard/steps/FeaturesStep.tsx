import { useEffect, useRef, useState } from "react";

import {
  ConnectionCheckAction,
  useConnectionCheck,
} from "@/components/admin/ConnectionCheckAction";
import { MobilePushPrivacyDisclosure } from "@/components/notifications/MobilePushPrivacyDisclosure";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";
import { MARKER_SHARING_DISCLOSURE_KEY } from "@/lib/markers";
import { EmbeddingProviderField } from "@/pages/admin-settings/AIProviders";
import { ModelField } from "@/pages/admin-settings/AIModelField";
import { SettingField } from "@/pages/admin-settings/SettingField";
import { markerSources, useMarkerSourceDraft } from "@/pages/admin-settings/useMarkerSourceDraft";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const APPLE_PUSH_KEY = "notifications.apple_push_delivery_enabled";
const ANDROID_PUSH_KEY = "notifications.android_push_delivery_enabled";

const SHARE_MARKERS_KEY = "markers.share_by_default";

const MARKER_KEYS = [
  "markers.mode",
  "markers.lazy_playback",
  "markers.online_storage",
  SHARE_MARKERS_KEY,
];

const SERVER_BANDWIDTH_KEY = "download.server_bandwidth_mbps";
const USER_BANDWIDTH_KEY = "download.user_bandwidth_mbps";
const DOWNLOAD_KEYS = [
  "download.enabled",
  SERVER_BANDWIDTH_KEY,
  USER_BANDWIDTH_KEY,
  "download.max_concurrent_per_user",
];

const EMBEDDING_PROVIDER_KEY = "recommendations.embedding_provider_id";
const EMBEDDING_MODEL_KEY = "recommendations.embedding_model";
const RECS_KEYS = ["recommendations.enabled", EMBEDDING_PROVIDER_KEY, EMBEDDING_MODEL_KEY];

const KEYS = [APPLE_PUSH_KEY, ANDROID_PUSH_KEY, ...MARKER_KEYS, ...DOWNLOAD_KEYS, ...RECS_KEYS];

export function FeaturesStep() {
  const t = useT();
  const form = useSettingsForm({ keys: KEYS });
  const { handleSubmit, busy } = useStepSubmit("features", form, t("web.setup.wizard.saveFailed"));
  const recsCheck = useConnectionCheck("recommendations_embedding", form, RECS_KEYS);
  const [showDisclosure, setShowDisclosure] = useState(false);
  const sharePrefilled = useRef(false);

  // A new server shares markers by default only once this choice is saved,
  // so stage the switch's on default while nothing is stored. Continuing
  // saves it; leaving the step unsaved keeps sharing off.
  const { loaded, getPersistedValue, setValue } = form;
  useEffect(() => {
    if (sharePrefilled.current || !loaded) return;
    sharePrefilled.current = true;
    if (getPersistedValue(SHARE_MARKERS_KEY) === "") setValue(SHARE_MARKERS_KEY, "true");
  }, [loaded, getPersistedValue, setValue]);

  // Both push keys are written together; either still on means push is on.
  // The snapshot carries the server default (on) for a fresh install, so the
  // switch reads on until the admin turns it off.
  const pushEnabled =
    form.getValue(APPLE_PUSH_KEY) === "true" || form.getValue(ANDROID_PUSH_KEY) === "true";
  const downloadsEnabled = form.getValue("download.enabled") === "true";
  // A field hidden by the switch above it stays in view, marked as changed,
  // while it holds an unsaved edit, so Continue never saves a value the admin
  // can't see.
  const serverBandwidthKept = !downloadsEnabled && form.isDirty(SERVER_BANDWIDTH_KEY);
  const userBandwidthKept = !downloadsEnabled && form.isDirty(USER_BANDWIDTH_KEY);
  const recsEnabled = form.getValue("recommendations.enabled") === "true";
  const embeddingProviderKept = !recsEnabled && form.isDirty(EMBEDDING_PROVIDER_KEY);
  const embeddingModelKept = !recsEnabled && form.isDirty(EMBEDDING_MODEL_KEY);
  const markerMode = form.getValue("markers.mode");
  const { online: onlineMarkersEnabled, local: localMarkersEnabled } = markerSources(markerMode);
  const onlineMarkerStorage = form.getValue("markers.online_storage") || "stored";
  // The storage choice stays in view while it holds an edit, after online
  // markers are turned off.
  const onlineStorageKept = !onlineMarkersEnabled && form.isDirty("markers.online_storage");
  // Unset reads as on, the default the effect above stages.
  const shareMarkers = form.getValue(SHARE_MARKERS_KEY) !== "false";

  const embeddingProviderID = form.getValue(EMBEDDING_PROVIDER_KEY);

  const enabledFeatures = [
    onlineMarkersEnabled || localMarkersEnabled ? t("web.setup.features.summarySkipMarkers") : null,
    pushEnabled ? t("web.setup.features.summaryPush") : null,
    downloadsEnabled ? t("web.setup.features.downloads") : null,
    recsEnabled ? t("web.setup.features.recommendations") : null,
  ].filter(Boolean);
  useStepSummary(
    "features",
    enabledFeatures.length ? enabledFeatures.join(", ") : t("web.setup.features.summaryNone"),
  );

  // Switching back to how the switch read when saved takes both keys back,
  // rather than writing a platform whose saved value differs.
  const savedPushEnabled =
    form.getPersistedValue(APPLE_PUSH_KEY) === "true" ||
    form.getPersistedValue(ANDROID_PUSH_KEY) === "true";
  function setPush(value: boolean) {
    for (const key of [APPLE_PUSH_KEY, ANDROID_PUSH_KEY]) {
      if (value === savedPushEnabled) form.resetValue(key);
      else form.setValue(key, String(value));
    }
  }

  // This step has no lazy playback row: the value only ever comes with a
  // marker switch, and is taken back with it.
  const markerDraft = useMarkerSourceDraft(form);

  function setOnlineMarkers(enabled: boolean) {
    let nextMode = enabled ? "online" : "off";
    if (localMarkersEnabled) nextMode = enabled ? "both" : "local";
    markerDraft.setMarkerMode(nextMode);
  }

  function setLocalMarkers(enabled: boolean) {
    let nextMode = enabled ? "local" : "off";
    if (onlineMarkersEnabled) nextMode = enabled ? "both" : "online";
    markerDraft.setMarkerMode(nextMode);
  }

  if (form.isPending) return <StepSkeleton rows={4} />;

  // Require the snapshot so enabled defaults are reviewed before continuing.
  if (form.loadError) {
    return (
      <StepFrame
        title={t("web.setup.features.title")}
        lede={t("web.setup.features.loadFailedLede")}
        onContinue={() => window.location.reload()}
        continueLabel={t("web.setup.features.reload")}
      >
        <StepSection>
          <p className="text-muted-foreground py-3 text-sm">
            {t("web.setup.features.loadFailedBody")}
          </p>
        </StepSection>
      </StepFrame>
    );
  }

  return (
    <StepFrame
      title={t("web.setup.features.title")}
      lede={t("web.setup.features.lede")}
      onSubmit={handleSubmit}
      busy={busy}
      footnote={t("web.setup.features.footnote")}
    >
      <StepSection>
        <SettingField
          label={t("web.setup.features.onlineMarkersLabel")}
          type="toggle"
          description={t("web.setup.features.onlineMarkersDescription")}
          value={onlineMarkersEnabled ? "true" : "false"}
          onChange={(value) => setOnlineMarkers(value === "true")}
        />
        {onlineMarkersEnabled || onlineStorageKept ? (
          <SettingField
            label={t("web.setup.features.storageLabel")}
            dirty={onlineStorageKept}
            type="select"
            description={
              onlineMarkerStorage === "stored"
                ? t("web.setup.features.storageStoredDescription")
                : t("web.setup.features.storageOnDemandDescription")
            }
            options={[
              { value: "stored", label: t("web.setup.features.storageStored") },
              { value: "on_demand", label: t("web.setup.features.storageOnDemand") },
            ]}
            value={onlineMarkerStorage}
            onChange={(value) => form.setValue("markers.online_storage", value)}
          />
        ) : null}
        <SettingField
          label={t("web.setup.features.localMarkersLabel")}
          type="toggle"
          description={t("web.setup.features.localMarkersDescription")}
          value={localMarkersEnabled ? "true" : "false"}
          onChange={(value) => setLocalMarkers(value === "true")}
        />
        <SettingField
          label={t("web.setup.features.shareMarkersLabel")}
          type="toggle"
          description={t(MARKER_SHARING_DISCLOSURE_KEY)}
          value={shareMarkers ? "true" : "false"}
          onChange={(value) => form.setValue(SHARE_MARKERS_KEY, value)}
        />
        <SettingField
          label={t("web.setup.features.pushLabel")}
          type="toggle"
          description={
            <>
              {t("web.setup.features.pushDescription")}{" "}
              <button
                type="button"
                className="text-foreground underline underline-offset-2"
                onClick={() => setShowDisclosure((v) => !v)}
                aria-expanded={showDisclosure}
              >
                {showDisclosure
                  ? t("web.setup.features.pushDisclosureHide")
                  : t("web.setup.features.pushDisclosureShow")}
              </button>
            </>
          }
          value={pushEnabled ? "true" : "false"}
          onChange={(v) => setPush(v === "true")}
        />
        {showDisclosure ? (
          <div className="px-0">
            <MobilePushPrivacyDisclosure />
          </div>
        ) : null}
        <SettingField
          label={t("web.setup.features.downloads")}
          type="toggle"
          description={t("web.setup.features.downloadsDescription")}
          value={downloadsEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("download.enabled", v)}
        />
        {(downloadsEnabled || serverBandwidthKept) && (
          <SettingField
            label={t("web.setup.features.serverBandwidthLabel")}
            dirty={serverBandwidthKept}
            type="number"
            unit={t("web.setup.features.mbps")}
            description={t("web.setup.features.serverBandwidthDescription")}
            value={form.getValue(SERVER_BANDWIDTH_KEY)}
            onChange={(v) => form.setValue(SERVER_BANDWIDTH_KEY, v)}
          />
        )}
        {(downloadsEnabled || userBandwidthKept) && (
          <SettingField
            label={t("web.setup.features.userBandwidthLabel")}
            dirty={userBandwidthKept}
            type="number"
            unit={t("web.setup.features.mbps")}
            description={t("web.setup.features.userBandwidthDescription")}
            value={form.getValue(USER_BANDWIDTH_KEY)}
            onChange={(v) => form.setValue(USER_BANDWIDTH_KEY, v)}
          />
        )}
        <SettingField
          label={t("web.setup.features.recommendations")}
          type="toggle"
          description={t("web.setup.features.recommendationsDescription")}
          value={recsEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("recommendations.enabled", v)}
          managedByEnv={form.envVar("recommendations.enabled")}
        />
      </StepSection>

      {recsEnabled || embeddingProviderKept || embeddingModelKept ? (
        <StepSection title={t("web.setup.features.providerTitle")}>
          {(recsEnabled || embeddingProviderKept) && (
            <EmbeddingProviderField
              value={embeddingProviderID}
              dirty={embeddingProviderKept}
              onChange={(id) => form.setValue(EMBEDDING_PROVIDER_KEY, id)}
              managedByEnv={form.envVar(EMBEDDING_PROVIDER_KEY)}
            />
          )}
          {(recsEnabled || embeddingModelKept) && (
            <ModelField
              label={t("web.setup.features.modelLabel")}
              dirty={embeddingModelKept}
              providerID={embeddingProviderID}
              kind="embeddings"
              value={form.getValue(EMBEDDING_MODEL_KEY)}
              onChange={(v) => form.setValue(EMBEDDING_MODEL_KEY, v)}
              managedByEnv={form.envVar(EMBEDDING_MODEL_KEY)}
              // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- example model identifier
              placeholder="gemini-embedding-001"
            />
          )}
          {recsEnabled ? (
            <ConnectionCheckAction
              onClick={recsCheck.run}
              result={recsCheck.result}
              isPending={recsCheck.isPending}
              disabled={busy}
              label={t("web.setup.wizard.checkConnection")}
            />
          ) : null}
        </StepSection>
      ) : null}
    </StepFrame>
  );
}
