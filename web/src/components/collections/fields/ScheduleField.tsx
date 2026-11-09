import { useId, useState } from "react";

import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { appIntl, useT, type MessageKey, type Translate } from "@/i18n";
import { serverTimeLabel } from "@/lib/collections/copy";

/** Radix Select can't hold "", so "no scheduled sync" is this in the menu. */
const NONE = "none";
const CUSTOM = "custom";
/** The cron a custom schedule starts from, and the cron field's placeholder. */
const DEFAULT_CRON = "0 3 * * *";

/** The cron schedules a server list offers by name; each is labelled by `describeCron`. */
const SERVER_PRESETS: readonly string[] = [
  "0 * * * *",
  "0 */6 * * *",
  "0 3 * * *",
  "0 3 * * 1",
  "0 3 * * 0",
  "0 3 1 * *",
];

const PERSONAL_OPTIONS: ReadonlyArray<{ value: string; labelKey: MessageKey }> = [
  { value: NONE, labelKey: "web.collections.fields.scheduleManualOnly" },
  { value: "daily", labelKey: "web.collections.fields.scheduleDaily" },
  { value: "weekly", labelKey: "web.collections.fields.scheduleWeekly" },
  { value: "monthly", labelKey: "web.collections.fields.scheduleMonthly" },
];

const WEEKDAYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

function clock(t: Translate, minute: string, hour: string): string | null {
  if (!/^\d{1,2}$/.test(minute) || !/^\d{1,2}$/.test(hour)) return null;
  const h = Number(hour);
  const m = Number(minute);
  if (h > 23 || m > 59) return null;
  return t("web.collections.fields.clockTime", {
    hour: h % 12 === 0 ? 12 : h % 12,
    // Two digits, in the screen's digits like the hour.
    minute: appIntl().formatNumber(m, { minimumIntegerDigits: 2, useGrouping: false }),
    period: h < 12 ? "am" : "pm",
  });
}

/** "1st", "22nd": a day of the month as an ordinal. */
function ordinal(t: Translate, day: number): string {
  return t("web.collections.fields.dayOrdinal", { day: day });
}

/** A cron schedule in words, for the shapes the step offers; null for anything else. */
function describeCron(t: Translate, cron: string): string | null {
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== 5) return null;
  const [minute, hour, dom, month, dow] = fields as [string, string, string, string, string];
  if (month !== "*") return null;
  if (minute === "0" && dom === "*" && dow === "*") {
    if (hour === "*") return t("web.collections.fields.everyHour");
    const step = /^\*\/(\d+)$/.exec(hour);
    if (step) {
      // The step as written ("02" stays "02"); the number only picks the plural form.
      return t("web.collections.fields.everyHours", {
        count: Number(step[1]),
        count_text: step[1],
      });
    }
  }
  const at = clock(t, minute, hour);
  if (!at) return null;
  if (dom === "*" && dow === "*") return t("web.collections.fields.everyDayAt", { time: at });
  if (dom === "*" && /^[0-6]$/.test(dow))
    return t("web.collections.fields.everyWeekdayAt", { day: WEEKDAYS[Number(dow)]!, time: at });
  if (/^\d{1,2}$/.test(dom) && dow === "*" && Number(dom) >= 1 && Number(dom) <= 31) {
    return t("web.collections.fields.monthlyAt", {
      day: ordinal(t, Number(dom)),
      time: at,
    });
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
  const t = useT();
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
        <SelectTrigger
          aria-label={t("web.collections.copy.fieldSchedule")}
          className="h-11 w-full sm:w-[280px]"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PERSONAL_OPTIONS.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {t(option.labelKey)}
            </SelectItem>
          ))}
          {value === CUSTOM ? (
            <SelectItem value={CUSTOM}>
              {t("web.collections.fields.customScheduleCurrent")}
            </SelectItem>
          ) : null}
        </SelectContent>
      </Select>
    );
  }

  const preset = SERVER_PRESETS.includes(value);
  const own =
    value && !preset
      ? (describeCron(t, value) ?? t("web.collections.fields.customCron", { cron: value }))
      : null;
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
              if (!value) onChange(DEFAULT_CRON);
            } else {
              onChange(next === NONE ? "" : next);
            }
          }}
        >
          <SelectTrigger
            aria-label={t("web.collections.copy.fieldSchedule")}
            aria-describedby={`${id}-zone`}
            className="h-11 min-w-0 flex-1"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NONE}>{t("web.collections.fields.noAutomaticSync")}</SelectItem>
            {SERVER_PRESETS.map((cron) => (
              <SelectItem key={cron} value={cron}>
                {describeCron(t, cron)}
              </SelectItem>
            ))}
            {own ? <SelectItem value={value}>{own}</SelectItem> : null}
            <SelectItem value={CUSTOM}>{t("web.collections.fields.customSchedule")}</SelectItem>
          </SelectContent>
        </Select>
        <span id={`${id}-zone`} className="text-muted-foreground text-[13px]">
          {serverTimeLabel(props.timeZone)}
        </span>
      </div>
      {customOpen ? (
        <div className="grid gap-1">
          <Input
            aria-label={t("web.collections.fields.cronSchedule")}
            aria-describedby={`${id}-cron-help`}
            className="h-10 font-mono"
            placeholder={DEFAULT_CRON}
            value={value}
            onChange={(event) => onChange(event.target.value)}
          />
          <p id={`${id}-cron-help`} className="text-muted-foreground text-[12.5px]">
            {t("web.collections.fields.cronHelp")}
          </p>
        </div>
      ) : null}
    </div>
  );
}
