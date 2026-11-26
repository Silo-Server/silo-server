import { captureProfileRequestContext } from "@/api/client";
import { v2, V2TransportError, type V2Result } from "./request";

/** An app code as a server's page hands it to the Silo app. */
export type AppCode = V2Result<"POST /api/v2/auth/app-codes">;

declare const codeKind: unique symbol;

declare const linkable: unique symbol;

/** A join code, from an invitation. It goes in a link only once narrowed by linkableJoinCode. */
export type JoinAppCode = AppCode & { readonly [codeKind]: "join" };

/**
 * A join code whose answer allowed a link (link_allowed): redeeming it creates
 * a new account with no administrative power. The only kind buildJoinLink
 * takes. An app that intercepts the link gets a new, empty account for an
 * ordinary invitation, which the invited person notices at once.
 */
export type LinkableJoinAppCode = JoinAppCode & { readonly [linkable]: true };

/** The code narrowed to one that may go in a link, or null when the answer said it may not. */
export function linkableJoinCode(code: JoinAppCode): LinkableJoinAppCode | null {
  return code.link_allowed === true ? (code as LinkableJoinAppCode) : null;
}

/**
 * A code for the signed-in account ("Open in Silo"). It reaches an existing
 * account, so it is shown to type and never put in a link: a link can be
 * received by an app other than Silo, which would redeem it first.
 */
export type SessionAppCode = AppCode & { readonly [codeKind]: "session" };

/** An app code answer as it may arrive: an older server leaves out link_allowed. */
interface AppCodeAnswer {
  code: string;
  expires_in: number;
  server_address: string;
  link_allowed?: unknown;
}

/**
 * The answer, checked. An answer without link_allowed (a server from before
 * the member, as during a deploy that runs two versions) means no link: the
 * code is shown to type. A member of any other type is refused.
 */
function checked(operation: string, result: AppCodeAnswer | undefined): AppCode {
  if (
    !result ||
    typeof result.code !== "string" ||
    !/^[A-Za-z0-9_-]{43}$/.test(result.code) ||
    !Number.isSafeInteger(result.expires_in) ||
    result.expires_in <= 0 ||
    typeof result.server_address !== "string" ||
    (result.link_allowed !== undefined && typeof result.link_allowed !== "boolean") ||
    !/^https?:\/\//.test(result.server_address)
  ) {
    throw new V2TransportError(operation, 200, "Invalid app code response");
  }
  // Checked above; the contract mark is the generated type's, which this
  // module adds to what it validated.
  return { ...result, link_allowed: result.link_allowed === true } as AppCode;
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

/** What a redeemed sign-in link hands its holder. */
export interface SignInLinkRedeemed {
  /** The app code, absent while the server has app key sign-in off. */
  appCode: SessionAppCode | null;
  /** The token that sets a password once, absent when the account may not set one. */
  setPasswordToken: string | null;
}

/**
 * Trades a sign-in link's token, once. Its app code signs the Silo app in to
 * an existing account, so it is a session code: shown to type, never put in a
 * link, whatever link_allowed says.
 */
export async function redeemSignInLink(token: string): Promise<SignInLinkRedeemed> {
  const result = await v2("POST /api/v2/auth/sign-in-links/redeem", {
    body: { token },
    retryAuthentication: false,
  });
  const setPasswordToken =
    typeof result?.set_password_token === "string" && result.set_password_token !== ""
      ? result.set_password_token
      : null;
  if (result?.code === undefined) {
    if (!setPasswordToken) {
      throw new V2TransportError("redeemSignInLink", 200, "Invalid sign-in link response");
    }
    return { appCode: null, setPasswordToken };
  }
  const code = checked("redeemSignInLink", {
    code: result.code,
    expires_in: result.expires_in ?? 0,
    server_address: result.server_address ?? "",
    link_allowed: result.link_allowed,
  });
  return { appCode: { ...code, link_allowed: false } as SessionAppCode, setPasswordToken };
}

/** Sets the account's password with a redeemed sign-in link's token. */
export async function setSignInLinkPassword(token: string, newPassword: string): Promise<void> {
  await v2("POST /api/v2/auth/sign-in-links/set-password", {
    body: { token, new_password: newPassword },
    retryAuthentication: false,
  });
}
