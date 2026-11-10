import { useMemo } from "react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { useHWAccelDetection, type HWAccelInfo } from "@/hooks/queries/admin/system";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT, type Translate } from "@/i18n";
import { SettingField, SettingFieldStatus } from "@/pages/admin-settings/SettingField";
import {
  HW_ACCEL_OPTIONS,
  describeDetection,
  hardwareVerified,
  formatResolved,
  hasHardwareToneMapCapability,
} from "@/pages/admin-settings/playbackSettings.utils";
import {
  DEFAULT_FFMPEG_PATH,
  DEFAULT_TRANSCODE_DIR,
} from "@/pages/admin-settings/settingsPathDefaults";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const KEYS = [
  "playback.transcode_enabled",
  "playback.hw_accel",
  "playback.transcode_hardware_tone_map_enabled",
  "playback.transcode_software_tone_map_enabled",
  "allow_4k_transcode",
  "playback.ffmpeg_path",
  "playback.transcode_dir",
];

type Status = { tone: "ok" | "warn" | "muted"; text: string };

function statusLine(status: Status | undefined) {
  return status ? (
    <SettingFieldStatus tone={status.tone}>{status.text}</SettingFieldStatus>
  ) : undefined;
}

function playbackSummary(
  t: Translate,
  transcodeEnabled: boolean,
  hwAccel: string,
  probed: HWAccelInfo | undefined,
): string | undefined {
  if (!transcodeEnabled) return t("web.setup.playback.summaryTranscodingOff");
  if (hwAccel === "none") return t("web.setup.playback.summarySoftware");
  if (hardwareVerified(probed)) return formatResolved(probed.resolved);
  return hwAccel === "auto" ? undefined : formatResolved(hwAccel);
}

export function PlaybackStep() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit(
    "playback",
    form,
    t("web.setup.playback.saveFailed"),
  );

  const transcodeEnabled = form.getValue("playback.transcode_enabled") !== "false";
  const hwAccel = form.getValue("playback.hw_accel") || "auto";
  const detection = useHWAccelDetection(hwAccel !== "none");
  const probed = hwAccel === "none" ? undefined : detection.data;
  const detectedLabel = describeDetection(probed);
  const hardwareFound = hardwareVerified(probed);
  const hardwareToneMapAvailable = hasHardwareToneMapCapability(probed);

  useStepSummary("playback", playbackSummary(t, transcodeEnabled, hwAccel, probed));

  if (form.isPending) return <StepSkeleton rows={4} />;

  let hwStatus: Status | undefined;
  if (hwAccel !== "none") {
    if (detectedLabel) hwStatus = { tone: hardwareFound ? "ok" : "warn", text: detectedLabel };
    else if (detection.isLoading)
      hwStatus = { tone: "muted", text: t("web.setup.playback.detecting") };
    else if (detection.isError)
      hwStatus = { tone: "warn", text: t("web.setup.playback.detectFailed") };
  }
  let gpuToneMapStatus: Status | undefined;
  if (hardwareToneMapAvailable) {
    gpuToneMapStatus = { tone: "ok", text: t("web.setup.playback.gpuToneMapAvailable") };
  } else if (hwAccel !== "none" && probed) {
    gpuToneMapStatus = { tone: "muted", text: t("web.setup.playback.gpuToneMapMissing") };
  }

  return (
    <StepFrame
      title={t("web.setup.playback.title")}
      lede={t("web.setup.playback.lede")}
      onSubmit={handleSubmit}
      busy={busy}
      onSkip={skip}
      footnote={t("web.setup.playback.footnote")}
    >
      <StepSection>
        <SettingField
          label={t("web.setup.playback.transcodingLabel")}
          type="toggle"
          description={t("web.setup.playback.transcodingDescription")}
          value={transcodeEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("playback.transcode_enabled", v)}
        />
        <SettingField
          label={t("web.setup.playback.hwAccelLabel")}
          type="select"
          options={HW_ACCEL_OPTIONS}
          description={t("web.setup.playback.hwAccelDescription")}
          status={statusLine(hwStatus)}
          value={hwAccel}
          onChange={(v) => form.setValue("playback.hw_accel", v)}
          disabled={!transcodeEnabled}
        />
        <SettingField
          label={t("web.setup.playback.allow4kLabel")}
          type="toggle"
          description={t("web.setup.playback.allow4kDescription")}
          value={form.getValue("allow_4k_transcode")}
          onChange={(v) => form.setValue("allow_4k_transcode", v)}
          disabled={!transcodeEnabled}
        />
      </StepSection>

      <StepSection
        title={t("web.setup.playback.toneMapTitle")}
        caption={t("web.setup.playback.toneMapCaption")}
      >
        <SettingField
          label={t("web.setup.playback.gpuToneMapLabel")}
          type="toggle"
          description={t("web.setup.playback.gpuToneMapDescription")}
          status={statusLine(gpuToneMapStatus)}
          value={form.getValue("playback.transcode_hardware_tone_map_enabled")}
          onChange={(v) => form.setValue("playback.transcode_hardware_tone_map_enabled", v)}
          disabled={!transcodeEnabled}
        />
        <SettingField
          label={t("web.setup.playback.cpuToneMapLabel")}
          type="toggle"
          description={t("web.setup.playback.cpuToneMapDescription")}
          value={form.getValue("playback.transcode_software_tone_map_enabled")}
          onChange={(v) => form.setValue("playback.transcode_software_tone_map_enabled", v)}
          disabled={!transcodeEnabled}
        />
      </StepSection>

      <StepSection>
        <AdvancedSection
          id="setup.playback"
          title={t("web.setup.playback.pathsTitle")}
          count={2}
          forceOpen={form.isDirty("playback.ffmpeg_path") || form.isDirty("playback.transcode_dir")}
        >
          <PathSettingField
            label={t("web.setup.playback.ffmpegPathLabel")}
            defaultValue={DEFAULT_FFMPEG_PATH}
            description={t("web.setup.playback.ffmpegPathDescription", {
              path: DEFAULT_FFMPEG_PATH,
            })}
            value={form.getValue("playback.ffmpeg_path")}
            onChange={(v) => form.setValue("playback.ffmpeg_path", v)}
          />
          <PathSettingField
            label={t("web.setup.playback.transcodeDirLabel")}
            defaultValue={DEFAULT_TRANSCODE_DIR}
            description={t("web.setup.playback.transcodeDirDescription", {
              path: DEFAULT_TRANSCODE_DIR,
            })}
            value={form.getValue("playback.transcode_dir")}
            onChange={(v) => form.setValue("playback.transcode_dir", v)}
          />
        </AdvancedSection>
      </StepSection>
    </StepFrame>
  );
}
