import { useId } from "react";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { useDateTimeFormat, useDateTimeFormatSettings } from "@/hooks/useDateTimeFormat";
import { useTheme } from "@/hooks/useTheme";
import { formatDate, formatTime } from "@/lib/datetime";
import type { DateFormatPreference, TimeFormatPreference } from "@/lib/datetime";

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

export default function AccessibilitySettings() {
  const { textScale, setTextScale, textWeight, setTextWeight, highContrast, setHighContrast } =
    useTheme();

  const textSizeLabelId = useId();
  const textWeightLabelId = useId();
  const contrastLabelId = useId();
  const { dateFormat, timeFormat, setDateFormat, setTimeFormat } = useDateTimeFormatSettings();
  useDateTimeFormat();
  const dateFormatLabelId = useId();
  const timeFormatLabelId = useId();
  const previewDate = new Date();

  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Accessibility</h2>

      <SettingsGroup
        title="Readability"
        description="Increase text size, strengthen type weight, and raise contrast for clearer reading."
      >
        <div className="space-y-4">
          <div className="space-y-2">
            <p id={textSizeLabelId} className="text-sm font-medium">
              Text size
            </p>
            <div
              role="radiogroup"
              aria-labelledby={textSizeLabelId}
              className="flex flex-wrap gap-2"
            >
              {[
                { value: "default" as const, label: "Default" },
                { value: "large" as const, label: "Large" },
                { value: "x-large" as const, label: "Extra Large" },
              ].map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={textScale === option.value}
                  variant={textScale === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setTextScale(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <p id={textWeightLabelId} className="text-sm font-medium">
              Text weight
            </p>
            <div
              role="radiogroup"
              aria-labelledby={textWeightLabelId}
              className="flex flex-wrap gap-2"
            >
              {[
                { value: "default" as const, label: "Default" },
                { value: "strong" as const, label: "Bolder" },
              ].map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={textWeight === option.value}
                  variant={textWeight === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setTextWeight(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <p id={contrastLabelId} className="text-sm font-medium">
              Contrast
            </p>
            <div
              role="radiogroup"
              aria-labelledby={contrastLabelId}
              className="flex flex-wrap gap-2"
            >
              <Button
                role="radio"
                aria-checked={!highContrast}
                variant={!highContrast ? "default" : "outline"}
                size="sm"
                onClick={() => setHighContrast(false)}
              >
                Standard
              </Button>
              <Button
                role="radio"
                aria-checked={highContrast}
                variant={highContrast ? "default" : "outline"}
                size="sm"
                onClick={() => setHighContrast(true)}
              >
                High Contrast
              </Button>
            </div>
          </div>
        </div>
      </SettingsGroup>

      <SettingsGroup
        title="Preview"
        description="See how text looks with your current readability settings."
      >
        <div className="border-border/50 space-y-2 rounded-lg border p-4">
          <p className="text-lg font-semibold">The quick brown fox jumps over the lazy dog</p>
          <p className="text-muted-foreground text-sm">
            This sample paragraph reflects your current text size, weight, and contrast preferences.
            Adjustments take effect across the entire interface.
          </p>
          <p className="text-muted-foreground/70 text-xs">
            Secondary text &middot; Metadata &middot; Captions
          </p>
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
    </div>
  );
}
