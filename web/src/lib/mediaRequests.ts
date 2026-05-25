import type {
  CreateMediaRequestInput,
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestMediaResult,
  RequestMediaType,
} from "@/api/types";

export const REQUEST_STATUSES: Array<MediaRequestStatus | "all"> = [
  "all",
  "pending",
  "approved",
  "queued",
  "downloading",
  "completed",
];

export const REQUEST_OUTCOMES: Array<MediaRequestOutcome | "all"> = [
  "all",
  "active",
  "declined",
  "cancelled",
  "failed",
];

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function formatMediaType(mediaType: RequestMediaType): string {
  return mediaType === "series" ? "Series" : "Movie";
}

export function formatRequestStatus(status?: MediaRequestStatus): string {
  switch (status) {
    case "pending":
      return "Pending";
    case "approved":
      return "Approved";
    case "queued":
      return "Queued";
    case "downloading":
      return "Downloading";
    case "completed":
      return "Completed";
    default:
      return "Requested";
  }
}

export function requestStatusBadgeVariant(status?: MediaRequestStatus): BadgeVariant {
  switch (status) {
    case "completed":
      return "default";
    case "pending":
      return "outline";
    default:
      return "secondary";
  }
}

export function formatRequestOutcome(outcome?: MediaRequestOutcome): string {
  switch (outcome) {
    case "active":
      return "Active";
    case "declined":
      return "Declined";
    case "cancelled":
      return "Cancelled";
    case "failed":
      return "Failed";
    default:
      return "Active";
  }
}

export function requestOutcomeBadgeVariant(outcome?: MediaRequestOutcome): BadgeVariant {
  switch (outcome) {
    case "failed":
    case "declined":
    case "cancelled":
      return "destructive";
    case "active":
      return "secondary";
    default:
      return "outline";
  }
}

export function formatRequestReason(reason?: string): string {
  switch (reason) {
    case "already_requested":
      return "Already requested";
    case "already_available":
      return "Available";
    case "requests_disabled":
      return "Requests disabled";
    case "blocked":
      return "Blocked";
    case "quota_exceeded":
      return "Limit reached";
    default:
      return "Unavailable";
  }
}

export function tmdbImageURL(path?: string, size = "w342"): string | null {
  if (!path) return null;
  return `https://image.tmdb.org/t/p/${size}${path}`;
}

export function requestInputFromMediaResult(item: RequestMediaResult): CreateMediaRequestInput {
  return {
    media_type: item.media_type,
    tmdb_id: item.tmdb_id,
    title: item.title,
    year: item.year || undefined,
    overview: item.overview || undefined,
    poster_path: item.poster_path || undefined,
    backdrop_path: item.backdrop_path || undefined,
  };
}

export function formatRequestDate(request: Pick<MediaRequest, "created_at">): string {
  return new Date(request.created_at).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}
