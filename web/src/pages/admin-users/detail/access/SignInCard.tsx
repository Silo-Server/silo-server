import { useId } from "react";

import type { AdminUser, UpdateUserRequest } from "@/api/types";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useViewerIsOwner } from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { INVALID_EMAIL_MESSAGE, isValidEmail } from "@/lib/email";

import { KeyValueRow } from "../ui";
import { EditableCard, type AccessCardProps } from "./EditableCard";
import { accessGroupName, parseWholeNumber, roleLabel } from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

interface SignInDraft {
  username: string;
  email: string;
  role: string;
  enabled: boolean;
  /** Kept as typed so a cleared box is an unsaved edit, not 0. */
  maxProfiles: string;
}

function toDraft(user: AdminUser): SignInDraft {
  return {
    username: user.username,
    email: user.email,
    role: user.role,
    enabled: user.enabled,
    maxProfiles: String(user.max_profiles),
  };
}

function changedRows(draft: SignInDraft, base: SignInDraft): string[] {
  const rows: string[] = [];
  if (draft.username !== base.username) rows.push("Username");
  if (draft.email !== base.email) rows.push("Email");
  if (draft.role !== base.role) rows.push("Role");
  if (draft.enabled !== base.enabled) rows.push("Can sign in");
  if (draft.maxProfiles.trim() !== base.maxProfiles) rows.push("Profiles allowed");
  return rows;
}

function toBody(draft: SignInDraft, base: AdminUser): UpdateUserRequest {
  const body: UpdateUserRequest = {};
  if (draft.username !== base.username) body.username = draft.username;
  if (draft.email !== base.email) body.email = draft.email;
  if (draft.role !== base.role) {
    body.role = draft.role;
    // Admins are never grouped: the server clears the group, and so does the save.
    if (draft.role === "admin") body.access_group_id = null;
  }
  if (draft.enabled !== base.enabled) body.enabled = draft.enabled;
  const profiles = parseWholeNumber(draft.maxProfiles, 1);
  if (profiles !== null && profiles !== base.max_profiles) body.max_profiles = profiles;
  return body;
}

function validate(draft: SignInDraft): string | null {
  if (draft.username.trim() === "") return "Enter a username.";
  if (!isValidEmail(draft.email)) return INVALID_EMAIL_MESSAGE;
  if (parseWholeNumber(draft.maxProfiles, 1) === null) return "Allow at least 1 profile.";
  return null;
}

