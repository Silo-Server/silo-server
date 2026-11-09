import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
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
  const restartKeys = useRestartKeys();
  if (!showsPreviewKey(KEY, execution, form.isDirty)) return null;
  return (
    <SettingField
      label="Seek previews per transcode node"
      type="number"
      description="How many files one transcode node makes seek previews for at once, across every server, 1 to 64. Each counts toward the node's job limit, so leave room for playback."
      dirty={form.isDirty(KEY)}
      value={form.getValue(KEY) || "1"}
      onChange={(v) => form.setValue(KEY, v)}
      restartRequired={restartKeys.has(KEY)}
    />
  );
}
