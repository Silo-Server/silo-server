import type { AdminBackupUpload, UploadRejectionReason } from "@/api/v2/adminBackupUpload";
import { V2ProblemError } from "@/api/v2/request";

import { backupDateTime, knownSiloVersion, sourceServerName } from "./backupPresentation";

/** How many times one part is sent before the upload stops and offers a retry. */
const UPLOAD_PART_ATTEMPTS = 3;

const KB = 1024;
const MB = KB * 1024;
const GB = MB * 1024;

/** The byte range of `part` (counting from 0): every part but the last is `partSize` long. */
export function partRange(part: number, partSize: number, size: number): [number, number] {
  const start = part * partSize;
  return [start, Math.min(start + partSize, size)];
}

/** "41 of 63 MB": both sizes in the unit the total reads best in. */
export function progressLabel(sent: number, total: number): string {
  const [unit, label, digits] =
    total >= GB ? [GB, "GB", 1] : total >= MB ? [MB, "MB", 0] : [KB, "KB", 0];
  const show = (bytes: number, round: (n: number) => number) => {
    const scale = 10 ** digits;
    return (round((bytes / unit) * scale) / scale).toFixed(digits);
  };
  const totalText = show(total, Math.round);
  // Never show the total before the last byte arrived.
  const sentText = sent >= total ? totalText : show(sent, Math.floor);
  return `${sentText} of ${totalText} ${label}`;
}

export function progressPercent(sent: number, total: number): number {
  if (total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.floor((sent / total) * 100)));
}

/** A size limit as a person says it: "2 GB". */
export function limitLabel(bytes: number): string {
  if (bytes >= GB) return `${+(bytes / GB).toFixed(1)} GB`;
  return `${+(bytes / MB).toFixed(1)} MB`;
}

export type SniffResult = "restore_set" | "not_a_restore_set";

function ascii(bytes: Uint8Array, start: number, end: number): string {
  let out = "";
  for (const code of bytes.subarray(start, end)) {
    if (code === 0) break;
    out += String.fromCharCode(code);
  }
  return out;
}

/**
 * What the first bytes of a file say it is, before uploading it: a tar whose
 * first entry is restore-set.json (directly, or behind the PAX header Silo's
 * writer puts first). The server checks the whole file again; this only saves
 * uploading something it would refuse.
 */
export function sniffRestoreSet(head: Uint8Array): SniffResult {
  if (head.length < 512 || ascii(head, 257, 262) !== "ustar") return "not_a_restore_set";
  const name = ascii(head, 0, 100);
  return name === "restore-set.json" || name.endsWith("/restore-set.json")
    ? "restore_set"
    : "not_a_restore_set";
}

/** Why an upload stopped, in the cases the dialog tells apart. */
export type UploadFailure =
  | { kind: "too_large"; sizeBytes: number; limitBytes: number }
  | { kind: "too_many" }
  | { kind: "busy" }
  | { kind: "part_failed"; part: number; partCount: number }
  | { kind: "rejected"; reason: UploadRejectionReason; message: string }
  | { kind: "error"; detail: string };

export type UploadStage = "create" | "part" | "complete";

/** Maps a failed request to what the dialog shows. */
export function failureOfError(
  error: unknown,
  stage: UploadStage,
  { sizeBytes, limitBytes }: { sizeBytes: number; limitBytes: number },
): UploadFailure {
  if (error instanceof V2ProblemError) {
    if (error.status === 413 && stage === "create")
      return { kind: "too_large", sizeBytes, limitBytes };
    if (error.problemType === "upload_limit_reached") return { kind: "too_many" };
    if (stage === "complete" && error.status === 409 && error.problemType === "conflict")
      return { kind: "busy" };
    return { kind: "error", detail: error.message };
  }
  return { kind: "error", detail: "The server didn't answer. Check that Silo is running." };
}

export interface FailureCopy {
  title: string;
  body: string;
  /** Whether "Try again" continues the same upload. */
  resumable: boolean;
}

const REJECTION_COPY: Record<UploadRejectionReason, Omit<FailureCopy, "resumable">> = {
  not_a_restore_set: {
    title: "This isn't a restore set",
    body: "Use the restore-set.tar file from Download on a Silo server's Backups page.",
  },
  newer_silo: {
    title: "This backup is from a newer Silo",
    body: "Upgrade this server first, then upload it again.",
  },
  checksum_mismatch: {
    title: "The file changed while it was uploading",
    body: "Upload it again.",
  },
};

