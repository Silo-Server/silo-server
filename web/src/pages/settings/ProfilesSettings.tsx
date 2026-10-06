import { useMemo, useState } from "react";
import { Pencil, Plus, Trash2, UserCheck } from "lucide-react";
import { toast } from "sonner";

import type { Profile } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/hooks/useAuth";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";
import { useDeleteProfile, useProfiles } from "@/hooks/queries/profiles";
import { ProfileEditorDialog } from "@/components/profiles/ProfileEditorDialog";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { getProfileToken } from "@/api/client";
import { buildProfileAccessSummary } from "@/lib/profile-management";
import { HouseholdStreamsPanel } from "@/components/profiles/HouseholdStreamsPanel";

function getDeleteGuardReason(
  selectedProfile: Profile,
  profiles: Profile[],
  activeProfileID: string | null,
): string | null {
  if (profiles.length <= 1) {
    return "At least one profile is required.";
  }

  if (selectedProfile.is_primary) {
    return "The primary profile can only be removed by deleting the account.";
  }

  if (selectedProfile.id === activeProfileID) {
    return "Switch to another profile before deleting this one.";
  }

  return null;
}

export default function ProfilesSettings() {
  const {
    data: profiles = [],
    isLoading: profilesLoading,
    avatarUploadEnabled,
    maxAdvisoryAgeSupported,
    requireAdvisoryAgeSupported,
  } = useProfiles();
  const { data: libraries = [], isLoading: librariesLoading } = useAvailableUserLibraries();
  const { profile: activeProfile, selectProfile, verifyProfilePin } = useAuth();
  const deleteMutation = useDeleteProfile();

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingProfile, setEditingProfile] = useState<Profile | null>(null);
  const [pinProfile, setPinProfile] = useState<Profile | null>(null);
  const [confirmDeleteProfile, setConfirmDeleteProfile] = useState<Profile | null>(null);

  const activeProfileID = activeProfile?.id ?? null;
  const isLoading = profilesLoading || librariesLoading;

  const profilesByID = useMemo(
    () => new Map(profiles.map((profile) => [profile.id, profile])),
    [profiles],
  );

  function openCreateDialog() {
    setEditingProfile(null);
    setEditorOpen(true);
  }

  function openEditDialog(profile: Profile) {
    setEditingProfile(profile);
    setEditorOpen(true);
  }

  function handleUseProfile(profile: Profile) {
    if (profile.has_pin) {
      setPinProfile(profile);
      return;
    }

    selectProfile(profile);
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-2">
          <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Profiles</h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            Manage profile names, PINs, and access rules.
          </p>
        </div>

        <Button onClick={openCreateDialog}>
          <Plus className="h-4 w-4" />
          New profile
        </Button>
      </div>

      <HouseholdStreamsPanel />

      <section className="surface-panel space-y-3 rounded-md border px-4 py-4 shadow-none sm:px-5">
        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <div
                key={index}
                className="border-border flex items-center justify-between rounded-md border px-4 py-3"
              >
                <div className="flex items-center gap-3">
                  <Skeleton className="h-10 w-10 rounded-full" />
                  <div className="space-y-2">
                    <Skeleton className="h-4 w-28 rounded" />
                    <Skeleton className="h-4 w-40 rounded" />
                  </div>
                </div>
                <Skeleton className="h-8 w-24 rounded" />
              </div>
            ))}
          </div>
        ) : profiles.length === 0 ? (
          <div className="border-border rounded-md border px-4 py-6">
            <p className="text-sm font-medium">No profiles yet</p>
            <p className="text-muted-foreground mt-1 text-sm">
              Create a profile to manage access and playback identity.
            </p>
          </div>
        ) : (
          profiles.map((profile) => {
            const accessSummary = buildProfileAccessSummary(profile);
            const deleteGuardReason = getDeleteGuardReason(profile, profiles, activeProfileID);

            return (
              <div
                key={profile.id}
                className="border-border flex flex-col gap-4 rounded-md border px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-start gap-3">
                    <Avatar className="mt-0.5 h-10 w-10">
                      {profile.avatar_url ? (
                        <AvatarImage src={profile.avatar_url} alt={profile.name} />
                      ) : null}
                      <AvatarFallback className="text-sm font-semibold">
                        {profile.name.charAt(0).toUpperCase()}
                      </AvatarFallback>
                    </Avatar>

                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-sm font-semibold">{profile.name}</span>
                        {profile.id === activeProfileID ? (
                          <Badge variant="outline">Current</Badge>
                        ) : null}
                        {profile.is_primary ? <Badge variant="outline">Primary</Badge> : null}
                        {profile.is_child ? <Badge variant="outline">Kids</Badge> : null}
                        {profile.has_pin ? <Badge variant="outline">PIN</Badge> : null}
                      </div>

                      <p className="text-muted-foreground text-sm">{accessSummary.text}</p>
                    </div>
                  </div>
                </div>

                <div className="flex flex-col items-start gap-2 sm:items-end">
                  <div className="flex flex-wrap gap-2">
                    {profile.id !== activeProfileID ? (
                      <Button size="sm" variant="outline" onClick={() => handleUseProfile(profile)}>
                        <UserCheck className="h-4 w-4" />
                        Use
                      </Button>
                    ) : null}

                    <Button size="sm" variant="outline" onClick={() => openEditDialog(profile)}>
                      <Pencil className="h-4 w-4" />
                      Edit
                    </Button>

                    <Button
                      size="sm"
                      variant="outline"
                      disabled={deleteGuardReason !== null}
                      onClick={() => setConfirmDeleteProfile(profile)}
                    >
                      <Trash2 className="h-4 w-4" />
                      Delete
                    </Button>
                  </div>

                  {deleteGuardReason ? (
                    <p className="text-muted-foreground text-xs">{deleteGuardReason}</p>
                  ) : null}
                </div>
              </div>
            );
          })
        )}
      </section>

      <ProfileEditorDialog
        open={editorOpen}
        profile={editingProfile}
        libraries={libraries}
        avatarUploadEnabled={avatarUploadEnabled}
        advisoryAgeSupported={maxAdvisoryAgeSupported}
        requireAdvisoryAgeSupported={requireAdvisoryAgeSupported}
        onOpenChange={(open) => {
          setEditorOpen(open);
          if (!open) {
            setEditingProfile(null);
          }
        }}
        onSaveSuccess={async (savedProfile, context) => {
          if (savedProfile.id !== activeProfileID) {
            return;
          }

          const currentToken = getProfileToken() ?? undefined;
          if (!currentToken && context.pin !== "" && savedProfile.has_pin) {
            try {
              const response = await verifyProfilePin(savedProfile.id, context.pin);
              if (response.valid && response.profile_token) {
                selectProfile(savedProfile, response.profile_token);
                return;
              }
            } catch {
              toast.error("Profile saved, but PIN verification failed");
              return;
            }
          }

          selectProfile(savedProfile, currentToken);
        }}
      />

      <ProfilePinDialog
        profile={pinProfile}
        onClose={() => setPinProfile(null)}
        onVerified={(profile, token) => {
          setPinProfile(null);
          selectProfile(profile, token);
        }}
        verifyPin={verifyProfilePin}
      />

      <ConfirmDialog
        open={confirmDeleteProfile !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirmDeleteProfile(null);
          }
        }}
        title="Delete profile"
        description={
          confirmDeleteProfile
            ? `Delete profile "${profilesByID.get(confirmDeleteProfile.id)?.name ?? confirmDeleteProfile.name}"? This action cannot be undone.`
            : ""
        }
        confirmLabel="Delete"
        variant="destructive"
        isPending={deleteMutation.isPending}
        onConfirm={() => {
          if (!confirmDeleteProfile) {
            return;
          }

          deleteMutation.mutate(confirmDeleteProfile.id, {
            onSuccess: () => setConfirmDeleteProfile(null),
          });
        }}
      />
    </div>
  );
}
