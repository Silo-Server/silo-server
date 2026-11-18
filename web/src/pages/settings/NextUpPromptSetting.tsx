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
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { NEXT_UP_OFF } from "./nextUpPicker";

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
 * opens. Values and choices come from nextUpPicker.
 */
export function NextUpPromptSetting({
  value,
  choices,
  disabled,
  onChange,
}: {
  value: string;
  choices: string[];
  disabled: boolean;
  onChange: (choice: string) => void;
}) {
  const t = useT();
  return (
    <SettingRow
      label={t("settings.nextUp.title")}
      description={settingDescription(
        SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS],
      )}
      control={(id) => (
        <Select value={value} disabled={disabled} onValueChange={onChange}>
          <SelectTrigger id={id} className="w-full sm:w-[220px]">
            {/* Named here, so a value another client stored off the list
                (the device slider allows any second) still reads as itself. */}
            <SelectValue>{nextUpPromptLabel(t, value)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {choices.map((choice) => (
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
