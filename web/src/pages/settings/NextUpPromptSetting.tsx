import { SettingRow } from "@/components/settings/SettingRow";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useT, type Translate } from "@/i18n";
import { settingDescription } from "@/lib/settingsText";
import { toast } from "sonner";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import {
  NEXT_UP_OFF,
  nextUpPickerChoices,
  nextUpPickerValue,
  nextUpPickerWrites,
} from "./nextUpPicker";

function nextUpPromptLabel(t: Translate, choice: string) {
  if (choice === NEXT_UP_OFF) return t("settings.off");
  const seconds = Number(choice);
  if (seconds <= 0) return t("settings.nextUp.atEnd");
  if (seconds % 60 === 0) {
    return t("settings.nextUp.minutesBeforeEnd", { count: seconds / 60 });
  }
  return t("settings.nextUp.secondsBeforeEnd", { count: seconds });
}

/**
 * The Show Next Up row: Off, or how long before the end of the file Next Up
 * opens. Off is offered only where the server defines next_up_enabled.
 */
export function NextUpPromptSetting({
  enabled,
  seconds,
  offOffered,
  disabled,
  save,
}: {
  enabled: boolean;
  seconds: number;
  offOffered: boolean;
  /**
   * Held until the capabilities and values arrive, also when either read
   * fails: a failed read means unknown, not old or default. Until then a choice
   * would not know whether to write next_up_enabled (so a seconds choice could
   * leave Next Up off), the row could show a lead time while Next Up is off,
   * and a device override would go uncleared.
   */
  disabled: boolean;
  /** The profile writer, which also clears a device override on the key. */
  save: (key: SettingKey, value: unknown) => Promise<void>;
}) {
  const t = useT();
  const value = nextUpPickerValue({ enabled, seconds });
  // One choice can write both Next Up keys, each through the profile writer.
  const choose = async (choice: string) => {
    try {
      for (const [key, next] of nextUpPickerWrites(choice, offOffered)) await save(key, next);
    } catch {
      toast.error(t("web.settings.playback.saveFailed"));
    }
  };
  return (
    <SettingRow
      label={t("settings.nextUp.title")}
      description={settingDescription(
        SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS],
      )}
      control={(id) => (
        <Select value={value} disabled={disabled} onValueChange={choose}>
          <SelectTrigger id={id} className="w-full sm:w-[220px]">
            {/* Named here, so a value another client stored off the list
                (the device slider allows any second) still reads as itself. */}
            <SelectValue>{nextUpPromptLabel(t, value)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {nextUpPickerChoices(offOffered).map((choice) => (
              <SelectItem key={choice} value={choice}>
                {nextUpPromptLabel(t, choice)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    />
  );
}
