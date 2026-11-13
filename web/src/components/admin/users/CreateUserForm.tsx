import { useId, useRef, useState } from "react";
import type { FormEvent } from "react";
import { flushSync } from "react-dom";
import { XIcon } from "lucide-react";

import type { CreateUserRequest } from "@/api/types";
import { captureAdminUserAuthority } from "@/api/v2/adminUsers";
import {
  PolicyAccessFields,
  PolicyLimitFields,
  effectiveAccessGroupID,
  policyCreateFields,
  policyDefaultSource,
  policyInheritHints,
  policyStateFromUser,
  type LimitPolicyKey,
} from "@/components/UserPolicyFields";
import { Button } from "@/components/ui/button";
import { DialogClose, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAdminPolicyDefaults,
  useAdminUserCapabilities,
  useCreateUser,
  useViewerIsOwner,
} from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { useT } from "@/i18n";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { newPasswordLengthProblem } from "@/lib/newPassword";
import {
  PERMISSION_MARKER_EDIT,
  PERMISSION_METADATA_CURATION,
  hasAssignedPermission,
  setAssignedPermission,
} from "@/lib/permissions";
import { cn } from "@/lib/utils";

// The dialog's tabs: 44px tall and padded like the library editor's below sm.
const DIALOG_TAB_CLASS = "flex-none px-1 max-sm:h-11 max-sm:px-3";

/*
 * Below sm the fields get 44px touch targets. The policy fields are shared
 * with the account page, so they are sized from here rather than in their own
 * components: inputs and selects grow, a switch's hit area grows past its
 * track, and the row holding a switch is tall enough for that hit area.
 */
const PHONE_TOUCH_FIELDS_CLASS = cn(
  "max-sm:[&_[data-slot=input]]:h-11",
  "max-sm:[&_[data-slot=select-trigger]]:min-h-11!",
  "max-sm:[&_:has(>[data-slot=switch])]:min-h-11",
  "max-sm:[&_[data-slot=switch]]:relative max-sm:[&_[data-slot=switch]]:after:absolute max-sm:[&_[data-slot=switch]]:after:-inset-x-1.5 max-sm:[&_[data-slot=switch]]:after:-inset-y-[13px]",
);

/**
 * The Add User dialog. Below sm it fills the screen, with its own 44px close
 * button, a scrolling body and a pinned Save bar; from sm up it is a centered box.
 */
export function CreateUserDialogContent({
  onClose,
  onBusy,
}: {
  onClose: () => void;
  onBusy: (busy: boolean) => void;
}) {
  const t = useT();
  return (
    <DialogContent
      className={cn(
        "sm:max-w-2xl",
        "max-sm:inset-0 max-sm:flex max-sm:h-dvh max-sm:max-h-none max-sm:w-dvw max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:flex-col max-sm:gap-0 max-sm:overflow-hidden max-sm:rounded-none max-sm:border-0 max-sm:p-0",
        // The header carries its own 44px close button on a phone.
        "max-sm:[&>[data-slot=dialog-close]]:hidden",
      )}
    >
      <DialogHeader className="max-sm:border-border max-sm:shrink-0 max-sm:border-b max-sm:ps-4 max-sm:pe-2 max-sm:pt-[calc(0.75rem+env(safe-area-inset-top))] max-sm:pb-3">
        <div className="flex items-center gap-3">
          <DialogTitle className="min-w-0 flex-1 max-sm:text-start">
            {t("web.admin.users.list.createDialogTitle")}
          </DialogTitle>
          <DialogClose asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-muted-foreground size-11 shrink-0 sm:hidden"
            >
              <XIcon className="size-5" />
              <span className="sr-only">{t("web.ui.close")}</span>
            </Button>
          </DialogClose>
        </div>
      </DialogHeader>
      <CreateUserForm onBusy={onBusy} onClose={onClose} />
    </DialogContent>
  );
}

