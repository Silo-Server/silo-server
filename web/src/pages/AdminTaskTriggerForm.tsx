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

export function TriggerFormRow({ trigger, onChange, onRemove }: TriggerFormRowProps) {
  const t = useT();
  // Convert interval_ms to a user-friendly value + unit
  const intervalToDisplay = (ms: number): { value: number; unit: string } => {
    if (ms >= 3_600_000 && ms % 3_600_000 === 0) return { value: ms / 3_600_000, unit: "hours" };
    if (ms >= 60_000 && ms % 60_000 === 0) return { value: ms / 60_000, unit: "minutes" };
    return { value: ms / 1000, unit: "seconds" };
  };

  const displayToMs = (value: number, unit: string): number => {
    if (unit === "hours") return value * 3_600_000;
    if (unit === "minutes") return value * 60_000;
    return value * 1000;
  };

  const intervalDisplay = intervalToDisplay(trigger.interval_ms ?? 60_000);
  const [intervalUnit, setIntervalUnit] = useState(intervalDisplay.unit);

  return (
    <div className="surface-panel-subtle flex flex-col gap-3 rounded-xl p-3 sm:flex-row sm:items-start">
      <div className="flex-1 space-y-2">
        <Select
          value={trigger.type}
          onValueChange={(v) => onChange({ ...trigger, type: v as TriggerType })}
        >
          <SelectTrigger className="w-full sm:w-[160px]">
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
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input
              type="number"
              min={1}
              className="w-full sm:w-[100px]"
              value={intervalDisplay.value}
              onChange={(e) => {
                const val = parseInt(e.target.value, 10) || 1;
                onChange({ ...trigger, interval_ms: displayToMs(val, intervalUnit) });
              }}
            />
            <Select
              value={intervalUnit}
              onValueChange={(u) => {
                setIntervalUnit(u);
                onChange({
                  ...trigger,
                  interval_ms: displayToMs(intervalDisplay.value, u),
                });
              }}
            >
              <SelectTrigger className="w-full sm:w-[120px]">
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
            className="w-full sm:w-[140px]"
            value={trigger.time_of_day ?? "00:00"}
            onChange={(e) => onChange({ ...trigger, time_of_day: e.target.value })}
          />
        )}

        {trigger.type === "weekly" && (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Select
              value={String(trigger.day_of_week ?? 0)}
              onValueChange={(v) => onChange({ ...trigger, day_of_week: parseInt(v, 10) })}
            >
              <SelectTrigger className="w-full sm:w-[140px]">
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
              className="w-full sm:w-[140px]"
              value={trigger.time_of_day ?? "00:00"}
              onChange={(e) => onChange({ ...trigger, time_of_day: e.target.value })}
            />
          </div>
        )}

        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <label className="text-muted-foreground text-xs whitespace-nowrap">
            {t("web.admin.tasks.detail.maxRuntimeLabel")}
          </label>
          <Input
            type="number"
            min={0}
            step="any"
            className="w-full sm:w-[100px]"
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
