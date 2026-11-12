import { useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";

import type { AccessGroup, AdminUser } from "@/api/types";
import {
  adminUserScope,
  captureAdminUserAuthority,
  getAdminUser,
  updateAdminUser,
} from "@/api/v2/adminUsers";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { accessGroupsKey } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useRequestSettings } from "@/hooks/queries/admin/requests";
import { useRequestGroupLimits } from "@/hooks/queries/admin/requestGroupLimits";
import { adminUsersKey, useAdminUsers } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import {
  NO_GROUP_POLICY,
  groupPolicyChanges,
  requestTermsChanges,
} from "@/lib/accessGroupPolicyChanges";

/** Members shown at first, and how many more each "Show more" reveals. */
const MEMBER_PAGE_SIZE = 10;
/** Accounts the Add users picker shows at first, and per "Show more". */
const PICKER_PAGE_SIZE = 50;

/** A pending group change awaiting the admin's confirmation. */
interface GroupMove {
  users: AdminUser[];
  target: AccessGroup;
}

function matchesQuery(user: AdminUser, needle: string) {
  return (
    user.username.toLocaleLowerCase().includes(needle) ||
    (user.email ?? "").toLocaleLowerCase().includes(needle)
  );
}

/**
 * The group editor's member list. Admins can move selected members to another
 * group, or add eligible users to this one; both confirm first, showing what
 * changes for the members who inherit the group's settings.
 *
 * The account list is read whole (useAdminUsers follows every page), so the
 * member list and the picker search and count every account; only rendering
 * is paged, with "Show more".
 */
