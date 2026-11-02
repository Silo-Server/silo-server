import type { useSettingsForm } from "@/hooks/useSettingsForm";
import type { RestartKeyMatcher } from "@/hooks/useRestartKeys";
import { SettingField } from "./SettingField";

const KEY = "playback.trickplay_private_storage";

export function TrickplayStorageField({
  form,
  restartKeys,
}: {
  form: ReturnType<typeof useSettingsForm>;
  restartKeys: RestartKeyMatcher;
}) {
  return (
    <SettingField
      label="Keep seek previews in the private bucket"
      type="toggle"
      description="Store seek previews in the private bucket set in Storage instead of the public one, and serve them through Silo. Useful when the public bucket charges per request. Does nothing without a private bucket. Changing it makes every seek preview again."
      value={form.getValue(KEY) || "false"}
      onChange={(v) => form.setValue(KEY, v)}
      restartRequired={restartKeys.has(KEY)}
    />
  );
}
