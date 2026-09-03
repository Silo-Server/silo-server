import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AdminSession, Profile, CreateProfileRequest, ProfileListResponse } from "@/api/types";
import { profileKeys, ratingKeys } from "./keys";
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
    mutationFn: ({ id, body }: { id: string; body: Partial<CreateProfileRequest> }) =>
      api<Profile>(`/profiles/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
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
      queryClient.invalidateQueries({ queryKey: ratingKeys.all });
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
      queryClient.invalidateQueries({ queryKey: ratingKeys.all });
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
      queryClient.invalidateQueries({ queryKey: ratingKeys.all });
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
      queryClient.invalidateQueries({ queryKey: ratingKeys.all });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
    },
  });
}
