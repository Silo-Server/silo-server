import { useId } from "react";

import type { AccessGroup, AdminPolicyDefaults, AdminUser, UpdateUserRequest } from "@/api/types";
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
import type { PolicyInheritHints } from "@/components/UserPolicyFields";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useAdminPolicyDefaults, useViewerIsOwner } from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { TOUCH_TARGETS } from "@/lib/touchTargets";

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

/** The space before a trailing note; kept out of the JSX so it is not read as text. */
const SPACE = " ";

const USERNAME_ROW = "web.admin.users.detail.access.signIn.username";
const EMAIL_ROW = "web.admin.users.detail.access.signIn.email";
const ROLE_ROW = "web.admin.users.detail.access.signIn.role";
const ENABLED_ROW = "web.admin.users.detail.access.signIn.canSignIn";
const PROFILES_ROW = "web.admin.users.detail.access.signIn.profilesAllowed";

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

function changedRows(draft: SignInDraft, base: SignInDraft): MessageKey[] {
  const rows: MessageKey[] = [];
  if (draft.username !== base.username) rows.push(USERNAME_ROW);
  if (draft.email !== base.email) rows.push(EMAIL_ROW);
  if (draft.role !== base.role) rows.push(ROLE_ROW);
  if (draft.enabled !== base.enabled) rows.push(ENABLED_ROW);
  if (rowChanged(draft.profiles, rowOverride(base.profiles) ?? null)) rows.push(PROFILES_ROW);
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
 * demoted admin joins the default group, or no group when none is the default.
 * Groups are undefined while they load.
 */
function inheritedProfileLimit(
  role: string,
  base: AdminUser,
  groups: AccessGroup[] | undefined,
  hints: PolicyInheritHints,
  defaults: AdminPolicyDefaults | undefined,
): number | undefined {
  if (role === base.role) return hints.max_profiles;
  if (role === "admin") return defaults?.admin.max_profiles;
  if (groups === undefined) return undefined;
  const defaultGroup = groups.find((group) => group.is_default);
  return defaultGroup ? defaultGroup.max_profiles : defaults?.ungrouped.max_profiles;
}

// A Custom limit with nothing valid in place of a saved override can't be
// saved; one in place of Default leaves the row on Default.
function profilesIncomplete(draft: SignInDraft, saved: SignInDraft): boolean {
  return rowOverride(draft.profiles) === undefined && rowOverride(saved.profiles) !== null;
}

function validate(t: Translate, draft: SignInDraft, base: AdminUser): string | null {
  if (draft.username.trim() === "") return t("web.admin.users.detail.access.signIn.enterUsername");
  if (!isValidEmail(draft.email)) return invalidEmailMessage();
  if (profilesIncomplete(draft, toDraft(base))) {
    return t("web.admin.users.detail.access.signIn.profilesInvalid");
  }
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
  policyLocked = false,
}: AccessCardProps & {
  /** The profile limit is access policy, which only the server owner changes on an admin. */
  policyLocked?: boolean;
}) {
  const t = useT();
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const profiles = useAdminUserProfiles(user.id);
  const policyDefaults = useAdminPolicyDefaults().data;
  const loadedGroups = useAccessGroups().data;
  const draft = useAccountCardDraft({
    id: "signin",
    editor,
    toDraft,
    toBody,
    changedRows,
    validate: (d, account) => validate(t, d, account),
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
    ? inheritedProfileLimit(d.role, base, loadedGroups, hints, policyDefaults)
    : hints.max_profiles;
  // Only the server owner may grant the admin role; nobody changes their own
  // role or disables themselves; the owner stays an enabled admin.
  const adminRoleLocked = !viewerIsOwner && base.role !== "admin";
  const ownAccount = base.id === viewerId;
  const changed = new Set(draft.changed);
  const saved = toDraft(base);
  const profilesInvalid = d !== undefined && profilesIncomplete(d, saved);

  function roleDescription(): string {
    if (ownAccount) return t("web.admin.users.detail.access.signIn.ownRole");
    if (adminRoleLocked) return t("web.admin.users.detail.access.signIn.ownerGrantsAdminRole");
    if (d?.role === "admin" && base.role !== "admin" && base.access_group_id !== null) {
      return t("web.admin.users.detail.access.signIn.adminLeavesGroup", {
        group: accessGroupName(base.access_group_id, groups),
      });
    }
    return t("web.admin.users.detail.access.signIn.ownerGrantsAdmin");
  }

  function enabledDescription(): string | undefined {
    if (base.is_owner) return t("web.admin.users.detail.access.signIn.ownerStaysEnabled");
    if (ownAccount) return t("web.admin.users.detail.access.signIn.cannotDisableSelf");
    return undefined;
  }

  const yesNo = (value: boolean) =>
    value
      ? t("web.admin.users.detail.access.value.yes")
      : t("web.admin.users.detail.access.value.no");
  const usedSuffix =
    used !== undefined ? (
      <span className="text-muted-foreground text-xs font-normal">
        {SPACE}
        {t("web.admin.users.detail.access.signIn.profilesUsedSuffix", { used: used })}
      </span>
    ) : null;

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
            label={<Label htmlFor={usernameId}>{t(USERNAME_ROW)}</Label>}
            changed={changed.has(USERNAME_ROW)}
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
            label={<Label htmlFor={emailId}>{t(EMAIL_ROW)}</Label>}
            changed={changed.has(EMAIL_ROW)}
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
            label={<Label htmlFor={roleId}>{t(ROLE_ROW)}</Label>}
            description={roleDescription()}
            changed={changed.has(ROLE_ROW)}
            value={
              <Select
                value={base.is_owner ? "owner" : d.role}
                onValueChange={(role) => draft.setDraft((prev) => ({ ...prev, role }))}
                disabled={base.is_owner || ownAccount}
              >
                <SelectTrigger id={roleId} className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent className={TOUCH_TARGETS}>
                  {base.is_owner ? (
                    <SelectItem value="owner">
                      {t("web.admin.users.detail.access.role.owner")}
                    </SelectItem>
                  ) : (
                    <>
                      <SelectItem value="user">
                        {t("web.admin.users.detail.access.role.user")}
                      </SelectItem>
                      <SelectItem value="admin" disabled={adminRoleLocked}>
                        {t("web.admin.users.detail.access.role.admin")}
                      </SelectItem>
                    </>
                  )}
                </SelectContent>
              </Select>
            }
          />
          {!base.password_login ? (
            <KeyValueRow
              label={t("web.admin.users.detail.access.signIn.password")}
              value={t("web.admin.users.detail.access.signIn.externalPassword")}
            />
          ) : null}
          <KeyValueRow
            label={<Label htmlFor={enabledId}>{t(ENABLED_ROW)}</Label>}
            description={enabledDescription()}
            changed={changed.has(ENABLED_ROW)}
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
                <Label htmlFor={profilesId}>{t(PROFILES_ROW)}</Label>
              ) : (
                t(PROFILES_ROW)
              )
            }
            description={
              used !== undefined
                ? t("web.admin.users.detail.access.signIn.profilesUsed", { used: used })
                : undefined
            }
            changed={changed.has(PROFILES_ROW)}
            value={
              <DefaultCustomSegment
                label={t(PROFILES_ROW)}
                defaultText={
                  inheritedProfiles === undefined ? undefined : String(inheritedProfiles)
                }
                custom={d.profiles.custom}
                disabled={policyLocked}
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
                    disabled={policyLocked}
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
                      {t("web.admin.users.detail.access.signIn.profilesMinHint")}
                    </span>
                  ) : null}
                </div>
              </DefaultCustomSegment>
            }
          />
        </>
      ) : (
        <>
          <KeyValueRow label={t(USERNAME_ROW)} value={user.username} />
          <KeyValueRow label={t(EMAIL_ROW)} value={user.email || "—"} />
          {!user.password_login ? (
            <KeyValueRow
              label={t("web.admin.users.detail.access.signIn.password")}
              value={t("web.admin.users.detail.access.signIn.externalPassword")}
            />
          ) : null}
          <KeyValueRow
            label={t(ROLE_ROW)}
            description={t("web.admin.users.detail.access.signIn.ownerGrantsAdmin")}
            value={roleLabel(user)}
          />
          <KeyValueRow
            label={t(ENABLED_ROW)}
            value={
              <>
                {yesNo(user.enabled)}
                {user.password_change_required ? (
                  <span className="text-muted-foreground text-xs font-normal">
                    {SPACE}
                    {t("web.admin.users.detail.access.signIn.mustChangePassword")}
                  </span>
                ) : null}
              </>
            }
          />
          <PolicyValueRow
            label={t(PROFILES_ROW)}
            value={
              <>
                {user.effective_policy.max_profiles}
                {usedSuffix}
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
