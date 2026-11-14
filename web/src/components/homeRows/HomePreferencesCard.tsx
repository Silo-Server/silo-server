import { useId } from "react";

import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/i18n";
import { optionsFor } from "@/lib/settingsDisplay";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { nextUpModeLabel } from "./nextUpModeLabel";

const NEXT_UP_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.UI_NEXT_UP_MODE]);

/**
 * Under a setting the profile has not chosen: the value it inherits. The
 * server default is the administrator's choice when one is set, else Silo's.
 */
function InheritedNote({ value }: { value: string }) {
  const t = useT();
  return (
    <p className="text-muted-foreground text-[13px] leading-relaxed">
      {t("web.home.preferences.inherited", { value })}
    </p>
  );
}

function UseServerDefaultButton({ onClick, disabled }: { onClick: () => void; disabled: boolean }) {
  const t = useT();
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-9 rounded-full px-3"
      onClick={onClick}
      disabled={disabled}
    >
      {t("web.home.preferences.useServerDefault")}
    </Button>
  );
}

/**
 * Settings > Home Screen, under the list: the profile's preferences for what
 * Home's rows hold, as opposed to which rows there are.
 */
export function HomePreferencesCard({
  nextUpMode,
  nextUpChosen,
  onNextUpModeChange,
  onNextUpReset,
  newReleasesFirst,
  newReleasesFirstChosen,
  onNewReleasesFirstChange,
  onNewReleasesFirstReset,
  hideWatched,
  onHideWatchedChange,
  loaded,
  disabled,
}: {
  nextUpMode: string;
  /** Whether the profile stored a choice, which is what a reset can undo. */
  nextUpChosen: boolean;
  onNextUpModeChange: (mode: string) => void;
  onNextUpReset: () => void;
  newReleasesFirst: boolean;
  newReleasesFirstChosen: boolean;
  onNewReleasesFirstChange: (checked: boolean) => void;
  onNewReleasesFirstReset: () => void;
  hideWatched: boolean;
  onHideWatchedChange: (checked: boolean) => void;
  /** Whether the effective values arrived; until then nothing claims a source. */
  loaded: boolean;
  disabled: boolean;
}) {
  const t = useT();
  const nextUpId = useId();
  const newReleasesFirstId = useId();
  const hideWatchedId = useId();
  const nextUpModeOption = NEXT_UP_MODES.find((mode) => mode.value === nextUpMode);

  return (
    <div className="surface-panel divide-border/60 divide-y rounded-[22px] px-[22px]">
      <div className="flex flex-col gap-3 py-5 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="min-w-0 space-y-1">
          <label htmlFor={nextUpId} className="text-sm font-semibold">
            {t("web.home.preferences.nextUpLabel")}
          </label>
          <p id={`${nextUpId}-help`} className="text-muted-foreground text-[13px] leading-relaxed">
            {t("web.home.preferences.nextUpHelp")}
          </p>
          {loaded && !nextUpChosen ? (
            <InheritedNote
              value={nextUpModeOption ? nextUpModeLabel(t, nextUpModeOption) : nextUpMode}
            />
          ) : null}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Select value={nextUpMode} onValueChange={onNextUpModeChange} disabled={disabled}>
            <SelectTrigger
              id={nextUpId}
              aria-describedby={`${nextUpId}-help`}
              className="w-full sm:w-[240px]"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {NEXT_UP_MODES.map((mode) => (
                <SelectItem key={mode.value} value={mode.value}>
                  {nextUpModeLabel(t, mode)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {nextUpChosen ? (
            <UseServerDefaultButton onClick={onNextUpReset} disabled={disabled} />
          ) : null}
        </div>
      </div>
      <div className="flex items-center justify-between gap-4 py-5">
        <div className="min-w-0 space-y-1">
          <label htmlFor={newReleasesFirstId} className="text-sm font-semibold">
            {t("web.home.preferences.newReleasesFirstLabel")}
          </label>
          <p
            id={`${newReleasesFirstId}-help`}
            className="text-muted-foreground text-[13px] leading-relaxed"
          >
            {t("web.home.preferences.newReleasesFirstHelp")}
          </p>
          {loaded && !newReleasesFirstChosen ? (
            <InheritedNote
              value={
                newReleasesFirst
                  ? t("web.home.preferences.valueOn")
                  : t("web.home.preferences.valueOff")
              }
            />
          ) : null}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {newReleasesFirstChosen ? (
            <UseServerDefaultButton onClick={onNewReleasesFirstReset} disabled={disabled} />
          ) : null}
          <Switch
            id={newReleasesFirstId}
            aria-describedby={`${newReleasesFirstId}-help`}
            checked={newReleasesFirst}
            disabled={disabled}
            onCheckedChange={(next) => onNewReleasesFirstChange(next === true)}
          />
        </div>
      </div>
      <div className="flex items-center justify-between gap-4 py-5">
        <div className="min-w-0 space-y-1">
          <label htmlFor={hideWatchedId} className="text-sm font-semibold">
            {t("web.home.preferences.hideWatchedLabel")}
          </label>
          <p
            id={`${hideWatchedId}-help`}
            className="text-muted-foreground text-[13px] leading-relaxed"
          >
            {t("web.home.preferences.hideWatchedHelp")}
          </p>
        </div>
        <Switch
          id={hideWatchedId}
          aria-describedby={`${hideWatchedId}-help`}
          checked={hideWatched}
          disabled={disabled}
          onCheckedChange={(next) => onHideWatchedChange(next === true)}
        />
      </div>
    </div>
  );
}
