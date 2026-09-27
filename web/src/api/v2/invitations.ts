import { type ProfileRequestContextSnapshot } from "@/api/client";
import {
  captureAdminAuthority,
  adminAuthorityScope,
  requireAdminAuthority,
} from "./adminAuthority";
import type { components } from "./schema";
import { v2, type V2Body } from "./request";
export type AdminInvitation = components["schemas"]["AdminInvitation"];
export type InvitationDelivery = components["schemas"]["InvitationDelivery"];
export type CreateInvitationBody = V2Body<"POST /api/v2/admin/invitations">;
export type InvitationAuthority = ProfileRequestContextSnapshot;
export type InvitationPage = {
  items: AdminInvitation[];
  page: { has_more: boolean; next_cursor?: string };
};
export const invitationScope = adminAuthorityScope;
export const captureInvitationAuthority = captureAdminAuthority;
const check = requireAdminAuthority;
export async function getAdminInvitationCapabilities(
  profileContext = captureInvitationAuthority(),
) {
  check(profileContext);
  const result = await v2("GET /api/v2/admin/invitations/capabilities", { profileContext });
  check(profileContext);
  return result;
}
export async function getAdminInvitation(
  id: string,
  profileContext = captureInvitationAuthority(),
) {
  check(profileContext);
  const result = await v2("GET /api/v2/admin/invitations/{id}", { path: { id }, profileContext });
  check(profileContext);
  return result;
}
export async function listAdminInvitationsPage(
  cursor?: string,
  profileContext = captureInvitationAuthority(),
): Promise<InvitationPage> {
  check(profileContext);
  const result = await v2("GET /api/v2/admin/invitations", {
    query: { limit: 50, cursor },
    profileContext,
  });
  check(profileContext);
  const page = result.page;
  if (
    !Array.isArray(result.items) ||
    !page ||
    typeof page.has_more !== "boolean" ||
    (page.has_more &&
      (typeof page.next_cursor !== "string" || !page.next_cursor || page.next_cursor === cursor)) ||
    (!page.has_more && !!page.next_cursor)
  )
    throw new Error("Invalid invitation pagination. Reload the list.");
  return { items: result.items, page: { has_more: page.has_more, next_cursor: page.next_cursor } };
}
function delivery(value: InvitationDelivery) {
  if (
    typeof value.invitation?.id !== "string" ||
    !value.invitation.id ||
    typeof value.claim_url !== "string" ||
    !value.claim_url ||
    !["sent", "not_configured", "failed_or_unknown"].includes(value.delivery_status)
  )
    throw new Error(
      "Invitation may have been created, but its response was incomplete. Reload history before continuing.",
    );
  return value;
}
export async function createAdminInvitation(
  body: CreateInvitationBody,
  profileContext = captureInvitationAuthority(),
) {
  check(profileContext);
  const result = await v2("POST /api/v2/admin/invitations", {
    body: {
      email: body.email,
      role: body.role,
      access_group_id: body.access_group_id,
      library_ids: body.library_ids,
      create_profile: body.create_profile,
      show_tour: body.show_tour,
      note: body.note,
    },
    profileContext,
    retryAuthentication: false,
  });
  check(profileContext);
  return delivery(result);
}
export async function resendAdminInvitation(
  id: string,
  profileContext = captureInvitationAuthority(),
) {
  check(profileContext);
  const result = await v2("POST /api/v2/admin/invitations/{id}/resend", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  check(profileContext);
  return delivery(result);
}
export async function revokeAdminInvitation(
  id: string,
  profileContext = captureInvitationAuthority(),
) {
  check(profileContext);
  await v2("DELETE /api/v2/admin/invitations/{id}", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  check(profileContext);
}
