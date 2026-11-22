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

/** Show Next Up choices, in seconds before the end; 0 is "At end". The native apps offer the same. */
const NEXT_UP_PROMPT_CHOICES = [0, 10, 30, 60, 120];

function nextUpPromptLabel(t: Translate, seconds: number) {
  if (seconds <= 0) return t("settings.nextUp.atEnd");
  if (seconds % 60 === 0) {
    return t("settings.nextUp.minutesBeforeEnd", { count: seconds / 60 });
  }
  return t("settings.nextUp.secondsBeforeEnd", { count: seconds });
}

/** The Show Next Up row: how long before the end of the file Next Up opens. */
export function NextUpPromptSetting({
  seconds,
  disabled,
  onChange,
}: {
  seconds: number;
  disabled: boolean;
  onChange: (seconds: number) => void;
}) {
  const t = useT();
  return (
    <SettingRow
      label={t("settings.nextUp.title")}
      description={settingDescription(
        SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS],
      )}
      control={(id) => (
        <Select
          value={String(seconds)}
          disabled={disabled}
          onValueChange={(value) => onChange(Number(value))}
        >
          <SelectTrigger id={id} className="w-full sm:w-[220px]">
            {/* Named here, so a value another client stored off the list
                (the device slider allows any second) still reads as itself. */}
            <SelectValue>{nextUpPromptLabel(t, seconds)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {NEXT_UP_PROMPT_CHOICES.map((choice) => (
              <SelectItem key={choice} value={String(choice)}>
                {nextUpPromptLabel(t, choice)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    />
  );
}
