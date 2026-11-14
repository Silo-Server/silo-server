import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useT } from "@/i18n";
import { SettingField } from "./SettingField";
import { TRICKPLAY_NODE_CAPACITY_KEY as KEY, showsPreviewKey } from "./playbackSettings.utils";

type SettingsForm = Pick<ReturnType<typeof useSettingsForm>, "getValue" | "setValue" | "isDirty">;

/** How many seek preview runs one transcode node takes at once. */
export function TrickplayNodeCapacityField({
  form,
  execution,
}: {
  form: SettingsForm;
  execution: string;
}) {
  const t = useT();
  const restartKeys = useRestartKeys();
  if (!showsPreviewKey(KEY, execution, form.isDirty)) return null;
  return (
    <SettingField
      label={t("web.admin.settings.playback.previews.trickplayNodeCapacity")}
      type="number"
      description={t("web.admin.settings.playback.previews.trickplayNodeCapacityDescription")}
      dirty={form.isDirty(KEY)}
      value={form.getValue(KEY) || "1"}
      onChange={(v) => form.setValue(KEY, v)}
      restartRequired={restartKeys.has(KEY)}
    />
  );
}
