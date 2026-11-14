import { useId, useState } from "react";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { useT, type MessageKey } from "@/i18n";
import {
  getProfileLaunchMode,
  setProfileLaunchMode,
  type ProfileLaunchMode,
} from "@/lib/profileLaunch";

const CHOICES: ReadonlyArray<{ value: ProfileLaunchMode; labelKey: MessageKey }> = [
  { value: "remember", labelKey: "web.settings.profileLaunch.remember" },
  { value: "ask", labelKey: "web.settings.profileLaunch.ask" },
];

/**
 * Whether this browser reopens the last profile or asks who is watching when
 * Silo is opened in a new tab or window. Stored in the browser only.
 */
export function ProfileLaunchSettingsGroup() {
  const t = useT();
  const [mode, setMode] = useState<ProfileLaunchMode>(getProfileLaunchMode);
  const labelId = useId();

  function choose(next: ProfileLaunchMode) {
    setProfileLaunchMode(next);
    setMode(next);
  }

  return (
    <SettingsGroup
      title={t("web.settings.profileLaunch.title")}
      description={t("web.settings.profileLaunch.description")}
    >
      <div className="space-y-2">
        <p id={labelId} className="text-sm font-medium">
          {t("web.settings.profileLaunch.label")}
        </p>
        <div role="radiogroup" aria-labelledby={labelId} className="flex flex-wrap gap-2">
          {CHOICES.map((choice) => (
            <Button
              key={choice.value}
              type="button"
              role="radio"
              aria-checked={mode === choice.value}
              variant={mode === choice.value ? "default" : "outline"}
              size="sm"
              onClick={() => choose(choice.value)}
            >
              {t(choice.labelKey)}
            </Button>
          ))}
        </div>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {mode === "ask"
            ? t("web.settings.profileLaunch.askHint")
            : t("web.settings.profileLaunch.rememberHint")}
        </p>
      </div>
    </SettingsGroup>
  );
}