export function AccessGroupMembers({
  group,
  groups,
  groupsReady,
  onRetryGroups,
}: {
  group: AccessGroup;
  groups: AccessGroup[];
  // The group list names each member's current group; without it the move
  // confirmation can't say what changes, so moves wait for it.
  groupsReady: boolean;
  onRetryGroups?: () => void;
}) {
  const t = useT();
  const headingId = useId();
  const users = useAdminUsers();
  const libraries = useAdminLibraries();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [visibleCount, setVisibleCount] = useState(MEMBER_PAGE_SIZE);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [moveTarget, setMoveTarget] = useState("");
  const [adding, setAdding] = useState(false);
  const [pending, setPending] = useState<GroupMove | null>(null);
  const [saving, setSaving] = useState(false);
  const [failures, setFailures] = useState<string[]>([]);
  const [failedAdding, setFailedAdding] = useState(false);
  // Users an add couldn't move in, pre-selected when Add users reopens.
  const [addRetryIds, setAddRetryIds] = useState<Set<number>>(new Set());

  const members = useMemo(
    () =>
      (users.data ?? [])
        .filter(
          (user) => user.role !== "admin" && String(user.access_group_id) === String(group.id),
        )
        .sort((a, b) => a.username.localeCompare(b.username)),
    [users.data, group.id],
  );
  const searchable = members.length > MEMBER_PAGE_SIZE;
  const needle = searchable ? query.trim().toLocaleLowerCase() : "";
  const matches = needle ? members.filter((member) => matchesQuery(member, needle)) : members;
  const shown = matches.slice(0, visibleCount);
  const otherGroups = groups.filter((candidate) => String(candidate.id) !== String(group.id));
  // Selection survives searching and paging, so a move can include members
  // that aren't on screen; the list says how many.
  const selectedMembers = members.filter((member) => selected.has(member.id));
  const shownIds = new Set(shown.map((member) => member.id));
  const hiddenSelected = selectedMembers.filter((member) => !shownIds.has(member.id)).length;
  // The target as the shared group list holds it now: a refresh while the
  // confirmation is open shows, and applies, its current policy rather than
  // the one captured when the move was chosen. The move itself carries only
  // the group id, so what's confirmed must be what the server will apply.
  const pendingMove: GroupMove | null = pending && {
    ...pending,
    target:
      groups.find((candidate) => String(candidate.id) === String(pending.target.id)) ??
      pending.target,
  };

  function toggle(id: number, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  async function applyMove(move: GroupMove) {
    const isAdd = String(move.target.id) === String(group.id);
    setSaving(true);
    setFailures([]);
    const context = captureAdminUserAuthority();
    const failed: string[] = [];
    const failedIds = new Set<number>();
    let moved = 0;
    for (const user of move.users) {
      try {
        const editor = await getAdminUser(user.id, context);
        // The confirmation's changes depend on the account's group and its
        // request override; when either changed since, it no longer applies.
        if (
          editor.user.access_group_id !== user.access_group_id ||
          (editor.user.requests_allowed ?? null) !== (user.requests_allowed ?? null)
        ) {
          throw new Error(t("web.admin.access.groups.members.groupChanged"));
        }
        await updateAdminUser(editor, { access_group_id: move.target.id });
        moved++;
      } catch (err) {
        failedIds.add(user.id);
        failed.push(
          t("web.admin.access.groups.members.failure", {
            username: user.username,
            reason:
              err instanceof Error
                ? err.message
                : isAdd
                  ? t("web.admin.access.groups.members.couldNotAdd")
                  : t("web.admin.access.groups.members.couldNotMove"),
          }),
        );
      }
    }
    const scope = adminUserScope(context);
    void queryClient.invalidateQueries({ queryKey: adminUsersKey(scope) });
    void queryClient.invalidateQueries({ queryKey: accessGroupsKey(scope) });
    setSaving(false);
    setPending(null);
    // Keep the users that failed ready for a retry: members moving out stay
    // selected with the same target; users being added are pre-selected the
    // next time Add users opens.
    if (isAdd) {
      setAddRetryIds(failedIds);
    } else {
      setSelected(failedIds);
      // A later move supersedes an earlier failed add's retry selection.
      setAddRetryIds(new Set());
      if (failedIds.size === 0) setMoveTarget("");
    }
    setFailedAdding(isAdd);
    setFailures(failed);
    if (moved > 0) {
      toast.success(
        t(
          isAdd ? "web.admin.access.groups.members.added" : "web.admin.access.groups.members.moved",
          {
            count: moved,
            group: move.target.name,
          },
        ),
      );
    }
  }

  return (
    <section
      className="surface-panel space-y-3 rounded-2xl border-0 p-5"
      aria-labelledby={headingId}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="flex items-center gap-2">
            <h2 id={headingId} className="text-sm font-semibold">
              {t("web.admin.access.groups.members.heading")}
            </h2>
            {users.isSuccess && members.length > 0 && (
              <span className="bg-secondary text-secondary-foreground rounded-full px-2 py-0.5 text-xs font-medium tabular-nums">
                {members.length}
              </span>
            )}
          </div>
          <p className="text-muted-foreground mt-0.5 text-xs">
            {t("web.admin.access.groups.members.manageHelp")}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!users.isSuccess || !groupsReady}
          onClick={() => setAdding(true)}
        >
          {t("web.admin.access.groups.members.addUsers")}
        </Button>
      </div>

      {users.isPending && (
        <p className="text-muted-foreground text-sm">
          {t("web.admin.access.groups.members.loading")}
        </p>
      )}
      {!groupsReady && onRetryGroups && (
        <p role="alert" className="text-sm">
          {t("web.admin.access.groups.members.groupsLoadFailed", {
            retry: (chunks) => (
              <Button variant="link" className="h-auto p-0" onClick={onRetryGroups}>
                {chunks}
              </Button>
            ),
          })}
        </p>
      )}
      {users.isError && (
        <p role="alert" className="text-sm">
          {t("web.admin.access.groups.members.loadFailed", {
            retry: (chunks) => (
              <Button variant="link" className="h-auto p-0" onClick={() => void users.refetch()}>
                {chunks}
              </Button>
            ),
          })}
        </p>
      )}
      {failures.length > 0 && (
        <div role="alert" className="text-sm">
          <p>
            {failedAdding
              ? t("web.admin.access.groups.members.addFailed")
              : t("web.admin.access.groups.members.moveFailed")}
          </p>
          <ul className="list-disc ps-5">
            {failures.map((failure) => (
              <li key={failure}>{failure}</li>
            ))}
          </ul>
        </div>
      )}
      {users.isSuccess && members.length === 0 && (
        <p className="text-muted-foreground text-sm">
          {t("web.admin.access.groups.members.empty")}
        </p>
      )}
      {searchable && (
        <div className="relative sm:max-w-sm">
          <Search
            className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setVisibleCount(MEMBER_PAGE_SIZE);
            }}
            placeholder={t("web.admin.users.list.searchPlaceholder")}
            aria-label={t("web.admin.access.groups.members.search")}
            className="ps-9"
          />
        </div>
      )}
      {members.length > 0 && matches.length === 0 && (
        <p role="status" className="text-muted-foreground text-sm">
          {t("web.admin.access.groups.members.noMatches")}
        </p>
      )}
      {shown.length > 0 && (
        <ul className="divide-border -mx-2 divide-y text-sm">
          {shown.map((member) => (
            <li key={member.id} className="flex min-w-0 items-center gap-1">
              <label className="flex min-h-11 shrink-0 items-center px-2">
                <input
                  type="checkbox"
                  aria-label={t("web.admin.access.groups.members.selectMember", {
                    username: member.username,
                  })}
                  checked={selected.has(member.id)}
                  onChange={(event) => toggle(member.id, event.target.checked)}
                />
              </label>
              <Link
                to={`/admin/users/${member.id}`}
                className="hover:bg-accent/40 focus-visible:ring-ring/50 flex min-h-11 min-w-0 flex-1 flex-col justify-center rounded-md px-2 py-1.5 outline-none focus-visible:ring-[3px]"
              >
                <span className="truncate font-medium">{member.username}</span>
                {member.email ? (
                  <span className="text-muted-foreground truncate text-xs">{member.email}</span>
                ) : null}
              </Link>
            </li>
          ))}
        </ul>
      )}
      {matches.length > shown.length && (
        <Button
          type="button"
          variant="outline"
          className="w-full sm:w-auto"
          onClick={() => setVisibleCount((count) => count + MEMBER_PAGE_SIZE)}
        >
          {t("web.admin.downloads.deviceCopies.showMore")}
        </Button>
      )}
      {hiddenSelected > 0 && (
        <p role="status" className="text-muted-foreground text-xs">
          {t("web.admin.access.groups.members.selectedHidden", { count: hiddenSelected })}
        </p>
      )}
      {members.length > 0 && otherGroups.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <Select value={moveTarget} onValueChange={setMoveTarget}>
            <SelectTrigger
              className="w-full sm:w-56"
              aria-label={t("web.admin.access.groups.members.moveTarget")}
            >
              <SelectValue
                placeholder={t("web.admin.access.groups.members.moveTargetPlaceholder")}
              />
            </SelectTrigger>
            <SelectContent className={TOUCH_TARGETS}>
              {otherGroups.map((candidate) => (
                <SelectItem key={candidate.id} value={String(candidate.id)}>
                  {candidate.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            type="button"
            size="sm"
            disabled={selectedMembers.length === 0 || !moveTarget || saving || !groupsReady}
            onClick={() => {
              const target = otherGroups.find((candidate) => String(candidate.id) === moveTarget);
              if (target) setPending({ users: selectedMembers, target });
            }}
          >
            {selectedMembers.length > 0
              ? t("web.admin.access.groups.members.moveSelectedCount", {
                  count: String(selectedMembers.length),
                })
              : t("web.admin.access.groups.members.moveSelected")}
          </Button>
        </div>
      )}

      {adding && (
        <AddUsersDialog
          group={group}
          groups={groups}
          users={users.data ?? []}
          initialChosen={addRetryIds}
          onClose={() => setAdding(false)}
          onChoose={(chosen) => {
            setAdding(false);
            // group is the editor's snapshot; the list may be newer.
            setPending({
              users: chosen,
              target:
                groups.find((candidate) => String(candidate.id) === String(group.id)) ?? group,
            });
          }}
        />
      )}

      {pendingMove && (
        <ConfirmMoveDialog
          move={pendingMove}
          groups={groups}
          libraryNames={
            new Map((libraries.data ?? []).map((library) => [library.id, library.name]))
          }
          saving={saving}
          onCancel={() => setPending(null)}
          onConfirm={() => void applyMove(pendingMove)}
        />
      )}
    </section>
  );
}

function AddUsersDialog({
  group,
  groups,
  users,
  initialChosen,
  onClose,
  onChoose,
}: {
  group: AccessGroup;
  groups: AccessGroup[];
  users: AdminUser[];
  initialChosen?: ReadonlySet<number>;
  onClose: () => void;
  onChoose: (users: AdminUser[]) => void;
}) {
  const t = useT();
  const [search, setSearch] = useState("");
  const [visibleCount, setVisibleCount] = useState(PICKER_PAGE_SIZE);
  const [chosen, setChosen] = useState<Set<number>>(() => new Set(initialChosen));
  const groupNames = new Map(groups.map((candidate) => [String(candidate.id), candidate.name]));
  // Admin accounts can't join groups, and members are already here.
  const eligible = useMemo(
    () =>
      users
        .filter(
          (user) => user.role !== "admin" && String(user.access_group_id) !== String(group.id),
        )
        .sort((a, b) => a.username.localeCompare(b.username)),
    [users, group.id],
  );
  // A retry can preselect a user who has since become ineligible (already in
  // this group, say); only eligible ones count.
  const selectedEligible = eligible.filter((user) => chosen.has(user.id));
  const needle = search.trim().toLocaleLowerCase();
  const matches = needle ? eligible.filter((user) => matchesQuery(user, needle)) : eligible;
  const shown = matches.slice(0, visibleCount);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className={TOUCH_TARGETS}>
        <DialogHeader>
          <DialogTitle>
            {t("web.admin.access.groups.addUsers.title", { group: group.name })}
          </DialogTitle>
          <DialogDescription>{t("web.admin.access.groups.addUsers.description")}</DialogDescription>
        </DialogHeader>
        <Input
          type="search"
          value={search}
          onChange={(event) => {
            setSearch(event.target.value);
            setVisibleCount(PICKER_PAGE_SIZE);
          }}
          placeholder={t("web.admin.access.groups.addUsers.searchPlaceholder")}
          aria-label={t("web.admin.access.groups.addUsers.search")}
        />
        {eligible.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("web.admin.access.groups.addUsers.none")}
          </p>
        ) : matches.length === 0 ? (
          <p role="status" className="text-muted-foreground text-sm">
            {t("web.admin.access.groups.addUsers.noMatches")}
          </p>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            <ul className="divide-border divide-y text-sm">
              {shown.map((user) => (
                <li key={user.id}>
                  <label className="flex min-h-11 items-center gap-3 py-2">
                    <input
                      type="checkbox"
                      checked={chosen.has(user.id)}
                      onChange={(event) =>
                        setChosen((current) => {
                          const next = new Set(current);
                          if (event.target.checked) next.add(user.id);
                          else next.delete(user.id);
                          return next;
                        })
                      }
                    />
                    <span className="min-w-0 truncate font-medium">{user.username}</span>
                    <span className="text-muted-foreground ms-auto shrink-0 truncate text-xs">
                      {user.access_group_id == null
                        ? t("web.admin.access.groups.addUsers.noGroup")
                        : (groupNames.get(String(user.access_group_id)) ??
                          t("web.admin.access.groups.addUsers.unknownGroup"))}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
            {matches.length > shown.length && (
              <Button
                type="button"
                variant="outline"
                className="mt-2 w-full"
                onClick={() => setVisibleCount((count) => count + PICKER_PAGE_SIZE)}
              >
                {t("web.admin.downloads.deviceCopies.showMore")}
              </Button>
            )}
          </div>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            type="button"
            disabled={selectedEligible.length === 0}
            onClick={() => onChoose(selectedEligible)}
          >
            {selectedEligible.length > 0
              ? t("web.admin.access.groups.addUsers.addSelectedCount", {
                  count: String(selectedEligible.length),
                })
              : t("web.admin.access.groups.addUsers.addSelected")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConfirmMoveDialog({
  move,
  groups,
  libraryNames,
  saving,
  onCancel,
  onConfirm,
}: {
  move: GroupMove;
  groups: AccessGroup[];
  libraryNames: ReadonlyMap<number, string>;
  saving: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const t = useT();
  // Summarize per current group: each source group changes different policies.
  const bySource = new Map<string, AdminUser[]>();
  for (const user of move.users) {
    const key = user.access_group_id == null ? "none" : String(user.access_group_id);
    const sourceUsers = bySource.get(key);
    if (sourceUsers) sourceUsers.push(user);
    else bySource.set(key, [user]);
  }
  const count = move.users.length;
  const unresolved = [...bySource.keys()].some(
    (key) => key !== "none" && !groups.some((candidate) => String(candidate.id) === key),
  );
  // Request limits and approval are stored per group apart from the group's
  // policy. An account override can allow requests even when the target doesn't.
  const requestSettings = useRequestSettings();
  const termsMatter = move.users.some(
    (user) => user.requests_allowed ?? move.target.requests_allowed,
  );
  const limitIds = termsMatter
    ? [
        ...new Set(
          [...bySource.keys(), String(move.target.id)].filter((key) => key !== "none").map(Number),
        ),
      ]
    : [];
  const groupLimits = useRequestGroupLimits(limitIds);
  const termsLoading = termsMatter && (requestSettings.isPending || groupLimits.isLoading);
  const termsFailed = termsMatter && (requestSettings.isError || groupLimits.isError);
  const server = requestSettings.data;
  const requestChanges = (key: string): ReturnType<typeof requestTermsChanges> => {
    if (!termsMatter || !server?.requests_enabled) return [];
    return requestTermsChanges(
      key === "none" ? null : (groupLimits.limits.get(Number(key)) ?? null),
      groupLimits.limits.get(Number(move.target.id)) ?? null,
      server,
    );
  };
  return (
    <AlertDialog open onOpenChange={(open) => !open && !saving && onCancel()}>
      <AlertDialogContent className={TOUCH_TARGETS}>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("web.admin.access.groups.moveConfirm.title", { count, group: move.target.name })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.admin.access.groups.moveConfirm.description")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="max-h-72 space-y-3 overflow-y-auto text-sm">
          {[...bySource.entries()].map(([key, sourceUsers]) => {
            const source =
              key === "none" ? null : groups.find((candidate) => String(candidate.id) === key);
            // An ungrouped account inherits the no-group policy; a group ID
            // missing from the list is unresolved (not "no group") and its
            // changes are unknown.
            const changes =
              source === undefined || termsLoading || termsFailed
                ? null
                : [
                    ...groupPolicyChanges(source ?? NO_GROUP_POLICY, move.target, libraryNames),
                    ...requestChanges(key),
                  ];
            const sourceCount = String(sourceUsers.length);
            const who =
              source === null
                ? t("web.admin.access.groups.moveConfirm.fromNoGroup", { count: sourceCount })
                : source
                  ? t("web.admin.access.groups.moveConfirm.fromGroup", {
                      count: sourceCount,
                      group: source.name,
                    })
                  : t("web.admin.access.groups.moveConfirm.fromUnknownGroup", {
                      count: sourceCount,
                      id: key,
                    });
            return (
              <div key={key}>
                <p className="font-medium">{who}</p>
                {changes === null && termsLoading ? (
                  <p className="text-muted-foreground">
                    {t("web.admin.access.groups.moveConfirm.loadingTerms")}
                  </p>
                ) : changes === null && termsFailed ? (
                  <p className="text-muted-foreground">
                    {t("web.admin.access.groups.moveConfirm.termsFailed")}
                  </p>
                ) : changes === null ? (
                  <p className="text-muted-foreground">
                    {t("web.admin.access.groups.moveConfirm.sourceUnknown")}
                  </p>
                ) : changes.length === 0 ? (
                  <p className="text-muted-foreground">
                    {t("web.admin.access.groups.moveConfirm.noChanges")}
                  </p>
                ) : (
                  <ul className="text-muted-foreground list-disc ps-5">
                    {changes.map((change) => (
                      <li key={change.label}>
                        {t("web.admin.access.groups.moveConfirm.change", { ...change })}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            );
          })}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={saving}>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            disabled={saving || unresolved || termsLoading || termsFailed}
            onClick={(event) => {
              event.preventDefault();
              onConfirm();
            }}
          >
            {saving
              ? t("web.admin.access.groups.moveConfirm.moving")
              : t("web.admin.access.groups.moveConfirm.move")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
