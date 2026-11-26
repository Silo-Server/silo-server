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
import type { components } from "@/api/v2/schema";
import { V2ProblemError, v2, v2ErrorText } from "@/api/v2/request";
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
    // An edit or delete of a profile from the person's app adds or ends a
    // record the "Changed on this server" card lists.
    client.invalidateQueries({
      queryKey: adminProfileSetEditsKey(userId, adminUserScope(context)),
    }),
    client.invalidateQueries({ queryKey: adminKeys.userProfiles(userId) }),
    client.invalidateQueries({ queryKey: profileKeys.list() }),
  ]);
}

function failed(fallback: MessageKey) {
  return (err: unknown) => toast.error(v2ErrorText(err, t(fallback)));
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
    // Sent even offline, so it fails at once. The body carries every field in
    // the editor and the PATCH has no revision check, so an edit held until
    // reconnect would replace whatever the household changed in the meantime,
    // parental limits included. The authority check only confirms the account
    // and server are unchanged.
    networkMode: "always",
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
    // Sent even offline, so it fails at once. The confirm dialog closes on
    // click, so a delete held until reconnect would remove the profile and its
    // watch history later, with nothing on screen saying one is pending.
    networkMode: "always",
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

export type AdminProfileSetEdit = components["schemas"]["AdminProfileSetEdit"];
export type AdminProfileSetCleared = components["schemas"]["AdminProfileSetCleared"];

function adminProfileSetEditsKey(userId: number, context = adminUserScope()) {
  return [...adminUsersKey(context), "profile-set-edits", userId] as const;
}

/**
 * The edits made on this server to the account's profiles from its app: the
 * administrator's overrides and hidden profiles, the person's own edits their
 * app has not merged, and settled records of the person's edits over the
 * administrator's. Only read while the account capability says it is served.
 */
export function useAdminUserProfileSetEdits(userId: number, enabled: boolean) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: adminProfileSetEditsKey(userId, adminUserScope(context)),
    queryFn: async (): Promise<AdminProfileSetEdit[]> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const out = await v2("GET /api/v2/admin/users/{id}/profile-set/edits", {
        path: { id: String(userId) },
        profileContext: authority,
      });
      return out.edits;
    },
    enabled: enabled && context !== null,
    retry: false,
  });
}

/**
 * Why the server refused a clear or dismissal that the card explains in
 * words instead of reporting a failure: the record was already gone (cleared
 * by someone else, or the person changed the field again), putting the
 * set's name back would give two profiles one name, or the override changed
 * since the owner was shown it (its etag no longer matches).
 */
export type ProfileSetEditRefusal = "gone" | "nameTaken" | "stale";

export function profileSetEditRefusal(err: unknown): ProfileSetEditRefusal | null {
  if (!(err instanceof V2ProblemError)) return null;
  if (err.status === 404) return "gone";
  if (err.status === 409) return "nameTaken";
  if (err.status === 412) return "stale";
  return null;
}

function failedUnlessExplained(fallback: MessageKey) {
  const report = failed(fallback);
  return (err: unknown) => {
    if (profileSetEditRefusal(err) === null) report(err);
  };
}

/** What to clear: an override of one field, or a hidden profile. */
export type ProfileSetClearTarget =
  | { kind: "override"; profileId: string; field: string; etag: string }
  | { kind: "hidden"; syncId: string };

export function useClearAdminUserProfileSetEdit(userId: number) {
  const client = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    // Sent even offline, so it fails at once rather than clearing later
    // with nothing on screen saying one is pending.
    networkMode: "always",
    retry: false,
    mutationFn: async (target: ProfileSetClearTarget): Promise<AdminProfileSetCleared> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      if (target.kind === "hidden") {
        return v2("DELETE /api/v2/admin/users/{id}/profile-set/hidden/{sync_id}", {
          path: { id: String(userId), sync_id: target.syncId },
          profileContext: authority,
        });
      }
      // The etag of the override the owner was shown: the server refuses
      // the clear (412) if what it would do has changed since.
      return v2("PATCH /api/v2/admin/users/{id}/profiles/{profile_id}/overrides/{field}", {
        path: { id: String(userId), profile_id: target.profileId, field: target.field },
        body: { state: "cleared" },
        headers: { "If-Match": target.etag },
        profileContext: authority,
      });
    },
    onError: failedUnlessExplained("web.admin.users.detail.profiles.setEdits.clearFailed"),
    onSettled: () => invalidateAccountProfiles(client, userId, context),
  });
}

export function useDismissAdminUserProfileSetEdit(userId: number) {
  const client = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    networkMode: "always",
    retry: false,
    mutationFn: async ({ profileId, field }: { profileId: string; field: string }) => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      await v2("DELETE /api/v2/admin/users/{id}/profiles/{profile_id}/settled-edits/{field}", {
        path: { id: String(userId), profile_id: profileId, field },
        profileContext: authority,
      });
    },
    onError: failedUnlessExplained("web.admin.users.detail.profiles.setEdits.dismissFailed"),
    onSettled: () =>
      client.invalidateQueries({
        queryKey: adminProfileSetEditsKey(userId, adminUserScope(context)),
      }),
  });
}
