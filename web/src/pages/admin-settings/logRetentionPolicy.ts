export const OPSLOG_RETENTION_DAYS_KEY = "opslog.retention_days";
export const OPSLOG_CLEANUP_INTERVAL_KEY = "opslog.cleanup_interval_minutes";
export const OPSLOG_MAX_ROWS_KEY = "opslog.max_rows";
export const OPSLOG_MAX_SIZE_MB_KEY = "opslog.max_size_mb";
export const OPSLOG_BUCKET_POLICIES_KEY = "opslog.bucket_policies";

export const LOG_LEVEL_OPTIONS = ["info", "warn", "error"] as const;

export interface LogRetentionBucketPolicy {
  component: string;
  level: string;
  retention_days: number;
  max_rows: number;
  max_size_mb: number;
}

export const DEFAULT_BUCKET_POLICIES: LogRetentionBucketPolicy[] = [
  { component: "metadata", level: "info", retention_days: 1, max_rows: 100000, max_size_mb: 128 },
  { component: "scanner", level: "info", retention_days: 2, max_rows: 150000, max_size_mb: 192 },
  { component: "metadata", level: "warn", retention_days: 7, max_rows: 250000, max_size_mb: 256 },
  { component: "scanner", level: "warn", retention_days: 7, max_rows: 250000, max_size_mb: 256 },
];

function normalizeBucketLimit(value: unknown): number {
  const parsed =
    typeof value === "number"
      ? value
      : typeof value === "string"
        ? Number.parseInt(value, 10)
        : Number.NaN;
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

export function parseBucketPolicies(raw: string): LogRetentionBucketPolicy[] {
  if (!raw.trim()) {
    return [];
  }

  const parsed = JSON.parse(raw);
  if (!Array.isArray(parsed)) {
    return [];
  }

  return parsed
    .map((entry) => {
      if (!entry || typeof entry !== "object") {
        return null;
      }
      const component = String((entry as { component?: unknown }).component ?? "").trim();
      const level = String((entry as { level?: unknown }).level ?? "")
        .trim()
        .toLowerCase();
      if (!component || !LOG_LEVEL_OPTIONS.includes(level as (typeof LOG_LEVEL_OPTIONS)[number])) {
        return null;
      }
      return {
        component,
        level,
        retention_days: normalizeBucketLimit(
          (entry as { retention_days?: unknown }).retention_days,
        ),
        max_rows: normalizeBucketLimit((entry as { max_rows?: unknown }).max_rows),
        max_size_mb: normalizeBucketLimit((entry as { max_size_mb?: unknown }).max_size_mb),
      };
    })
    .filter((entry): entry is LogRetentionBucketPolicy => entry !== null);
}

export function serializeBucketPolicies(policies: LogRetentionBucketPolicy[]): string {
  const normalized = policies
    .map((policy) => ({
      component: policy.component.trim(),
      level: policy.level.trim().toLowerCase(),
      retention_days: normalizeBucketLimit(policy.retention_days),
      max_rows: normalizeBucketLimit(policy.max_rows),
      max_size_mb: normalizeBucketLimit(policy.max_size_mb),
    }))
    .filter(
      (policy) =>
        policy.component &&
        LOG_LEVEL_OPTIONS.includes(policy.level as (typeof LOG_LEVEL_OPTIONS)[number]),
    );

  return JSON.stringify(normalized);
}

/** A bucket policy plus the stable row id the editor keys React on. */
export type LogRetentionBucketRow = LogRetentionBucketPolicy & { id: string };

export function createBucketRow(
  policy?: Partial<LogRetentionBucketPolicy>,
  id = "0",
): LogRetentionBucketRow {
  return {
    id,
    component: policy?.component ?? "",
    level: policy?.level ?? "info",
    retention_days: policy?.retention_days ?? 1,
    max_rows: policy?.max_rows ?? 100000,
    max_size_mb: policy?.max_size_mb ?? 128,
  };
}

export function recommendedBucketRows(): LogRetentionBucketRow[] {
  return DEFAULT_BUCKET_POLICIES.map((policy, index) => createBucketRow(policy, String(index + 1)));
}

export interface LogRetentionBucketRowsState {
  rows: LogRetentionBucketRow[];
  /** Empty unless the stored JSON was unreadable and the rows fell back. */
  error: string;
}

/**
 * Turns the stored `opslog.bucket_policies` JSON into editor rows. Unreadable
 * JSON falls back to the recommended rules with an error the caller shows, so
 * an admin can always recover without hand-editing the database.
 */
export function bucketRowsFromRaw(raw: string): LogRetentionBucketRowsState {
  try {
    const parsed = parseBucketPolicies(raw);
    return {
      rows: parsed.map((policy, index) => createBucketRow(policy, String(index + 1))),
      error: "",
    };
  } catch (error) {
    return {
      rows: recommendedBucketRows(),
      error: error instanceof Error ? error.message : "Failed to parse bucket rules",
    };
  }
}

function nextBucketRowID(rows: LogRetentionBucketRow[]): string {
  const highest = rows.reduce((max, row) => Math.max(max, Number.parseInt(row.id, 10) || 0), 0);
  return String(highest + 1);
}

export function appendBucketRow(rows: LogRetentionBucketRow[]): LogRetentionBucketRow[] {
  return [...rows, createBucketRow(undefined, nextBucketRowID(rows))];
}

export function removeBucketRow(
  rows: LogRetentionBucketRow[],
  id: string,
): LogRetentionBucketRow[] {
  return rows.filter((row) => row.id !== id);
}

export function updateBucketRow(
  rows: LogRetentionBucketRow[],
  id: string,
  field: keyof LogRetentionBucketPolicy,
  value: string,
): LogRetentionBucketRow[] {
  return rows.map((row) =>
    row.id === id
      ? {
          ...row,
          [field]:
            field === "component" || field === "level"
              ? value
              : Math.max(0, Number.parseInt(value, 10) || 0),
        }
      : row,
  );
}

export function serializeBucketRows(rows: LogRetentionBucketRow[]): string {
  return serializeBucketPolicies(rows);
}
