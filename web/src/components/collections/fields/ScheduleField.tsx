import { useId, useState } from "react";

import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { serverTimeLabel } from "@/lib/collections/copy";

/** Radix Select can't hold "", so "no scheduled sync" is this in the menu. */
const NONE = "none";
const CUSTOM = "custom";

const SERVER_PRESETS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "0 * * * *", label: "Every hour" },
  { value: "0 */6 * * *", label: "Every 6 hours" },
  { value: "0 3 * * *", label: "Every day at 3:00 AM" },
  { value: "0 3 * * 1", label: "Every Monday at 3:00 AM" },
  { value: "0 3 * * 0", label: "Every Sunday at 3:00 AM" },
  { value: "0 3 1 * *", label: "On the 1st of every month at 3:00 AM" },
];

const PERSONAL_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: NONE, label: "Manual only" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
];

const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

function clock(minute: string, hour: string): string | null {
  if (!/^\d{1,2}$/.test(minute) || !/^\d{1,2}$/.test(hour)) return null;
  const h = Number(hour);
  const m = Number(minute);
  if (h > 23 || m > 59) return null;
  return `${h % 12 === 0 ? 12 : h % 12}:${String(m).padStart(2, "0")} ${h < 12 ? "AM" : "PM"}`;
}

function ordinal(day: number): string {
  const tens = day % 100;
  if (tens >= 11 && tens <= 13) return `${day}th`;
  return `${day}${["th", "st", "nd", "rd"][day % 10] ?? "th"}`;
}

/** A cron schedule in words, for the shapes the step offers; null for anything else. */
function describeCron(cron: string): string | null {
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== 5) return null;
  const [minute, hour, dom, month, dow] = fields as [string, string, string, string, string];
  if (month !== "*") return null;
  if (minute === "0" && dom === "*" && dow === "*") {
    if (hour === "*") return "Every hour";
    const step = /^\*\/(\d+)$/.exec(hour);
    if (step) return `Every ${step[1]} hours`;
  }
  const at = clock(minute, hour);
  if (!at) return null;
  if (dom === "*" && dow === "*") return `Every day at ${at}`;
  if (dom === "*" && /^[0-6]$/.test(dow)) return `Every ${WEEKDAYS[Number(dow)]} at ${at}`;
  if (/^\d{1,2}$/.test(dom) && dow === "*" && Number(dom) >= 1 && Number(dom) <= 31) {
    return `On the ${ordinal(Number(dom))} of every month at ${at}`;
  }
  return null;
}

export type ScheduleFieldProps =
  | {
      scope: "server";
      /** A cron expression; "" for no scheduled sync. */
      value: string;
      onChange: (value: string) => void;
      /** The answering server's zone, from the collections capabilities. */
      timeZone?: { utc_offset: string; name?: string };
      /** Shown as it is; the panel says why next to it. */
      disabled?: boolean;
    }
  | {
      scope: "personal";
      /** "", "daily", "weekly", "monthly", or "custom" for a schedule kept until changed. */
      value: string;
      onChange: (value: string) => void;
      disabled?: boolean;
    };

/**
 * When a synced list syncs. A server list picks a cron schedule, labelled with
 * the server's time zone because cron runs in it. A profile picks a named
 * schedule; a schedule it can't name stays as "Custom schedule (current)"
 * until another is chosen.
 */
export function ScheduleField(props: ScheduleFieldProps) {
  const id = useId();
  const [customOpen, setCustomOpen] = useState(false);
  const { value, onChange, disabled } = props;

  if (props.scope === "personal") {
    return (
      <Select
        value={value || NONE}
        disabled={disabled}
        onValueChange={(next) => onChange(next === NONE ? "" : next)}
      >
        <SelectTrigger aria-label="Sync schedule" className="h-11 w-full sm:w-[280px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PERSONAL_OPTIONS.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
          {value === CUSTOM ? (
            <SelectItem value={CUSTOM}>Custom schedule (current)</SelectItem>
          ) : null}
        </SelectContent>
      </Select>
    );
  }

  const preset = SERVER_PRESETS.some((option) => option.value === value);
  const own = value && !preset ? (describeCron(value) ?? `Custom: ${value}`) : null;
  const selected = customOpen ? CUSTOM : value || NONE;
  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Select
          value={selected}
          disabled={disabled}
          onValueChange={(next) => {
            setCustomOpen(next === CUSTOM);
            if (next === CUSTOM) {
              if (!value) onChange("0 3 * * *");
            } else {
              onChange(next === NONE ? "" : next);
            }
          }}
        >
          <SelectTrigger
            aria-label="Sync schedule"
            aria-describedby={`${id}-zone`}
            className="h-11 min-w-0 flex-1"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NONE}>No automatic sync</SelectItem>
            {SERVER_PRESETS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
            {own ? <SelectItem value={value}>{own}</SelectItem> : null}
            <SelectItem value={CUSTOM}>Custom schedule…</SelectItem>
          </SelectContent>
        </Select>
        <span id={`${id}-zone`} className="text-muted-foreground text-[13px]">
          {serverTimeLabel(props.timeZone)}
        </span>
      </div>
      {customOpen ? (
        <div className="grid gap-1">
          <Input
            aria-label="Cron schedule"
            aria-describedby={`${id}-cron-help`}
            className="h-10 font-mono"
            placeholder="0 3 * * *"
            value={value}
            onChange={(event) => onChange(event.target.value)}
          />
          <p id={`${id}-cron-help`} className="text-muted-foreground text-[12.5px]">
            Minute, hour, day of month, month, day of week, in server time.
          </p>
        </div>
      ) : null}
    </div>
  );
}
