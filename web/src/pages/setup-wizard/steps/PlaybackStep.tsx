import { useMemo } from "react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { useHWAccelDetection, type HWAccelInfo } from "@/hooks/queries/admin/system";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField, SettingFieldStatus } from "@/pages/admin-settings/SettingField";
import {
  HW_ACCEL_OPTIONS,
  describeDetection,
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
  transcodeEnabled: boolean,
  hwAccel: string,
  probed: HWAccelInfo | undefined,
): string | undefined {
  if (!transcodeEnabled) return "Transcoding off";
  if (hwAccel === "none") return "Software";
  if (probed?.resolved && probed.resolved !== "none") return formatResolved(probed.resolved);
  return hwAccel === "auto" ? undefined : formatResolved(hwAccel);
}

export function PlaybackStep() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit(
    "playback",
    form,
    "Failed to save playback settings",
  );

  const transcodeEnabled = form.getValue("playback.transcode_enabled") !== "false";
  const hwAccel = form.getValue("playback.hw_accel") || "auto";
  const detection = useHWAccelDetection(hwAccel !== "none");
  const probed = hwAccel === "none" ? undefined : detection.data;
  const detectedLabel = describeDetection(probed);
  const hardwareFound = Boolean(probed?.resolved && probed.resolved !== "none");
  const hardwareToneMapAvailable = hasHardwareToneMapCapability(probed);

  useStepSummary("playback", playbackSummary(transcodeEnabled, hwAccel, probed));

  if (form.isPending) return <StepSkeleton rows={4} />;

  let hwStatus: Status | undefined;
  if (hwAccel !== "none") {
    if (detectedLabel) hwStatus = { tone: hardwareFound ? "ok" : "warn", text: detectedLabel };
    else if (detection.isLoading) hwStatus = { tone: "muted", text: "Detecting hardware…" };
    else if (detection.isError)
      hwStatus = { tone: "warn", text: "Couldn't probe this server's hardware." };
  }
  let gpuToneMapStatus: Status | undefined;
  if (hardwareToneMapAvailable) {
    gpuToneMapStatus = { tone: "ok", text: "Hardware tone mapper available" };
  } else if (hwAccel !== "none" && probed) {
    gpuToneMapStatus = {
      tone: "muted",
      text: "No validated hardware tone mapper found yet; enabling it here does no harm.",
    };
  }

  return (
    <StepFrame
      title="Playback"
      lede="When a device can't play a file as-is, Silo converts it on the fly. A GPU makes that cheap; without one it runs on the CPU."
      onSubmit={handleSubmit}
      busy={busy}
      onSkip={skip}
      footnote="More in Admin › Settings › Playback."
    >
      <StepSection>
        <SettingField
          label="Transcoding"
          type="toggle"
          description="Off serves only files clients can already play."
          value={transcodeEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("playback.transcode_enabled", v)}
        />
        <SettingField
          label="Hardware acceleration"
          type="select"
          options={HW_ACCEL_OPTIONS}
          description="Auto picks the best device this server can see."
          status={statusLine(hwStatus)}
          value={hwAccel}
          onChange={(v) => form.setValue("playback.hw_accel", v)}
          disabled={!transcodeEnabled}
        />
        <SettingField
          label="Allow 4K transcoding"
          type="toggle"
          description="Heavy load on most hardware."
          value={form.getValue("allow_4k_transcode")}
          onChange={(v) => form.setValue("allow_4k_transcode", v)}
          disabled={!transcodeEnabled}
        />
      </StepSection>

      <StepSection
        title="HDR tone mapping"
        caption="Converts HDR video to SDR for screens that can't show HDR. Each path has to be allowed before Silo will use it."
      >
        <SettingField
          label="On the GPU"
          type="toggle"
          description="Fast. Uses a validated hardware tone mapper on this server or a transcode node."
          status={statusLine(gpuToneMapStatus)}
          value={form.getValue("playback.transcode_hardware_tone_map_enabled") || "false"}
          onChange={(v) => form.setValue("playback.transcode_hardware_tone_map_enabled", v)}
          disabled={!transcodeEnabled}
        />
        <SettingField
          label="On the CPU"
          type="toggle"
          description="Works everywhere, but a single stream can max out several cores."
          value={form.getValue("playback.transcode_software_tone_map_enabled") || "false"}
          onChange={(v) => form.setValue("playback.transcode_software_tone_map_enabled", v)}
          disabled={!transcodeEnabled}
        />
      </StepSection>

      <StepSection>
        <AdvancedSection
          id="setup.playback"
          title="Paths"
          count={2}
          forceOpen={form.isDirty("playback.ffmpeg_path") || form.isDirty("playback.transcode_dir")}
        >
          <PathSettingField
            label="FFmpeg path"
            defaultValue={DEFAULT_FFMPEG_PATH}
            description={`Leave blank to use the FFmpeg that ships with the server, at ${DEFAULT_FFMPEG_PATH}.`}
            value={form.getValue("playback.ffmpeg_path")}
            onChange={(v) => form.setValue("playback.ffmpeg_path", v)}
          />
          <PathSettingField
            label="Transcode directory"
            defaultValue={DEFAULT_TRANSCODE_DIR}
            description={`Fast local storage with room to spare. Leave blank to use ${DEFAULT_TRANSCODE_DIR}.`}
            value={form.getValue("playback.transcode_dir")}
            onChange={(v) => form.setValue("playback.transcode_dir", v)}
          />
        </AdvancedSection>
      </StepSection>
    </StepFrame>
  );
}
