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
import { optionsFor } from "@/lib/settingsDisplay";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

const NEXT_UP_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.UI_NEXT_UP_MODE]);

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
  onNewReleasesFirstChange,
  hideWatched,
  onHideWatchedChange,
  disabled,
}: {
  nextUpMode: string;
  /** Whether the profile stored a choice, which is what a reset can undo. */
  nextUpChosen: boolean;
  onNextUpModeChange: (mode: string) => void;
  onNextUpReset: () => void;
  newReleasesFirst: boolean;
  onNewReleasesFirstChange: (checked: boolean) => void;
  hideWatched: boolean;
  onHideWatchedChange: (checked: boolean) => void;
  disabled: boolean;
}) {
  const nextUpId = useId();
  const newReleasesFirstId = useId();
  const hideWatchedId = useId();

  return (
    <div className="surface-panel divide-border/60 divide-y rounded-[22px] px-[22px]">
      <div className="flex flex-col gap-3 py-5 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="min-w-0 space-y-1">
          <label htmlFor={nextUpId} className="text-sm font-semibold">
            Next up episodes
          </label>
          <p id={`${nextUpId}-help`} className="text-muted-foreground text-[13px] leading-relaxed">
            Keep the next episode of a show you&apos;re watching in Continue Watching, or give them
            their own row.
          </p>
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
                  {mode.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {nextUpChosen ? (
            <Button
              variant="ghost"
              size="sm"
              className="h-9 rounded-full px-3"
              onClick={onNextUpReset}
              disabled={disabled}
            >
              Reset
            </Button>
          ) : null}
        </div>
      </div>
      <div className="flex items-center justify-between gap-4 py-5">
        <div className="min-w-0 space-y-1">
          <label htmlFor={newReleasesFirstId} className="text-sm font-semibold">
            Show new episodes first
          </label>
          <p
            id={`${newReleasesFirstId}-help`}
            className="text-muted-foreground text-[13px] leading-relaxed"
          >
            Move a show you&apos;re caught up on to the front of Continue Watching and Next Up when
            a new episode or season arrives.
          </p>
        </div>
        <Switch
          id={newReleasesFirstId}
          aria-describedby={`${newReleasesFirstId}-help`}
          checked={newReleasesFirst}
          disabled={disabled}
          onCheckedChange={(next) => onNewReleasesFirstChange(next === true)}
        />
      </div>
      <div className="flex items-center justify-between gap-4 py-5">
        <div className="min-w-0 space-y-1">
          <label htmlFor={hideWatchedId} className="text-sm font-semibold">
            Hide watched items
          </label>
          <p
            id={`${hideWatchedId}-help`}
            className="text-muted-foreground text-[13px] leading-relaxed"
          >
            Remove titles you&apos;ve finished from ordinary rows. Hero banners and watch-history
            rows keep them.
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
