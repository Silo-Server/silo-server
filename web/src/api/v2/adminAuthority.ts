import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";

export type AdminAuthority = ProfileRequestContextSnapshot;

export function captureAdminAuthority(): AdminAuthority {
  const context = captureProfileRequestContext();
  if (!context) throw new StaleApiRequestContextError();
  return context;
}

export function adminAuthorityScope(context = captureProfileRequestContext()) {
  return context
    ? `${context.serverOrigin}:${context.authContextVersion}:${context.profileId}`
    : "unavailable";
}

export function requireAdminAuthority(context: AdminAuthority) {
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
}
