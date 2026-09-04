import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AdminSession, Profile, CreateProfileRequest, ProfileListResponse } from "@/api/types";
import { v2, type V2Body, type V2Response } from "@/api/v2/request";
import { profileKeys } from "./keys";
import { toast } from "sonner";

function replaceProfileInList(profiles: Profile[] | undefined, updatedProfile: Profile) {
  if (!profiles || profiles.length === 0) {
    return [updatedProfile];
  }
  let replaced = false;
  const nextProfiles = profiles.map((profile) => {
    if (profile.id !== updatedProfile.id) {
      return profile;
    }
    replaced = true;
    return updatedProfile;
  });
  if (!replaced) {
    nextProfiles.push(updatedProfile);
  }
  return nextProfiles;
}

/** The PATCH body of the v2 updateProfile operation: omitted leaves a member unchanged, null clears it. */
export type ProfileUpdate = V2Body<"PATCH /api/v2/profiles/{id}">;

/**
 * Projects a v2 profile onto the `Profile` shape the profile list, the
 * session, and the editor still read from v1 (list, create, avatar, and
 * delete stay on v1 in the pilot). Library ids are the same ids as strings.
 */
export function profileFromV2(profile: V2Response<"PATCH /api/v2/profiles/{id}">): Profile {
  return {
    id: profile.id,
    name: profile.name,
    avatar: profile.avatar,
    avatar_url: profile.avatar_url,
    avatar_source: profile.avatar_source,
    has_pin: profile.has_pin,
    is_child: profile.is_child,
    is_primary: profile.is_primary,
    max_content_rating: profile.max_content_rating,
    quality_preference: profile.quality_preference,
    language: profile.language,
    preferred_metadata_language: profile.preferred_metadata_language,
    subtitle_language: profile.subtitle_language,
    subtitle_mode: profile.subtitle_mode,
    show_forced_subtitles: profile.show_forced_subtitles,
    auto_skip_intro: profile.auto_skip_intro,
    auto_skip_credits: profile.auto_skip_credits,
    auto_skip_recap: profile.auto_skip_recap,
    auto_play_next_preview: profile.auto_play_next_preview,
    library_restrictions_enabled: profile.library_restrictions_enabled,
    allowed_library_ids: profile.allowed_library_ids.map(Number),
    max_playback_quality: profile.max_playback_quality,
    created_at: profile.created_at,
    updated_at: profile.updated_at,
  };
}

const HOUSEHOLD_SESSIONS_POLL_MS = 10_000;

export function useHouseholdSessions(enabled = true) {
  return useQuery({
    queryKey: profileKeys.householdSessions(),
    queryFn: () =>
      api<AdminSession[]>("/profiles/household/sessions").then((sessions) => sessions ?? []),
    enabled,
    staleTime: HOUSEHOLD_SESSIONS_POLL_MS,
    refetchInterval: HOUSEHOLD_SESSIONS_POLL_MS,
  });
}

export function useProfiles() {
  const query = useQuery({
    queryKey: profileKeys.list(),
    queryFn: () => api<ProfileListResponse>("/profiles"),
  });

  return {
    ...query,
    data: query.data?.profiles ?? [],
    avatarUploadEnabled: query.data?.avatar_upload_enabled ?? false,
  };
}

export function useCreateProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateProfileRequest) =>
      api<Profile>("/profiles", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Profile created");
      queryClient.invalidateQueries({ queryKey: profileKeys.list() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save profile");
    },
  });
}

export function useUpdateProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: ProfileUpdate }) =>
      v2("PATCH /api/v2/profiles/{id}", { path: { id }, body }).then(profileFromV2),
    onSuccess: (updatedProfile) => {
      queryClient.setQueryData<ProfileListResponse | undefined>(profileKeys.list(), (current) => {
        const profiles = replaceProfileInList(current?.profiles, updatedProfile);
        return {
          profiles,
          avatar_upload_enabled: current?.avatar_upload_enabled ?? false,
        };
      });
      toast.success("Profile updated");
      queryClient.invalidateQueries({ queryKey: profileKeys.list() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save profile");
    },
  });
}

export function useUploadProfileAvatar() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, file }: { id: string; file: File }) => {
      const body = new FormData();
      body.set("avatar", file);
      return api<Profile>(`/profiles/${id}/avatar`, {
        method: "PUT",
        body,
      });
    },
    onSuccess: (updatedProfile) => {
      queryClient.setQueryData<ProfileListResponse | undefined>(profileKeys.list(), (current) => ({
        profiles: replaceProfileInList(current?.profiles, updatedProfile),
        avatar_upload_enabled: current?.avatar_upload_enabled ?? false,
      }));
      toast.success("Avatar updated");
      queryClient.invalidateQueries({ queryKey: profileKeys.list() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to upload avatar");
    },
  });
}

export function useDeleteProfileAvatar() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<Profile>(`/profiles/${id}/avatar`, {
        method: "DELETE",
      }),
    onSuccess: (updatedProfile) => {
      queryClient.setQueryData<ProfileListResponse | undefined>(profileKeys.list(), (current) => ({
        profiles: replaceProfileInList(current?.profiles, updatedProfile),
        avatar_upload_enabled: current?.avatar_upload_enabled ?? false,
      }));
      toast.success("Avatar removed");
      queryClient.invalidateQueries({ queryKey: profileKeys.list() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove avatar");
    },
  });
}

export function useDeleteProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api(`/profiles/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Profile deleted");
      queryClient.invalidateQueries({ queryKey: profileKeys.list() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
    },
  });
}
