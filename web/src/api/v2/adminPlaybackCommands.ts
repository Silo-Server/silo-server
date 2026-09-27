import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { randomUUID } from "@/lib/uuid";
import { v2, V2ProblemError, type V2Result } from "./request";

/**
 * Sequenced administrator playback commands (pause, resume, stop, message).
 *
 * Every command carries an ordered identity the server applies once per
 * session: a client-allocated `command_id` and a `sequence` that must rise
 * within the session. A retry preserves the exact identity and body, so the
 * server replays the recorded receipt instead of dispatching again, and a
 * delayed retry that lands after a newer command is refused as stale rather
 * than reverting the newer playback state. Sequences are allocated per session
 * from a monotonic clock so two commands issued from this browser never share
 * one, even across reloads within the same millisecond boundary.
 *
 * Several administrators may control one session from different browsers, and
 * they share the server's ledger but no counter: a browser whose clock runs
 * behind another's allocates below the latest applied sequence and is refused
 * as stale. The refusal carries that sequence (`X-Silo-Latest-Sequence`), and
 * `sendAdminPlaybackCommand` records it so the next allocation for the session
 * lands above it; one refused click, then the UI works again.
 */

export type AdminPlaybackCommandAction = "pause" | "resume" | "stop" | "message";

export type AdminPlaybackCommandReceipt =
  V2Result<"POST /api/v2/admin/sessions/{session_id}/pause">;

export type AdminPlaybackCommandIdentity = {
  command_id: string;
  sequence: number;
};

/** Terminate is a separate row with a pending decision; it is deliberately absent. */
const sequencedActions: ReadonlySet<AdminPlaybackCommandAction> = new Set([
  "pause",
  "resume",
  "stop",
  "message",
]);

const lastSequenceBySession = new Map<string, number>();

/**
 * The largest sequence the contract accepts (2^53-1): every client, including
 * this one working in JavaScript numbers, can represent the latest applied
 * sequence exactly and allocate above it. Reaching it would take a clock
 * hundreds of thousands of years ahead, so allocation simply refuses there.
 */
export const MAX_ADMIN_PLAYBACK_SEQUENCE = Number.MAX_SAFE_INTEGER;

/** The response header a stale refusal carries with the session's latest applied sequence. */
export const LATEST_SEQUENCE_HEADER = "X-Silo-Latest-Sequence";

/** Allocate one ordered command identity for a session. Call once per intended command. */
export function allocateAdminPlaybackCommand(sessionId: string): AdminPlaybackCommandIdentity {
  const previous = lastSequenceBySession.get(sessionId) ?? 0;
  const sequence = Math.max(previous + 1, Date.now());
  if (sequence > MAX_ADMIN_PLAYBACK_SEQUENCE) {
    throw new Error("The session's command sequence is exhausted.");
  }
  lastSequenceBySession.set(sessionId, sequence);
  return { command_id: randomUUID(), sequence };
}

/**
 * Raise the session's allocation floor to a sequence the server reported as
 * already applied, so the next `allocateAdminPlaybackCommand` is above it.
 */
export function observeAdminPlaybackSequence(sessionId: string, latest: number): void {
  if (!Number.isSafeInteger(latest) || latest < 1 || latest > MAX_ADMIN_PLAYBACK_SEQUENCE) return;
  const previous = lastSequenceBySession.get(sessionId) ?? 0;
  if (latest > previous) lastSequenceBySession.set(sessionId, latest);
}

/** The latest applied sequence a stale refusal reported, or null for any other error. */
export function latestSequenceOf(error: unknown): number | null {
  if (!(error instanceof V2ProblemError)) return null;
  const raw = error.headers.get(LATEST_SEQUENCE_HEADER);
  if (raw === null) return null;
  const latest = Number(raw.trim());
  return Number.isSafeInteger(latest) && latest > 0 && latest <= MAX_ADMIN_PLAYBACK_SEQUENCE
    ? latest
    : null;
}

export function captureAdminPlaybackCommandAuthority() {
  const context = captureProfileRequestContext();
  if (!context) throw new StaleApiRequestContextError();
  return context;
}

function requireAuthority(context: ProfileRequestContextSnapshot) {
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
}

