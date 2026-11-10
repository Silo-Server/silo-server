export const FILTER_KEYS = ["user_id", "platform", "report_type", "from", "to", "short_id"];

export interface FilterDraft {
  userID: string;
  platform: string;
  reportType: string;
  from: string;
  to: string;
  shortID: string;
}

export function draftFromSearchParams(searchParams: URLSearchParams): FilterDraft {
  return {
    userID: searchParams.get("user_id") ?? "",
    platform: searchParams.get("platform") ?? "all",
    reportType: searchParams.get("report_type") ?? "all",
    from: toLocalDateTimeInput(searchParams.get("from")),
    to: toLocalDateTimeInput(searchParams.get("to")),
    shortID: searchParams.get("short_id") ?? "",
  };
}

export function setOrDelete(params: URLSearchParams, key: string, value: string | undefined) {
  const normalized = value?.trim();
  if (normalized) {
    params.set(key, normalized);
  } else {
    params.delete(key);
  }
}

export function normalizeUserID(value: string) {
  const normalized = value.trim();
  if (!normalized) return undefined;
  if (!/^[1-9]\d*$/.test(normalized)) return undefined;
  const parsed = Number(normalized);
  return Number.isSafeInteger(parsed) ? String(parsed) : undefined;
}

export function toRFC3339(value: string) {
  if (!value.trim()) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}

function toLocalDateTimeInput(value: string | null) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}
