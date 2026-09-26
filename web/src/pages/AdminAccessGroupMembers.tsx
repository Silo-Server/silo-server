import { useMemo, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";

import type { AccessGroup, AdminUser } from "@/api/types";
import { captureAdminUserAuthority, getAdminUser } from "@/api/v2/adminUsers";
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
import { useAdminUsers, useUpdateUser } from "@/hooks/queries/admin/users";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { groupPolicyChanges } from "@/lib/accessGroupPolicyChanges";

/** A pending group change awaiting the admin's confirmation. */
interface GroupMove {
  users: AdminUser[];
  target: AccessGroup;
}

/**
 * The group editor's member list. Admins can move selected members to another
 * group, or add eligible users to this one; both confirm first, showing what
 * changes for the members who inherit the group's settings.
 */
export function AccessGroupMembers({
  group,
  groups,
}: {
  group: AccessGroup;
  groups: AccessGroup[];
}) {
  const users = useAdminUsers();
  const libraries = useAdminLibraries();
  const updateUser = useUpdateUser();
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [moveTarget, setMoveTarget] = useState("");
  const [adding, setAdding] = useState(false);
  const [pending, setPending] = useState<GroupMove | null>(null);
  const [saving, setSaving] = useState(false);
  const [failures, setFailures] = useState<string[]>([]);

  const members = useMemo(
    () =>
      (users.data ?? [])
        .filter(
          (user) => user.role !== "admin" && String(user.access_group_id) === String(group.id),
        )
        .sort((a, b) => a.username.localeCompare(b.username)),
    [users.data, group.id],
  );
  const otherGroups = groups.filter((candidate) => String(candidate.id) !== String(group.id));
  const selectedMembers = members.filter((member) => selected.has(member.id));

  function toggle(id: number, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  async function applyMove(move: GroupMove) {
    setSaving(true);
    setFailures([]);
    const context = captureAdminUserAuthority();
    const failed: string[] = [];
    let moved = 0;
    for (const user of move.users) {
      try {
        const editor = await getAdminUser(user.id, context);
        if (editor.user.access_group_id !== user.access_group_id) {
          throw new Error("This user's group changed. Reload and try again.");
        }
        await updateUser.mutateAsync({ editor, body: { access_group_id: move.target.id } });
        moved++;
      } catch (err) {
        failed.push(
          `${user.username}: ${err instanceof Error ? err.message : "could not be moved"}`,
        );
      }
    }
    setSaving(false);
    setPending(null);
    setSelected(new Set());
    setMoveTarget("");
    setFailures(failed);
    if (moved > 0) {
      toast.success(`Moved ${moved} ${moved === 1 ? "user" : "users"} to ${move.target.name}`);
    }
  }

  return (
    <section className="surface-panel space-y-3 rounded-2xl border-0 p-5" aria-label="Members">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold">Members</h2>
          <p className="text-muted-foreground mt-0.5 text-xs">
            Move members to another group, or add users to this one.
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!users.isSuccess}
          onClick={() => setAdding(true)}
        >
          Add users
        </Button>
      </div>

      {users.isPending && <p className="text-muted-foreground text-sm">Loading members...</p>}
      {users.isError && (
        <p role="alert" className="text-sm">
          Could not load members.{" "}
          <Button variant="link" className="h-auto p-0" onClick={() => void users.refetch()}>
            Retry
          </Button>
        </p>
      )}
      {failures.length > 0 && (
        <div role="alert" className="text-sm">
          <p>Some users could not be moved:</p>
          <ul className="list-disc pl-5">
            {failures.map((failure) => (
              <li key={failure}>{failure}</li>
            ))}
          </ul>
        </div>
      )}
      {users.isSuccess && members.length === 0 && (
        <p className="text-muted-foreground text-sm">No members yet.</p>
      )}

      {members.length > 0 && (
        <>
          <ul className="divide-border divide-y text-sm">
            {members.map((member) => (
              <li key={member.id} className="flex items-center gap-3 py-2">
                <input
                  type="checkbox"
                  aria-label={`Select ${member.username}`}
                  checked={selected.has(member.id)}
                  onChange={(event) => toggle(member.id, event.target.checked)}
                />
                <Link to={`/admin/users/${member.id}`} className="font-medium hover:underline">
                  {member.username}
                </Link>
                <span className="text-muted-foreground ml-auto truncate">{member.email}</span>
              </li>
            ))}
          </ul>
          {otherGroups.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <Select value={moveTarget} onValueChange={setMoveTarget}>
                <SelectTrigger className="w-56" aria-label="Move selected members to">
                  <SelectValue placeholder="Move to group..." />
                </SelectTrigger>
                <SelectContent>
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
                disabled={selectedMembers.length === 0 || !moveTarget || saving}
                onClick={() => {
                  const target = otherGroups.find(
                    (candidate) => String(candidate.id) === moveTarget,
                  );
                  if (target) setPending({ users: selectedMembers, target });
                }}
              >
                Move {selectedMembers.length > 0 ? selectedMembers.length : ""} selected
              </Button>
            </div>
          )}
        </>
      )}

      {adding && (
        <AddUsersDialog
          group={group}
          groups={groups}
          users={users.data ?? []}
          onClose={() => setAdding(false)}
          onChoose={(chosen) => {
            setAdding(false);
            setPending({ users: chosen, target: group });
          }}
        />
      )}

      {pending && (
        <ConfirmMoveDialog
          move={pending}
          groups={groups}
          libraryNames={
            new Map((libraries.data ?? []).map((library) => [library.id, library.name]))
          }
          saving={saving}
          onCancel={() => setPending(null)}
          onConfirm={() => void applyMove(pending)}
        />
      )}
    </section>
  );
}

