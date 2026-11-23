import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useT } from "@/i18n";
import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";
import { PAUSED_SESSION_TIMEOUT_KEY as KEY } from "./playbackSettings.keys";

type SettingsForm = Pick<ReturnType<typeof useSettingsForm>, "getValue" | "setValue">;

/** How many minutes a video may stay paused before the server ends its playback session. */
export function PausedPlaybackGroup({ form }: { form: SettingsForm }) {
  const t = useT();
  const restartKeys = useRestartKeys();
  return (
    <FieldGroup
      label={t("web.admin.settings.playback.pausedPlayback.group")}
      restartAll={restartKeys.has(KEY)}
    >
      <SettingField
        label={t("web.admin.settings.playback.pausedPlayback.timeout")}
        type="number"
        unit={t("web.admin.settings.playback.unitMinutes")}
        description={t("web.admin.settings.playback.pausedPlayback.timeoutDescription")}
        value={form.getValue(KEY)}
        onChange={(v) => form.setValue(KEY, v)}
        restartRequired={restartKeys.has(KEY)}
      />
    </FieldGroup>
  );
}
