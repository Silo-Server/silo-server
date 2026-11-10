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
import { useAdminUserCapabilities, useAdminUsers } from "@/hooks/queries/admin/users";
import { useProfiles } from "@/hooks/queries/profiles";
import { useT } from "@/i18n";

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
  const t = useT();
  const capabilities = useAdminUserCapabilities().data;
  const supported = capabilities?.profile_management === true;
  const manage = manageable && supported;
  // manageable reads the viewer's Owner standing from the account list; until
  // that list loads, a false is not yet a refusal, so no reason is given.
  const ownerKnown = useAdminUsers().isSuccess;
  let readOnlyReason: string | null = null;
  if (!manageable) {
    if (ownerKnown) readOnlyReason = t("web.admin.users.detail.profiles.readOnly.ownerOnly");
  } else if (capabilities && !supported) {
    readOnlyReason = t("web.admin.users.detail.profiles.readOnly.unsupported");
  }
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
        title={t("web.admin.users.detail.profiles.title")}
        description={
          count === undefined
            ? undefined
            : t("web.admin.users.detail.common.profilesAllowed", {
                count: count,
                max: limit,
              })
        }
        actions={
          manage ? (
            <Button
              type="button"
              size="sm"
              disabled={!profiles.data || atLimit}
              title={atLimit ? t("web.admin.users.detail.profiles.atLimit") : undefined}
              onClick={() => setEditing({ mode: "create" })}
            >
              <Plus />
              {t("web.admin.users.detail.profiles.add")}
            </Button>
          ) : null
        }
      >
        {profiles.isError ? (
          <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-4 text-sm sm:px-5">
            {t("web.admin.users.detail.common.loadProfilesFailed")}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void profiles.refetch()}
            >
              {t("common.retry")}
            </Button>
          </div>
        ) : !profiles.data ? (
          <div className="space-y-3 px-4 py-4 sm:px-5">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : profiles.data.length === 0 ? (
          <p className="text-muted-foreground px-4 py-6 text-center text-sm sm:px-5">
            {t("web.admin.users.detail.common.noProfiles")}
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
          <ProfileEditor
            user={user}
            editing={editing}
            createsPrimary={profiles.data?.length === 0}
            onClose={() => setEditing(null)}
          />
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
  const t = useT();
  return (
    <div className="border-border/50 flex items-center gap-3 border-t px-4 py-3 first:border-t-0 sm:px-5">
      <InitialAvatar name={profile.name} seed={seed} src={profile.avatar_url} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="truncate text-sm font-medium">{profile.name}</span>
          {profile.is_primary ? (
            <Badge variant="secondary">{t("web.admin.users.detail.profiles.badge.primary")}</Badge>
          ) : null}
          {profile.is_child ? (
            <Badge variant="secondary">{t("web.admin.users.detail.profiles.badge.kids")}</Badge>
          ) : null}
          {profile.has_pin ? (
            <Badge variant="outline">{t("web.admin.users.detail.profiles.badge.pin")}</Badge>
          ) : null}
        </div>
        <div className="text-muted-foreground text-xs">
          {watching
            ? t("web.admin.users.detail.common.watchingNow")
            : profile.last_seen_at
              ? formatLastSeen(profile.last_seen_at)
              : t("web.admin.users.detail.common.noDeviceActivity")}
        </div>
      </div>
      {manage ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t("web.admin.users.detail.profiles.actionsFor", { name: profile.name })}
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-44">
            <DropdownMenuItem onSelect={onEdit}>
              {t("web.admin.users.detail.profiles.editProfile")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onSetPin}>
              {profile.has_pin
                ? t("web.admin.users.detail.profiles.changePin")
                : t("web.admin.users.detail.profiles.setPin")}
            </DropdownMenuItem>
            {profile.has_pin ? (
              <DropdownMenuItem onSelect={onRemovePin}>
                {t("web.admin.users.detail.profiles.removePin")}
              </DropdownMenuItem>
            ) : null}
            {profile.is_primary ? null : (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={onDelete}>
                  {t("web.admin.users.detail.profiles.deleteProfile")}
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
  createsPrimary,
  onClose,
}: {
  user: AdminUser;
  editing: Editing | null;
  /** The account has no profiles, so a created one becomes its primary. */
  createsPrimary: boolean;
  onClose: () => void;
}) {
  const t = useT();
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
      toast.error(t("web.admin.users.detail.profiles.loadFailedToast"));
      onClose();
    }
  }, [failed, onClose, t]);

  const open = editing?.mode === "create" || (editId !== null && full.data?.id === editId);
  const loading = editId !== null && !open && !failed;
  return (
    <>
      <Dialog open={loading} onOpenChange={(next) => (next ? undefined : onClose())}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>{t("web.admin.users.detail.profiles.editProfile")}</DialogTitle>
            <DialogDescription>{t("web.admin.users.detail.profiles.loading")}</DialogDescription>
          </DialogHeader>
          <Skeleton className="h-8 w-full" />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <ProfileEditorDialog
        open={open}
        profile={editId !== null ? full.data : null}
        createsPrimary={createsPrimary}
        libraries={libraries}
        advisoryAgeSupported={server.maxAdvisoryAgeSupported}
        requireAdvisoryAgeSupported={server.requireAdvisoryAgeSupported}
        save={save}
        onOpenChange={(next) => {
          if (!next) onClose();
        }}
        onSaveSuccess={(_, { mode }) => {
          if (mode === "edit") toast.success(t("web.admin.users.detail.profiles.updatedToast"));
        }}
      />
    </>
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
  const t = useT();
  const primary = profile?.is_primary ? "yes" : "no";
  return (
    <Dialog open={profile !== null} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>
            {profile?.has_pin
              ? t("web.admin.users.detail.profiles.changePinTitle", {
                  name: profile.name,
                })
              : t("web.admin.users.detail.profiles.setPinTitle", { name: profile?.name ?? "" })}
          </DialogTitle>
          <DialogDescription>
            {profile?.has_pin
              ? t("web.admin.users.detail.profiles.changePinDescription", { primary })
              : t("web.admin.users.detail.profiles.setPinDescription", { primary })}
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
  const t = useT();
  const update = useUpdateAdminUserProfile(userId);
  const [pin, setPin] = useState("");
  const [error, setError] = useState<string | null>(null);
  const pinId = useId();

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!/^\d{4}$/.test(pin)) {
      setError(t("web.admin.users.detail.profiles.pinInvalid"));
      return;
    }
    update.mutate(
      { id: profile.id, body: { pin } },
      {
        onSuccess: () => {
          toast.success(
            profile.has_pin
              ? t("web.admin.users.detail.profiles.pinChangedToast")
              : t("web.admin.users.detail.profiles.pinSetToast"),
          );
          onClose();
        },
      },
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="space-y-2">
        <Label htmlFor={pinId}>{t("web.admin.users.detail.profiles.newPin")}</Label>
        <Input
          id={pinId}
          type="password"
          inputMode="numeric"
          autoComplete="off"
          maxLength={4}
          placeholder={t("web.admin.users.detail.profiles.pinPlaceholder")}
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
          {t("common.cancel")}
        </Button>
        <Button type="submit" disabled={update.isPending}>
          {update.isPending
            ? t("web.admin.users.detail.common.saving")
            : t("web.admin.users.detail.profiles.savePin")}
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
  const t = useT();
  const update = useUpdateAdminUserProfile(userId);
  return (
    <ConfirmDialog
      open={profile !== null}
      onOpenChange={(next) => (next ? undefined : onClose())}
      title={
        profile
          ? t("web.admin.users.detail.profiles.removePinTitle", {
              name: profile.name,
            })
          : t("web.admin.users.detail.profiles.removePinTitleUnknown")
      }
      description={
        profile?.is_primary
          ? t("web.admin.users.detail.profiles.removePinPrimary")
          : t("web.admin.users.detail.profiles.removePinDescription")
      }
      confirmLabel={t("web.admin.users.detail.profiles.removePin")}
      isPending={update.isPending}
      onConfirm={() => {
        if (!profile) return;
        update.mutate(
          { id: profile.id, body: { pin: null } },
          {
            onSuccess: () => {
              toast.success(t("web.admin.users.detail.profiles.pinRemovedToast"));
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
  const t = useT();
  const remove = useDeleteAdminUserProfile(userId);
  return (
    <ConfirmDialog
      open={profile !== null}
      onOpenChange={(next) => (next ? undefined : onClose())}
      title={
        profile
          ? t("web.admin.users.detail.profiles.deleteTitle", { name: profile.name })
          : t("web.admin.users.detail.profiles.deleteTitleUnknown")
      }
      description={t("web.admin.users.detail.profiles.deleteDescription")}
      confirmLabel={t("web.admin.users.detail.profiles.deleteProfile")}
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
