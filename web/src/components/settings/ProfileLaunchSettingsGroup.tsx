import { useId, useState } from "react";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import {
  getProfileLaunchMode,
  setProfileLaunchMode,
  type ProfileLaunchMode,
} from "@/lib/profileLaunch";

const CHOICES: ReadonlyArray<{ value: ProfileLaunchMode; label: string }> = [
  { value: "remember", label: "Remember last profile" },
  { value: "ask", label: "Ask who's watching" },
];

/**
 * Whether this browser reopens the last profile or asks who is watching when
 * Silo is opened in a new tab or window. Stored in the browser only.
 */
export function ProfileLaunchSettingsGroup() {
  const [mode, setMode] = useState<ProfileLaunchMode>(getProfileLaunchMode);
  const labelId = useId();

  function choose(next: ProfileLaunchMode) {
    setProfileLaunchMode(next);
    setMode(next);
  }

  return (
    <SettingsGroup
      title="This browser"
      description="Applies only to this browser. Your other devices keep their own choice."
    >
      <div className="space-y-2">
        <p id={labelId} className="text-sm font-medium">
          Profile at launch
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
              {choice.label}
            </Button>
          ))}
        </div>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {mode === "ask"
            ? "Each new tab or window starts at “Who's watching?”, and a PIN-protected profile needs its PIN again."
            : "New tabs and windows open the profile last used here."}
        </p>
      </div>
    </SettingsGroup>
  );
}
