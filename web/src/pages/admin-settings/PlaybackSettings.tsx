import { useMemo, useState } from "react";
import { ArrowRight } from "lucide-react";
import { Link } from "react-router";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useHWAccelDetection } from "@/hooks/queries/admin/system";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { useLibraryCapabilities } from "@/hooks/queries/admin/libraries";
import { useAdminMarkerCapabilities } from "@/hooks/queries/admin/markers";
import { useAdminTrickplayLibraries } from "@/hooks/queries/admin/trickplay";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Switch } from "@/components/ui/switch";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { SaveBar } from "@/components/SaveBar";
import { useT } from "@/i18n";
import { FieldGroup } from "./FieldGroup";
import { MarkerTasksCard } from "./MarkerTasksCard";
import { TrickplayNodeCapacityField } from "./TrickplayNodeCapacityField";
import { DEFAULT_FFMPEG_PATH, DEFAULT_TRANSCODE_DIR } from "./settingsPathDefaults";
import {
  IMAGE_EXECUTION_DEFAULT,
  HW_ACCEL_OPTIONS,
  buildHWDeviceRows,
  imageExecutionOptions,
  describeDetection,
  hardwareVerified,
  hasUsableTranscodeNode,
  hasUsableTrickplayNode,
  nodeInventoriesDiverge,
  parseHWDeviceList,
  toggleHWDevice,
  showsPreviewKey,
} from "./playbackSettings.utils";
import {
  KEYS,
  MARKER_KEYS,
  PREVIEW_ADVANCED_KEYS,
  PREVIEW_IMAGE_KEYS,
  PREVIEW_PLACEMENT_KEYS,
  PREVIEW_WIDTH_KEY,
  REMAKE_DEFAULTS,
  remakeDescription,
  SPEED_TEST_KEYS,
  TRANSCODING_ADVANCED_KEYS,
  TRANSCODING_ESSENTIAL_KEYS,
  TRICKPLAY_INTERVAL_KEY,
  WATCH_KEYS,
} from "./playbackSettings.keys";

