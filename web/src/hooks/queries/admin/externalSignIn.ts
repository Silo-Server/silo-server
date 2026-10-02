// Admin side of external sign-in (OIDC and LDAP): the Sign-in settings page
// and the Sign-in tab of an account. The rules are in
// docs/architecture/external-sign-in.md; the operations in docs/auth-api.md.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import {
  adminUserScope,
  captureAdminUserAuthority,
  getAdminUser,
  requireAdminUserAuthority,
  updateAdminUser,
} from "@/api/v2/adminUsers";
import { v2, type V2Body, type V2Result } from "@/api/v2/request";

import { adminKeys } from "../keys";
import { refreshAuthProviders } from "../authProviders";

const ADMIN_STALE_TIME = 30_000;

export type ExternalSignInCapabilities = V2Result<"GET /api/v2/auth/external-sign-in/capabilities">;
export type AuthConnectionTestResult =
  V2Result<"POST /api/v2/admin/plugins/installations/{id}/auth-binding/test">;
export type AuthConnectionTestStaged = NonNullable<
  V2Body<"POST /api/v2/admin/plugins/installations/{id}/auth-binding/test">["config"]
>[number];
export type AdminUserIdentity =
  V2Result<"GET /api/v2/admin/users/{id}/identities">["items"][number];
export type AdminUserIdentityInput = V2Body<"POST /api/v2/admin/users/{id}/identities">;
export type SignInBindingWrite =
  V2Body<"PUT /api/v2/admin/plugins/installations/{id}/auth-binding">;

export const externalSignInKeys = {
  capabilities: () => ["admin", "external-sign-in", "capabilities"] as const,
  // Under the admin users prefix, so every account write (a password turns
  // local sign-in back on) refreshes the identities beside it.
  identities: (scope: string, userId: number) =>
    [...adminKeys.users(), scope, "identities", userId] as const,
};

/** What this server supports of external sign-in; gates the admin controls. */
export function useExternalSignInCapabilities() {
  return useQuery({
    queryKey: externalSignInKeys.capabilities(),
    queryFn: () => v2("GET /api/v2/auth/external-sign-in/capabilities"),
    staleTime: ADMIN_STALE_TIME,
    retry: false,
  });
}

function invalidateSignInProviders(queryClient: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: adminKeys.pluginInstallations() }),
    refreshAuthProviders(queryClient),
  ]);
}

function requireActiveAuthority(profileContext: ProfileRequestContextSnapshot) {
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
}

function captureAuthority(): ProfileRequestContextSnapshot {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return profileContext;
}

/**
 * Every sign-in binding write. The binding is written as a whole row, so the
 * Sign-in page lets only one run at a time.
 */
export const signInBindingMutationKey = ["admin", "external-sign-in", "binding"] as const;

/**
 * Writes an installation's sign-in binding (enabled, auto-create accounts).
 * Changes apply at once on every node; the page reports the result itself,
 * so this hook shows no toast.
 */
export function useUpdateSignInBinding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: signInBindingMutationKey,
    retry: false,
    mutationFn: async (input: { installationId: number; body: SignInBindingWrite }) => {
      const profileContext = captureAuthority();
      await v2("PUT /api/v2/admin/plugins/installations/{id}/auth-binding", {
        path: { id: String(input.installationId) },
        body: input.body,
        profileContext,
        retryAuthentication: false,
      });
      requireActiveAuthority(profileContext);
    },
    onSettled: () => invalidateSignInProviders(queryClient),
  });
}

/**
 * Saves one plugin configuration entry from the Sign-in page. Like the
 * plugin page's save, but the page shows the outcome inline.
 */
export function useSaveSignInPluginConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (input: {
      installationId: number;
      key: string;
      value: Record<string, unknown>;
      clearSecrets: string[];
    }) => {
      const profileContext = captureAuthority();
      await v2("PUT /api/v2/admin/plugins/installations/{id}/config", {
        path: { id: String(input.installationId) },
        body: { key: input.key, value: input.value, clear_secrets: input.clearSecrets },
        profileContext,
        retryAuthentication: false,
      });
      requireActiveAuthority(profileContext);
    },
    onSettled: () => invalidateSignInProviders(queryClient),
  });
}

/**
 * Runs the plugin's connection test on the saved configuration, or on staged
 * entries laid over it. Nothing is saved, and a failed check is a result
 * (ok false), not an error.
 */
export function useTestSignInConnection() {
  return useMutation({
    retry: false,
    mutationFn: async (input: {
      installationId: number;
      capabilityId: string;
      config?: AuthConnectionTestStaged[];
    }): Promise<AuthConnectionTestResult> => {
      const profileContext = captureAuthority();
      const result = await v2("POST /api/v2/admin/plugins/installations/{id}/auth-binding/test", {
        path: { id: String(input.installationId) },
        body: { capability_id: input.capabilityId, config: input.config ?? [] },
        profileContext,
        retryAuthentication: false,
      });
      requireActiveAuthority(profileContext);
      return result;
    },
  });
}

/** The external sign-in identities linked to one account. */
export function useAdminUserIdentities(userId: number, enabled = true) {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: externalSignInKeys.identities(adminUserScope(profileContext), userId),
    queryFn: async () => {
      const authority = profileContext ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const result = await v2("GET /api/v2/admin/users/{id}/identities", {
        path: { id: String(userId) },
        profileContext: authority,
      });
      requireAdminUserAuthority(authority);
      return result.items;
    },
    enabled: enabled && profileContext !== null && Number.isSafeInteger(userId) && userId > 0,
    staleTime: ADMIN_STALE_TIME,
    retry: false,
  });
}

function invalidateAccount(queryClient: ReturnType<typeof useQueryClient>) {
  // Linking and unlinking change password_login; the identities list lives
  // under the same prefix.
  return queryClient.invalidateQueries({ queryKey: adminKeys.users() });
}

/** Links an account to an identity by the provider's exact subject. */
export function useLinkAdminUserIdentity() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (input: { userId: number; body: AdminUserIdentityInput }) => {
      const authority = captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const result = await v2("POST /api/v2/admin/users/{id}/identities", {
        path: { id: String(input.userId) },
        body: input.body,
        profileContext: authority,
        retryAuthentication: false,
      });
      requireAdminUserAuthority(authority);
      return result;
    },
    onSettled: () => invalidateAccount(queryClient),
  });
}

/** Unlinks one identity from an account. */
export function useUnlinkAdminUserIdentity() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (input: { userId: number; identityId: string }) => {
      const authority = captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      await v2("DELETE /api/v2/admin/users/{id}/identities/{identity_id}", {
        path: { id: String(input.userId), identity_id: input.identityId },
        profileContext: authority,
        retryAuthentication: false,
      });
      requireAdminUserAuthority(authority);
    },
    onSettled: () => invalidateAccount(queryClient),
  });
}

/**
 * Writes one sign-in field of an account (break-glass, or a new password),
 * against a fresh read so an older copy of the account cannot trip the
 * If-Match guard.
 */
export function useUpdateAdminUserSignIn() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (input: {
      userId: number;
      body: { break_glass?: boolean; password?: string; require_password_change?: boolean };
    }) => {
      const editor = await getAdminUser(input.userId, captureAdminUserAuthority());
      await updateAdminUser(editor, input.body);
    },
    onSettled: () => invalidateAccount(queryClient),
  });
}
