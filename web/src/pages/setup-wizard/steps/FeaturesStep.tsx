import { useState } from "react";

import {
  ConnectionCheckAction,
  useConnectionCheck,
} from "@/components/admin/ConnectionCheckAction";
import { MobilePushPrivacyDisclosure } from "@/components/notifications/MobilePushPrivacyDisclosure";
import { Button } from "@/components/ui/button";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import {
  RECOMMENDATION_PROVIDER_OPTIONS,
  matchRecommendationProviderPreset,
  type RecommendationProviderPreset,
} from "@/lib/recommendation-provider-presets";
import { SettingField } from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const APPLE_PUSH_KEY = "notifications.apple_push_delivery_enabled";
const ANDROID_PUSH_KEY = "notifications.android_push_delivery_enabled";

const MARKER_KEYS = ["markers.mode", "markers.lazy_playback", "markers.online_storage"];

const DOWNLOAD_KEYS = [
  "download.enabled",
  "download.server_bandwidth_mbps",
  "download.user_bandwidth_mbps",
  "download.max_concurrent_per_user",
];

const RECS_KEYS = [
  "recommendations.enabled",
  "recommendations.embedding_base_url",
  "recommendations.embedding_model",
  "recommendations.embedding_auth_token",
];

const KEYS = [APPLE_PUSH_KEY, ANDROID_PUSH_KEY, ...MARKER_KEYS, ...DOWNLOAD_KEYS, ...RECS_KEYS];

