import { useId } from "react";

import type { AccessGroup, AdminUser, UpdateUserRequest } from "@/api/types";
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
import { policyInheritHints, type PolicyInheritHints } from "@/components/UserPolicyFields";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useViewerIsOwner } from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { INVALID_EMAIL_MESSAGE, isValidEmail } from "@/lib/email";

import { KeyValueRow } from "../ui";
import { EditableCard, type AccessCardProps } from "./EditableCard";
import { DefaultCustomSegment, PolicyValueRow } from "./PolicyRow";
import {
  accessGroupName,
  inheritedValueText,
  parseWholeNumber,
  roleLabel,
  rowChanged,
  rowDraft,
  rowOverride,
  rowSource,
  type RowDraft,
} from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

const PROFILES_LABEL = "Profiles allowed";

interface SignInDraft {
  username: string;
  email: string;
  role: string;
  enabled: boolean;
  /** The profile limit override; `text` keeps a cleared box an unsaved edit, not 0. */
  profiles: RowDraft<number>;
}

function toDraft(user: AdminUser): SignInDraft {
  return {
    username: user.username,
    email: user.email,
    role: user.role,
    enabled: user.enabled,
    profiles: rowDraft(
      user.max_profiles,
      user.max_profiles === null ? "" : String(user.max_profiles),
    ),
  };
}

function changedRows(draft: SignInDraft, base: SignInDraft): string[] {
  const rows: string[] = [];
  if (draft.username !== base.username) rows.push("Username");
  if (draft.email !== base.email) rows.push("Email");
  if (draft.role !== base.role) rows.push("Role");
  if (draft.enabled !== base.enabled) rows.push("Can sign in");
  if (rowChanged(draft.profiles, rowOverride(base.profiles) ?? null)) rows.push(PROFILES_LABEL);
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
  const profiles = rowOverride(draft.profiles);
  if (profiles !== undefined && rowChanged(draft.profiles, base.max_profiles)) {
    body.max_profiles = profiles;
  }
  return body;
}

/**
 * The profile limit Default resolves to once the draft saves. A role change
 * moves the account out of or into a group: an admin is never grouped, and a
 * demoted admin joins the default group.
 */
function inheritedProfileLimit(
  role: string,
  base: AdminUser,
  groups: AccessGroup[],
  hints: PolicyInheritHints,
): number | undefined {
  if (role === base.role) return hints.max_profiles;
  if (role === "admin") return policyInheritHints(null, groups)?.max_profiles;
  return groups.find((group) => group.is_default)?.max_profiles;
}

// A Custom limit with nothing valid in place of a saved override can't be
// saved; one in place of Default leaves the row on Default.
function profilesIncomplete(draft: SignInDraft, saved: SignInDraft): boolean {
  return rowOverride(draft.profiles) === undefined && rowOverride(saved.profiles) !== null;
}

function validate(draft: SignInDraft, base: AdminUser): string | null {
  if (draft.username.trim() === "") return "Enter a username.";
  if (!isValidEmail(draft.email)) return INVALID_EMAIL_MESSAGE;
  if (profilesIncomplete(draft, toDraft(base)))
    return "Allow at least 1 profile, or choose Default.";
  return null;
}

export function SignInCard({
  user,
  editor,
  manageable,
  available,
  groups,
  libraries,
  ctx,
  hints,
}: AccessCardProps) {
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
  const inheritedProfiles = d
    ? inheritedProfileLimit(d.role, base, groups, hints)
    : hints.max_profiles;
  // Only the server owner may grant the admin role; nobody changes their own
  // role or disables themselves; the owner stays an enabled admin.
  const adminRoleLocked = !viewerIsOwner && base.role !== "admin";
  const ownAccount = base.id === viewerId;
  const changed = new Set(draft.changed);
  const saved = toDraft(base);
  const profilesInvalid = d !== undefined && profilesIncomplete(d, saved);

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
      invalid={profilesInvalid}
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
            label={
              d.profiles.custom ? (
                <Label htmlFor={profilesId}>{PROFILES_LABEL}</Label>
              ) : (
                PROFILES_LABEL
              )
            }
            description={used !== undefined ? `${used} used` : undefined}
            changed={changed.has(PROFILES_LABEL)}
            value={
              <DefaultCustomSegment
                label={PROFILES_LABEL}
                defaultText={
                  inheritedProfiles === undefined ? undefined : String(inheritedProfiles)
                }
                custom={d.profiles.custom}
                onCustomChange={(on) =>
                  // Custom starts from the inherited limit; with that unknown
                  // the box starts empty.
                  draft.setDraft((prev) => ({
                    ...prev,
                    profiles: on
                      ? {
                          custom: true,
                          value: inheritedProfiles ?? null,
                          text: inheritedProfiles === undefined ? "" : String(inheritedProfiles),
                        }
                      : { custom: false, value: null, text: "" },
                  }))
                }
              >
                <div className="flex flex-col items-end gap-1">
                  <Input
                    id={profilesId}
                    type="number"
                    inputMode="numeric"
                    min={1}
                    step={1}
                    className="w-24"
                    value={d.profiles.text ?? ""}
                    aria-invalid={d.profiles.value === null ? true : undefined}
                    onChange={(event) => {
                      const text = event.target.value;
                      draft.setDraft((prev) => ({
                        ...prev,
                        profiles: { custom: true, text, value: parseWholeNumber(text, 1) },
                      }));
                    }}
                  />
                  {d.profiles.value === null ? (
                    <span className="text-muted-foreground text-xs">
                      Enter a whole number of at least 1, or switch back to Default.
                    </span>
                  ) : null}
                </div>
              </DefaultCustomSegment>
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
          <PolicyValueRow
            label={PROFILES_LABEL}
            value={
              <>
                {user.effective_policy.max_profiles}
                {used !== undefined ? (
                  <span className="text-muted-foreground text-xs font-normal"> · {used} used</span>
                ) : null}
              </>
            }
            source={rowSource(user, "maxProfiles", ctx)}
            base={inheritedValueText("maxProfiles", hints, ctx, libraries)}
          />
        </>
      )}
    </EditableCard>
  );
}
