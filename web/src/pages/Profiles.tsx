import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { Lock, Plus, UserCog } from "lucide-react";
import { toast } from "sonner";

import {
  captureProfileRequestContextAs,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { Profile, UserLibrary } from "@/api/types";
import { ProfileEditorDialog } from "@/components/profiles/ProfileEditorDialog";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { pinLockoutMessage } from "@/components/profiles/pinLockout";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useAuth } from "@/hooks/useAuth";
import { listUserLibraries, useAvailableUserLibraries } from "@/hooks/queries/libraries";
import { useProfiles } from "@/hooks/queries/profiles";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";

type ManagePurpose = "add" | "manage";
/** What a PIN prompt on the picker unlocks. */
type PinPurpose = "select" | ManagePurpose;

const PIN_PURPOSE_DESCRIPTIONS: Partial<Record<PinPurpose, string>> = {
  add: "Adding a profile needs the primary profile's PIN.",
  manage: "Managing profiles needs the primary profile's PIN.",
};

/** A new profile being created as the verified primary profile. */
interface AddProfileSession {
  profileContext: ProfileRequestContextSnapshot;
  libraries: UserLibrary[];
}

export default function Profiles() {
  const {
    data: profiles = [],
    isLoading: profilesLoading,
    avatarUploadEnabled,
    maxAdvisoryAgeSupported,
    requireAdvisoryAgeSupported,
  } = useProfiles();
  const { data: libraries = [], isLoading: librariesLoading } = useAvailableUserLibraries();
  const [editorOpen, setEditorOpen] = useState(false);
  const [pinRequest, setPinRequest] = useState<{ profile: Profile; purpose: PinPurpose } | null>(
    null,
  );
  const [addSession, setAddSession] = useState<AddProfileSession | null>(null);
  const [preparingAdd, setPreparingAdd] = useState(false);
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
      setPinRequest({ profile, purpose: "select" });
      return;
    }

    selectProfile(profile);
    enterApp();
  }

  // Adding and managing profiles verify the household's primary profile
  // first, as the apps do: its PIN when it has one. The server decides
  // whether the account may manage profiles at all.
  function startManaging(purpose: ManagePurpose) {
    const primary = profiles.find((profile) => profile.is_primary);
    if (!primary) {
      toast.error("Couldn't find the primary profile needed to manage profiles.");
      return;
    }
    if (primary.has_pin) {
      setPinRequest({ profile: primary, purpose });
      return;
    }
    void continueManaging(purpose, primary, null);
  }

  async function continueManaging(
    purpose: ManagePurpose,
    primary: Profile,
    profileToken: string | null,
  ) {
    if (purpose === "manage") {
      selectProfile(primary, profileToken ?? undefined);
      navigate("/settings/profiles");
      return;
    }

    // The new profile is created as the primary profile without selecting
    // it, so the browser's profile stays as it was and the person still
    // picks who's watching. The editor lists the primary's libraries, not
    // those of a profile this browser may still remember.
    const profileContext = captureProfileRequestContextAs(primary.id, profileToken);
    if (!profileContext) {
      toast.error("Your session ended. Sign in again.");
      return;
    }
    setPreparingAdd(true);
    try {
      const libraries = await listUserLibraries({ profileContext });
      setAddSession({ profileContext, libraries });
    } catch (err) {
      if (!(err instanceof StaleApiRequestContextError)) {
        toast.error("Couldn't open the new profile form. Try again.");
      }
    } finally {
      setPreparingAdd(false);
    }
  }

  function handlePinVerified(profile: Profile, profileToken: string) {
    const purpose = pinRequest?.purpose ?? "select";
    setPinRequest(null);
    if (purpose === "select") {
      selectProfile(profile, profileToken);
      enterApp();
      return;
    }
    void continueManaging(purpose, profile, profileToken);
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
    } catch (err) {
      const lockout = pinLockoutMessage(err);
      if (lockout) {
        toast.error(`Profile created. ${lockout}`);
        return;
      }
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
          advisoryAgeSupported={maxAdvisoryAgeSupported}
          requireAdvisoryAgeSupported={requireAdvisoryAgeSupported}
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
              disabled={preparingAdd}
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
        <button
          type="button"
          onClick={() => startManaging("add")}
          disabled={preparingAdd}
          className="group border-border hover:border-primary text-muted-foreground hover:text-foreground flex w-[148px] flex-col items-center gap-3 rounded-[1.75rem] border-2 border-dashed p-5 transition-all duration-150 hover:-translate-y-1 disabled:pointer-events-none disabled:opacity-60"
        >
          <span className="border-border group-hover:border-primary/40 flex h-20 w-20 items-center justify-center rounded-full border-2 transition-colors">
            <Plus className="h-8 w-8" aria-hidden="true" />
          </span>
          <span className="text-sm font-medium">Add profile</span>
        </button>
      </div>

      <div className="relative z-10 mt-2 flex flex-wrap justify-center gap-3">
        <Button
          variant="outline"
          size="sm"
          onClick={() => startManaging("manage")}
          disabled={preparingAdd}
        >
          <UserCog className="h-4 w-4" />
          Manage profiles
        </Button>
        <Button variant="outline" size="sm" onClick={logout}>
          Sign out
        </Button>
      </div>

      <ProfilePinDialog
        profile={pinRequest?.profile ?? null}
        description={pinRequest ? PIN_PURPOSE_DESCRIPTIONS[pinRequest.purpose] : undefined}
        onClose={() => setPinRequest(null)}
        onVerified={handlePinVerified}
        verifyPin={verifyProfilePin}
      />

      <ProfileEditorDialog
        open={addSession !== null}
        libraries={addSession?.libraries ?? []}
        avatarUploadEnabled={avatarUploadEnabled}
        advisoryAgeSupported={maxAdvisoryAgeSupported}
        requireAdvisoryAgeSupported={requireAdvisoryAgeSupported}
        profileContext={addSession?.profileContext}
        onOpenChange={(open) => {
          if (!open) {
            setAddSession(null);
          }
        }}
      />
    </div>
  );
}
