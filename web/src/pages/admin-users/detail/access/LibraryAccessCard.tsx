import { useId } from "react";
import { Link } from "react-router";
import { ArrowUpRight, Lock } from "lucide-react";

import type {
  AccessGroup,
  AdminUser,
  Library,
  RequestApprovalMode,
  UpdateUserRequest,
} from "@/api/types";
import {
  effectiveAccessGroupID,
  policyInheritHints,
  savedUserPolicyInheritHints,
} from "@/components/UserPolicyFields";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useRequestGroupLimit } from "@/hooks/queries/admin/requests";
import {
  PERMISSION_MARKER_EDIT,
  PERMISSION_METADATA_CURATION,
  hasAssignedPermission,
  setAssignedPermission,
} from "@/lib/permissions";
import { cn } from "@/lib/utils";

import { KeyValueRow } from "../ui";
import { EditableCard, type AccessCardProps } from "./EditableCard";
import { PolicyValueRow } from "./PolicyRow";
import {
  accessGroupName,
  formatAllowed,
  formatQuality,
  formatStreams,
  inheritedValueText,
  libraryListText,
  permissionLock,
  rowSource,
} from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

const PERMISSIONS = [
  {
    permission: PERMISSION_MARKER_EDIT,
    label: "Marker editing",
    description: "Intro, recap, credits, and preview markers",
  },
  {
    permission: PERMISSION_METADATA_CURATION,
    label: "Metadata curation",
    description: "Edit, refresh, and rematch metadata",
  },
] as const;

interface LibraryDraft {
  groupId: number | null;
  libraryIds: number[] | null;
  permissions: string[];
}

function toDraft(user: AdminUser): LibraryDraft {
  return {
    groupId: user.access_group_id,
    libraryIds: user.library_ids,
    permissions: [...(user.permissions ?? [])].sort(),
  };
}

function sameIds(a: number[] | null, b: number[] | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((id) => set.has(id));
}

function samePermissions(a: string[], b: string[]): boolean {
  return a.length === b.length && [...a].sort().join("\n") === [...b].sort().join("\n");
}

function changedRows(d: LibraryDraft, base: LibraryDraft): string[] {
  const rows: string[] = [];
  if (d.groupId !== base.groupId) rows.push("Access group");
  if (!sameIds(d.libraryIds, base.libraryIds)) rows.push("Libraries");
  for (const { permission, label } of PERMISSIONS) {
    if (
      hasAssignedPermission(d.permissions, permission) !==
      hasAssignedPermission(base.permissions, permission)
    ) {
      rows.push(label);
    }
  }
  return rows;
}

function toBody(d: LibraryDraft, user: AdminUser): UpdateUserRequest {
  const base = toDraft(user);
  const body: UpdateUserRequest = {};
  if (user.role !== "admin" && d.groupId !== base.groupId) body.access_group_id = d.groupId;
  if (!sameIds(d.libraryIds, base.libraryIds)) body.library_ids = d.libraryIds;
  if (!samePermissions(d.permissions, base.permissions)) body.permissions = d.permissions;
  return body;
}

/**
 * Rebases a library draft onto a reloaded account: the group, the library
 * list and each permission switch the admin left alone take the newer value.
 */
function rebase(d: LibraryDraft, oldBase: LibraryDraft, fresh: LibraryDraft): LibraryDraft {
  let permissions = fresh.permissions;
  for (const { permission } of PERMISSIONS) {
    const mine = hasAssignedPermission(d.permissions, permission);
    if (mine !== hasAssignedPermission(oldBase.permissions, permission)) {
      permissions = setAssignedPermission(permissions, permission, mine);
    }
  }
  return {
    groupId: d.groupId === oldBase.groupId ? fresh.groupId : d.groupId,
    libraryIds: sameIds(d.libraryIds, oldBase.libraryIds) ? fresh.libraryIds : d.libraryIds,
    permissions: [...permissions].sort(),
  };
}

const APPROVAL_SUMMARY: Partial<Record<RequestApprovalMode, string>> = {
  auto: "requests approved automatically",
  manual: "requests need approval",
};

/** "Family: Movies, TV · 1080p · 2 streams · requests need approval". */
function GroupSummary({ group, libraries }: { group: AccessGroup; libraries: Library[] }) {
  const limit = useRequestGroupLimit(group.id);
  const mode = limit.data?.approval_mode;
  const approval = mode === undefined ? undefined : APPROVAL_SUMMARY[mode];
  const parts = [
    libraryListText(group.library_ids, libraries),
    formatQuality(group.max_playback_quality),
    `${formatStreams(group.max_streams).toLowerCase()} streams`,
    approval,
  ].filter(Boolean);
  return (
    <p className="text-muted-foreground text-xs">
      {group.name}: {parts.join(" · ")}
    </p>
  );
}

