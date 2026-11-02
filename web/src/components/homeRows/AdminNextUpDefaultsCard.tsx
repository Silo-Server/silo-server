import { useId } from "react";
import { toast } from "sonner";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useAdminServerSettings, useUpdateServerSetting } from "@/hooks/queries/admin/settings";
import { adminDefaultServerKey, storedAdminDefault } from "@/lib/settingsAdminDefaults";
import { optionsFor } from "@/lib/settingsDisplay";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

const NEXT_UP_MODE = SETTING_DEFINITIONS[SETTING_KEYS.UI_NEXT_UP_MODE];
const NEW_RELEASES_FIRST = SETTING_DEFINITIONS[SETTING_KEYS.UI_NEXT_UP_NEW_RELEASES_FIRST];
const NEXT_UP_MODES = optionsFor(NEXT_UP_MODE);

const NEXT_UP_MODE_DEFAULT_KEY = adminDefaultServerKey(SETTING_KEYS.UI_NEXT_UP_MODE);
const NEW_RELEASES_FIRST_DEFAULT_KEY = adminDefaultServerKey(
  SETTING_KEYS.UI_NEXT_UP_NEW_RELEASES_FIRST,
);

/**
 * Admin > Home Sections, under the list: the server-wide Next Up defaults. Profiles
 * that have not chosen their own follow them; a profile's own choice wins.
 * Each control saves as soon as it changes, like the rows above it.
 */
export function AdminNextUpDefaultsCard() {
  const nextUpId = useId();
  const newReleasesFirstId = useId();
  const settings = useAdminServerSettings();
  const update = useUpdateServerSetting(settings.data);

  const mode = String(
    storedAdminDefault(SETTING_KEYS.UI_NEXT_UP_MODE, settings.data?.[NEXT_UP_MODE_DEFAULT_KEY]),
  );
  const newReleasesFirst =
    storedAdminDefault(
      SETTING_KEYS.UI_NEXT_UP_NEW_RELEASES_FIRST,
      settings.data?.[NEW_RELEASES_FIRST_DEFAULT_KEY],
    ) === true;
  // One save at a time: each write carries the settings version it read.
  const disabled = !settings.data || update.isPending;

  function save(key: string, value: unknown) {
    update.mutate(
      { key, value: JSON.stringify(value) },
      { onSuccess: () => toast.success("Saved the default for profiles") },
    );
  }

  return (
    <section
      aria-labelledby={`${nextUpId}-heading`}
      className="surface-panel divide-border/60 divide-y rounded-[22px] px-[22px]"
    >
      <div className="space-y-1 py-5">
        <h2 id={`${nextUpId}-heading`} className="text-sm font-semibold">
          Next Up defaults for every profile
        </h2>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Applies on every page to profiles that haven&apos;t chosen their own in Settings &gt; Home
          Screen. A profile&apos;s own choice always wins.
        </p>
      </div>
      <div className="flex flex-col gap-3 py-5 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="min-w-0 space-y-1">
          <label htmlFor={nextUpId} className="text-sm font-semibold">
            {NEXT_UP_MODE.label}
          </label>
          <p id={`${nextUpId}-help`} className="text-muted-foreground text-[13px] leading-relaxed">
            {NEXT_UP_MODE.description}
          </p>
        </div>
        <Select
          value={mode}
          onValueChange={(next) => save(NEXT_UP_MODE_DEFAULT_KEY, next)}
          disabled={disabled}
        >
          <SelectTrigger
            id={nextUpId}
            aria-describedby={`${nextUpId}-help`}
            className="w-full shrink-0 sm:w-[240px]"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {NEXT_UP_MODES.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="flex items-center justify-between gap-4 py-5">
        <div className="min-w-0 space-y-1">
          <label htmlFor={newReleasesFirstId} className="text-sm font-semibold">
            {NEW_RELEASES_FIRST.label}
          </label>
          <p
            id={`${newReleasesFirstId}-help`}
            className="text-muted-foreground text-[13px] leading-relaxed"
          >
            {NEW_RELEASES_FIRST.description}
          </p>
        </div>
        <Switch
          id={newReleasesFirstId}
          aria-describedby={`${newReleasesFirstId}-help`}
          checked={newReleasesFirst}
          disabled={disabled}
          onCheckedChange={(next) => save(NEW_RELEASES_FIRST_DEFAULT_KEY, next === true)}
        />
      </div>
    </section>
  );
}
