import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { Pencil, Plus, Trash2, UserCheck } from "lucide-react";
import { toast } from "sonner";

import type { Profile } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PageHeader } from "@/components/page/PageHeader";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/hooks/useAuth";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";
import { useDeleteProfile, useProfiles } from "@/hooks/queries/profiles";
import { ProfileEditorDialog } from "@/components/profiles/ProfileEditorDialog";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { pinLockoutMessage } from "@/components/profiles/pinLockout";
import { getProfileToken } from "@/api/client";
import {
  buildProfileAccessSummary,
  canAddProfile,
  profileLimitMessage,
} from "@/lib/profile-management";
import { HouseholdStreamsPanel } from "@/components/profiles/HouseholdStreamsPanel";
import { useT, type Translate } from "@/i18n";

function getDeleteGuardReason(
  t: Translate,
  selectedProfile: Profile,
  profiles: Profile[],
  activeProfileID: string | null,
): string | null {
  if (profiles.length <= 1) {
    return t("web.settings.profiles.deleteGuard.lastProfile");
  }

  if (selectedProfile.is_primary) {
    return t("web.settings.profiles.deleteGuard.primary");
  }

  if (selectedProfile.id === activeProfileID) {
    return t("web.settings.profiles.deleteGuard.active");
  }

  return null;
}

export default function ProfilesSettings() {
  const t = useT();
  const {
    data: profiles = [],
    isLoading: profilesLoading,
    avatarUploadEnabled,
    maxAdvisoryAgeSupported,
    requireAdvisoryAgeSupported,
    maxProfiles,
  } = useProfiles();
  const { data: libraries = [], isLoading: librariesLoading } = useAvailableUserLibraries({
    includeHidden: true,
  });
  const { profile: activeProfile, selectProfile, verifyProfilePin, isImpersonating } = useAuth();
  const deleteMutation = useDeleteProfile();
  const navigate = useNavigate();

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingProfile, setEditingProfile] = useState<Profile | null>(null);
  const [pinProfile, setPinProfile] = useState<Profile | null>(null);
  const [confirmDeleteProfile, setConfirmDeleteProfile] = useState<Profile | null>(null);

  const activeProfileID = activeProfile?.id ?? null;
  const isLoading = profilesLoading || librariesLoading;
  const householdFull = !canAddProfile(profiles.length, maxProfiles);

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
    // View-as opens a locked profile without its PIN; the server allows it too.
    if (profile.has_pin && !isImpersonating) {
      setPinProfile(profile);
      return;
    }

    selectProfile(profile);
  }

  // View-as opens a locked profile without its PIN, but managing the
  // household still needs the primary profile's. Ask for it before loading
  // anything that needs it: the server refuses those reads, and that refusal
  // would send the browser back to the profile picker.
  if (isImpersonating && activeProfile?.has_pin && !getProfileToken()) {
    return (
      <div className="space-y-6">
        <PageHeader
          title={t("web.settings.profiles.title")}
          subtitle={t("web.settings.profiles.subtitle")}
        />
        <ProfilePinDialog
          profile={profiles.find((profile) => profile.is_primary) ?? null}
          description={t("web.profiles.picker.manageNeedsPin")}
          onClose={() => navigate("/settings")}
          onVerified={(profile, token) => selectProfile(profile, token)}
          verifyPin={verifyProfilePin}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.profiles.title")}
        subtitle={t("web.settings.profiles.subtitle")}
        actions={
          householdFull ? undefined : (
            <Button onClick={openCreateDialog} disabled={profilesLoading}>
              <Plus className="h-4 w-4" />
              {t("web.settings.profiles.newProfile")}
            </Button>
          )
        }
      />

      {householdFull && maxProfiles !== null ? (
        <p className="text-muted-foreground text-sm">{profileLimitMessage(maxProfiles)}</p>
      ) : null}

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
            <p className="text-sm font-medium">{t("web.settings.profiles.empty.title")}</p>
            <p className="text-muted-foreground mt-1 text-sm">
              {t("web.settings.profiles.empty.body")}
            </p>
          </div>
        ) : (
          profiles.map((profile) => {
            const accessSummary = buildProfileAccessSummary(profile);
            const deleteGuardReason = getDeleteGuardReason(t, profile, profiles, activeProfileID);

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
                          <Badge variant="outline">
                            {t("web.settings.profiles.badge.current")}
                          </Badge>
                        ) : null}
                        {profile.is_primary ? (
                          <Badge variant="outline">
                            {t("web.settings.profiles.badge.primary")}
                          </Badge>
                        ) : null}
                        {profile.is_child ? (
                          <Badge variant="outline">{t("web.settings.profiles.badge.kids")}</Badge>
                        ) : null}
                        {profile.has_pin ? (
                          <Badge variant="outline">{t("web.settings.profiles.badge.pin")}</Badge>
                        ) : null}
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
                        {t("web.settings.profiles.use")}
                      </Button>
                    ) : null}

                    <Button size="sm" variant="outline" onClick={() => openEditDialog(profile)}>
                      <Pencil className="h-4 w-4" />
                      {t("web.settings.profiles.edit")}
                    </Button>

                    <Button
                      size="sm"
                      variant="outline"
                      disabled={deleteGuardReason !== null}
                      onClick={() => setConfirmDeleteProfile(profile)}
                    >
                      <Trash2 className="h-4 w-4" />
                      {t("web.settings.profiles.delete")}
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
        createsPrimary={!profilesLoading && profiles.length === 0}
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
            } catch (err) {
              const lockout = pinLockoutMessage(err);
              toast.error(
                lockout
                  ? t("web.settings.profiles.savedWithLockout", { lockout })
                  : t("web.settings.profiles.savedPinFailed"),
              );
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
        title={t("web.settings.profiles.deleteTitle")}
        description={
          confirmDeleteProfile
            ? t("web.settings.profiles.deleteDescription", {
                name: profilesByID.get(confirmDeleteProfile.id)?.name ?? confirmDeleteProfile.name,
              })
            : ""
        }
        confirmLabel={t("web.settings.profiles.delete")}
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
