import { useState } from "react";
import { Navigate, useNavigate } from "react-router";
import { Plus } from "lucide-react";

import type { Profile } from "@/api/types";
import { ProfileEditorDialog } from "@/components/profiles/ProfileEditorDialog";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useAuth, getBootstrapProfile } from "@/hooks/useAuth";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";
import { useProfiles } from "@/hooks/queries/profiles";
import { isHouseholdSetupDone, setHouseholdSetupDone } from "@/lib/onboarding";

/**
 * The "Who's watching?" onboarding step, shown once right after an invite is
 * claimed. Profiles are not logins — everything here posts through the
 * existing /profiles endpoint on the freshly created account. "Just me for
 * now" is a first-class exit that creates nothing.
 */
export default function HouseholdSetup() {
  const { user, loading, selectProfile } = useAuth();
  const { data: profiles = [], isLoading: profilesLoading, avatarUploadEnabled } = useProfiles();
  const { data: libraries = [] } = useAvailableUserLibraries();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<Profile | null>(null);
  const navigate = useNavigate();

  useDocumentTitle("Who's watching?");

  if (loading) {
    return (
      <div className="auth-shell">
        <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
      </div>
    );
  }
  if (!user) {
    return <Navigate to="/login" replace />;
  }
  // Re-visiting after completing setup goes home. An auto-selected sole
  // profile does NOT bounce us: useAuth picks the only PIN-less profile as
  // soon as it loads, which races this screen right after an invite accept.
  if (isHouseholdSetupDone()) {
    return <Navigate to="/" replace />;
  }

  function finish() {
    setHouseholdSetupDone();
    // With exactly one PIN-less profile we can enter directly; otherwise the
    // regular profile picker takes over (it owns PIN entry).
    const sole = getBootstrapProfile(profiles);
    if (sole) {
      selectProfile(sole);
      navigate("/", { replace: true });
      return;
    }
    navigate("/profiles", { replace: true });
  }

  function openEditor(profile: Profile | null) {
    setEditing(profile);
    setEditorOpen(true);
  }

  return (
    <div className="auth-shell">
      <AuthBackground />
      <div className="relative z-10 w-full max-w-2xl px-4">
        <div className="mb-10 text-center">
          <h1 className="text-3xl font-extrabold tracking-[-0.04em] sm:text-4xl">
            Who&apos;s watching?
          </h1>
          <p className="text-muted-foreground mx-auto mt-3 max-w-md text-sm leading-6">
            Everyone gets their own history, watchlist, and recommendations. Add the whole household
            now or later — this is your account either way.
          </p>
        </div>

        {profilesLoading ? (
          <div className="flex justify-center py-10">
            <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
          </div>
        ) : (
          <div className="flex flex-wrap items-start justify-center gap-6">
            {profiles.map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => openEditor(p)}
                className="group flex w-24 flex-col items-center gap-2 text-center"
                title="Edit profile"
              >
                <div className="relative">
                  <Avatar className="border-border group-hover:border-primary size-20 rounded-2xl border transition-colors">
                    {p.avatar_url ? <AvatarImage src={p.avatar_url} alt="" /> : null}
                    <AvatarFallback className="rounded-2xl text-2xl font-bold">
                      {p.name.slice(0, 1).toUpperCase()}
                    </AvatarFallback>
                  </Avatar>
                  {(p.is_child || p.has_pin) && (
                    <Badge
                      variant="outline"
                      className="bg-background absolute -bottom-2 left-1/2 -translate-x-1/2 px-1.5 py-0 font-mono text-[9px] tracking-wider uppercase"
                    >
                      {p.is_child ? "Kids" : "PIN"}
                    </Badge>
                  )}
                </div>
                <span className="mt-1 text-sm font-semibold">{p.name}</span>
              </button>
            ))}
            <button
              type="button"
              onClick={() => openEditor(null)}
              className="group flex w-24 flex-col items-center gap-2 text-center"
            >
              <div className="border-border group-hover:border-primary text-muted-foreground grid size-20 place-items-center rounded-2xl border border-dashed transition-colors">
                <Plus className="size-7" />
              </div>
              <span className="text-muted-foreground mt-1 text-sm font-medium">Add profile</span>
            </button>
          </div>
        )}

        <div className="mt-12 flex justify-center gap-3">
          <Button variant="ghost" onClick={finish}>
            {profiles.length > 1 ? "Skip for now" : "Just me for now"}
          </Button>
          <Button onClick={finish} disabled={profilesLoading}>
            Continue
          </Button>
        </div>
      </div>

      <ProfileEditorDialog
        open={editorOpen}
        profile={editing}
        libraries={libraries}
        avatarUploadEnabled={avatarUploadEnabled}
        onOpenChange={(open) => {
          setEditorOpen(open);
          if (!open) setEditing(null);
        }}
      />
    </div>
  );
}
