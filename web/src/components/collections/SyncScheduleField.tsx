import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const PRESET_VALUES = [
  { value: "0 * * * *", label: "Every hour" },
  { value: "0 */6 * * *", label: "Every 6 hours" },
  { value: "0 3 * * *", label: "Daily at 3:00 AM" },
  { value: "0 3 * * 1", label: "Weekly (Monday 3:00 AM)" },
  { value: "0 3 * * 0", label: "Weekly (Sunday 3:00 AM)" },
  { value: "0 3 1 * *", label: "Monthly (1st at 3:00 AM)" },
] as const;

function findPreset(value: string): string | undefined {
  return PRESET_VALUES.find((p) => p.value === value)?.value;
}

function deriveMode(value: string): "none" | "preset" | "custom" {
  if (!value) return "none";
  if (findPreset(value)) return "preset";
  return "custom";
}

interface SyncScheduleFieldProps {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  label?: string;
  inputId?: string;
}

export function SyncScheduleField({
  value,
  onChange,
  disabled,
  label = "Sync Schedule",
  inputId,
}: SyncScheduleFieldProps) {
  const [mode, setMode] = useState<"none" | "preset" | "custom">(() => deriveMode(value));

  const selectValue =
    mode === "none" ? "__none__" : mode === "custom" ? "custom" : (findPreset(value) ?? "custom");

  return (
    <div className="space-y-2">
      <Label htmlFor={inputId}>{label}</Label>
      <Select
        value={selectValue}
        onValueChange={(v) => {
          if (v === "__none__") {
            setMode("none");
            onChange("");
          } else if (v === "custom") {
            setMode("custom");
            if (!value || findPreset(value)) {
              onChange("0 3 * * *");
            }
          } else {
            setMode("preset");
            onChange(v);
          }
        }}
        disabled={disabled}
      >
        <SelectTrigger id={inputId} className="w-full sm:w-[280px]">
          <SelectValue placeholder="Select a schedule" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="__none__">No automatic sync</SelectItem>
          {PRESET_VALUES.map((preset) => (
            <SelectItem key={preset.value} value={preset.value}>
              {preset.label}
            </SelectItem>
          ))}
          <SelectItem value="custom">Custom cron expression</SelectItem>
        </SelectContent>
      </Select>

      {mode === "custom" && (
        <div className="space-y-1">
          <Input
            type="text"
            placeholder="0 3 * * *"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={disabled}
            className="w-full font-mono sm:w-[280px]"
          />
          <p className="text-muted-foreground text-xs">
            Standard cron format: minute hour day-of-month month day-of-week
          </p>
        </div>
      )}
    </div>
  );
}
