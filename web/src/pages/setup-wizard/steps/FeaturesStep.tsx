import { useEffect, useRef, useState } from "react";

import {
  ConnectionCheckAction,
  useConnectionCheck,
} from "@/components/admin/ConnectionCheckAction";
import { MobilePushPrivacyDisclosure } from "@/components/notifications/MobilePushPrivacyDisclosure";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";
import { MARKER_SHARING_DISCLOSURE } from "@/lib/markers";
import { EmbeddingProviderField } from "@/pages/admin-settings/AIProviders";
import { ModelField } from "@/pages/admin-settings/AIModelField";
import { SettingField } from "@/pages/admin-settings/SettingField";

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

const DOWNLOAD_KEYS = [
  "download.enabled",
  "download.server_bandwidth_mbps",
  "download.user_bandwidth_mbps",
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
  const recsEnabled = form.getValue("recommendations.enabled") === "true";
  const markerMode = form.getValue("markers.mode");
  const onlineMarkersEnabled = markerMode === "online" || markerMode === "both";
  const localMarkersEnabled = markerMode === "local" || markerMode === "both";
  const onlineMarkerStorage = form.getValue("markers.online_storage") || "stored";
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

  function setPush(value: boolean) {
    form.setValue(APPLE_PUSH_KEY, String(value));
    form.setValue(ANDROID_PUSH_KEY, String(value));
  }

  function setOnlineMarkers(enabled: boolean) {
    let nextMode = enabled ? "online" : "off";
    if (localMarkersEnabled) nextMode = enabled ? "both" : "local";
    form.setValue("markers.mode", nextMode);
    if (enabled || !localMarkersEnabled) form.setValue("markers.lazy_playback", String(enabled));
  }

  function setLocalMarkers(enabled: boolean) {
    let nextMode = enabled ? "local" : "off";
    if (onlineMarkersEnabled) nextMode = enabled ? "both" : "online";
    form.setValue("markers.mode", nextMode);
    if (enabled || !onlineMarkersEnabled) form.setValue("markers.lazy_playback", String(enabled));
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
        {onlineMarkersEnabled ? (
          <SettingField
            label={t("web.setup.features.storageLabel")}
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
            onChange={(value) => {
              form.setValue("markers.online_storage", value);
              if (value === "on_demand") form.setValue("markers.lazy_playback", "true");
            }}
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
          description={MARKER_SHARING_DISCLOSURE}
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
        {downloadsEnabled ? (
          <>
            <SettingField
              label={t("web.setup.features.serverBandwidthLabel")}
              type="number"
              unit={t("web.setup.features.mbps")}
              description={t("web.setup.features.serverBandwidthDescription")}
              value={form.getValue("download.server_bandwidth_mbps")}
              onChange={(v) => form.setValue("download.server_bandwidth_mbps", v)}
            />
            <SettingField
              label={t("web.setup.features.userBandwidthLabel")}
              type="number"
              unit={t("web.setup.features.mbps")}
              description={t("web.setup.features.userBandwidthDescription")}
              value={form.getValue("download.user_bandwidth_mbps")}
              onChange={(v) => form.setValue("download.user_bandwidth_mbps", v)}
            />
          </>
        ) : null}
        <SettingField
          label={t("web.setup.features.recommendations")}
          type="toggle"
          description={t("web.setup.features.recommendationsDescription")}
          value={recsEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("recommendations.enabled", v)}
        />
      </StepSection>

      {recsEnabled ? (
        <StepSection title={t("web.setup.features.providerTitle")}>
          <EmbeddingProviderField
            value={embeddingProviderID}
            onChange={(id) => form.setValue(EMBEDDING_PROVIDER_KEY, id)}
          />
          <ModelField
            label={t("web.setup.features.modelLabel")}
            providerID={embeddingProviderID}
            kind="embeddings"
            value={form.getValue(EMBEDDING_MODEL_KEY)}
            onChange={(v) => form.setValue(EMBEDDING_MODEL_KEY, v)}
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- example model identifier
            placeholder="gemini-embedding-001"
          />
          <ConnectionCheckAction
            onClick={recsCheck.run}
            result={recsCheck.result}
            isPending={recsCheck.isPending}
            disabled={busy}
            label={t("web.setup.wizard.checkConnection")}
          />
        </StepSection>
      ) : null}
    </StepFrame>
  );
}
