import { type QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { captureProfileRequestContext } from "@/api/client";
import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import {
  adminUserScope,
  captureAdminUserAuthority,
  requireAdminUserAuthority,
} from "@/api/v2/adminUsers";
import { v2 } from "@/api/v2/request";
import { sectionKeys } from "@/hooks/queries/keys";
import { profileSectionOverridesFromV2 } from "@/hooks/queries/sections";

import { adminUsersKey } from "./users";

// One profile's Home layout, read and changed by an administrator through
// /api/v2/admin/users/{id}/profiles/{profile_id}/sections. The shapes match
// the profile's own /api/v2/profile/sections, so the editor saves through the
// same override builder. Only the Home page is edited here.

const HOME = { scope: "home" } as const;

function path(userId: number, profileId: string) {
  return { id: String(userId), profile_id: profileId };
}

export function adminProfileSectionsKey(
  userId: number,
  profileId: string,
  scope = adminUserScope(),
) {
  return [...adminUsersKey(scope), "profile-sections", userId, profileId] as const;
}

/** The Home page as the profile's layout orders it, overrides applied. */
export function useAdminProfileSectionSettings(userId: number, profileId: string, enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminProfileSectionsKey(userId, profileId, adminUserScope(context)), "settings"],
    queryFn: async (): Promise<SettingsSectionEntry[]> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const settings = await v2(
        "GET /api/v2/admin/users/{id}/profiles/{profile_id}/sections/settings",
        {
          path: path(userId, profileId),
          query: HOME,
          profileContext: authority,
        },
      );
      return settings.items;
    },
    enabled: enabled && context !== null && Boolean(profileId),
    retry: false,
  });
}

/** The profile's saved Home overrides, round-tripped on save. */
export function useAdminProfileSectionOverrides(userId: number, profileId: string, enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminProfileSectionsKey(userId, profileId, adminUserScope(context)), "overrides"],
    queryFn: async (): Promise<SectionOverride[]> => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      const overrides = await v2("GET /api/v2/admin/users/{id}/profiles/{profile_id}/sections", {
        path: path(userId, profileId),
        query: HOME,
        profileContext: authority,
      });
      return profileSectionOverridesFromV2(overrides.items);
    },
    enabled: enabled && context !== null && Boolean(profileId),
    retry: false,
  });
}

// The edited profile may be the acting one, whose own Home layout and section
// settings are cached under the sections keys, so those refetch too.
function invalidateProfileSections(
  client: QueryClient,
  userId: number,
  profileId: string,
  context: ReturnType<typeof captureProfileRequestContext>,
) {
  return Promise.all([
    client.invalidateQueries({
      queryKey: adminProfileSectionsKey(userId, profileId, adminUserScope(context)),
    }),
    client.invalidateQueries({ queryKey: sectionKeys.all }),
  ]);
}

/** Replaces the profile's Home override set. */
export function useSaveAdminProfileSections(userId: number, profileId: string) {
  const client = useQueryClient();
  // Captured without throwing: a render with no profile context (signing out)
  // must not crash the page; the mutation refuses instead.
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async (overrides: SectionOverride[]) => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      await v2("PUT /api/v2/admin/users/{id}/profiles/{profile_id}/sections", {
        path: path(userId, profileId),
        query: HOME,
        body: { overrides },
        profileContext: authority,
      });
    },
    onSettled: () => invalidateProfileSections(client, userId, profileId, context),
  });
}

/** Deletes the profile's Home overrides, so it follows the server layout. */
export function useResetAdminProfileSections(userId: number, profileId: string) {
  const client = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async () => {
      const authority = context ?? captureAdminUserAuthority();
      requireAdminUserAuthority(authority);
      await v2("DELETE /api/v2/admin/users/{id}/profiles/{profile_id}/sections", {
        path: path(userId, profileId),
        query: HOME,
        profileContext: authority,
      });
    },
    onSettled: () => invalidateProfileSections(client, userId, profileId, context),
  });
}
