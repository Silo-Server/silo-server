import { Lock, RotateCcw } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { LanguageSelect } from "@/components/settings/LanguageSelect";
import { SettingSlider } from "@/components/settings/SettingSlider";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { groupDeviceSettings } from "@/lib/deviceSettingGroups";
import type { EffectiveSetting } from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, type SettingKey } from "@/lib/settingsContract";
import { formatQualityBitrate } from "@/player/hooks/useTranscodeQuality";
import { namedLanguageOptionsFor } from "@/lib/languageOptions";
import { controlKindFor, optionsFor } from "@/lib/settingsDisplay";
import { cn } from "@/lib/utils";

const EMPTY_SELECT_VALUE = "__empty__";

export interface DeviceSettingGroupsProps {
  /** Effective values resolved for the device being edited. */
  settings: Partial<Record<SettingKey, EffectiveSetting>>;
  /**
   * Whose settings these are, for the reset label. "your" on your own devices,
   * a name when the household parent is acting for someone else.
   */
  ownerLabel: string;
  /**
   * The target device's self-reported platform string. When given, settings
   * the manifest marks as not applying to that platform are hidden — unless
   * the device already stores a value, which must stay clearable.
   */
  devicePlatform?: string;
  disabled?: boolean;
  onChange: (key: SettingKey, value: unknown) => void;
  onReset: (key: SettingKey) => void;
  /** Opens the subtitle appearance panel, which is not an inline control. */
  onOpenPanel?: (key: SettingKey) => void;
}

export function DeviceSettingGroups({
  settings,
  ownerLabel,
  devicePlatform,
  disabled = false,
  onChange,
  onReset,
  onOpenPanel,
}: DeviceSettingGroupsProps) {
  const storedHere = new Set(
    (Object.keys(settings) as SettingKey[]).filter(
      (key) => settings[key]?.scope === "profile_device",
    ),
  );
  return (
    <div className="space-y-4">
      {groupDeviceSettings(undefined, {
        devicePlatform,
        keysWithStoredValues: storedHere,
      }).map((group) => (
        <SettingsGroup key={group.id} title={group.title} description={group.description}>
          {group.keys.map((key) => (
            <DeviceSettingRow
              key={key}
              settingKey={key}
              effective={settings[key]}
              ownerLabel={ownerLabel}
              disabled={disabled}
              onChange={onChange}
              onReset={onReset}
              onOpenPanel={onOpenPanel}
            />
          ))}
        </SettingsGroup>
      ))}
    </div>
  );
}

interface DeviceSettingRowProps {
  settingKey: SettingKey;
  effective: EffectiveSetting | undefined;
  ownerLabel: string;
  disabled: boolean;
  onChange: (key: SettingKey, value: unknown) => void;
  onReset: (key: SettingKey) => void;
  onOpenPanel?: (key: SettingKey) => void;
}

function DeviceSettingRow({
  settingKey,
  effective,
  ownerLabel,
  disabled,
  onChange,
  onReset,
  onOpenPanel,
}: DeviceSettingRowProps) {
  const definition = SETTING_DEFINITIONS[settingKey];
  if (!definition) return null;

  // "Changed here" means a row exists at this exact device, which is also what
  // makes the reset meaningful — reset clears that row rather than copying the
  // profile value into it.
  const changedHere = effective?.scope === "profile_device";
  const locked = effective?.constraint_kind === "locked";
  const constrained = Boolean(effective?.constrained);
  const value = effective?.value ?? definition.defaultValue;
  const inlineControl = controlKindFor(definition) === "switch";

  return (
    <div
      className={cn(
        "border-border/50 grid gap-3 border-t pt-4 first:border-t-0 first:pt-0 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center",
        // A switch fits beside its label even at 360px, and keeping it there
        // saves a whole row on each of the ~18 toggles this screen renders.
        // Wider controls still drop below, where they have room.
        inlineControl && "grid-cols-[minmax(0,1fr)_auto] items-center",
      )}
    >
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{definition.label}</span>
          {changedHere ? (
            <span className="rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 py-px text-[10px] font-semibold tracking-[0.04em] text-amber-300 uppercase">
              Changed here
            </span>
          ) : null}
          {constrained ? (
            <span className="border-info/30 bg-info/10 text-info inline-flex items-center gap-1 rounded-full border px-1.5 py-px text-[10px] font-semibold tracking-[0.04em] uppercase">
              <Lock className="h-2.5 w-2.5" />
              Household limit
            </span>
          ) : null}
        </div>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {definition.description}
        </p>
        {constrained ? (
          <p className="text-[12.5px] leading-relaxed text-amber-300/90">
            {constraintExplanation(effective)}
          </p>
        ) : null}
      </div>

      <div
        className={cn(
          "flex flex-wrap items-center gap-x-3 gap-y-1 sm:flex-nowrap sm:justify-end",
          inlineControl && "justify-end",
        )}
      >
        {changedHere && !locked ? (
          <button
            type="button"
            onClick={() => onReset(settingKey)}
            disabled={disabled}
            className={cn(
              "text-muted-foreground hover:text-foreground order-2 inline-flex min-h-11 shrink-0 items-center gap-1 text-[13px] transition-colors disabled:opacity-50 sm:order-none sm:min-h-0 sm:text-xs",
              // Inline rows have no room beside the switch; the reset sits
              // under the description instead.
              inlineControl && "col-start-1 row-start-2 -mt-1 sm:col-auto sm:row-auto sm:mt-0",
            )}
          >
            <RotateCcw className="h-3.5 w-3.5 sm:h-3 sm:w-3" />
            Use {ownerLabel} setting
          </button>
        ) : null}
        <DeviceSettingControl
          settingKey={settingKey}
          effective={effective}
          value={value}
          disabled={disabled || locked}
          onChange={onChange}
          onOpenPanel={onOpenPanel}
        />
      </div>
    </div>
  );
}

