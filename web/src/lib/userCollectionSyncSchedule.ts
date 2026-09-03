import type { UserCollectionSyncSchedule } from "@/api/types";

export const MANUAL_SCHEDULE = "none" as const;
export type BoundedScheduleChoice = typeof MANUAL_SCHEDULE | UserCollectionSyncSchedule;

export const BOUNDED_SCHEDULE_OPTIONS: Array<{
  value: BoundedScheduleChoice;
  label: string;
}> = [
  { value: MANUAL_SCHEDULE, label: "Manual only" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
];

export const ADMIN_CRON_BY_BOUNDED_VALUE: Record<UserCollectionSyncSchedule, string> = {
  daily: "0 3 * * *",
  weekly: "0 3 * * 0",
  monthly: "0 3 1 * *",
};

// Admins submit the same cron values as server collections. This conversion
// covers a form whose capabilities load after it initialized with a bounded
// value.
export function adminScheduleValue(value: string): string {
  const normalized = value.trim().toLowerCase() as UserCollectionSyncSchedule;
  return ADMIN_CRON_BY_BOUNDED_VALUE[normalized] ?? value;
}

// Regular accounts keep the existing >=24h cadence. Built-in templates store
// cron, so map any template cron to its closest bounded choice.
export function boundedScheduleChoice(value: string): BoundedScheduleChoice {
  const normalized = value.trim().toLowerCase();
  if (!normalized) return MANUAL_SCHEDULE;
  if (normalized === "daily" || normalized === "weekly" || normalized === "monthly") {
    return normalized;
  }
  const fields = normalized.split(/\s+/);
  if (fields.length !== 5) return "daily";
  const [, , dayOfMonth, month, dayOfWeek] = fields;
  if (dayOfMonth === "1" && month === "*") return "monthly";
  if (dayOfMonth === "*" && month === "*" && dayOfWeek !== "*") return "weekly";
  return "daily";
}

export function userCollectionScheduleRequestValue(value: string, allowCustomCron: boolean) {
  if (allowCustomCron) return adminScheduleValue(value).trim();
  const choice = boundedScheduleChoice(value);
  return choice === MANUAL_SCHEDULE ? "" : choice;
}
