import type { AdminBackupS3TestBody, AdminBackupS3TestResult } from "@/api/v2/adminBackups";

import { BACKUP_DESTINATION_KEY } from "./backupPresentation";

/**
 * The settings the S3 setup dialog writes. `backup.s3.secret_key` is
 * sensitive: the server never returns it. `backup.s3.key_acknowledged` is
 * machine-managed and never sent.
 */
export const BACKUP_S3_KEYS = {
  endpoint: "backup.s3.endpoint",
  region: "backup.s3.region",
  bucket: "backup.s3.bucket",
  keyPrefix: "backup.s3.key_prefix",
  accessKey: "backup.s3.access_key",
  secretKey: "backup.s3.secret_key",
  pathStyle: "backup.s3.path_style",
} as const;

export interface S3Fields {
  endpoint: string;
  region: string;
  bucket: string;
  keyPrefix: string;
  accessKey: string;
  /** Typed by the admin; empty keeps the stored one. */
  secretKey: string;
  pathStyle: boolean;
}

/** The dialog's starting values, from the saved settings. The secret is never among them. */
export function s3FieldsFromSettings(read: (key: string) => string): S3Fields {
  return {
    endpoint: read(BACKUP_S3_KEYS.endpoint),
    region: read(BACKUP_S3_KEYS.region),
    bucket: read(BACKUP_S3_KEYS.bucket),
    keyPrefix: read(BACKUP_S3_KEYS.keyPrefix),
    accessKey: read(BACKUP_S3_KEYS.accessKey),
    secretKey: "",
    pathStyle: read(BACKUP_S3_KEYS.pathStyle) === "true",
  };
}

/**
 * What "Test connection" sends. An empty secret is left out, so the server
 * uses the stored one (only for the stored endpoint and access key).
 */
export function s3TestBody(fields: S3Fields): AdminBackupS3TestBody {
  const body: AdminBackupS3TestBody = {
    endpoint: fields.endpoint.trim(),
    bucket: fields.bucket.trim(),
    region: fields.region.trim(),
    key_prefix: fields.keyPrefix.trim(),
    access_key: fields.accessKey.trim(),
    path_style: fields.pathStyle,
  };
  if (fields.secretKey !== "") body.secret_key = fields.secretKey;
  return body;
}

/** What the dialog shows of a test: the server's answer, or why there was none. */
export type S3TestResult = Pick<AdminBackupS3TestResult, "ok" | "message">;

/** A test result and the field values it was for. */
export interface S3TestOutcome {
  body: AdminBackupS3TestBody;
  result: S3TestResult;
}

function sameBody(a: AdminBackupS3TestBody, b: AdminBackupS3TestBody): boolean {
  const keys = new Set([...Object.keys(a), ...Object.keys(b)]) as Set<keyof AdminBackupS3TestBody>;
  return [...keys].every((key) => a[key] === b[key]);
}

/** The test result for the fields as they are now, or null when they changed since. */
export function currentS3Test(
  fields: S3Fields,
  outcome: S3TestOutcome | null,
): S3TestResult | null {
  return outcome && sameBody(outcome.body, s3TestBody(fields)) ? outcome.result : null;
}

/**
 * Why "Save and use this bucket" is unavailable, or null when it can save:
 * the latest test passed for the values in the fields now, and the admin
 * confirmed keeping a copy of SECRET_KEY elsewhere.
 */
export function s3SaveBlocker({
  fields,
  outcome,
  keyKept,
}: {
  fields: S3Fields;
  outcome: S3TestOutcome | null;
  keyKept: boolean;
}): string | null {
  if (!fields.endpoint.trim() || !fields.bucket.trim()) return "Enter the endpoint and the bucket.";
  const test = currentS3Test(fields, outcome);
  if (!test) return "Test the connection first.";
  if (!test.ok) return "The connection test failed.";
  if (!keyKept) return "Confirm you keep a copy of SECRET_KEY.";
  return null;
}

/**
 * The settings batch that switches backups to the bucket. The secret goes
 * only when the admin typed one; leaving it out keeps the stored one.
 */
export function s3SettingsValues(fields: S3Fields): Record<string, string> {
  const body = s3TestBody(fields);
  const values: Record<string, string> = {
    [BACKUP_DESTINATION_KEY]: "s3",
    [BACKUP_S3_KEYS.endpoint]: body.endpoint,
    [BACKUP_S3_KEYS.region]: body.region ?? "",
    [BACKUP_S3_KEYS.bucket]: body.bucket,
    [BACKUP_S3_KEYS.keyPrefix]: body.key_prefix ?? "",
    [BACKUP_S3_KEYS.accessKey]: body.access_key ?? "",
    [BACKUP_S3_KEYS.pathStyle]: fields.pathStyle ? "true" : "false",
  };
  if (body.secret_key !== undefined) values[BACKUP_S3_KEYS.secretKey] = body.secret_key;
  return values;
}