function AddUsersDialog({
  group,
  groups,
  users,
  onClose,
  onChoose,
}: {
  group: AccessGroup;
  groups: AccessGroup[];
  users: AdminUser[];
  onClose: () => void;
  onChoose: (users: AdminUser[]) => void;
}) {
  const [search, setSearch] = useState("");
  const [chosen, setChosen] = useState<Set<number>>(new Set());
  const groupNames = new Map(groups.map((candidate) => [String(candidate.id), candidate.name]));
  // Admin accounts can't join groups, and members are already here.
  const eligible = users
    .filter((user) => user.role !== "admin" && String(user.access_group_id) !== String(group.id))
    .sort((a, b) => a.username.localeCompare(b.username));
  const query = search.trim().toLowerCase();
  const shown = query
    ? eligible.filter(
        (user) =>
          user.username.toLowerCase().includes(query) ||
          (user.email ?? "").toLowerCase().includes(query),
      )
    : eligible;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add users to {group.name}</DialogTitle>
          <DialogDescription>
            Regular accounts outside this group. Admin accounts can&apos;t join groups.
          </DialogDescription>
        </DialogHeader>
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="Search by username or email"
          aria-label="Search users"
        />
        {eligible.length === 0 ? (
          <p className="text-muted-foreground text-sm">Every regular account is already here.</p>
        ) : (
          <ul className="divide-border max-h-72 divide-y overflow-y-auto text-sm">
            {shown.map((user) => (
              <li key={user.id}>
                <label className="flex items-center gap-3 py-2">
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
                  <span className="font-medium">{user.username}</span>
                  <span className="text-muted-foreground ml-auto truncate text-xs">
                    {user.access_group_id == null
                      ? "No group"
                      : (groupNames.get(String(user.access_group_id)) ?? "Unknown group")}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={chosen.size === 0}
            onClick={() => onChoose(eligible.filter((user) => chosen.has(user.id)))}
          >
            Add {chosen.size > 0 ? chosen.size : ""} selected
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
  // Summarize per current group: each source group changes different policies.
  const bySource = new Map<string, AdminUser[]>();
  for (const user of move.users) {
    const key = user.access_group_id == null ? "none" : String(user.access_group_id);
    bySource.set(key, [...(bySource.get(key) ?? []), user]);
  }
  const count = move.users.length;
  return (
    <AlertDialog open onOpenChange={(open) => !open && !saving && onCancel()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Move {count} {count === 1 ? "user" : "users"} to {move.target.name}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            Settings a user has overridden on their own account stay as they are. Their current
            sessions are signed out so the new access applies.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="max-h-72 space-y-3 overflow-y-auto text-sm">
          {[...bySource.entries()].map(([key, sourceUsers]) => {
            const source = groups.find((candidate) => String(candidate.id) === key);
            const changes = source ? groupPolicyChanges(source, move.target, libraryNames) : null;
            const who = `${sourceUsers.length} from ${source?.name ?? "no group"}`;
            return (
              <div key={key}>
                <p className="font-medium">{who}</p>
                {changes === null ? (
                  <p className="text-muted-foreground">
                    They start inheriting {move.target.name}&apos;s settings instead of the server
                    defaults.
                  </p>
                ) : changes.length === 0 ? (
                  <p className="text-muted-foreground">No inherited settings change.</p>
                ) : (
                  <ul className="text-muted-foreground list-disc pl-5">
                    {changes.map((change) => (
                      <li key={change.label}>
                        {change.label}: {change.from} → {change.to}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            );
          })}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={saving}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={saving}
            onClick={(event) => {
              event.preventDefault();
              onConfirm();
            }}
          >
            {saving ? "Moving..." : "Move"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
