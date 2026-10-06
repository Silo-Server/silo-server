import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  captureProfileRequestContext,
  captureSessionIdentity,
  isSessionIdentityCurrent,
  StaleApiRequestContextError,
  type SessionIdentitySnapshot,
} from "@/api/client";
import { v2, type V2Body, type V2Result } from "@/api/v2/request";

/** Whether and within which limits the caller may replace the account password. */
export type AccountPasswordCapability = V2Result<"GET /api/v2/account/password/capability">;

export const accountKeys = {
  passwordCapability: () => ["account", "password-capability"] as const,
};

export function useAccountPasswordCapability() {
  return useQuery({
    queryKey: accountKeys.passwordCapability(),
    queryFn: () => v2("GET /api/v2/account/password/capability"),
  });
}

export function useChangeAccountPassword() {
  return useMutation({
    mutationFn: (body: V2Body<"POST /api/v2/account/password">) => {
      // Bind the write to the profile that was active when the user submitted:
      // a household profile switch while it is in flight must not re-author it.
      const profileContext = captureProfileRequestContext();
      return profileContext
        ? v2("POST /api/v2/account/password", { body, profileContext })
        : v2("POST /api/v2/account/password", { body });
    },
  });
}

/** An external sign-in identity linked to the caller's account. */
export type AccountIdentity = V2Result<"GET /api/v2/account/identities">["items"][number];

export const accountIdentityKeys = {
  all: () => ["account", "identities"] as const,
};

export function useAccountIdentities(enabled = true) {
  return useQuery({
    queryKey: accountIdentityKeys.all(),
    queryFn: () => v2("GET /api/v2/account/identities"),
    enabled,
  });
}

function invalidateSignInState(queryClient: ReturnType<typeof useQueryClient>) {
  // Linking turns local password sign-in off (except for break-glass
  // accounts), so the password form's capability changes with it.
  void queryClient.invalidateQueries({ queryKey: accountIdentityKeys.all() });
  void queryClient.invalidateQueries({ queryKey: accountKeys.passwordCapability() });
}

function requireIdentityMutationSession(session: SessionIdentitySnapshot) {
  if (!isSessionIdentityCurrent(session)) throw new StaleApiRequestContextError();
}

export function useUnlinkAccountIdentity() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      const session = captureSessionIdentity();
      const result = await v2("DELETE /api/v2/account/identities/{id}", {
        path: { id },
        profileContext: captureProfileRequestContext() ?? undefined,
        retryAuthentication: false,
      });
      requireIdentityMutationSession(session);
      return result;
    },
    onSettled: () => invalidateSignInState(queryClient),
  });
}

export function useLinkAccountIdentityWithCredentials() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (body: V2Body<"POST /api/v2/account/identities/link-credentials">) => {
      const session = captureSessionIdentity();
      const result = await v2("POST /api/v2/account/identities/link-credentials", {
        body,
        profileContext: captureProfileRequestContext() ?? undefined,
        retryAuthentication: false,
      });
      requireIdentityMutationSession(session);
      return result;
    },
    onSuccess: () => invalidateSignInState(queryClient),
  });
}

/**
 * Links the network identity of this device (such as its Tailscale login)
 * after the local password is confirmed. Works only while the browser reached
 * the server through that provider (network_identity_required otherwise).
 */
export function useLinkAccountIdentityWithNetwork() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (body: V2Body<"POST /api/v2/account/identities/link-network">) => {
      const session = captureSessionIdentity();
      const result = await v2("POST /api/v2/account/identities/link-network", {
        body,
        profileContext: captureProfileRequestContext() ?? undefined,
        retryAuthentication: false,
      });
      requireIdentityMutationSession(session);
      return result;
    },
    onSuccess: () => invalidateSignInState(queryClient),
  });
}

/**
 * Starts linking an OAuth provider in this browser: confirms the local
 * password for a link ticket, then trades the ticket for the provider URL
 * (and the flow's browser-binding cookie). The flow comes back to next with
 * linked=1, or error=oauth_link_failed&reason=<reason>.
 */
export function useStartAccountIdentityLink() {
  return useMutation({
    retry: false,
    mutationFn: async (input: { installationId: string; password: string; next: string }) => {
      const session = captureSessionIdentity();
      const profileContext = captureProfileRequestContext() ?? undefined;
      const ticket = await v2("POST /api/v2/account/identities/link-ticket", {
        body: { installation_id: input.installationId, password: input.password },
        profileContext,
        retryAuthentication: false,
      });
      requireIdentityMutationSession(session);
      let result;
      try {
        result = await v2("POST /api/v2/account/identities/link-start", {
          body: { link_ticket: ticket.ticket, next: input.next },
          profileContext,
          retryAuthentication: false,
        });
      } catch (error) {
        requireIdentityMutationSession(session);
        throw new LinkStartError(error);
      }
      requireIdentityMutationSession(session);
      return result;
    },
  });
}

/**
 * A failure of the second step (link-start). Its problems read differently
 * from the ticket's: there a 409 means the page is not on the public URL.
 */
export class LinkStartError extends Error {
  readonly original: unknown;
  constructor(original: unknown) {
    super(original instanceof Error ? original.message : "Couldn't start connecting the provider.");
    this.name = "LinkStartError";
    this.original = original;
  }
}
