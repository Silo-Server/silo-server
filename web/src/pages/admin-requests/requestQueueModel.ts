import type { MediaRequest, RequestMediaType, RequestTarget } from "@/api/types";
import type { AdminRequestCounts, AdminRequestQueueView } from "@/api/v2/adminRequests";
import { canWithdrawRequest, formatRequestStatus } from "@/lib/mediaRequests";

export type { AdminRequestQueueView as RequestQueueView };

/** The queue's views, in tab order, with the count each one shows. */
export const REQUEST_QUEUE_VIEWS: readonly {
  value: AdminRequestQueueView;
  label: string;
  empty: string;
}[] = [
  { value: "needs_approval", label: "Needs approval", empty: "Nothing is waiting for approval." },
  { value: "in_progress", label: "In progress", empty: "Nothing is on its way to the library." },
  { value: "failed", label: "Failed", empty: "No request has failed." },
  { value: "done", label: "Done", empty: "No request has finished yet." },
];

export function parseRequestQueueView(value: string | null): AdminRequestQueueView | undefined {
  return REQUEST_QUEUE_VIEWS.find((view) => view.value === value)?.value;
}

/** The view the queue opens on: what needs approval, else what is under way. */
export function defaultRequestQueueView(counts: AdminRequestCounts): AdminRequestQueueView {
  return counts.needs_approval > 0 ? "needs_approval" : "in_progress";
}

/**
 * The view a request belongs to, by the server's own rule: needs approval
 * (pending), in progress (approved, queued or downloading), failed, and done
 * (completed, or closed by a decline or cancellation).
 */
export function requestQueueView(
  request: Pick<MediaRequest, "status" | "outcome">,
): AdminRequestQueueView {
  switch (request.outcome) {
    case "failed":
      return "failed";
    case "declined":
    case "cancelled":
      return "done";
  }
  if (request.status === "pending") return "needs_approval";
  if (request.status === "completed") return "done";
  return "in_progress";
}

export type RequestQueueAction = "approve" | "decline" | "retry" | "cancel";

/**
 * What an admin can do with a request, by its view and the server's rules:
 * approve or decline what needs approval; cancel what is in progress while
 * nothing has been sent for it; retry or close (cancel) what failed.
 */
export function requestQueueActions(
  request: Pick<MediaRequest, "status" | "outcome" | "targets">,
): RequestQueueAction[] {
  const withdrawable = canWithdrawRequest(request);
  switch (requestQueueView(request)) {
    case "needs_approval":
      return withdrawable ? ["approve", "decline"] : [];
    case "in_progress":
      return withdrawable ? ["cancel"] : [];
    case "failed":
      return ["retry", "cancel"];
    case "done":
      return [];
  }
}

const EVENT_LABELS: Record<string, string> = {
  created: "Requested",
  approved: "Approved",
  // The server records an admin's approval as a status change, and an
  // automatic one as `approved`.
  status_approved: "Approved",
  status_queued: "Sent to the server",
  status_downloading: "Downloading",
  status_completed: "Downloaded",
  available_in_library: "In the library",
  outcome_declined: "Declined",
  outcome_cancelled: "Cancelled",
  outcome_failed: "Failed",
  retried: "Retried",
  submit_deferred: "Couldn't send yet",
};

/** A history entry's type in plain words; a type this client doesn't know shows as it is. */
export function formatRequestEventType(type: string): string {
  return EVENT_LABELS[type] ?? type;
}

/** The account a `?user=` link names, or undefined for anything but a positive ID. */
export function parseRequestQueueUser(value: string | null): number | undefined {
  if (!value || !/^[1-9][0-9]{0,9}$/.test(value)) return undefined;
  const id = Number(value);
  // Account IDs are 4-byte integers on the server.
  return id <= 2_147_483_647 ? id : undefined;
}

export type RequestQueueMediaFilter = RequestMediaType | "all";

/** A target's status in words; its server's own status is shown beside it. */
export function formatTargetStatus(status: RequestTarget["status"]): string {
  return status === "failed" ? "Failed" : formatRequestStatus(status);
}

/** A target's server, falling back to the kind of integration. */
export function targetServerName(
  target: Pick<RequestTarget, "instance_name" | "integration_kind">,
) {
  return target.instance_name || target.integration_kind || "Unknown server";
}

/** The request's own closing reason, or the error that stopped it. */
export function requestProblemText(
  request: Pick<MediaRequest, "outcome_reason" | "last_error">,
): { text: string; error: boolean } | null {
  if (request.last_error) return { text: request.last_error, error: true };
  if (request.outcome_reason) return { text: request.outcome_reason, error: false };
  return null;
}