export function failureCopy(failure: UploadFailure): FailureCopy {
  switch (failure.kind) {
    case "too_large":
      return {
        title: "This file is too large",
        body: `Uploads can be at most ${limitLabel(failure.limitBytes)}, and this file is ${limitLabel(failure.sizeBytes)}.`,
        resumable: false,
      };
    case "too_many":
      return {
        title: "Too many uploads in progress",
        body: "Three uploads are already in progress on this server. Finish or delete one first. Unfinished uploads are deleted after 24 hours.",
        resumable: false,
      };
    case "busy":
      return {
        title: "A backup or restore is running",
        body: "The file arrived. Try again when it finishes to open it.",
        resumable: true,
      };
    case "part_failed":
      return {
        title: "The upload stopped",
        body: `Part ${failure.part + 1} of ${failure.partCount} didn't arrive. The parts already sent are kept, so trying again picks up where it stopped.`,
        resumable: true,
      };
    case "rejected":
      return { ...REJECTION_COPY[failure.reason], resumable: false };
    case "error":
      return { title: "The upload stopped", body: failure.detail, resumable: false };
  }
}

/** "Opened. Backup from Living room, Oct 30, 2026, 4:00 AM, Silo 0.9.1." */
export function openedSentence(upload: AdminBackupUpload): string {
  const source = upload.source;
  if (!source) return "Opened.";
  const name = sourceServerName(source) ?? "another server";
  const taken = backupDateTime(source.created_at);
  const version = knownSiloVersion(source.silo_version);
  return `Opened. Backup from ${name}, ${taken}${version ? `, Silo ${version}` : ""}.`;
}

/** A part that failed after its retries; the parts before it arrived. */
export class PartFailedError extends Error {
  readonly part: number;
  readonly failure: unknown;

  constructor(part: number, failure: unknown) {
    super(`part ${part} failed`);
    this.name = "PartFailedError";
    this.part = part;
    this.failure = failure;
  }
}

/**
 * Whether sending a part again could help: no answer, a timeout, a server
 * error, or a conflict, which for a part means an earlier send of it is
 * still being received.
 */
export function retryable(error: unknown): boolean {
  if (!(error instanceof V2ProblemError)) return true;
  return error.status === 408 || error.status === 409 || error.status >= 500;
}

/** How long to wait before sending a part the `attempt`th time again. */
export function retryDelayMs(attempt: number): number {
  return 1_000 * 2 ** (attempt - 1);
}

function waitMs(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        reject(signal.reason);
      },
      { once: true },
    );
  });
}

export interface SendPartsOptions {
  upload: AdminBackupUpload;
  file: Blob;
  /** The first part to send; earlier parts arrived. */
  from: number;
  put: (part: number, bytes: Blob) => Promise<AdminBackupUpload>;
  onProgress?: (upload: AdminBackupUpload) => void;
  signal?: AbortSignal;
  attempts?: number;
  /** Waits between sends of a part; tests pass one that does not. */
  wait?: (ms: number, signal?: AbortSignal) => Promise<void>;
}

/**
 * Sends parts `from` to the last in order, each up to `attempts` times. A
 * part that still fails throws PartFailedError naming it, so the caller can
 * continue from there: the server keeps every part that arrived, and a part
 * sent again replaces the earlier copy.
 */
export async function sendParts({
  upload,
  file,
  from,
  put,
  onProgress,
  signal,
  attempts = UPLOAD_PART_ATTEMPTS,
  wait = waitMs,
}: SendPartsOptions): Promise<AdminBackupUpload> {
  let current = upload;
  for (let part = from; part < upload.part_count; part += 1) {
    const [start, end] = partRange(part, upload.part_size, upload.size_bytes);
    for (let attempt = 1; ; attempt += 1) {
      signal?.throwIfAborted();
      try {
        current = await put(part, file.slice(start, end));
        break;
      } catch (error) {
        if (signal?.aborted) throw signal.reason;
        if (attempt >= attempts || !retryable(error)) throw new PartFailedError(part, error);
        await wait(retryDelayMs(attempt), signal);
      }
    }
    onProgress?.(current);
  }
  return current;
}
