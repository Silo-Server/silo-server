import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { MoreHorizontal, Plus } from "lucide-react";
import { toast } from "sonner";

import type { AdminUser, UserLibrary } from "@/api/types";
import type { AdminUserProfileActivity } from "@/api/v2/adminUserActivity";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  ProfileEditorDialog,
  type ProfileEditorSave,
} from "@/components/profiles/ProfileEditorDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAdminUserProfile,
  useCreateAdminUserProfile,
  useDeleteAdminUserProfile,
  useUpdateAdminUserProfile,
} from "@/hooks/queries/admin/userProfiles";
import {
  useAdminUserLiveSessions,
  useAdminUserProfileActivity,
} from "@/hooks/queries/admin/userActivity";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";
import { useProfiles } from "@/hooks/queries/profiles";

import { formatLastSeen } from "../format";
import { DetailCard, InitialAvatar } from "../ui";

type Editing = { mode: "create" } | { mode: "edit"; id: string };

/**
 * The account's household profiles, managed by the administrator: add,
 * edit, and delete profiles, and set or remove a profile's PIN (how a
 * forgotten PIN, the primary profile's included, is reset). `manageable` is
 * the Owner rule the server applies to these writes: only the Owner changes
 * the Owner's or another admin's profiles; anyone else sees them read-only.
 */
export function ProfilesTab({ user, manageable }: { user: AdminUser; manageable: boolean }) {
  const capabilities = useAdminUserCapabilities().data;
  const supported = capabilities?.profile_management === true;
  const manage = manageable && supported;
  let readOnlyReason: string | null = null;
  if (!manageable) readOnlyReason = "Only the server owner can change this account's profiles.";
  else if (capabilities && !supported)
    readOnlyReason = "This server can't change profiles from the admin page.";
  const profiles = useAdminUserProfileActivity(user.id);
  const liveQuery = useAdminUserLiveSessions(user.id);
  const watching = new Set((liveQuery.data ?? []).map((session) => session.profile_id));

  const [editing, setEditing] = useState<Editing | null>(null);
  const [pinTarget, setPinTarget] = useState<AdminUserProfileActivity | null>(null);
  const [removePinTarget, setRemovePinTarget] = useState<AdminUserProfileActivity | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminUserProfileActivity | null>(null);

  const count = profiles.data?.length;
  const limit = user.effective_policy.max_profiles;
  const atLimit = count !== undefined && limit >= 1 && count >= limit;

  return (
    <>
      <DetailCard
        title="Profiles"
        description={count === undefined ? undefined : `${count} of ${limit} allowed`}
        actions={
          manage ? (
            <Button
              type="button"
              size="sm"
              disabled={!profiles.data || atLimit}
              title={atLimit ? "The account is at its profile limit" : undefined}
              onClick={() => setEditing({ mode: "create" })}
            >
              <Plus />
              Add profile
            </Button>
          ) : null
        }
      >
        {profiles.isError ? (
          <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-4 text-sm sm:px-5">
            Couldn&apos;t load profiles.
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void profiles.refetch()}
            >
              Retry
            </Button>
          </div>
        ) : !profiles.data ? (
          <div className="space-y-3 px-4 py-4 sm:px-5">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : profiles.data.length === 0 ? (
          <p className="text-muted-foreground px-4 py-6 text-center text-sm sm:px-5">
            This account has no profiles.
          </p>
        ) : (
          profiles.data.map((profile, index) => (
            <ProfileRow
              key={profile.id}
              profile={profile}
              seed={index}
              watching={watching.has(profile.id)}
              manage={manage}
              onEdit={() => setEditing({ mode: "edit", id: profile.id })}
              onSetPin={() => setPinTarget(profile)}
              onRemovePin={() => setRemovePinTarget(profile)}
              onDelete={() => setDeleteTarget(profile)}
            />
          ))
        )}
        {readOnlyReason ? (
          <p className="text-muted-foreground border-border/50 border-t px-4 py-3 text-xs sm:px-5">
            {readOnlyReason}
          </p>
        ) : null}
      </DetailCard>

      {manage ? (
        <>
          <ProfileEditor user={user} editing={editing} onClose={() => setEditing(null)} />
          <SetPinDialog userId={user.id} profile={pinTarget} onClose={() => setPinTarget(null)} />
          <RemovePinDialog
            userId={user.id}
            profile={removePinTarget}
            onClose={() => setRemovePinTarget(null)}
          />
          <DeleteProfileDialog
            userId={user.id}
            profile={deleteTarget}
            onClose={() => setDeleteTarget(null)}
          />
        </>
      ) : null}
    </>
  );
}