export function LibraryAccessCard({
  user,
  editor,
  manageable,
  available,
  groups,
  libraries,
  ctx,
  hints,
}: AccessCardProps) {
  const draft = useAccountCardDraft({
    id: "library",
    editor,
    toDraft,
    toBody,
    changedRows,
    rebase,
  });
  const groupSelectId = useId();
  const radioName = useId();
  const d = draft.draft;
  const base = draft.base ?? user;
  const admin = base.role === "admin";
  const changed = new Set(draft.changed);
  const effective = user.effective_policy;

  const cardProps = {
    id: "library",
    description:
      user.role === "admin"
        ? "Admins don't use access groups. Unset values follow the server default."
        : undefined,
    manageable,
    available,
    canEdit: editor !== undefined,
    state: draft,
  } as const;

  if (draft.editing && d) {
    // Hints follow the group picked here; other cards see the change after it saves.
    const hintGroupId = effectiveAccessGroupID(base.role, d.groupId);
    const groupHints = policyInheritHints(hintGroupId, groups);
    const draftHints =
      hintGroupId === effectiveAccessGroupID(base.role, base.access_group_id)
        ? savedUserPolicyInheritHints(base, groupHints)
        : groupHints;
    const inheritedLibraries = draftHints?.library_ids;
    const pickedGroup =
      d.groupId === null ? undefined : groups.find((group) => group.id === d.groupId);
    const groupMissing = d.groupId !== null && pickedGroup === undefined;
    const draftAccount = { ...base, access_group_id: d.groupId };
    const choose = d.libraryIds !== null;
    const allIds = libraries.map((library) => library.id);

    return (
      <EditableCard {...cardProps}>
        {!admin ? (
          <div
            data-changed={changed.has("Access group") ? "true" : undefined}
            className="space-y-2 px-4 py-3.5 sm:px-5"
          >
            <Label htmlFor={groupSelectId}>Access group</Label>
            <div className="flex flex-wrap items-center gap-2">
              <Select
                value={d.groupId === null ? "none" : String(d.groupId)}
                onValueChange={(value) =>
                  draft.setDraft((prev) => ({
                    ...prev,
                    groupId: value === "none" ? null : Number(value),
                  }))
                }
              >
                <SelectTrigger id={groupSelectId} className="w-full sm:w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">No group</SelectItem>
                  {groupMissing ? (
                    <SelectItem value={String(d.groupId)}>#{d.groupId}</SelectItem>
                  ) : null}
                  {groups.map((group) => (
                    <SelectItem key={group.id} value={String(group.id)}>
                      {group.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {d.groupId !== null ? (
                <Button asChild variant="ghost" size="xs">
                  <Link to={`/admin/access-groups/${d.groupId}`}>
                    Open group
                    <ArrowUpRight aria-hidden="true" />
                  </Link>
                </Button>
              ) : null}
            </div>
            {pickedGroup ? <GroupSummary group={pickedGroup} libraries={libraries} /> : null}
          </div>
        ) : null}

        <fieldset
          data-changed={changed.has("Libraries") ? "true" : undefined}
          className={cn(
            "border-border/50 space-y-2 px-4 py-3.5 sm:px-5",
            !admin && "border-t",
            changed.has("Libraries") && "shadow-[inset_3px_0_0_var(--color-amber-500)]",
          )}
        >
          <legend className="sr-only">Libraries</legend>
          <div aria-hidden="true" className="text-sm font-medium">
            Libraries
          </div>
          <label
            className={cn(
              "border-border/70 flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5",
              !choose && "border-amber-500/40 bg-amber-500/5",
            )}
          >
            <input
              type="radio"
              name={radioName}
              className="mt-1 accent-amber-500"
              checked={!choose}
              onChange={() => draft.setDraft((prev) => ({ ...prev, libraryIds: null }))}
            />
            <span className="min-w-0">
              <span className="block text-sm font-medium">
                {hintGroupId === null ? "Use the server default" : "Use the group's libraries"}
              </span>
              {inheritedLibraries !== undefined ? (
                <span className="text-muted-foreground block text-xs">
                  {libraryListText(inheritedLibraries, libraries)}
                </span>
              ) : null}
            </span>
          </label>
          <div
            className={cn(
              "border-border/70 rounded-lg border px-3 py-2.5",
              choose && "border-amber-500/40 bg-amber-500/5",
            )}
          >
            <label className="flex cursor-pointer items-start gap-3">
              <input
                type="radio"
                name={radioName}
                className="mt-1 accent-amber-500"
                checked={choose}
                onChange={() =>
                  // Start from what the account can see now.
                  draft.setDraft((prev) => ({
                    ...prev,
                    libraryIds: effective.library_ids ?? allIds,
                  }))
                }
              />
              <span className="text-sm font-medium">Choose for this account</span>
            </label>
            {choose ? (
              <div className="mt-2.5 space-y-2 pl-7">
                <div className="flex flex-wrap gap-2">
                  {libraries.map((library) => {
                    const checked = d.libraryIds?.includes(library.id) ?? false;
                    return (
                      <label
                        key={library.id}
                        className={cn(
                          "border-border/70 inline-flex cursor-pointer items-center gap-2 rounded-full border px-3 py-1 text-[13px]",
                          checked && "border-amber-500/40 bg-amber-500/10",
                        )}
                      >
                        <input
                          type="checkbox"
                          className="accent-amber-500"
                          checked={checked}
                          onChange={(event) => {
                            const on = event.target.checked;
                            draft.setDraft((prev) => {
                              const current = prev.libraryIds ?? [];
                              const next = on
                                ? allIds.filter((id) => id === library.id || current.includes(id))
                                : current.filter((id) => id !== library.id);
                              return { ...prev, libraryIds: next };
                            });
                          }}
                        />
                        {library.name}
                      </label>
                    );
                  })}
                </div>
                {d.libraryIds?.length === 0 ? (
                  <p className="text-muted-foreground text-xs">
                    This account won't see any library.
                  </p>
                ) : null}
              </div>
            ) : null}
          </div>
        </fieldset>

        {PERMISSIONS.map(({ permission, label, description: help }) => {
          const lock = permissionLock(draftAccount, groups, permission);
          const assigned = hasAssignedPermission(d.permissions, permission);
          const switchId = `${groupSelectId}-${permission}`;
          // A locked switch shows what applies (off); an assignment the group
          // blocks is noted instead of reading as on.
          let description: string = help;
          if (lock.locked && assigned) {
            description = `Set on this account; has no effect under ${lock.groupName}`;
          } else if (!lock.locked && lock.groupName) {
            description = `The ${lock.groupName} group allows this`;
          }
          return (
            <KeyValueRow
              key={permission}
              label={<Label htmlFor={switchId}>{label}</Label>}
              description={description}
              changed={changed.has(label)}
              value={
                <span className="flex items-center gap-2">
                  {lock.locked ? (
                    <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
                      <Lock aria-hidden="true" className="size-3" />
                      {lock.groupName} group doesn't allow this
                    </span>
                  ) : null}
                  {lock.locked && assigned ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        draft.setDraft((prev) => ({
                          ...prev,
                          permissions: setAssignedPermission(prev.permissions, permission, false),
                        }))
                      }
                    >
                      Remove from account
                    </Button>
                  ) : null}
                  <Switch
                    id={switchId}
                    checked={assigned && !lock.locked}
                    disabled={lock.locked}
                    onCheckedChange={(on) =>
                      draft.setDraft((prev) => ({
                        ...prev,
                        permissions: setAssignedPermission(prev.permissions, permission, on),
                      }))
                    }
                  />
                </span>
              }
            />
          );
        })}
      </EditableCard>
    );
  }

  return (
    <EditableCard {...cardProps}>
      {user.role !== "admin" ? (
        <KeyValueRow
          label="Access group"
          value={
            user.access_group_id === null
              ? "No group"
              : accessGroupName(user.access_group_id, groups)
          }
        />
      ) : null}
      <PolicyValueRow
        label="Libraries"
        value={libraryListText(effective.library_ids, libraries)}
        source={rowSource(user, "libraries", ctx)}
        base={inheritedValueText("libraries", hints, ctx, libraries)}
      />
      {PERMISSIONS.map(({ permission, label, description: help }) => {
        const lock = permissionLock(user, groups, permission);
        const blocked = lock.locked && hasAssignedPermission(user.permissions, permission);
        return (
          <KeyValueRow
            key={permission}
            label={label}
            description={blocked ? `Blocked by the ${lock.groupName} group` : help}
            value={formatAllowed(hasAssignedPermission(effective.permissions, permission))}
          />
        );
      })}
    </EditableCard>
  );
}