function constraintExplanation(effective: EffectiveSetting | undefined): string {
  if (!effective) return "";
  const permitted = effective.value;
  if (effective.constraint_kind === "locked") {
    return "This is set for your household and can't be changed here.";
  }
  // The stored preference is still theirs; it is just capped today. Saying so
  // beats silently showing a value they did not choose.
  if (effective.stored_value !== undefined && effective.stored_value !== permitted) {
    return `Your household settings limit this to ${String(permitted)}, so your choice of ${String(effective.stored_value)} isn't available right now.`;
  }
  return "Your household settings limit this option.";
}

interface DeviceSettingControlProps {
  settingKey: SettingKey;
  effective: EffectiveSetting | undefined;
  value: unknown;
  disabled: boolean;
  onChange: (key: SettingKey, value: unknown) => void;
  onOpenPanel?: (key: SettingKey) => void;
}

/**
 * The control for one device setting.
 *
 * Values are typed JSON here, not strings: a slider round-tripping through
 * text is a hazard on a screen a viewer uses, and the contract already knows
 * every value's type. When policy narrows the choices, the select renders the
 * permitted list rather than the manifest's.
 */
function DeviceSettingControl({
  settingKey,
  effective,
  value,
  disabled,
  onChange,
  onOpenPanel,
}: DeviceSettingControlProps) {
  const definition = SETTING_DEFINITIONS[settingKey];
  const control = controlKindFor(definition);

  if (control === "panel" || definition.type === "object") {
    return (
      <Button
        variant="outline"
        disabled={disabled}
        onClick={() => onOpenPanel?.(settingKey)}
        className="order-1 min-h-11 w-full sm:order-none sm:h-8 sm:min-h-0 sm:w-auto sm:px-3 sm:text-sm"
      >
        Change how they look
      </Button>
    );
  }

  if (control === "switch") {
    return (
      <span className="order-1 flex min-h-11 items-center sm:order-none sm:min-h-0">
        <Switch
          checked={value === true}
          disabled={disabled}
          onCheckedChange={(checked) => onChange(settingKey, checked)}
        />
      </span>
    );
  }

  if (control === "slider" || control === "stepper") {
    const numeric = typeof value === "number" ? value : Number(definition.defaultValue ?? 0);
    return (
      <SettingSlider
        className="order-1 flex w-full items-center gap-3 sm:order-none sm:max-w-[260px]"
        value={numeric}
        min={definition.minimum}
        max={definition.maximum}
        step={definition.step}
        unit={definition.unit}
        disabled={disabled}
        aria-label={definition.label}
        onCommit={(next) => onChange(settingKey, next)}
      />
    );
  }

  const options = permittedOptions(settingKey, effective);
  const asString = value === null || value === undefined ? "" : String(value);

  // Open language values get the shared picker with "Other…" free entry —
  // the contract floor is a short authored list, and any tag beyond it is
  // typed rather than fetched from the catalog. A permitted_values constraint
  // pins the list closed, so the free entry disappears with it.
  if (definition.type === "language_tag") {
    const permitted = (effective as { permitted_values?: unknown[] } | undefined)?.permitted_values;
    const languageOptions = namedLanguageOptionsFor(settingKey, asString || undefined).filter(
      (option) => !permitted?.length || permitted.some((entry) => String(entry) === option.value),
    );
    return (
      <div className="order-1 w-full sm:order-none sm:w-[220px] sm:min-w-[180px]">
        <LanguageSelect
          aria-label={definition.label}
          value={asString === "" ? EMPTY_SELECT_VALUE : asString}
          options={languageOptions}
          disabled={disabled}
          allowOther={!permitted?.length}
          className="h-11 w-full text-base sm:h-9 sm:text-sm"
          onValueChange={(next) => onChange(settingKey, next === EMPTY_SELECT_VALUE ? null : next)}
        >
          {definition.nullable && (
            <SelectItem value={EMPTY_SELECT_VALUE}>{definition.unsetLabel ?? "Unset"}</SelectItem>
          )}
        </LanguageSelect>
      </div>
    );
  }

  // A numeric "select" the manifest gives no members — the bandwidth cap is
  // declared as a range, not a list — would render as a one-entry dropdown
  // showing nothing at all. Present the range as bandwidth choices people
  // recognise instead, and keep the stored value visible if it is not one of
  // them.
  const numericChoices = numericSelectChoices(settingKey, definition, options, asString);
  if (numericChoices) {
    return (
      <Select
        value={asString === "" ? EMPTY_SELECT_VALUE : asString}
        disabled={disabled}
        onValueChange={(next) =>
          onChange(settingKey, next === EMPTY_SELECT_VALUE ? null : Number(next))
        }
      >
        <SelectTrigger
          aria-label={definition.label}
          className={cn(
            "order-1 h-11 w-full text-base sm:order-none sm:h-9 sm:w-[220px] sm:min-w-[180px] sm:text-sm",
          )}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {numericChoices.map((choice) => (
            <SelectItem
              key={choice.value || EMPTY_SELECT_VALUE}
              value={choice.value === "" ? EMPTY_SELECT_VALUE : choice.value}
            >
              {choice.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    );
  }

  return (
    <Select
      value={asString === "" ? EMPTY_SELECT_VALUE : asString}
      disabled={disabled}
      onValueChange={(next) =>
        onChange(
          settingKey,
          next === EMPTY_SELECT_VALUE ? null : typedSelectValue(settingKey, next),
        )
      }
    >
      <SelectTrigger
        aria-label={definition.label}
        className={cn(
          "order-1 h-11 w-full text-base sm:order-none sm:h-9 sm:w-[220px] sm:min-w-[180px] sm:text-sm",
        )}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
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

/**
 * The options this viewer may actually pick. `permitted_values` narrows the
 * manifest's list for a viewer under a policy cap, so a child's quality picker
 * shows what they can have rather than offering 4K and delivering 1080p.
 */
function permittedOptions(settingKey: SettingKey, effective: EffectiveSetting | undefined) {
  const definition = SETTING_DEFINITIONS[settingKey];
  const all = optionsFor(definition);
  const permitted = (effective as { permitted_values?: unknown[] } | undefined)?.permitted_values;
  if (!permitted?.length) return all;
  const allowed = new Set(permitted.map((entry) => String(entry)));
  const narrowed = all.filter((option) => option.value === "" || allowed.has(option.value));
  return narrowed.length > 0 ? narrowed : all;
}

/**
 * Bandwidth caps people recognise, bounded by the definition's own range.
 *
 * Returns null for any select the manifest actually gives members, which is
 * every other one — this exists only for a numeric range declared with a
 * select control.
 */
/**
 * The bandwidth ladder, in kbps.
 *
 * The low end matches the in-player quality switcher
 * (web/src/player/hooks/useTranscodeQuality.ts) so a cap chosen here lines up
 * with what the player offers mid-playback. Above that it keeps climbing to
 * the definition's own ceiling of 200 Mbps, which is what remuxed 4K HDR and
 * untouched Blu-ray rips actually need — a ladder that stopped short would cap
 * people below what their server can already send them.
 *
 * Entries outside a definition's declared range are filtered out, so this list
 * can cover more ground than any single setting allows.
 */
const BITRATE_CHOICES_KBPS = [
  1500, 2000, 4000, 6000, 10000, 15000, 20000, 30000, 40000, 60000, 80000, 100000, 150000, 200000,
];

function numericSelectChoices(
  settingKey: SettingKey,
  definition: (typeof SETTING_DEFINITIONS)[SettingKey],
  options: { value: string; label: string }[],
  currentValue: string,
): { value: string; label: string }[] | null {
  const isNumeric = definition.type === "integer" || definition.type === "number";
  const hasMembers = options.some((option) => option.value !== "");
  if (!isNumeric || hasMembers) return null;

  const min = definition.minimum ?? 0;
  const max = definition.maximum ?? Number.MAX_SAFE_INTEGER;
  const choices = BITRATE_CHOICES_KBPS.filter((kbps) => kbps >= min && kbps <= max).map((kbps) => ({
    value: String(kbps),
    label: formatBitrate(kbps),
  }));

  // A value set elsewhere (an API call, another client) must stay selectable
  // rather than silently reading as "No limit".
  if (currentValue !== "" && !choices.some((choice) => choice.value === currentValue)) {
    const parsed = Number(currentValue);
    if (Number.isFinite(parsed)) {
      choices.push({ value: currentValue, label: formatBitrate(parsed) });
      choices.sort((a, b) => Number(a.value) - Number(b.value));
    }
  }

  const unsetLabel = settingKey === "playback.max_bitrate_kbps" ? "No limit" : "Unset";
  return [{ value: "", label: unsetLabel }, ...choices];
}

function formatBitrate(kbps: number): string {
  return formatQualityBitrate(kbps);
}

/** Selects edit strings; integers travel back as numbers. */
function typedSelectValue(settingKey: SettingKey, raw: string): unknown {
  const definition = SETTING_DEFINITIONS[settingKey];
  if (definition.type === "integer" || definition.type === "number") {
    const parsed = Number(raw);
    return Number.isFinite(parsed) ? parsed : raw;
  }
  return raw;
}