function ProfileRow({
  profile,
  seed,
  watching,
  manage,
  onEdit,
  onSetPin,
  onRemovePin,
  onDelete,
}: {
  profile: AdminUserProfileActivity;
  seed: number;
  watching: boolean;
  manage: boolean;
  onEdit: () => void;
  onSetPin: () => void;
  onRemovePin: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="border-border/50 flex items-center gap-3 border-t px-4 py-3 first:border-t-0 sm:px-5">
      <InitialAvatar name={profile.name} seed={seed} src={profile.avatar_url} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="truncate text-sm font-medium">{profile.name}</span>
          {profile.is_primary ? <Badge variant="secondary">Primary</Badge> : null}
          {profile.is_child ? <Badge variant="secondary">Kids</Badge> : null}
          {profile.has_pin ? <Badge variant="outline">PIN</Badge> : null}
        </div>
        <div className="text-muted-foreground text-xs">
          {watching
            ? "Watching now"
            : profile.last_seen_at
              ? formatLastSeen(profile.last_seen_at)
              : "No device activity"}
        </div>
      </div>
      {manage ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={`Actions for ${profile.name}`}
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-44">
            <DropdownMenuItem onSelect={onEdit}>Edit profile</DropdownMenuItem>
            <DropdownMenuItem onSelect={onSetPin}>
              {profile.has_pin ? "Change PIN" : "Set PIN"}
            </DropdownMenuItem>
            {profile.has_pin ? (
              <DropdownMenuItem onSelect={onRemovePin}>Remove PIN</DropdownMenuItem>
            ) : null}
            {profile.is_primary ? null : (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={onDelete}>
                  Delete profile
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : null}
    </div>
  );
}

/** The libraries the account can see: the choices for a profile's allowlist. */
function useAccountLibraries(user: AdminUser): UserLibrary[] {
  const libraries = useAdminLibraries().data;
  const allowed = user.effective_policy.library_ids;
  return useMemo(
    () =>
      (libraries ?? [])
        .filter((library) => allowed === null || allowed.includes(library.id))
        .map(({ id, name, type, sort_order }) => ({ id, name, type, sort_order })),
    [libraries, allowed],
  );
}

function ProfileEditor({
  user,
  editing,
  onClose,
}: {
  user: AdminUser;
  editing: Editing | null;
  onClose: () => void;
}) {
  const libraries = useAccountLibraries(user);
  // The advisory-age flags describe this server, which the admin's own
  // profile list reports.
  const server = useProfiles();
  const editId = editing?.mode === "edit" ? editing.id : null;
  const full = useAdminUserProfile(user.id, editId);
  const create = useCreateAdminUserProfile(user.id);
  const update = useUpdateAdminUserProfile(user.id);
  const save: ProfileEditorSave = {
    create: create.mutateAsync,
    update: (id, body) => update.mutateAsync({ id, body }),
    pending: create.isPending || update.isPending,
  };
  const failed = editId !== null && full.isError;

  useEffect(() => {
    if (failed) {
      toast.error("Couldn't load the profile");
      onClose();
    }
  }, [failed, onClose]);

  const open = editing?.mode === "create" || (editId !== null && full.data?.id === editId);
  return (
    <ProfileEditorDialog
      open={open}
      profile={editId !== null ? full.data : null}
      libraries={libraries}
      advisoryAgeSupported={server.maxAdvisoryAgeSupported}
      requireAdvisoryAgeSupported={server.requireAdvisoryAgeSupported}
      save={save}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      onSaveSuccess={(_, { mode }) => {
        if (mode === "edit") toast.success("Profile updated");
      }}
    />
  );
}

function SetPinDialog({
  userId,
  profile,
  onClose,
}: {
  userId: number;
  profile: AdminUserProfileActivity | null;
  onClose: () => void;
}) {
  const guards = profile?.is_primary
    ? " The primary profile's PIN also guards managing this household."
    : "";
  return (
    <Dialog open={profile !== null} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>
            {profile?.has_pin ? `Change ${profile.name}'s PIN` : `Set a PIN for ${profile?.name}`}
          </DialogTitle>
          <DialogDescription>
            {profile?.has_pin
              ? `Devices unlocked with the old PIN will ask for the new one.${guards}`
              : `The profile will ask for this PIN when it is opened.${guards}`}
          </DialogDescription>
        </DialogHeader>
        {profile ? (
          <SetPinForm key={profile.id} userId={userId} profile={profile} onClose={onClose} />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function SetPinForm({
  userId,
  profile,
  onClose,
}: {
  userId: number;
  profile: AdminUserProfileActivity;
  onClose: () => void;
}) {
  const update = useUpdateAdminUserProfile(userId);
  const [pin, setPin] = useState("");
  const [error, setError] = useState<string | null>(null);
  const pinId = useId();

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!/^\d{4}$/.test(pin)) {
      setError("PIN must be exactly 4 digits.");
      return;
    }
    update.mutate(
      { id: profile.id, body: { pin } },
      {
        onSuccess: () => {
          toast.success(profile.has_pin ? "PIN changed" : "PIN set");
          onClose();
        },
      },
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="space-y-2">
        <Label htmlFor={pinId}>New PIN</Label>
        <Input
          id={pinId}
          type="password"
          inputMode="numeric"
          autoComplete="off"
          maxLength={4}
          placeholder="4 digits"
          value={pin}
          onChange={(event) => {
            setPin(event.target.value.replace(/\D/g, "").slice(0, 4));
            setError(null);
          }}
        />
        {error ? <p className="text-destructive text-sm">{error}</p> : null}
      </div>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" disabled={update.isPending}>
          {update.isPending ? "Saving..." : "Save PIN"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function RemovePinDialog({
  userId,
  profile,
  onClose,
}: {
  userId: number;
  profile: AdminUserProfileActivity | null;
  onClose: () => void;
}) {
  const update = useUpdateAdminUserProfile(userId);
  return (
    <ConfirmDialog
      open={profile !== null}
      onOpenChange={(next) => (next ? undefined : onClose())}
      title={`Remove ${profile?.name ?? "this profile"}'s PIN?`}
      description={
        profile?.is_primary
          ? "Anyone signed in to this account can then open this profile and manage the household without a PIN."
          : "Anyone signed in to this account can then open this profile without a PIN."
      }
      confirmLabel="Remove PIN"
      isPending={update.isPending}
      onConfirm={() => {
        if (!profile) return;
        update.mutate(
          { id: profile.id, body: { pin: null } },
          {
            onSuccess: () => {
              toast.success("PIN removed");
              onClose();
            },
          },
        );
      }}
    />
  );
}

function DeleteProfileDialog({
  userId,
  profile,
  onClose,
}: {
  userId: number;
  profile: AdminUserProfileActivity | null;
  onClose: () => void;
}) {
  const remove = useDeleteAdminUserProfile(userId);
  return (
    <ConfirmDialog
      open={profile !== null}
      onOpenChange={(next) => (next ? undefined : onClose())}
      title={`Delete ${profile?.name ?? "this profile"}?`}
      description="The profile and what it saved on this account, such as its settings, watchlist, and ratings, are removed."
      confirmLabel="Delete profile"
      variant="destructive"
      irreversible
      isPending={remove.isPending}
      onConfirm={() => {
        if (!profile) return;
        remove.mutate(profile.id, { onSuccess: onClose });
      }}
    />
  );
}
