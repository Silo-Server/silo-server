import { ArrowRight } from "lucide-react";
import { Link } from "react-router";

import { useAdminMarkerCapabilities } from "@/hooks/queries/admin/markers";
import type { RestartKeyMatcher } from "@/hooks/useRestartKeys";
import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { MarkerTasksCard } from "./MarkerTasksCard";
import { SettingField } from "./SettingField";
import { MARKER_KEYS } from "./playbackSettings.keys";
import { markerSources } from "./useMarkerSourceDraft";

/** The Skip markers group on Playback settings. */
export function MarkerSettingsGroup({
  form,
  restartKeys,
}: {
  form: ReturnType<typeof useSettingsForm>;
  restartKeys: RestartKeyMatcher;
}) {
  const t = useT();
  // An older API node saves the detection kind switches but ignores them, so
  // they are offered only where the server says it honors them.
  const { data: markerCapabilities } = useAdminMarkerCapabilities();
  const detectionKindSettings = markerCapabilities?.detection_kind_settings === true;
  const markerMode = form.getValue("markers.mode") || "both";
  const { online: onlineMarkersEnabled, local: localMarkersEnabled } = markerSources(markerMode);
  const onlineMarkerStorage = form.getValue("markers.online_storage") || "stored";
  // Lazy playback looks stored online markers up at playback and runs local
  // detection there. On-demand online markers are looked up at playback
  // either way, so only online-only on-demand leaves it nothing to change.
  const lazyPlaybackOnline = onlineMarkersEnabled && onlineMarkerStorage === "stored";
  const showLazyPlayback = localMarkersEnabled || lazyPlaybackOnline;
  // Each marker row the source would hide stays in view, marked as changed,
  // while it holds an unsaved edit.
  const detectIntrosKept = !localMarkersEnabled && form.isDirty("markers.detect_intros");
  const detectCreditsKept = !localMarkersEnabled && form.isDirty("markers.detect_credits");
  const detectionWorkersKept = !localMarkersEnabled && form.isDirty("markers.detection_workers");
  const onlineStorageKept = !onlineMarkersEnabled && form.isDirty("markers.online_storage");
  const lazyPlaybackKept = !showLazyPlayback && form.isDirty("markers.lazy_playback");

  // Detection behavior lives here; which online providers answer a lookup,
  // and on what terms, is provider configuration and lives with the other
  // providers.
  return (
    <FieldGroup
      label={t("web.admin.settings.playback.markers.group")}
      description={t("web.admin.settings.playback.markers.groupDescription")}
      restartAll={MARKER_KEYS.every((key) => restartKeys.has(key))}
      actions={
        <Link
          to="/admin/settings/providers"
          className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
        >
          {t("web.admin.settings.playback.markers.providersLink")}
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      }
    >
      <SettingField
        label={t("web.admin.settings.playback.markers.mode")}
        type="select"
        description={t("web.admin.settings.playback.markers.modeDescription")}
        className="[&_[data-slot=select-trigger]]:h-auto [&_[data-slot=select-trigger]]:min-h-9 [&_[data-slot=select-value]]:line-clamp-none [&_[data-slot=select-value]]:text-start [&_[data-slot=select-value]]:whitespace-normal"
        options={[
          { value: "off", label: t("web.admin.settings.playback.markers.modeOff") },
          { value: "local", label: t("web.admin.settings.playback.markers.modeLocal") },
          { value: "both", label: t("web.admin.settings.playback.markers.modeBoth") },
          { value: "online", label: t("web.admin.settings.playback.markers.modeOnline") },
        ]}
        value={markerMode}
        onChange={(value) => form.setValue("markers.mode", value)}
        restartRequired={restartKeys.has("markers.mode")}
      />

      {detectionKindSettings && (localMarkersEnabled || detectIntrosKept) && (
        <SettingField
          label={t("web.admin.settings.playback.markers.detectIntros")}
          dirty={detectIntrosKept}
          type="toggle"
          description={t("web.admin.settings.playback.markers.detectIntrosDescription")}
          value={form.getValue("markers.detect_intros") || "true"}
          onChange={(value) => form.setValue("markers.detect_intros", value)}
          restartRequired={restartKeys.has("markers.detect_intros")}
        />
      )}
      {detectionKindSettings && (localMarkersEnabled || detectCreditsKept) && (
        <SettingField
          label={t("web.admin.settings.playback.markers.detectCredits")}
          dirty={detectCreditsKept}
          type="toggle"
          description={t("web.admin.settings.playback.markers.detectCreditsDescription")}
          value={form.getValue("markers.detect_credits") || "true"}
          onChange={(value) => form.setValue("markers.detect_credits", value)}
          restartRequired={restartKeys.has("markers.detect_credits")}
        />
      )}

      {(onlineMarkersEnabled || onlineStorageKept) && (
        <SettingField
          label={t("web.admin.settings.playback.markers.onlineStorage")}
          dirty={onlineStorageKept}
          type="select"
          description={
            onlineMarkerStorage === "stored"
              ? t("web.admin.settings.playback.markers.onlineStorageStored")
              : t("web.admin.settings.playback.markers.onlineStorageOnDemand")
          }
          options={[
            { value: "stored", label: t("web.admin.settings.playback.markers.storageStored") },
            {
              value: "on_demand",
              label: t("web.admin.settings.playback.markers.storageOnDemand"),
            },
          ]}
          value={onlineMarkerStorage}
          onChange={(value) => form.setValue("markers.online_storage", value)}
          restartRequired={restartKeys.has("markers.online_storage")}
        />
      )}

      {(showLazyPlayback || lazyPlaybackKept) && (
        <SettingField
          label={t("web.admin.settings.playback.markers.lazyPlayback")}
          dirty={lazyPlaybackKept}
          type="toggle"
          description={
            onlineMarkersEnabled && !localMarkersEnabled
              ? t("web.admin.settings.playback.markers.lazyPlaybackOnline")
              : lazyPlaybackOnline
                ? t("web.admin.settings.playback.markers.lazyPlaybackBoth")
                : t("web.admin.settings.playback.markers.lazyPlaybackLocal")
          }
          value={form.getValue("markers.lazy_playback") || "true"}
          onChange={(value) => form.setValue("markers.lazy_playback", value)}
          restartRequired={restartKeys.has("markers.lazy_playback")}
        />
      )}

      {(localMarkersEnabled || detectionWorkersKept) && (
        <SettingField
          label={t("web.admin.settings.playback.markers.detectionWorkers")}
          dirty={detectionWorkersKept}
          type="number"
          description={t("web.admin.settings.playback.markers.detectionWorkersDescription")}
          value={form.getValue("markers.detection_workers")}
          onChange={(value) => form.setValue("markers.detection_workers", value)}
          restartRequired={restartKeys.has("markers.detection_workers")}
        />
      )}

      <div className="py-3.5">
        <MarkerTasksCard />
      </div>
    </FieldGroup>
  );
}
