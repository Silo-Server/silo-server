import { useId } from "react";
import { Check, Monitor } from "lucide-react";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { useDateTimeFormat, useDateTimeFormatSettings } from "@/hooks/useDateTimeFormat";
import { useTheme } from "@/hooks/useTheme";
import { formatDate, formatTime } from "@/lib/datetime";
import type { DateFormatPreference, TimeFormatPreference } from "@/lib/datetime";
import { CURATED_THEME_IDS, THEMES } from "@/lib/themes";
import { cn } from "@/lib/utils";

const DATE_FORMAT_CHOICES: ReadonlyArray<{ value: DateFormatPreference; label: string }> = [
  { value: "auto", label: "Auto (browser)" },
  { value: "DD/MM/YYYY", label: "DD/MM/YYYY" },
  { value: "MM/DD/YYYY", label: "MM/DD/YYYY" },
  { value: "YYYY-MM-DD", label: "YYYY-MM-DD" },
];

const TIME_FORMAT_CHOICES: ReadonlyArray<{ value: TimeFormatPreference; label: string }> = [
  { value: "auto", label: "Auto (browser)" },
  { value: "12h", label: "12-hour" },
  { value: "24h", label: "24-hour" },
];

export default function AppearanceSettings() {
  const { theme, setTheme, previewTheme, resetPreviewTheme } = useTheme();
  const activeTheme = THEMES[theme];
  const { dateFormat, timeFormat, setDateFormat, setTimeFormat } = useDateTimeFormatSettings();
  useDateTimeFormat();
  const dateFormatLabelId = useId();
  const timeFormatLabelId = useId();
  const previewDate = new Date();

  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Appearance</h2>

      <SettingsGroup
        title="Theme"
        description="Hover to preview. Click to apply and persist the selection to this profile."
      >
        <div className="grid gap-3 sm:grid-cols-2">
          {CURATED_THEME_IDS.map((id) => {
            const def = THEMES[id];
            const isActive = theme === id;

            return (
              <button
                key={id}
                type="button"
                onClick={() => setTheme(id)}
                onFocus={() => previewTheme(id)}
                onBlur={resetPreviewTheme}
                onMouseEnter={() => previewTheme(id)}
                onMouseLeave={resetPreviewTheme}
                className={cn(
                  "surface-panel-subtle text-left transition-all duration-150",
                  "rounded-[1.35rem] border px-4 py-4",
                  isActive
                    ? "border-primary/30 bg-accent/70 shadow-[0_18px_50px_-30px_rgba(0,0,0,0.55)]"
                    : "border-border/60 hover:border-primary/20 hover:bg-accent/45",
                )}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-semibold tracking-tight">{def.label}</span>
                      {isActive ? <Check className="text-primary h-4 w-4" /> : null}
                    </div>
                    {def.description ? (
                      <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                        {def.description}
                      </p>
                    ) : null}
                  </div>
                  <div className="text-muted-foreground flex shrink-0 items-center gap-1.5">
                    <div
                      className="h-3.5 w-3.5 rounded-full border border-white/15"
                      style={{ backgroundColor: def.previewAccent }}
                    />
                    <Monitor className="h-4 w-4" />
                  </div>
                </div>

                <div
                  className="mt-4 overflow-hidden rounded-[1rem] border"
                  style={{
                    backgroundColor: def.previewBg,
                    borderColor: "color-mix(in srgb, var(--border) 60%, transparent)",
                  }}
                >
                  <div className="px-3 py-3">
                    <div className="flex items-center gap-2">
                      <div
                        className="h-2.5 w-2.5 rounded-full"
                        style={{ backgroundColor: def.previewAccent }}
                      />
                      <div className="h-2 w-16 rounded-full bg-white/70" />
                    </div>
                    <div className="mt-3 grid grid-cols-[1.2fr_0.8fr] gap-2">
                      <div className="space-y-2 rounded-[0.85rem] border border-white/10 bg-white/6 p-2.5">
                        <div className="h-2 w-20 rounded-full bg-white/80" />
                        <div className="h-2 w-full rounded-full bg-white/18" />
                        <div className="h-2 w-3/4 rounded-full bg-white/12" />
                      </div>
                      <div className="rounded-[0.85rem] border border-white/10 bg-white/8 p-2.5">
                        <div
                          className="h-full min-h-14 rounded-[0.7rem]"
                          style={{
                            background:
                              "linear-gradient(180deg, color-mix(in srgb, white 14%, transparent), transparent)",
                          }}
                        />
                      </div>
                    </div>
                  </div>
                </div>
              </button>
            );
          })}
        </div>
      </SettingsGroup>

      <SettingsGroup
        title="Date & time"
        description="Choose how dates and clock times are displayed across the app. Saved for the active profile."
      >
        <div className="space-y-4">
          <div className="space-y-2">
            <p id={dateFormatLabelId} className="text-sm font-medium">
              Date format
            </p>
            <div
              role="radiogroup"
              aria-labelledby={dateFormatLabelId}
              className="flex flex-wrap gap-2"
            >
              {DATE_FORMAT_CHOICES.map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={dateFormat === option.value}
                  variant={dateFormat === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setDateFormat(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <p id={timeFormatLabelId} className="text-sm font-medium">
              Time format
            </p>
            <div
              role="radiogroup"
              aria-labelledby={timeFormatLabelId}
              className="flex flex-wrap gap-2"
            >
              {TIME_FORMAT_CHOICES.map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={timeFormat === option.value}
                  variant={timeFormat === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setTimeFormat(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          </div>

          <p className="text-muted-foreground text-[13px]">
            Preview: {formatDate(previewDate)} · {formatDate(previewDate, "medium")} ·{" "}
            {formatTime(previewDate)}
          </p>
        </div>
      </SettingsGroup>

      <SettingsGroup
        title="Current selection"
        description="Changes apply immediately and are saved for the active profile."
      >
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-1">
            <p className="text-sm font-medium">{activeTheme.label}</p>
            <p className="text-muted-foreground text-[13px] leading-relaxed">
              {activeTheme.description}
            </p>
          </div>
          <button
            type="button"
            onClick={() => setTheme("midnight-cinema")}
            className="border-border text-foreground hover:bg-accent inline-flex h-8 items-center justify-center rounded-md border px-3 text-sm font-medium transition-colors"
          >
            Reset to Cinema Dark
          </button>
        </div>
      </SettingsGroup>
    </div>
  );
}