export type AdminPlaybackCommandRequest = {
  sessionId: string;
  action: AdminPlaybackCommandAction;
  identity: AdminPlaybackCommandIdentity;
  reason?: string;
  deadlineMs?: number;
  /** Required for `message`; ignored for the other actions. */
  title?: string;
  message?: string;
};

/**
 * Send one sequenced command under the captured administrator authority. The
 * request is never retried automatically after an uncertain response; a caller
 * that retries must pass the same identity so the server replays the receipt.
 */
export async function sendAdminPlaybackCommand(
  request: AdminPlaybackCommandRequest,
  profileContext = captureAdminPlaybackCommandAuthority(),
): Promise<AdminPlaybackCommandReceipt> {
  if (!sequencedActions.has(request.action)) {
    throw new Error(`Unsupported session command: ${request.action}`);
  }
  if (
    !Number.isSafeInteger(request.identity.sequence) ||
    request.identity.sequence < 1 ||
    request.identity.sequence > MAX_ADMIN_PLAYBACK_SEQUENCE
  ) {
    throw new Error("A command sequence must be a positive integer within the contract bound.");
  }
  requireAuthority(profileContext);
  const identity = {
    command_id: request.identity.command_id,
    sequence: request.identity.sequence,
    ...(request.reason ? { reason: request.reason } : {}),
    ...(request.deadlineMs ? { deadline_ms: request.deadlineMs } : {}),
  };
  const path = { session_id: request.sessionId };
  const options = { path, profileContext, retryAuthentication: false } as const;
  let receipt: AdminPlaybackCommandReceipt;
  try {
    switch (request.action) {
      case "pause":
        receipt = await v2("POST /api/v2/admin/sessions/{session_id}/pause", {
          ...options,
          body: identity,
        });
        break;
      case "resume":
        receipt = await v2("POST /api/v2/admin/sessions/{session_id}/resume", {
          ...options,
          body: identity,
        });
        break;
      case "stop":
        receipt = await v2("POST /api/v2/admin/sessions/{session_id}/stop", {
          ...options,
          body: identity,
        });
        break;
      case "message": {
        const message = request.message?.trim() ?? "";
        if (!message) throw new Error("Message is required");
        receipt = await v2("POST /api/v2/admin/sessions/{session_id}/message", {
          ...options,
          body: { ...identity, message, ...(request.title ? { title: request.title } : {}) },
        });
        break;
      }
    }
  } catch (error) {
    // A stale refusal names the sequence that won; raise this browser's
    // floor so the next click allocates above it instead of failing again.
    const latest = latestSequenceOf(error);
    if (latest !== null) observeAdminPlaybackSequence(request.sessionId, latest);
    throw error;
  }
  requireAuthority(profileContext);
  if (receipt.command_id !== request.identity.command_id) {
    throw new Error("The server answered a different command identity.");
  }
  return receipt;
}

export async function getAdminPlaybackCommandCapabilities(
  profileContext = captureAdminPlaybackCommandAuthority(),
) {
  requireAuthority(profileContext);
  const capabilities = await v2("GET /api/v2/admin/sessions/command-capabilities", {
    profileContext,
  });
  requireAuthority(profileContext);
  return capabilities;
}

export type AdminPlaybackTerminateReceipt =
  V2Result<"POST /api/v2/admin/sessions/{session_id}/terminate">;

/**
 * Terminate revokes the session's playback authority durably first, then
 * notifies the client as best effort. The receipt reports both facts; a
 * repeat converges without dispatching again, so the request may be retried
 * after an uncertain response.
 */
export async function terminateAdminPlaybackSession(
  sessionId: string,
  reason?: string,
  profileContext = captureAdminPlaybackCommandAuthority(),
): Promise<AdminPlaybackTerminateReceipt> {
  requireAuthority(profileContext);
  const receipt = await v2("POST /api/v2/admin/sessions/{session_id}/terminate", {
    path: { session_id: sessionId },
    body: reason ? { reason } : {},
    profileContext,
  });
  requireAuthority(profileContext);
  if (receipt.session_id !== sessionId) {
    throw new Error("The server answered for a different playback session.");
  }
  return receipt;
}
