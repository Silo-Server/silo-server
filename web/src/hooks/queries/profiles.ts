import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { AdminSession, Profile } from "@/api/types";
import type { components } from "@/api/v2/schema";
import { v2, type V2Body, type V2Result } from "@/api/v2/request";
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

/** The POST body of the v2 createProfile operation. */
export type ProfileCreate = V2Body<"POST /api/v2/profiles">;

/** The answer of the v2 verifyProfilePIN operation. */
export type ProfileVerification = V2Result<"POST /api/v2/profiles/{id}/verify-pin">;

/** The profile list as the app models it: the profiles plus the avatar-upload capability. */
export interface ProfileList {
  profiles: Profile[];
  avatar_upload_enabled: boolean;
}

/**
 * Projects a v2 profile onto the `Profile` shape the profile list, the
 * session, and the editor read. Library ids are the same ids as strings.
 */
export function profileFromV2(profile: components["schemas"]["Profile"]): Profile {
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

function optionalNumber(value: string | null): number | undefined {
  return value === null ? undefined : Number(value);
}

/**
 * Projects a v2 playback session onto the `AdminSession` shape the session
 * panels share with the admin activity views. Numeric v1 identifiers are the
 * same ids rendered as strings on v2.
 */
export function sessionFromV2(session: components["schemas"]["PlaybackSession"]): AdminSession {
  const { id, user_id, media_file_id, requested_media_file_id, ...rest } = session;
  return {
    ...rest,
    session_id: id,
    user_id: Number(user_id),
    media_file_id: Number(media_file_id),
    requested_media_file_id: Number(requested_media_file_id),
    routing_execution_node_id: optionalNumber(session.routing_execution_node_id),
    routing_egress_node_id: optionalNumber(session.routing_egress_node_id),
  };
}

export async function listProfiles(): Promise<ProfileList> {
  const list = await v2("GET /api/v2/profiles");
  return {
    profiles: list.items.map(profileFromV2),
    avatar_upload_enabled: list.avatar_upload_enabled,
  };
}

export function createProfile(body: ProfileCreate): Promise<Profile> {
  return v2("POST /api/v2/profiles", { body }).then(profileFromV2);
}

export function verifyProfilePIN(profileId: string, pin: string): Promise<ProfileVerification> {
  return v2("POST /api/v2/profiles/{id}/verify-pin", { path: { id: profileId }, body: { pin } });
}

export function uploadProfileAvatar(id: string, file: File): Promise<Profile> {
  return v2("PUT /api/v2/profiles/{id}/avatar", { path: { id }, form: { avatar: file } }).then(
    profileFromV2,
  );
}

export async function listHouseholdSessions(): Promise<AdminSession[]> {
  const sessions = await v2("GET /api/v2/profiles/household/sessions");
  return sessions.items.map(sessionFromV2);
}

const HOUSEHOLD_SESSIONS_POLL_MS = 10_000;

export function useHouseholdSessions(enabled = true) {
  return useQuery({
    queryKey: profileKeys.householdSessions(),
    queryFn: listHouseholdSessions,
    enabled,
    staleTime: HOUSEHOLD_SESSIONS_POLL_MS,
    refetchInterval: HOUSEHOLD_SESSIONS_POLL_MS,
  });
}

export function useProfiles() {
  const query = useQuery({
    queryKey: profileKeys.list(),
    queryFn: listProfiles,
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
    mutationFn: createProfile,
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
      queryClient.setQueryData<ProfileList | undefined>(profileKeys.list(), (current) => {
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
    mutationFn: ({ id, file }: { id: string; file: File }) => uploadProfileAvatar(id, file),
    onSuccess: (updatedProfile) => {
      queryClient.setQueryData<ProfileList | undefined>(profileKeys.list(), (current) => ({
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
    // deleteProfileAvatar answers 204 and the editor wants the profile back.
    // The DELETE is the mutation result: once it succeeds the avatar is gone,
    // so a failed follow-up read must not report the mutation as failed. The
    // caller passes the profile it is clearing; the list is re-read when
    // possible and the cached copy is patched locally otherwise.
    mutationFn: async (profile: Profile): Promise<Profile> => {
      await v2("DELETE /api/v2/profiles/{id}/avatar", { path: { id: profile.id } });
      const cleared: Profile = {
        ...profile,
        avatar: "",
        avatar_url: undefined,
        avatar_source: "none",
      };
      try {
        const list = await listProfiles();
        queryClient.setQueryData<ProfileList | undefined>(profileKeys.list(), list);
        return list.profiles.find((candidate) => candidate.id === profile.id) ?? cleared;
      } catch {
        queryClient.setQueryData<ProfileList | undefined>(profileKeys.list(), (current) =>
          current
            ? {
                ...current,
                profiles: current.profiles.map((p) => (p.id === profile.id ? cleared : p)),
              }
            : current,
        );
        return cleared;
      }
    },
    onSuccess: () => {
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
    mutationFn: (id: string) => v2("DELETE /api/v2/profiles/{id}", { path: { id } }),
    onSuccess: () => {
      toast.success("Profile deleted");
      queryClient.invalidateQueries({ queryKey: profileKeys.list() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
    },
  });
}
