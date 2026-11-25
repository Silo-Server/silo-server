import { captureProfileRequestContext } from "@/api/client";
import { v2, V2TransportError, type V2Result } from "./request";

/** An app code as a server's page hands it to the Silo app. */
export type AppCode = V2Result<"POST /api/v2/auth/app-codes">;

declare const codeKind: unique symbol;

/**
 * A join code, from an invitation: the only kind that may travel in a link
 * (buildJoinLink takes nothing else). An app that intercepts the link gets a
 * new, empty account and the invited person notices at once.
 */
export type JoinAppCode = AppCode & { readonly [codeKind]: "join" };

/**
 * A code for the signed-in account ("Open in Silo"). It reaches an existing
 * account, so it is shown to type and never put in a link: a link can be
 * received by an app other than Silo, which would redeem it first.
 */
export type SessionAppCode = AppCode & { readonly [codeKind]: "session" };

function checked(operation: string, result: AppCode | undefined): AppCode {
  if (
    !result ||
    typeof result.code !== "string" ||
    !/^[A-Za-z0-9_-]{43}$/.test(result.code) ||
    !Number.isSafeInteger(result.expires_in) ||
    result.expires_in <= 0 ||
    typeof result.server_address !== "string" ||
    !/^https?:\/\//.test(result.server_address)
  ) {
    throw new V2TransportError(operation, 200, "Invalid app code response");
  }
  return result;
}

/**
 * Starts a join through the Silo app. `username` and `email` are what the
 * person chose for an invitation with no address; omit them otherwise.
 */
export async function createInvitationAppCode(
  token: string,
  signal: AbortSignal,
  choice?: { username: string; email: string },
): Promise<JoinAppCode> {
  const body: { token: string; username?: string; email?: string } = { token };
  if (choice) {
    body.username = choice.username;
    if (choice.email) body.email = choice.email;
  }
  const result = await v2("POST /api/v2/invitations/app-code", {
    body,
    signal,
    retryAuthentication: false,
  });
  return checked("createInvitationAppCode", result) as JoinAppCode;
}

/** An app code for the signed-in account ("Open in Silo"). */
export async function createSessionAppCode(): Promise<SessionAppCode> {
  // Bound to the profile active when the person asked: only the household's
  // main profile may make one.
  const profileContext = captureProfileRequestContext();
  const result = profileContext
    ? await v2("POST /api/v2/auth/app-codes", { body: {}, profileContext })
    : await v2("POST /api/v2/auth/app-codes", { body: {} });
  return checked("createAppCode", result) as SessionAppCode;
}
