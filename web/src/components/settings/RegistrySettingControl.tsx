import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SettingSlider } from "@/components/settings/SettingSlider";
import { bitrateSelectChoices } from "@/lib/bitrateOptions";
import { controlKindFor, optionsFor, type SettingDisplay } from "@/lib/settingsDisplay";

const EMPTY_SELECT_VALUE = "__empty__";

interface RegistrySettingControlProps {
  /**
   * The generated definition this control edits, as
   * {@link SettingDisplay} resolves it from SETTING_DEFINITIONS.
   */
  definition: SettingDisplay;
  value: string;
  disabled?: boolean;
  onChange: (value: string) => void;
}

/**
 * A select's entries. A numeric select with no enum members (the bandwidth
 * cap) offers the shared bitrate ladder plus the stored value, as the profile
 * and device settings screens do, rather than only an unset entry.
 */
function selectOptions(definition: SettingDisplay, value: string) {
  const options = optionsFor(definition);
  const numeric = definition.type === "integer" || definition.type === "number";
  if (!numeric || options.some((option) => option.value !== "")) return options;
  const unsetLabel = definition.key === "playback.max_bitrate_kbps" ? "No limit" : "Unset";
  return bitrateSelectChoices(definition, value, unsetLabel);
}

/**
 * Renders one admin-facing setting control from the generated contract.
 *
 * The control shape, bounds, and option list all come from the manifest, so a
 * definition the server knows and this build does not is impossible to edit
 * with the wrong widget — the hand-written registry this used to read could
 * (and did) disagree with the server about a setting's type and range.
 *
 * Values are strings here on purpose: the admin surface edits the string form
 * and re-types on save through the contract (see
 * hooks/queries/admin/users.ts), which keeps this component free of the typed
 * JSON round trip.
 */
export function RegistrySettingControl({
  definition,
  value,
  disabled = false,
  onChange,
}: RegistrySettingControlProps) {
  const control = controlKindFor(definition);

  if (control === "switch") {
    return (
      <Switch
        checked={value === "true"}
        disabled={disabled}
        onCheckedChange={(checked) => onChange(checked ? "true" : "false")}
      />
    );
  }

  if (control === "slider" || control === "stepper") {
    const fallback = Number(definition.defaultValue ?? 0);
    const parsed = Number(value);
    const numericValue = Number.isFinite(parsed) && value !== "" ? parsed : fallback;
    return (
      <SettingSlider
        className="flex w-full max-w-[260px] items-center gap-3"
        value={numericValue}
        min={definition.minimum}
        max={definition.maximum}
        step={definition.step}
        unit={definition.unit}
        disabled={disabled}
        aria-label={definition.label}
        onCommit={(next) => onChange(String(next))}
      />
    );
  }

  return (
    <Select
      value={value === "" ? EMPTY_SELECT_VALUE : value}
      onValueChange={(nextValue) => onChange(nextValue === EMPTY_SELECT_VALUE ? "" : nextValue)}
      disabled={disabled}
    >
      <SelectTrigger className="w-full min-w-[180px] sm:w-[220px]">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {selectOptions(definition, value).map((option) => (
          <SelectItem
            key={option.value || EMPTY_SELECT_VALUE}
            value={option.value === "" ? EMPTY_SELECT_VALUE : option.value}
          >
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
