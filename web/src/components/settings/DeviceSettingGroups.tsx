import { Lock, RotateCcw } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { groupDeviceSettings } from "@/lib/deviceSettingGroups";
import type { EffectiveSetting } from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, type SettingKey } from "@/lib/settingsContract";
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
  disabled?: boolean;
  onChange: (key: SettingKey, value: unknown) => void;
  onReset: (key: SettingKey) => void;
  /** Opens the subtitle appearance panel, which is not an inline control. */
  onOpenPanel?: (key: SettingKey) => void;
}

export function DeviceSettingGroups({
  settings,
  ownerLabel,
  disabled = false,
  onChange,
  onReset,
  onOpenPanel,
}: DeviceSettingGroupsProps) {
  return (
    <div className="space-y-4">
      {groupDeviceSettings().map((group) => (
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

  return (
    <div className="border-border/50 grid gap-3 border-t pt-4 first:border-t-0 first:pt-0 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
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

      <div className="flex flex-wrap items-center gap-2 sm:flex-nowrap sm:justify-end">
        {changedHere && !locked ? (
          <button
            type="button"
            onClick={() => onReset(settingKey)}
            disabled={disabled}
            className="text-muted-foreground hover:text-foreground inline-flex shrink-0 items-center gap-1 text-xs transition-colors disabled:opacity-50"
          >
            <RotateCcw className="h-3 w-3" />
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
        size="sm"
        disabled={disabled}
        onClick={() => onOpenPanel?.(settingKey)}
      >
        Change how they look
      </Button>
    );
  }

  if (control === "switch") {
    return (
      <Switch
        checked={value === true}
        disabled={disabled}
        onCheckedChange={(checked) => onChange(settingKey, checked)}
      />
    );
  }

  if (control === "slider" || control === "stepper") {
    const numeric = typeof value === "number" ? value : Number(definition.defaultValue ?? 0);
    return (
      <div className="flex w-full max-w-[260px] items-center gap-3">
        <Slider
          value={[numeric]}
          min={definition.minimum}
          max={definition.maximum}
          step={definition.step}
          disabled={disabled}
          onValueCommit={(values) => onChange(settingKey, values[0] ?? numeric)}
        />
        <span className="text-muted-foreground min-w-16 text-right text-xs font-medium">
          {numeric}
          {definition.unit ? ` ${definition.unit}` : ""}
        </span>
      </div>
    );
  }

  const options = permittedOptions(settingKey, effective);
  const asString = value === null || value === undefined ? "" : String(value);

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
      <SelectTrigger className={cn("w-full min-w-[180px] sm:w-[220px]")}>
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

/** Selects edit strings; integers travel back as numbers. */
function typedSelectValue(settingKey: SettingKey, raw: string): unknown {
  const definition = SETTING_DEFINITIONS[settingKey];
  if (definition.type === "integer" || definition.type === "number") {
    const parsed = Number(raw);
    return Number.isFinite(parsed) ? parsed : raw;
  }
  return raw;
}
