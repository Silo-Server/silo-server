import { t } from "@/i18n";

interface TerminalJob {
  message?: string;
  error_message?: string;
}

/**
 * The error a waiter for a failed admin job rejects with, which a toast shows.
 * The server's own message stays as sent; only the fallback is translated.
 */
export function jobFailedError(job: TerminalJob): Error {
  return new Error(job.error_message || job.message || t("web.common.job.failed"));
}

/** The error a waiter for a cancelled admin job rejects with. */
export function jobCancelledError(job: TerminalJob): Error {
  return new Error(job.message || t("web.common.job.cancelled"));
}
