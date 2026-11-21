import { useState } from "react";
import { Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { TriggerConfig, TriggerType } from "@/api/types";
import { useT, type MessageKey } from "@/i18n";

// --- Trigger form ---

const TRIGGER_TYPES: { value: TriggerType; label: MessageKey }[] = [
  { value: "interval", label: "web.admin.tasks.detail.triggerType.interval" },
  { value: "daily", label: "web.admin.tasks.detail.triggerType.daily" },
  { value: "weekly", label: "web.admin.tasks.detail.triggerType.weekly" },
  { value: "startup", label: "web.admin.tasks.detail.triggerType.startup" },
];

// Index = the trigger's day_of_week (0 = Sunday); the catalog numbers weekdays from Monday = 1.
const DAYS_OF_WEEK: MessageKey[] = [
  "calendar.weekday.7",
  "calendar.weekday.1",
  "calendar.weekday.2",
  "calendar.weekday.3",
  "calendar.weekday.4",
  "calendar.weekday.5",
  "calendar.weekday.6",
];

interface TriggerFormRowProps {
  trigger: TriggerConfig;
  onChange: (updated: TriggerConfig) => void;
  onRemove: () => void;
}

// The trigger's type, weekday, and time fields share one width. 10rem holds the
// widest time format measured (Amharic, 159 px at 16 px text) and the longest
// weekday name ("Segunda-feira"). A field whose text needs more grows to fit it:
// a longer label ("Przy uruchomieniu"), or a minimum font size larger than the
// browser's default font size, which rem follows. It stops at the column's width,
// so one long label can't push the Remove button out of view.
const TRIGGER_FIELD_WIDTH = "w-full sm:w-fit sm:min-w-40 sm:max-w-full";

// Sized for the widest "None" placeholder measured, Croatian "Bez ograničenja"
// (167 px at 16 px text and 152 px at 14 px in Firefox), beside the number
// spinners Firefox and Safari always draw: about 11.5em. Plus Jakarta Sans's cap
// height is about 0.75em, so 15.5cap is the same width; change both together.
// A browser minimum font size moves ic and cap in Firefox, but only cap in
// Chromium, and em in neither, so where cap is supported the box takes the
// larger of the two. Other browsers keep 11.5em. The box stops at its column's
// width.
const MAX_RUNTIME_INPUT_WIDTH =
  "w-full sm:w-[11.5em] sm:supports-[width:1cap]:w-[max(11.5ic,15.5cap)] sm:max-w-full";

type IntervalUnit = "seconds" | "minutes" | "hours";

const UNIT_MS: Record<IntervalUnit, number> = {
  seconds: 1000,
  minutes: 60_000,
  hours: 3_600_000,
};

interface IntervalDraft {
  // The interval_ms this draft was read from or last sent to onChange.
  ms: number | undefined;
  text: string;
  unit: IntervalUnit;
}

// Shows a stored interval in the largest unit that divides it evenly.
function intervalToDraft(ms: number | undefined): IntervalDraft {
  if (ms == null) return { ms, text: "", unit: "minutes" };
  if (ms >= UNIT_MS.hours && ms % UNIT_MS.hours === 0)
    return { ms, text: String(ms / UNIT_MS.hours), unit: "hours" };
  if (ms >= UNIT_MS.minutes && ms % UNIT_MS.minutes === 0)
    return { ms, text: String(ms / UNIT_MS.minutes), unit: "minutes" };
  return { ms, text: String(ms / UNIT_MS.seconds), unit: "seconds" };
}

// The largest interval_ms the tasks API accepts.
const MAX_INTERVAL_MS = 9_223_372_036_854;

// Undefined unless the box holds an interval the API accepts. The schedule
// panel keeps Save off until every interval trigger has one.
function draftToMs(text: string, unit: IntervalUnit): number | undefined {
  if (text.trim() === "") return undefined;
  const ms = Math.round(Number(text) * UNIT_MS[unit]);
  return Number.isFinite(ms) && ms >= 1 && ms <= MAX_INTERVAL_MS ? ms : undefined;
}

export function TriggerFormRow({ trigger, onChange, onRemove }: TriggerFormRowProps) {
  const t = useT();
  // The box and the unit select show what was typed and picked, not the unit
  // that divides interval_ms most evenly, so typing 60 minutes stays 60 minutes.
  const [intervalDraft, setIntervalDraft] = useState(() => intervalToDraft(trigger.interval_ms));
  if (intervalDraft.ms !== trigger.interval_ms) {
    // interval_ms changed outside this row, so show the new value.
    setIntervalDraft(intervalToDraft(trigger.interval_ms));
  }
  const updateInterval = (text: string, unit: IntervalUnit) => {
    const ms = draftToMs(text, unit);
    setIntervalDraft({ ms, text, unit });
    onChange({ ...trigger, interval_ms: ms });
  };

  return (
    <div className="surface-panel-subtle flex flex-col gap-3 rounded-xl p-3 sm:flex-row sm:items-start">
      <div className="min-w-0 flex-1 space-y-2">
        <Select
          value={trigger.type}
          onValueChange={(v) => {
            // Store the values the new type's inputs show until edited, so Save
            // sends them instead of leaving the fields out of the request.
            const type = v as TriggerType;
            const next = { ...trigger, type };
            if (type === "interval") next.interval_ms ??= 60_000;
            if (type === "daily" || type === "weekly") next.time_of_day ??= "00:00";
            if (type === "weekly") next.day_of_week ??= 0;
            onChange(next);
          }}
        >
          <SelectTrigger className={TRIGGER_FIELD_WIDTH}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TRIGGER_TYPES.map((tt) => (
              <SelectItem key={tt.value} value={tt.value}>
                {t(tt.label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {trigger.type === "interval" && (
          <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
            <Input
              type="number"
              min={1}
              className="w-full sm:w-[100px]"
              value={intervalDraft.text}
              aria-invalid={draftToMs(intervalDraft.text, intervalDraft.unit) === undefined}
              onChange={(e) => updateInterval(e.target.value, intervalDraft.unit)}
            />
            <Select
              value={intervalDraft.unit}
              onValueChange={(u) => updateInterval(intervalDraft.text, u as IntervalUnit)}
            >
              <SelectTrigger className="w-full sm:w-fit sm:max-w-full sm:min-w-30">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="seconds">
                  {t("web.admin.tasks.detail.intervalUnit.seconds")}
                </SelectItem>
                <SelectItem value="minutes">
                  {t("web.admin.tasks.detail.intervalUnit.minutes")}
                </SelectItem>
                <SelectItem value="hours">
                  {t("web.admin.tasks.detail.intervalUnit.hours")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}

        {trigger.type === "daily" && (
          <Input
            type="time"
            className={TRIGGER_FIELD_WIDTH}
            value={trigger.time_of_day ?? "00:00"}
            onChange={(e) => onChange({ ...trigger, time_of_day: e.target.value })}
          />
        )}

        {trigger.type === "weekly" && (
          <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
            <Select
              value={String(trigger.day_of_week ?? 0)}
              onValueChange={(v) => onChange({ ...trigger, day_of_week: parseInt(v, 10) })}
            >
              <SelectTrigger className={TRIGGER_FIELD_WIDTH}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {DAYS_OF_WEEK.map((day, i) => (
                  <SelectItem key={i} value={String(i)}>
                    {t(day)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              type="time"
              className={TRIGGER_FIELD_WIDTH}
              value={trigger.time_of_day ?? "00:00"}
              onChange={(e) => onChange({ ...trigger, time_of_day: e.target.value })}
            />
          </div>
        )}

        {/* At large text sizes the label wraps above the box instead of widening the
            row past its panel; the box and its unit stay together. */}
        <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
          <label className="text-muted-foreground text-xs text-balance break-words">
            {t("web.admin.tasks.detail.maxRuntimeLabel")}
          </label>
          <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
            <Input
              type="number"
              min={0}
              step="any"
              className={MAX_RUNTIME_INPUT_WIDTH}
              placeholder={t("web.admin.tasks.detail.maxRuntimeNone")}
              value={trigger.max_runtime_ms != null ? trigger.max_runtime_ms / 60_000 : ""}
              onChange={(e) => {
                // Fractional minutes keep sub-minute limits saved through the API.
                // A typed 0 stays 0 (no limit) so "0.5" can be entered; only an
                // empty field clears it.
                const raw = e.target.value;
                onChange({
                  ...trigger,
                  max_runtime_ms:
                    raw === "" ? undefined : Math.max(0, Math.round(Number(raw) * 60_000)),
                });
              }}
            />
            <span className="text-muted-foreground text-xs">
              {t("web.admin.tasks.detail.maxRuntimeUnit")}
            </span>
          </div>
        </div>
      </div>

      <Button
        variant="ghost"
        size="icon"
        onClick={onRemove}
        className="shrink-0 self-end sm:self-auto"
      >
        <Trash2 className="h-4 w-4" />
      </Button>
    </div>
  );
}
