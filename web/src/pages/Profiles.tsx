import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { Lock, Plus } from "lucide-react";
import { toast } from "sonner";

import type { Profile } from "@/api/types";
import { ProfileEditorDialog } from "@/components/profiles/ProfileEditorDialog";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useAuth } from "@/hooks/useAuth";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";
import { useProfiles } from "@/hooks/queries/profiles";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";

export default function Profiles() {
  const { data: profiles = [], isLoading: profilesLoading, avatarUploadEnabled } = useProfiles();
  const { data: libraries = [], isLoading: librariesLoading } = useAvailableUserLibraries();
  const [editorOpen, setEditorOpen] = useState(false);
  const [pinProfile, setPinProfile] = useState<Profile | null>(null);
  const { selectProfile, verifyProfilePin, logout } = useAuth();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const redirectTarget = sanitizeAuthRedirect(searchParams.get("redirect"));

  useDocumentTitle("Profiles");

  const isLoading = profilesLoading || librariesLoading;

  function enterApp() {
    navigate(redirectTarget ?? "/");
  }

  function handleSelect(profile: Profile) {
    if (profile.has_pin) {
      setPinProfile(profile);
      return;
    }

    selectProfile(profile);
    enterApp();
  }

  async function handleCreateSuccess(
    profile: Profile,
    context: {
      pin: string;
    },
  ) {
    if (context.pin === "") {
      selectProfile(profile);
      enterApp();
      return;
    }

    try {
      const response = await verifyProfilePin(profile.id, context.pin);
      if (response.valid && response.profile_token) {
        selectProfile(profile, response.profile_token);
        enterApp();
        return;
      }
    } catch {
      // fall through to the toast below
    }

    toast.error("Profile created, but PIN verification failed");
  }

  if (isLoading) {
    return (
      <div className="auth-shell flex-col gap-8">
        <Skeleton className="h-12 w-64" />
        <div className="flex flex-wrap justify-center gap-5">
          {Array.from({ length: 3 }).map((_, index) => (
            <div key={index} className="flex w-[148px] flex-col items-center gap-3 p-5">
              <Skeleton className="h-20 w-20 rounded-full" />
              <Skeleton className="h-4 w-16 rounded" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  if (profiles.length === 0) {
    return (
      <div className="auth-shell flex-col gap-6">
        <div className="relative z-10 flex flex-col items-center gap-6">
          <div className="w-full max-w-md space-y-3 text-center">
            <h1 className="page-title text-[clamp(2.1rem,6vw,4rem)]">Create your first profile</h1>
            <p className="text-muted-foreground text-sm">
              You need a profile before you can enter the app.
            </p>
          </div>

          <Button onClick={() => setEditorOpen(true)}>
            <Plus className="h-4 w-4" />
            Create profile
          </Button>

          <Button variant="outline" size="sm" onClick={logout}>
            Sign out
          </Button>
        </div>

        <ProfileEditorDialog
          open={editorOpen}
          libraries={libraries}
          avatarUploadEnabled={avatarUploadEnabled}
          onOpenChange={setEditorOpen}
          onSaveSuccess={(profile, context) => void handleCreateSuccess(profile, context)}
        />
      </div>
    );
  }

  return (
    <div className="auth-shell flex-col gap-8">
      <div className="relative z-10 text-center">
        <h1 className="page-title text-[clamp(2.2rem,7vw,4.5rem)]">Who&apos;s watching?</h1>
      </div>

      <div className="relative z-10 flex max-w-5xl flex-wrap justify-center gap-5">
        {profiles.map((profile) => (
          <div key={profile.id} className="group relative">
            <button
              onClick={() => handleSelect(profile)}
              aria-label={profile.has_pin ? `${profile.name} (PIN protected)` : profile.name}
              className="surface-panel hover:border-primary flex w-[148px] flex-col items-center gap-3 rounded-[1.75rem] p-5 transition-all duration-150 hover:-translate-y-1"
            >
              <div className="relative">
                <Avatar className="ring-border group-hover:ring-primary/30 h-20 w-20 ring-2 transition-colors">
                  {profile.avatar_url ? <AvatarImage src={profile.avatar_url} alt="" /> : null}
                  <AvatarFallback className="bg-surface text-primary text-2xl font-bold">
                    {profile.name.charAt(0).toUpperCase()}
                  </AvatarFallback>
                </Avatar>
                {profile.has_pin ? (
                  <span className="bg-surface border-border text-muted-foreground absolute -right-0.5 -bottom-0.5 flex h-6 w-6 items-center justify-center rounded-full border">
                    <Lock className="h-3 w-3" aria-hidden="true" />
                  </span>
                ) : null}
              </div>
              <span className="text-sm font-medium">{profile.name}</span>
            </button>
          </div>
        ))}
      </div>

      <Button variant="outline" size="sm" onClick={logout} className="relative z-10 mt-2">
        Sign out
      </Button>

      <ProfilePinDialog
        profile={pinProfile}
        onClose={() => setPinProfile(null)}
        onVerified={(profile, token) => {
          setPinProfile(null);
          selectProfile(profile, token);
          enterApp();
        }}
        verifyPin={verifyProfilePin}
      />
    </div>
  );
}
