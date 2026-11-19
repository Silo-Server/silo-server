import { type QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { t, type MessageKey } from "@/i18n";

import { captureProfileRequestContext } from "@/api/client";
import type { Profile } from "@/api/types";
import {
  adminUserScope,
  captureAdminUserAuthority,
  requireAdminUserAuthority,
} from "@/api/v2/adminUsers";
import { v2 } from "@/api/v2/request";
import { adminKeys, profileKeys } from "@/hooks/queries/keys";
import { profileFromV2, type ProfileCreate, type ProfileUpdate } from "@/hooks/queries/profiles";

import { adminUsersKey } from "./users";

// An administrator's reads and writes of one account's profiles, through
// /api/v2/admin/users/{id}/profiles. The bodies are the profile's own
// createProfile and updateProfile bodies, so the profile editor builds them.
// "pin": null on an update removes the profile's PIN.

function adminProfileKey(userId: number, profileId: string, context = adminUserScope()) {
  return [...adminUsersKey(context), "profile", userId, profileId] as const;
}

/** One profile of the account, in full, for the editor. */
export function useAdminUserProfile(userId: number, profileId: string | null) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: adminProfileKey(userId, profileId ?? "", adminUserScope(context)),
    queryFn: async (): Promise<Profile> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const profile = await v2("GET /api/v2/admin/users/{id}/profiles/{profile_id}", {
        path: { id: String(userId), profile_id: profileId ?? "" },
        profileContext: authority,
      });
      return profileFromV2(profile);
    },
    enabled: context !== null && Boolean(profileId),
    retry: false,
    // The editor seeds its form once from this read, so a closed editor
    // drops it rather than reopening on a cached, possibly stale, profile.
    gcTime: 0,
  });
}

// Every view of the account's profiles refetches after a write: the profile
// lists on the account page, the edited profile, and the admin's own household
// list, since the account may be the admin's own.
function invalidateAccountProfiles(
  client: QueryClient,
  userId: number,
  context: ReturnType<typeof captureProfileRequestContext>,
) {
  const base = adminUsersKey(adminUserScope(context));
  return Promise.all([
    client.invalidateQueries({ queryKey: [...base, "profile-activity", userId] }),
    client.invalidateQueries({ queryKey: [...base, "profile", userId] }),
    client.invalidateQueries({ queryKey: adminKeys.userProfiles(userId) }),
    client.invalidateQueries({ queryKey: profileKeys.list() }),
  ]);
}

function failed(fallback: MessageKey) {
  return (err: unknown) =>
    toast.error(err instanceof Error && err.message ? err.message : t(fallback));
}

export function useCreateAdminUserProfile(userId: number) {
  const client = useQueryClient();
  // Captured without throwing: a render with no profile context (signing out)
  // must not crash the page; the mutation refuses instead.
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async (body: ProfileCreate): Promise<Profile> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const profile = await v2("POST /api/v2/admin/users/{id}/profiles", {
        path: { id: String(userId) },
        body,
        profileContext: authority,
      });
      return profileFromV2(profile);
    },
    onSuccess: () => toast.success(t("web.admin.common.userProfiles.created")),
    onError: failed("web.admin.common.userProfiles.createFailed"),
    onSettled: () => invalidateAccountProfiles(client, userId, context),
  });
}

export function useUpdateAdminUserProfile(userId: number) {
  const client = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async ({ id, body }: { id: string; body: ProfileUpdate }): Promise<Profile> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const profile = await v2("PATCH /api/v2/admin/users/{id}/profiles/{profile_id}", {
        path: { id: String(userId), profile_id: id },
        body,
        profileContext: authority,
      });
      return profileFromV2(profile);
    },
    onError: failed("web.admin.common.userProfiles.saveFailed"),
    onSettled: () => invalidateAccountProfiles(client, userId, context),
  });
}

export function useDeleteAdminUserProfile(userId: number) {
  const client = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async (profileId: string) => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      await v2("DELETE /api/v2/admin/users/{id}/profiles/{profile_id}", {
        path: { id: String(userId), profile_id: profileId },
        profileContext: authority,
      });
    },
    onSuccess: () => toast.success(t("web.admin.common.userProfiles.deleted")),
    onError: failed("web.admin.common.userProfiles.deleteFailed"),
    onSettled: () => invalidateAccountProfiles(client, userId, context),
  });
}