export function FeaturesStep() {
  const form = useSettingsForm({ keys: KEYS });
  const { handleSubmit, busy } = useStepSubmit("features", form, "Failed to save");
  const recsCheck = useConnectionCheck("recommendations_embedding", form, RECS_KEYS);
  const [showDisclosure, setShowDisclosure] = useState(false);
  const [presetId, setPresetId] = useState<string | null>(null);

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

  const baseUrl = form.getValue("recommendations.embedding_base_url");
  const model = form.getValue("recommendations.embedding_model");
  const activePreset =
    RECOMMENDATION_PROVIDER_OPTIONS.find((p) => p.id === presetId) ??
    matchRecommendationProviderPreset(baseUrl, model) ??
    null;
  const tokenConfigured = form.sensitiveConfigured.includes("recommendations.embedding_auth_token");
  const showToken =
    activePreset?.needsToken ??
    (form.getValue("recommendations.embedding_auth_token") !== "" || tokenConfigured);

  const enabledFeatures = [
    onlineMarkersEnabled || localMarkersEnabled ? "Skip markers" : null,
    pushEnabled ? "Push" : null,
    downloadsEnabled ? "Downloads" : null,
    recsEnabled ? "Recommendations" : null,
  ].filter(Boolean);
  useStepSummary("features", enabledFeatures.length ? enabledFeatures.join(", ") : "Nothing extra");

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

  function applyPreset(preset: RecommendationProviderPreset) {
    setPresetId(preset.id);
    if (preset.id !== "custom") {
      form.setValue("recommendations.embedding_base_url", preset.baseUrl);
      form.setValue("recommendations.embedding_model", preset.model);
    }
  }

  if (form.isPending) return <StepSkeleton rows={4} />;

  // Require the snapshot so enabled defaults are reviewed before continuing.
  if (form.loadError) {
    return (
      <StepFrame
        title="Features"
        lede="Couldn't load the current settings for this step."
        onContinue={() => window.location.reload()}
        continueLabel="Reload"
      >
        <StepSection>
          <p className="text-muted-foreground py-3 text-sm">
            Online markers, local marker detection, and mobile push are on by default. Reload to
            review these settings before continuing.
          </p>
        </StepSection>
      </StepFrame>
    );
  }

  return (
    <StepFrame
      title="Features"
      lede="Choose the features you want to use. You can change these settings later."
      onSubmit={handleSubmit}
      busy={busy}
      footnote="Each of these has its own page under Admin › Settings."
    >
      <StepSection>
        <SettingField
          label="Skip markers from TheIntroDB"
          type="toggle"
          description="TheIntroDB is installed by default to find intros, credits, recaps, and other segments you can skip. Online markers are preferred over local detection."
          value={onlineMarkersEnabled ? "true" : "false"}
          onChange={(value) => setOnlineMarkers(value === "true")}
        />
        {onlineMarkersEnabled ? (
          <SettingField
            label="Save online markers"
            type="select"
            description={
              onlineMarkerStorage === "stored"
                ? "Silo saves markers from enabled providers such as TheIntroDB. The Sync online markers task fetches missing markers and refreshes saved markers daily at 03:00 (server time) by default."
                : "Fetch markers when needed without saving them to your library. Scheduled online sync is disabled."
            }
            options={[
              { value: "stored", label: "Save to library" },
              { value: "on_demand", label: "Fetch when needed" },
            ]}
            value={onlineMarkerStorage}
            onChange={(value) => {
              form.setValue("markers.online_storage", value);
              if (value === "on_demand") form.setValue("markers.lazy_playback", "true");
            }}
          />
        ) : null}
        <SettingField
          label="Detect markers on this server"
          type="toggle"
          description="Detect missing intros using this server's CPU. Saved online intros take priority. Applies to libraries with marker detection enabled."
          value={localMarkersEnabled ? "true" : "false"}
          onChange={(value) => setLocalMarkers(value === "true")}
        />
        <SettingField
          label="Mobile push notifications"
          type="toggle"
          description={
            <>
              Delivers to the iOS and Android apps through Silo's open-source relay. On by default.{" "}
              <button
                type="button"
                className="text-foreground underline underline-offset-2"
                onClick={() => setShowDisclosure((v) => !v)}
                aria-expanded={showDisclosure}
              >
                {showDisclosure ? "Hide what the relay sees" : "What does the relay see?"}
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
          label="Downloads"
          type="toggle"
          description="Let people save files to their devices for offline viewing."
          value={downloadsEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("download.enabled", v)}
        />
        {downloadsEnabled ? (
          <>
            <SettingField
              label="Total download bandwidth"
              type="number"
              unit="Mbps"
              description="Shared across everyone. 0 means unlimited."
              value={form.getValue("download.server_bandwidth_mbps")}
              onChange={(v) => form.setValue("download.server_bandwidth_mbps", v)}
            />
            <SettingField
              label="Per-person download bandwidth"
              type="number"
              unit="Mbps"
              description="0 means unlimited."
              value={form.getValue("download.user_bandwidth_mbps")}
              onChange={(v) => form.setValue("download.user_bandwidth_mbps", v)}
            />
          </>
        ) : null}
        <SettingField
          label="Recommendations"
          type="toggle"
          description="Suggests what to watch next using embeddings from an AI provider. Needs the pgvector extension."
          value={recsEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("recommendations.enabled", v)}
        />
      </StepSection>

      {recsEnabled ? (
        <StepSection title="Recommendation provider">
          <div className="py-3">
            <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Provider">
              {RECOMMENDATION_PROVIDER_OPTIONS.map((preset) => {
                const selected = activePreset?.id === preset.id;
                return (
                  <Button
                    key={preset.id}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    variant={selected ? "default" : "secondary"}
                    size="sm"
                    onClick={() => applyPreset(preset)}
                  >
                    {preset.label}
                    {preset.tag ? (
                      <span className="ml-1.5 text-[10px] opacity-70">{preset.tag}</span>
                    ) : null}
                  </Button>
                );
              })}
            </div>
            {activePreset ? (
              <p className="text-muted-foreground mt-2 text-xs">{activePreset.description}</p>
            ) : null}
          </div>
          <SettingField
            label="Base URL"
            hint="https://generativelanguage.googleapis.com"
            value={baseUrl}
            onChange={(v) => form.setValue("recommendations.embedding_base_url", v)}
          />
          <SettingField
            label="Model"
            hint="gemini-embedding-001"
            value={model}
            onChange={(v) => form.setValue("recommendations.embedding_model", v)}
          />
          {showToken ? (
            <SettingField
              label="API key"
              type="password"
              value={form.getValue("recommendations.embedding_auth_token")}
              onChange={(v) => form.setValue("recommendations.embedding_auth_token", v)}
              sensitiveConfigured={tokenConfigured}
            />
          ) : null}
          <ConnectionCheckAction
            onClick={recsCheck.run}
            result={recsCheck.result}
            isPending={recsCheck.isPending}
            disabled={busy}
            label="Check connection"
          />
        </StepSection>
      ) : null}
    </StepFrame>
  );
}