export function SignInCard({ user, editor, manageable, available, groups }: AccessCardProps) {
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const profiles = useAdminUserProfiles(user.id);
  const draft = useAccountCardDraft({
    id: "signin",
    editor,
    toDraft,
    toBody,
    changedRows,
    validate,
  });
  const usernameId = useId();
  const emailId = useId();
  const roleId = useId();
  const enabledId = useId();
  const profilesId = useId();

  const used = profiles.data?.length;
  const d = draft.draft;
  const base = draft.base ?? user;
  // Only the server owner may grant the admin role; nobody changes their own
  // role or disables themselves; the owner stays an enabled admin.
  const adminRoleLocked = !viewerIsOwner && base.role !== "admin";
  const ownAccount = base.id === viewerId;
  const changed = new Set(draft.changed);

  function roleDescription(): string {
    if (ownAccount) return "You can't change your own role.";
    if (adminRoleLocked) return "Only the server owner can grant the admin role.";
    if (d?.role === "admin" && base.role !== "admin" && base.access_group_id !== null) {
      return `Admins don't use access groups. Saving removes this account from ${accessGroupName(base.access_group_id, groups)}.`;
    }
    return "Only the server owner can grant admin";
  }

  function enabledDescription(): string | undefined {
    if (base.is_owner) return "The server owner stays an enabled admin.";
    if (ownAccount) return "You can't disable your own account.";
    return undefined;
  }

  return (
    <EditableCard
      id="signin"
      manageable={manageable}
      available={available}
      canEdit={editor !== undefined}
      state={draft}
    >
      {draft.editing && d ? (
        <>
          <KeyValueRow
            label={<Label htmlFor={usernameId}>Username</Label>}
            changed={changed.has("Username")}
            value={
              <Input
                id={usernameId}
                className="w-64 max-w-full"
                required
                value={d.username}
                onChange={(event) =>
                  draft.setDraft((prev) => ({ ...prev, username: event.target.value }))
                }
              />
            }
          />
          <KeyValueRow
            label={<Label htmlFor={emailId}>Email</Label>}
            changed={changed.has("Email")}
            value={
              <Input
                id={emailId}
                type="email"
                className="w-64 max-w-full"
                required
                value={d.email}
                onChange={(event) =>
                  draft.setDraft((prev) => ({ ...prev, email: event.target.value }))
                }
              />
            }
          />
          <KeyValueRow
            label={<Label htmlFor={roleId}>Role</Label>}
            description={roleDescription()}
            changed={changed.has("Role")}
            value={
              <Select
                value={base.is_owner ? "owner" : d.role}
                onValueChange={(role) => draft.setDraft((prev) => ({ ...prev, role }))}
                disabled={base.is_owner || ownAccount}
              >
                <SelectTrigger id={roleId} className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {base.is_owner ? (
                    <SelectItem value="owner">Owner</SelectItem>
                  ) : (
                    <>
                      <SelectItem value="user">User</SelectItem>
                      <SelectItem value="admin" disabled={adminRoleLocked}>
                        Admin
                      </SelectItem>
                    </>
                  )}
                </SelectContent>
              </Select>
            }
          />
          {!base.password_login ? (
            <KeyValueRow label="Password" value="Managed by an external sign-in provider" />
          ) : null}
          <KeyValueRow
            label={<Label htmlFor={enabledId}>Can sign in</Label>}
            description={enabledDescription()}
            changed={changed.has("Can sign in")}
            value={
              <Switch
                id={enabledId}
                checked={d.enabled}
                disabled={base.is_owner || ownAccount}
                onCheckedChange={(enabled) => draft.setDraft((prev) => ({ ...prev, enabled }))}
              />
            }
          />
          <KeyValueRow
            label={<Label htmlFor={profilesId}>Profiles allowed</Label>}
            description={used !== undefined ? `${used} used` : undefined}
            changed={changed.has("Profiles allowed")}
            value={
              <Input
                id={profilesId}
                type="number"
                inputMode="numeric"
                min={1}
                step={1}
                className="w-24"
                value={d.maxProfiles}
                aria-invalid={parseWholeNumber(d.maxProfiles, 1) === null ? true : undefined}
                onChange={(event) =>
                  draft.setDraft((prev) => ({ ...prev, maxProfiles: event.target.value }))
                }
              />
            }
          />
        </>
      ) : (
        <>
          <KeyValueRow label="Username" value={user.username} />
          <KeyValueRow label="Email" value={user.email || "—"} />
          {!user.password_login ? (
            <KeyValueRow label="Password" value="Managed by an external sign-in provider" />
          ) : null}
          <KeyValueRow
            label="Role"
            description="Only the server owner can grant admin"
            value={roleLabel(user)}
          />
          <KeyValueRow
            label="Can sign in"
            value={
              <>
                {user.enabled ? "Yes" : "No"}
                {user.password_change_required ? (
                  <span className="text-muted-foreground text-xs font-normal">
                    {" "}
                    · must change password
                  </span>
                ) : null}
              </>
            }
          />
          <KeyValueRow
            label="Profiles allowed"
            value={
              <>
                {user.max_profiles}
                {used !== undefined ? (
                  <span className="text-muted-foreground text-xs font-normal"> · {used} used</span>
                ) : null}
              </>
            }
          />
        </>
      )}
    </EditableCard>
  );
}