export default function PlaybackSettings() {
  const t = useT();
  const supportsTrickplay = useLibraryCapabilities().data?.trickplay === true;
  const keys = useMemo(
    () =>
      supportsTrickplay
        ? KEYS
        : KEYS.filter((key) => key !== PREVIEW_WIDTH_KEY && !key.startsWith("playback.trickplay_")),
    [supportsTrickplay],
  );
  const form = useSettingsForm({ keys });
  const restartKeys = useRestartKeys();
  const hwAccel = form.getValue("playback.hw_accel");
  const hwDetection = useHWAccelDetection(hwAccel !== "none");
  const hwDevice = form.getValue("playback.hw_device");
  const selectedDevices = parseHWDeviceList(hwDevice);
  const deviceRows = buildHWDeviceRows(hwDetection.data, hwDevice);
  const detectedPaths = deviceRows.filter((row) => row.detected).map((row) => row.path);
  // Balancing is QSV/VAAPI-only: NVENC addresses GPUs by CUDA index/UUID, so
  // the multi-select picker is hidden for it (the server uses the first
  // configured entry).
  const isNvenc =
    hwAccel === "nvenc" || (hwAccel === "auto" && hwDetection.data?.resolved === "nvenc");
  const inventoriesDiverge = nodeInventoriesDiverge(hwDetection.data);
  const showDevicePicker = hwAccel !== "none" && !isNvenc && deviceRows.length > 0;

  const nodes = useAdminNodes();
  const trickplayExecution =
    form.getValue("playback.trickplay_execution") || IMAGE_EXECUTION_DEFAULT;
  // Pending replacements can retain a publication. Include them when asking
  // before a width or interval change; a server without previews reports none.
  const trickplayLibraries = useAdminTrickplayLibraries({ enabled: supportsTrickplay });
  const previewsToRemake = (trickplayLibraries.data ?? []).reduce(
    (count, library) => count + library.ready + library.running + library.pending,
    0,
  );
  const changed = (key: string) =>
    form.isDirty(key) &&
    (form.getValue(key) || REMAKE_DEFAULTS[key]) !==
      (form.getPersistedValue(key) || REMAKE_DEFAULTS[key]);
  const widthChanged = changed(PREVIEW_WIDTH_KEY);
  const asksToRemake =
    widthChanged ||
    (changed(TRICKPLAY_INTERVAL_KEY) && (!trickplayLibraries.isSuccess || previewsToRemake > 0));
  const [confirmRemake, setConfirmRemake] = useState(false);
  const save = () => {
    if (asksToRemake) {
      setConfirmRemake(true);
      return;
    }
    return form.save();
  };
  const chapterExecution =
    form.getValue("playback.chapter_thumbnail_execution") || IMAGE_EXECUTION_DEFAULT;
  // Gate the node-backed extraction modes only on a node list we actually
  // have: while the query is in flight or after it failed, leave every option
  // reachable rather than blocking a valid choice on a transient error.
  const transcodeNodeAvailable = !nodes.isSuccess || hasUsableTranscodeNode(nodes.data);
  const trickplayNodeAvailable = !nodes.isSuccess || hasUsableTrickplayNode(nodes.data);
  // Showing the placement selects takes a node list that has one, so a
  // single-server page never shows them while the list loads.
  const showPreviewPlacement = hasUsableTranscodeNode(nodes.data);
  const previewAdvancedKeys = [
    ...PREVIEW_ADVANCED_KEYS,
    ...(showPreviewPlacement ? [] : PREVIEW_PLACEMENT_KEYS),
  ].filter((key) => keys.includes(key) && showsPreviewKey(key, trickplayExecution, form.isDirty));

  const isDirty = form.isDirty;
  const anyDirty = (keys: readonly string[]) => keys.some((key) => isDirty(key));
  const allRestart = (keys: readonly string[]) => keys.every((key) => restartKeys.has(key));

  const throttleOn = form.getValue("enable_transcode_throttle") === "true";
  const allow4KTranscode = form.getValue("allow_4k_transcode") === "true";
  // Every advanced transcoding key has one row, except the device picker,
  // which renders only when it applies.
  const transcodingAdvancedCount = TRANSCODING_ADVANCED_KEYS.length - (showDevicePicker ? 0 : 1);

  // An older API node saves the detection kind switches but ignores them, so
  // they are offered only where the server says it honors them.
  const { data: markerCapabilities } = useAdminMarkerCapabilities();
  const detectionKindSettings = markerCapabilities?.detection_kind_settings === true;
  const markerMode = form.getValue("markers.mode") || "both";
  const onlineMarkersEnabled = markerMode === "online" || markerMode === "both";
  const onlineMarkerStorage = form.getValue("markers.online_storage") || "stored";
  const localMarkersEnabled = markerMode === "local" || markerMode === "both";
  const showLazyPlayback =
    markerMode !== "off" && (!onlineMarkersEnabled || onlineMarkerStorage === "stored");

  const detection = hwAccel === "none" ? undefined : hwDetection.data;
  const detectedLabel = describeDetection(detection);

  // Everything the hardware-acceleration field has to say about itself, in its
  // own status slot. The NVENC note used to be a bare paragraph between rows,
  // which read as a row with no label and broke the group's rhythm.
  const hwAccelLines =
    hwAccel === "none"
      ? []
      : [
          detectedLabel ? (
            <SettingFieldStatus key="detected" tone={hardwareVerified(detection) ? "ok" : "warn"}>
              {detectedLabel}
            </SettingFieldStatus>
          ) : hwDetection.isLoading ? (
            <SettingFieldStatus key="detecting" tone="muted">
              {t("web.admin.settings.playback.hwAccel.detecting")}
            </SettingFieldStatus>
          ) : null,
          isNvenc && selectedDevices.length > 1 ? (
            <SettingFieldStatus key="nvenc" tone="warn">
              {t("web.admin.settings.playback.hwAccel.nvencFirstDevice", {
                device: selectedDevices[0],
              })}
            </SettingFieldStatus>
          ) : null,
        ].filter(Boolean);
  const hwAccelStatus =
    hwAccelLines.length > 0 ? (
      <span className="flex flex-col items-start gap-1">{hwAccelLines}</span>
    ) : undefined;

  const chapterExecutionField = (
    <SettingField
      label={t("web.admin.settings.playback.previews.chapterExecution")}
      type="select"
      options={imageExecutionOptions(chapterExecution, transcodeNodeAvailable)}
      status={
        transcodeNodeAvailable ? undefined : (
          <SettingFieldStatus tone="warn">
            {t("web.admin.settings.playback.previews.noTranscodeNodes")}
          </SettingFieldStatus>
        )
      }
      value={chapterExecution}
      onChange={(v) => form.setValue("playback.chapter_thumbnail_execution", v)}
      restartRequired={restartKeys.has("playback.chapter_thumbnail_execution")}
    />
  );
  const trickplayExecutionField = supportsTrickplay ? (
    <SettingField
      label={t("web.admin.settings.playback.previews.trickplayExecution")}
      type="select"
      options={imageExecutionOptions(trickplayExecution, trickplayNodeAvailable)}
      status={
        trickplayNodeAvailable ? undefined : (
          <SettingFieldStatus tone="warn">
            {t("web.admin.settings.playback.previews.noTrickplayNode")}
          </SettingFieldStatus>
        )
      }
      value={trickplayExecution}
      onChange={(v) => form.setValue("playback.trickplay_execution", v)}
      restartRequired={restartKeys.has("playback.trickplay_execution")}
    />
  ) : null;

  if (form.isLoading) return <div>{t("web.admin.settings.playback.loading")}</div>;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.playback.title")} />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label={t("web.admin.settings.playback.watch.group")}
          restartAll={allRestart(WATCH_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.playback.watch.markWatched")}
            type="number"
            unit="%"
            value={form.getValue("playback.watched_threshold")}
            onChange={(v) => form.setValue("playback.watched_threshold", v)}
            restartRequired={restartKeys.has("playback.watched_threshold")}
          />
          <SettingField
            label={t("web.admin.settings.playback.watch.minResume")}
            type="number"
            unit="%"
            description={t("web.admin.settings.playback.watch.minResumeDescription")}
            value={form.getValue("playback.min_resume_threshold")}
            onChange={(v) => form.setValue("playback.min_resume_threshold", v)}
            restartRequired={restartKeys.has("playback.min_resume_threshold")}
          />
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.playback.transcoding.group")}
          restartAll={allRestart([...TRANSCODING_ESSENTIAL_KEYS, ...TRANSCODING_ADVANCED_KEYS])}
        >
          <SettingField
            label={t("web.admin.settings.playback.transcoding.enabled")}
            type="toggle"
            description={t("web.admin.settings.playback.transcoding.enabledDescription")}
            value={form.getValue("playback.transcode_enabled")}
            onChange={(v) => form.setValue("playback.transcode_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_enabled")}
          />
          <SettingField
            label={t("web.admin.settings.playback.hwAccel.label")}
            type="select"
            options={HW_ACCEL_OPTIONS}
            description={t("web.admin.settings.playback.hwAccel.description")}
            status={hwAccelStatus}
            value={hwAccel}
            onChange={(v) => form.setValue("playback.hw_accel", v)}
            restartRequired={restartKeys.has("playback.hw_accel")}
          />
          <SettingField
            label={t("web.admin.settings.playback.transcoding.allow4k")}
            type="toggle"
            description={t("web.admin.settings.playback.transcoding.allow4kDescription")}
            value={form.getValue("allow_4k_transcode")}
            onChange={(v) => form.setValue("allow_4k_transcode", v)}
            restartRequired={restartKeys.has("allow_4k_transcode")}
          />
          {allow4KTranscode && (
            <SettingField
              label={t("web.admin.settings.playback.transcoding.preferLower")}
              type="toggle"
              description={t("web.admin.settings.playback.transcoding.preferLowerDescription")}
              value={form.getValue("playback.prefer_lower_version_transcode")}
              onChange={(v) => form.setValue("playback.prefer_lower_version_transcode", v)}
            />
          )}
          <SettingField
            label={t("web.admin.settings.playback.transcoding.allowHevc")}
            type="toggle"
            description={t("web.admin.settings.playback.transcoding.allowHevcDescription")}
            value={form.getValue("playback.allow_hevc_encoding")}
            onChange={(v) => form.setValue("playback.allow_hevc_encoding", v)}
          />
          <SettingField
            label={t("web.admin.settings.playback.transcoding.hardwareToneMap")}
            type="toggle"
            hint={t("web.admin.settings.playback.transcoding.hardwareToneMapHint")}
            value={form.getValue("playback.transcode_hardware_tone_map_enabled")}
            onChange={(v) => form.setValue("playback.transcode_hardware_tone_map_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_hardware_tone_map_enabled")}
          />
          <SettingField
            label={t("web.admin.settings.playback.transcoding.softwareToneMap")}
            type="toggle"
            hint={t("web.admin.settings.playback.transcoding.softwareToneMapHint")}
            value={form.getValue("playback.transcode_software_tone_map_enabled")}
            onChange={(v) => form.setValue("playback.transcode_software_tone_map_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_software_tone_map_enabled")}
          />
          <SettingField
            label={t("web.admin.settings.playback.transcoding.throttle")}
            type="toggle"
            description={t("web.admin.settings.playback.transcoding.throttleDescription")}
            value={form.getValue("enable_transcode_throttle")}
            onChange={(v) => form.setValue("enable_transcode_throttle", v)}
            restartRequired={restartKeys.has("enable_transcode_throttle")}
          />
          {throttleOn && (
            <SettingField
              label={t("web.admin.settings.playback.transcoding.bufferAhead")}
              type="number"
              unit={t("web.admin.settings.playback.unitSeconds")}
              value={form.getValue("transcode_throttle_seconds")}
              onChange={(v) => form.setValue("transcode_throttle_seconds", v)}
              restartRequired={restartKeys.has("transcode_throttle_seconds")}
            />
          )}
          <PathSettingField
            label={t("web.admin.settings.playback.transcoding.dir")}
            defaultValue={DEFAULT_TRANSCODE_DIR}
            description={t("web.admin.settings.playback.transcoding.dirDescription", {
              path: DEFAULT_TRANSCODE_DIR,
            })}
            value={form.getValue("playback.transcode_dir")}
            onChange={(v) => form.setValue("playback.transcode_dir", v)}
            restartRequired={restartKeys.has("playback.transcode_dir")}
          />

          <AdvancedSection
            id="playback.transcoding"
            count={transcodingAdvancedCount}
            forceOpen={anyDirty(TRANSCODING_ADVANCED_KEYS)}
          >
            <PathSettingField
              label={t("web.admin.settings.playback.transcoding.ffmpegPath")}
              defaultValue={DEFAULT_FFMPEG_PATH}
              description={t("web.admin.settings.playback.transcoding.ffmpegPathDescription", {
                path: DEFAULT_FFMPEG_PATH,
              })}
              value={form.getValue("playback.ffmpeg_path")}
              onChange={(v) => form.setValue("playback.ffmpeg_path", v)}
              restartRequired={restartKeys.has("playback.ffmpeg_path")}
            />
            {showDevicePicker && (
              <div>
                <SettingsSubheading
                  caption={
                    selectedDevices.length === 0
                      ? t("web.admin.settings.playback.devices.captionAuto")
                      : selectedDevices.length === 1
                        ? t("web.admin.settings.playback.devices.captionOne")
                        : t("web.admin.settings.playback.devices.captionMany")
                  }
                >
                  {t("web.admin.settings.playback.devices.heading")}
                </SettingsSubheading>
                {inventoriesDiverge && (
                  <p className="pb-2 text-xs text-amber-500">
                    {t("web.admin.settings.playback.devices.inventoriesDiverge", {
                      link: (chunks) => (
                        <Link
                          to="/admin/nodes"
                          className="font-medium underline-offset-2 hover:underline"
                        >
                          {chunks}
                        </Link>
                      ),
                    })}
                  </p>
                )}
                {/* One shared row shell per device, so these switches land on the
                    same edge as every other control in the group instead of
                    hugging the panel. */}
                {deviceRows.map((row) => (
                  <SettingFieldRow
                    key={row.path}
                    label={
                      <span className={row.detected ? undefined : "text-muted-foreground"}>
                        {row.description}
                      </span>
                    }
                    description={
                      <>
                        <span className="block font-mono">{row.path}</span>
                        {row.missingOnNodes.length > 0 && (
                          <span className="mt-0.5 block text-amber-500">
                            {t("web.admin.settings.playback.devices.missingOnNodes", {
                              nodes: row.missingOnNodes.join(", "),
                            })}
                          </span>
                        )}
                      </>
                    }
                  >
                    <Switch
                      checked={selectedDevices.includes(row.path)}
                      aria-label={row.description}
                      onCheckedChange={() =>
                        form.setValue(
                          "playback.hw_device",
                          toggleHWDevice(
                            form.getValue("playback.hw_device"),
                            row.path,
                            detectedPaths,
                          ),
                        )
                      }
                    />
                  </SettingFieldRow>
                ))}
              </div>
            )}
            <SettingField
              label={t("web.admin.settings.playback.transcoding.backBuffer")}
              type="number"
              unit={t("web.admin.settings.playback.unitSeconds")}
              hint={t("web.admin.settings.playback.transcoding.backBufferHint")}
              value={form.getValue("playback.segment_retention_seconds")}
              onChange={(v) => form.setValue("playback.segment_retention_seconds", v)}
              restartRequired={restartKeys.has("playback.segment_retention_seconds")}
            />
            <SettingField
              label={t("web.admin.settings.playback.transcoding.copyKeyframePlaylist")}
              type="toggle"
              description={t(
                "web.admin.settings.playback.transcoding.copyKeyframePlaylistDescription",
              )}
              value={form.getValue("playback.copy_keyframe_playlist") || "false"}
              onChange={(v) => form.setValue("playback.copy_keyframe_playlist", v)}
              restartRequired={restartKeys.has("playback.copy_keyframe_playlist")}
            />
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.playback.speedTests.group")}
          restartAll={allRestart(SPEED_TEST_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.playback.speedTests.automatic")}
            type="toggle"
            description={t("web.admin.settings.playback.speedTests.automaticDescription")}
            value={form.getValue("playback.bandwidth_probe_automatic")}
            onChange={(v) => form.setValue("playback.bandwidth_probe_automatic", v)}
            restartRequired={restartKeys.has("playback.bandwidth_probe_automatic")}
          />
          <SettingField
            label={t("web.admin.settings.playback.speedTests.maxAge")}
            type="number"
            unit={t("web.admin.settings.playback.unitDays")}
            description={t("web.admin.settings.playback.speedTests.maxAgeDescription")}
            value={form.getValue("playback.bandwidth_probe_max_age_days")}
            onChange={(v) => form.setValue("playback.bandwidth_probe_max_age_days", v)}
            restartRequired={restartKeys.has("playback.bandwidth_probe_max_age_days")}
          />
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.playback.previews.group")}
          description={
            supportsTrickplay
              ? t("web.admin.settings.playback.previews.groupDescription")
              : t("web.admin.settings.playback.previews.groupDescriptionNoTrickplay")
          }
          restartAll={allRestart(PREVIEW_IMAGE_KEYS)}
          // FieldGroup descriptions are plain text, so the link sits on the
          // heading line, as Skip markers links to its providers.
          actions={
            <Link
              to="/admin/libraries"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              {t("web.admin.settings.playback.previews.librariesLink")}
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          {supportsTrickplay && (
            <>
              <SettingField
                label={t("web.admin.settings.playback.previews.width")}
                type="number"
                unit="px"
                description={t("web.admin.settings.playback.previews.widthDescription")}
                value={form.getValue("playback.preview_image_width")}
                onChange={(v) => form.setValue("playback.preview_image_width", v)}
                restartRequired={restartKeys.has("playback.preview_image_width")}
              />
              <SettingField
                label={t("web.admin.settings.playback.previews.interval")}
                type="number"
                unit={t("web.admin.settings.playback.unitSeconds")}
                description={t("web.admin.settings.playback.previews.intervalDescription")}
                value={form.getValue("playback.trickplay_interval_seconds")}
                onChange={(v) => form.setValue("playback.trickplay_interval_seconds", v)}
                restartRequired={restartKeys.has("playback.trickplay_interval_seconds")}
              />
            </>
          )}
          <SettingField
            label={t("web.admin.settings.playback.previews.chapterWorkers")}
            type="number"
            description={t("web.admin.settings.playback.previews.chapterWorkersDescription")}
            value={form.getValue("playback.chapter_thumbnail_workers")}
            onChange={(v) => form.setValue("playback.chapter_thumbnail_workers", v)}
            restartRequired={restartKeys.has("playback.chapter_thumbnail_workers")}
          />
          {showPreviewPlacement && (
            <>
              {chapterExecutionField}
              {trickplayExecutionField}
            </>
          )}

          <AdvancedSection
            id="playback.preview_images"
            count={previewAdvancedKeys.length}
            forceOpen={
              anyDirty(previewAdvancedKeys) ||
              // Node-only extraction with no node to run on is stranded work;
              // its warning must not sit inside a closed disclosure.
              (!transcodeNodeAvailable && chapterExecution === "transcode_nodes_only") ||
              (!trickplayNodeAvailable && trickplayExecution === "transcode_nodes_only")
            }
          >
            {!showPreviewPlacement && chapterExecutionField}
            {supportsTrickplay && (
              <SettingField
                label={t("web.admin.settings.playback.previews.trickplayWorkers")}
                type="number"
                description={t("web.admin.settings.playback.previews.trickplayWorkersDescription")}
                value={form.getValue("playback.trickplay_workers")}
                onChange={(v) => form.setValue("playback.trickplay_workers", v)}
                restartRequired={restartKeys.has("playback.trickplay_workers")}
              />
            )}
            {supportsTrickplay && (
              <TrickplayNodeCapacityField form={form} execution={trickplayExecution} />
            )}
            {!showPreviewPlacement && trickplayExecutionField}
            {/* internal/trickplay tone-maps every HDR seek preview, in
                software when it has to; these two settings are read only by
                chapter thumbnail extraction. */}
            <SettingsSubheading
              caption={
                supportsTrickplay ? t("web.admin.settings.playback.previews.hdrCaption") : undefined
              }
            >
              {t("web.admin.settings.playback.previews.hdrHeading")}
            </SettingsSubheading>
            <SettingField
              label={t("web.admin.settings.playback.previews.hdrPolicy")}
              type="select"
              options={[
                {
                  value: "best_effort",
                  label: t("web.admin.settings.playback.previews.hdrBestEffort"),
                },
                { value: "disabled", label: t("web.admin.settings.playback.previews.hdrDisabled") },
              ]}
              description={t("web.admin.settings.playback.previews.hdrPolicyDescription")}
              value={form.getValue("playback.chapter_thumbnail_hdr_policy") || "best_effort"}
              onChange={(v) => form.setValue("playback.chapter_thumbnail_hdr_policy", v)}
              restartRequired={restartKeys.has("playback.chapter_thumbnail_hdr_policy")}
            />
            <SettingField
              label={t("web.admin.settings.playback.previews.softwareToneMap")}
              type="toggle"
              description={t("web.admin.settings.playback.previews.softwareToneMapDescription")}
              value={form.getValue("playback.chapter_thumbnail_software_tone_map_enabled")}
              onChange={(v) =>
                form.setValue("playback.chapter_thumbnail_software_tone_map_enabled", v)
              }
              disabled={form.getValue("playback.chapter_thumbnail_hdr_policy") === "disabled"}
              restartRequired={restartKeys.has(
                "playback.chapter_thumbnail_software_tone_map_enabled",
              )}
            />
          </AdvancedSection>
        </FieldGroup>

        {/*
          Detection behavior lives here; which online providers answer a lookup,
          and on what terms, is provider configuration and lives with the other
          providers.
        */}
        <FieldGroup
          label={t("web.admin.settings.playback.markers.group")}
          description={t("web.admin.settings.playback.markers.groupDescription")}
          restartAll={allRestart(MARKER_KEYS)}
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
            className="[&_[data-slot=select-trigger]]:h-auto [&_[data-slot=select-trigger]]:min-h-9 [&_[data-slot=select-value]]:line-clamp-none [&_[data-slot=select-value]]:text-left [&_[data-slot=select-value]]:whitespace-normal"
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

          {localMarkersEnabled && detectionKindSettings && (
            <>
              <SettingField
                label={t("web.admin.settings.playback.markers.detectIntros")}
                type="toggle"
                description={t("web.admin.settings.playback.markers.detectIntrosDescription")}
                value={form.getValue("markers.detect_intros") || "true"}
                onChange={(value) => form.setValue("markers.detect_intros", value)}
                restartRequired={restartKeys.has("markers.detect_intros")}
              />
              <SettingField
                label={t("web.admin.settings.playback.markers.detectCredits")}
                type="toggle"
                description={t("web.admin.settings.playback.markers.detectCreditsDescription")}
                value={form.getValue("markers.detect_credits") || "true"}
                onChange={(value) => form.setValue("markers.detect_credits", value)}
                restartRequired={restartKeys.has("markers.detect_credits")}
              />
            </>
          )}

          {onlineMarkersEnabled && (
            <SettingField
              label={t("web.admin.settings.playback.markers.onlineStorage")}
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
              onChange={(value) => {
                form.setValue("markers.online_storage", value);
                if (value === "on_demand") form.setValue("markers.lazy_playback", "true");
              }}
              restartRequired={restartKeys.has("markers.online_storage")}
            />
          )}

          {showLazyPlayback && (
            <SettingField
              label={t("web.admin.settings.playback.markers.lazyPlayback")}
              type="toggle"
              description={
                onlineMarkersEnabled
                  ? markerMode === "both"
                    ? t("web.admin.settings.playback.markers.lazyPlaybackBoth")
                    : t("web.admin.settings.playback.markers.lazyPlaybackOnline")
                  : t("web.admin.settings.playback.markers.lazyPlaybackLocal")
              }
              value={form.getValue("markers.lazy_playback") || "true"}
              onChange={(value) => form.setValue("markers.lazy_playback", value)}
              restartRequired={restartKeys.has("markers.lazy_playback")}
            />
          )}

          {localMarkersEnabled && (
            <SettingField
              label={t("web.admin.settings.playback.markers.detectionWorkers")}
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
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
      <AlertDialog open={confirmRemake} onOpenChange={setConfirmRemake}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {widthChanged
                ? t("web.admin.settings.playback.remake.widthTitle")
                : t("web.admin.settings.playback.remake.intervalTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {remakeDescription(
                t,
                widthChanged,
                trickplayLibraries.isSuccess ? previewsToRemake : null,
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => void form.save().catch(() => {})}>
              {t("common.save")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