function CreateUserForm({
  onClose,
  onBusy,
}: {
  onClose: () => void;
  onBusy: (busy: boolean) => void;
}) {
  const t = useT();
  const [authority] = useState(captureAdminUserAuthority);
  const busy = useRef(false);
  const [error, setError] = useState("");
  const capabilities = useAdminUserCapabilities();
  // Only the server Owner may grant the admin role; the server refuses anyone else.
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const adminRoleLocked = !viewerIsOwner;
  const [createDefaultProfile, setCreateDefaultProfile] = useState(true);

  const { data: libraries = [] } = useAdminLibraries();
  const { data: accessGroups = [], isSuccess: accessGroupsLoaded } = useAccessGroups();
  const { data: policyDefaults } = useAdminPolicyDefaults();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [requirePasswordChange, setRequirePasswordChange] = useState(false);
  const [role, setRole] = useState("user");
  const [permissions, setPermissions] = useState<string[]>([PERMISSION_MARKER_EDIT]);
  // Policy fields inherit from the access group unless explicitly overridden.
  const [policy, setPolicy] = useState(() => policyStateFromUser(null));
  const [invalidLimitDrafts, setInvalidLimitDrafts] = useState<ReadonlyMap<LimitPolicyKey, string>>(
    new Map(),
  );
  const [dialogTab, setDialogTab] = useState("account");
  const formRef = useRef<HTMLFormElement>(null);
  function handleInvalidLimitDraftChange(key: LimitPolicyKey, draft: string | undefined) {
    setInvalidLimitDrafts((current) => {
      if (current.get(key) === draft) return current;
      const next = new Map(current);
      if (draft === undefined) next.delete(key);
      else next.set(key, draft);
      return next;
    });
  }
  const usernameId = useId();
  const emailId = useId();
  const passwordId = useId();
  const requireChangeId = useId();
  const roleId = useId();
  const markerEditId = useId();
  const metadataCurationId = useId();
  const accessGroupSelectId = useId();
  const createMutation = useCreateUser();
  // The group picker starts on the default group (undefined until picked),
  // which is where the server would place the account anyway. An admin stays
  // ungrouped (auth.Repository create), so the picker is disabled for admins.
  const [pickedGroupID, setPickedGroupID] = useState<number | null | undefined>(undefined);
  const defaultGroupID = accessGroups.find((group) => group.is_default)?.id ?? null;
  const selectedGroupID = pickedGroupID === undefined ? defaultGroupID : pickedGroupID;
  const inheritGroupID = effectiveAccessGroupID(role, selectedGroupID);
  // Until the group list loads, the default group such an account joins is
  // unknown, and so is what it inherits; it is not the no-group defaults.
  const awaitingDefaultGroup =
    pickedGroupID === undefined && role !== "admin" && !accessGroupsLoaded;
  const hintSource = awaitingDefaultGroup ? "group" : policyDefaultSource(role, inheritGroupID);
  const inheritHints = awaitingDefaultGroup
    ? undefined
    : policyInheritHints(role, inheritGroupID, accessGroups, policyDefaults);
  // The group to send: none while the default group is still unknown, so the
  // server applies its own default instead of an accidental "no group".
  const groupToSend = awaitingDefaultGroup
    ? undefined
    : effectiveAccessGroupID(role, selectedGroupID);
  const accessGroupValue =
    awaitingDefaultGroup || role === "admin" || selectedGroupID === null
      ? "none"
      : String(selectedGroupID);
  // Creating an account can't choose "no group": the server treats a missing or
  // null group alike and places the account in the default group. Offer it only
  // when there is no default group to show instead.
  const offerNoGroup = awaitingDefaultGroup || defaultGroupID === null;
  const selectedGroupMissing =
    selectedGroupID !== null &&
    accessGroupsLoaded &&
    !accessGroups.some((group) => group.id === selectedGroupID);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (
      busy.current ||
      !capabilities.data?.available ||
      (createDefaultProfile && !capabilities.data.default_profile)
    )
      return;
    if (!isValidEmail(email)) {
      setError(invalidEmailMessage());
      return;
    }
    // The server's refusal names the limit in Huma's words, which a translated
    // interface replaces with "This value is out of range."
    const passwordProblem = newPasswordLengthProblem(password);
    if (passwordProblem) {
      setDialogTab("account");
      setError(passwordProblem);
      return;
    }
    // A cleared or half-typed limit is not in the policy state, which still
    // holds the last valid value; saving now would send that instead.
    if (invalidLimitDrafts.size > 0) {
      // The Limits fields mount only once the tab has rendered, so render it
      // now and then let the browser point at the invalid field.
      flushSync(() => setDialogTab("limits"));
      formRef.current?.reportValidity();
      return;
    }
    busy.current = true;
    onBusy(true);
    setError("");
    try {
      const body: CreateUserRequest = {
        username,
        email,
        password,
        ...(requirePasswordChange ? { require_password_change: true } : {}),
        role,
        permissions,
        create_default_profile: createDefaultProfile,
        ...policyCreateFields(policy),
        ...(typeof groupToSend === "number" ? { access_group_id: groupToSend } : {}),
      };
      await createMutation.mutateAsync({ body, profileContext: authority });
      createMutation.reset();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("web.admin.users.list.create.saveFailed"));
    } finally {
      busy.current = false;
      onBusy(false);
    }
  }

  return (
    <form
      ref={formRef}
      onSubmit={handleSubmit}
      className={cn(
        "flex max-h-[70vh] flex-col max-sm:max-h-none max-sm:min-h-0 max-sm:flex-1",
        PHONE_TOUCH_FIELDS_CLASS,
      )}
    >
      {error && (
        <p role="alert" className="max-sm:px-4 max-sm:pt-3 max-sm:break-words">
          {error}
        </p>
      )}
      <label className="mb-3 flex items-center gap-2 max-sm:mb-0 max-sm:min-h-11 max-sm:px-4 max-sm:py-1">
        <input
          type="checkbox"
          className="max-sm:size-5 max-sm:shrink-0"
          checked={createDefaultProfile}
          onChange={(event) => setCreateDefaultProfile(event.target.checked)}
        />
        {capabilities.data?.default_profile
          ? t("web.admin.users.list.create.defaultProfile")
          : t("web.admin.users.list.create.defaultProfileUnavailable")}
      </label>

      <Tabs value={dialogTab} onValueChange={setDialogTab} className="min-h-0 flex-1 max-sm:gap-0">
        {/* Below sm the tab strip scrolls sideways if the labels outgrow the screen. */}
        <div className="max-sm:shrink-0 max-sm:overflow-x-auto max-sm:[scrollbar-width:none] max-sm:[&::-webkit-scrollbar]:hidden">
          <TabsList
            variant="line"
            className="border-border mb-4 w-full justify-start border-b pb-1 max-sm:mb-0 max-sm:h-auto! max-sm:min-w-max max-sm:px-2"
          >
            <TabsTrigger value="account" className={DIALOG_TAB_CLASS}>
              {t("web.admin.users.list.create.tab.account")}
            </TabsTrigger>
            <TabsTrigger value="access" className={DIALOG_TAB_CLASS}>
              {t("web.admin.users.list.create.tab.access")}
            </TabsTrigger>
            <TabsTrigger value="limits" className={DIALOG_TAB_CLASS}>
              {t("web.admin.users.list.create.tab.limits")}
            </TabsTrigger>
          </TabsList>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto pe-1 max-sm:px-4 max-sm:py-4">
          <TabsContent value="account" className="mt-0 space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor={usernameId}>{t("web.admin.users.list.create.username")}</Label>
                <Input
                  id={usernameId}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor={emailId}>{t("web.admin.users.list.create.email")}</Label>
                <Input
                  id={emailId}
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor={passwordId}>{t("web.admin.users.list.create.password")}</Label>
                <Input
                  id={passwordId}
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                />
                <div className="flex items-center gap-2">
                  <Switch
                    id={requireChangeId}
                    checked={requirePasswordChange && password !== ""}
                    disabled={password === ""}
                    onCheckedChange={setRequirePasswordChange}
                  />
                  <Label htmlFor={requireChangeId} className="text-xs font-normal">
                    {t("web.admin.users.list.create.requirePasswordChange")}
                  </Label>
                </div>
              </div>
              <div className="space-y-2">
                <Label htmlFor={roleId}>{t("web.admin.users.list.create.role")}</Label>
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger id={roleId}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">
                      {t("web.admin.users.list.create.role.user")}
                    </SelectItem>
                    <SelectItem value="admin" disabled={adminRoleLocked}>
                      {t("web.admin.users.list.create.role.admin")}
                    </SelectItem>
                  </SelectContent>
                </Select>
                {adminRoleLocked && (
                  <p className="text-muted-foreground text-xs">
                    {t("web.admin.users.list.create.adminOwnerOnly")}
                  </p>
                )}
              </div>
            </div>
          </TabsContent>

          <TabsContent value="access" className="mt-0 space-y-4">
            <div className="space-y-2">
              <Label htmlFor={accessGroupSelectId}>{t("web.admin.users.list.create.group")}</Label>
              <Select
                value={accessGroupValue}
                onValueChange={(value) => {
                  setPickedGroupID(value === "none" ? null : Number(value));
                }}
                disabled={role === "admin" || awaitingDefaultGroup}
              >
                <SelectTrigger id={accessGroupSelectId} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {offerNoGroup && (
                    <SelectItem value="none">
                      {awaitingDefaultGroup
                        ? t("web.admin.users.list.create.defaultGroup")
                        : t("web.admin.users.list.noGroup")}
                    </SelectItem>
                  )}
                  {selectedGroupMissing && (
                    <SelectItem value={String(selectedGroupID)}>
                      {t("web.admin.users.list.create.missingGroup", {
                        id: selectedGroupID,
                      })}
                    </SelectItem>
                  )}
                  {accessGroups.map((group) => (
                    <SelectItem key={group.id} value={String(group.id)}>
                      {group.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {role === "admin" && (
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.users.list.create.adminNoGroups")}
                </p>
              )}
            </div>
            <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
              <div>
                <Label htmlFor={markerEditId}>{t("web.admin.users.list.create.markerEdit")}</Label>
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.users.list.create.markerEditHelp")}
                </p>
              </div>
              <Switch
                id={markerEditId}
                checked={hasAssignedPermission(permissions, PERMISSION_MARKER_EDIT)}
                onCheckedChange={(checked) =>
                  setPermissions((current) =>
                    setAssignedPermission(current, PERMISSION_MARKER_EDIT, checked),
                  )
                }
              />
            </div>
            <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
              <div>
                <Label htmlFor={metadataCurationId}>
                  {t("web.admin.users.list.create.metadataCuration")}
                </Label>
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.users.list.create.metadataCurationHelp")}
                </p>
              </div>
              <Switch
                id={metadataCurationId}
                checked={hasAssignedPermission(permissions, PERMISSION_METADATA_CURATION)}
                onCheckedChange={(checked) =>
                  setPermissions((current) =>
                    setAssignedPermission(current, PERMISSION_METADATA_CURATION, checked),
                  )
                }
              />
            </div>
            <PolicyAccessFields
              state={policy}
              onChange={setPolicy}
              source={hintSource}
              effective={inheritHints}
              libraries={libraries}
            />
          </TabsContent>

          <TabsContent value="limits" className="mt-0 space-y-4">
            <PolicyLimitFields
              state={policy}
              onChange={setPolicy}
              source={hintSource}
              effective={inheritHints}
              invalidLimitDrafts={invalidLimitDrafts}
              onInvalidLimitDraftChange={handleInvalidLimitDraftChange}
            />
          </TabsContent>
        </div>
      </Tabs>

      <div className="border-border mt-4 border-t pt-4 max-sm:mt-0 max-sm:flex max-sm:shrink-0 max-sm:gap-2 max-sm:px-4 max-sm:pt-3 max-sm:pb-[calc(0.75rem+env(safe-area-inset-bottom))]">
        <DialogClose asChild>
          <Button type="button" variant="ghost" className="h-11 sm:hidden">
            {t("common.cancel")}
          </Button>
        </DialogClose>
        <Button
          type="submit"
          className="w-full max-sm:h-11 max-sm:w-auto max-sm:min-w-0 max-sm:flex-1"
          disabled={
            createMutation.isPending ||
            !capabilities.data?.available ||
            (createDefaultProfile && !capabilities.data.default_profile)
          }
        >
          {createMutation.isPending ? t("web.admin.users.list.create.saving") : t("common.save")}
        </Button>
      </div>
    </form>
  );
}
