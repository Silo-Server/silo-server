import { ArrowLeft, Plus, Trash2, UsersRound } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import { toast } from "sonner";

import { useAuth } from "@/hooks/useAuth";
import {
  accessGroupScope,
  captureAccessGroupAuthority,
  getAccessGroup,
  type AccessGroupEditor as GroupEditor,
} from "@/api/v2/accessGroups";
import { V2ProblemError } from "@/api/v2/request";
import type { AccessGroup, AccessGroupInput } from "@/api/types";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { StreamBitrateLimitInput } from "@/components/StreamBitrateLimitInput";
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
import {
  useAccessGroupCapabilities,
  useAccessGroups,
  useCreateAccessGroup,
  useDeleteAccessGroup,
  useUpdateAccessGroup,
} from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";
import {
  PLAYBACK_QUALITY_OPTIONS,
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
  type PlaybackQualityPreset,
} from "@/lib/playback-quality";

// The two assignable permissions (mirrors auth.assignablePermissions). A group
// mask of `null` means "all assignable"; a list narrows to those named.
const ASSIGNABLE_PERMISSIONS: Array<{ value: string; label: string; description: string }> = [
  {
    value: PERMISSION_METADATA_CURATION,
    label: "Metadata curation",
    description: "Edit metadata for items in the member's libraries.",
  },
  {
    value: PERMISSION_MARKER_EDIT,
    label: "Marker editing",
    description: "Create and adjust intro / credit markers.",
  },
];

function limitLabel(value: number) {
  return value > 0 ? String(value) : "Unlimited";
}

export default function AdminAccessGroups() {
  useAuth();
  return <AccessGroupsPage key={accessGroupScope()} />;
}
function AccessGroupsPage() {
  useDocumentTitle("Access Groups");
  const groups = useAccessGroups();
  const capabilities = useAccessGroupCapabilities();
  const available = capabilities.data?.access_groups === true;
  // The open group lives in the URL (/admin/access-groups/:id) so Back returns
  // to the list and a group link can be reloaded or shared.
  const { id: selectedId } = useParams();
  const navigate = useNavigate();
  const [selected, setSelected] = useState<GroupEditor | null>(null);
  const [authority] = useState(captureAccessGroupAuthority);
  const busy = useRef(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  // Bumped to retry a failed load of the group already in the URL.
  const [loadAttempt, setLoadAttempt] = useState(0);
  const mounted = useRef(true);
  // One page instance serves the list and every group URL, so a finished create
  // or delete compares history entries to tell whether the admin moved on meanwhile.
  const location = useLocation();
  const locationKey = useRef(location.key);
  const createGroup = useCreateAccessGroup();

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useLayoutEffect(() => {
    locationKey.current = location.key;
  }, [location.key]);

  function stillOn(key: string) {
    return mounted.current && locationKey.current === key;
  }

  useEffect(() => {
    setSelected(null);
    if (!selectedId || !available) {
      // A load cancelled by leaving the group must not leave its status behind.
      setLoading(false);
      setError("");
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError("");
    getAccessGroup(Number(selectedId), authority)
      .then((editor) => {
        if (!cancelled) setSelected(editor);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not load group.");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId, available, authority, loadAttempt]);

  function select(id: number | string) {
    if (!available) return;
    if (String(id) === selectedId) {
      setLoadAttempt((attempt) => attempt + 1);
      return;
    }
    navigate(`/admin/access-groups/${id}`);
  }
  async function create() {
    const name = newName.trim();
    if (!name || busy.current || !available) return;
    busy.current = true;
    const startedAt = locationKey.current;
    setError("");
    try {
      const group = await createGroup.mutateAsync({ body: { name }, profileContext: authority });
      setNewName("");
      setCreating(false);
      // Don't pull the admin away if they opened another group or left the page
      // while the group was created.
      if (stillOn(startedAt)) select(group.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create group.");
    } finally {
      busy.current = false;
    }
  }

  if (selected && available) {
    return (
      <div className="page-shell space-y-6 py-4 sm:py-6">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-muted-foreground -ml-2 w-fit"
          onClick={() => navigate("/admin/access-groups")}
        >
          <ArrowLeft className="size-4" />
          All groups
        </Button>
        <AccessGroupEditor
          key={selected.group.id}
          initialEditor={selected}
          onSaved={() => {
            toast.success("Group saved");
            // Replace the group's entry so Back and "All groups" reach the same list.
            if (stillOn(location.key)) navigate("/admin/access-groups", { replace: true });
          }}
          onDeleted={() => {
            if (stillOn(location.key)) navigate("/admin/access-groups", { replace: true });
          }}
        />
      </div>
    );
  }

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Access Groups</h1>
          <p className="page-subtitle text-sm sm:text-base">
            The shared policy layer for a set of users. A group supplies the value for every field
            its members leave on Inherit; a per-user override replaces the group value in either
            direction.
          </p>
        </div>
        {!creating && available && (
          <Button type="button" onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            New group
          </Button>
        )}
      </div>

      {!available && <p role="status">Access group editing is unavailable.</p>}
      {error && <p role="alert">{error}</p>}
      {loading && <p>Loading group editor...</p>}
      {groups.isError && (
        <div role="alert">
          Could not load access groups.{" "}
          <Button onClick={() => void groups.refetch()}>Reload groups</Button>
        </div>
      )}
      {creating && available && (
        <div className="surface-panel-subtle flex flex-wrap items-center gap-2 rounded-2xl p-4">
          <Input
            value={newName}
            onChange={(event) => setNewName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") void create();
            }}
            placeholder="Group name (e.g. Kids, Guests)"
            aria-label="New group name"
            className="max-w-xs"
            autoFocus
          />
          <Button type="button" onClick={create} disabled={createGroup.isPending}>
            Create
          </Button>
          <Button
            type="button"
            variant="ghost"
            disabled={createGroup.isPending}
            onClick={() => {
              setCreating(false);
              setNewName("");
            }}
          >
            Cancel
          </Button>
        </div>
      )}

      {groups.isLoading && (
        <p className="text-muted-foreground text-sm">Loading access groups...</p>
      )}

      {!groups.isLoading && groups.data?.length === 0 && !creating && (
        <div className="surface-panel-subtle rounded-2xl p-8 text-center">
          <UsersRound className="text-muted-foreground mx-auto size-8" />
          <h2 className="mt-3 text-lg font-semibold">No access groups yet</h2>
          <p className="text-muted-foreground mx-auto mt-1 max-w-md text-sm">
            Create a group to manage libraries, downloads, streams, and permissions for many users
            at once, then assign users to it from their profile.
          </p>
        </div>
      )}

      {groups.data && groups.data.length > 0 && (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {groups.data.map((group) => (
            <AccessGroupCard key={group.id} group={group} onClick={() => select(group.id)} />
          ))}
        </div>
      )}
    </div>
  );
}

function AccessGroupCard({ group, onClick }: { group: AccessGroup; onClick: () => void }) {
  const facts = [
    group.library_ids === null
      ? "All libraries"
      : `${group.library_ids.length} librar${group.library_ids.length === 1 ? "y" : "ies"}`,
    group.download_allowed ? "Downloads on" : "No downloads",
    `${limitLabel(group.max_streams)} stream${group.max_streams === 1 ? "" : "s"}`,
    group.requests_allowed ? "Requests on" : "No requests",
  ];
  return (
    <button
      type="button"
      onClick={onClick}
      className="surface-panel hover:border-ring/40 focus-visible:ring-ring/60 flex flex-col gap-3 rounded-2xl border border-transparent p-5 text-left transition-colors outline-none focus-visible:ring-2"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="truncate text-base font-semibold tracking-tight">{group.name}</h2>
            {group.is_default && (
              <span className="border-border text-muted-foreground shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-medium tracking-wide uppercase">
                Default
              </span>
            )}
          </div>
          {group.description && (
            <p className="text-muted-foreground mt-0.5 line-clamp-2 text-sm">{group.description}</p>
          )}
        </div>
        <span className="bg-secondary text-secondary-foreground shrink-0 rounded-full px-2.5 py-0.5 text-xs font-medium">
          {group.member_count} {group.member_count === 1 ? "member" : "members"}
        </span>
      </div>
      <div className="flex flex-wrap gap-1.5">
        {facts.map((fact) => (
          <span
            key={fact}
            className="border-border text-muted-foreground rounded-md border px-2 py-0.5 text-xs"
          >
            {fact}
          </span>
        ))}
      </div>
    </button>
  );
}

interface AccessGroupEditorProps {
  initialEditor: GroupEditor;
  onSaved: () => void;
  onDeleted: () => void;
}

function AccessGroupEditor({ initialEditor, onSaved, onDeleted }: AccessGroupEditorProps) {
  const [editor, setEditor] = useState(initialEditor);
  const group = editor.group;
  const busy = useRef(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [reloading, setReloading] = useState(false);
  function failed(err: unknown) {
    setError(err instanceof Error ? err.message : "Group action failed.");
    if (err instanceof V2ProblemError && err.status === 412) setConflict(true);
  }
  async function reload() {
    if (busy.current) return;
    busy.current = true;
    setReloading(true);
    try {
      setEditor(await getAccessGroup(group.id, editor.profileContext));
      setConflict(false);
      setError("");
    } catch (err) {
      failed(err);
    } finally {
      busy.current = false;
      setReloading(false);
    }
  }

  const libraries = useAdminLibraries();
  const updateGroup = useUpdateAccessGroup();
  const deleteGroup = useDeleteAccessGroup();
  const [confirmDelete, setConfirmDelete] = useState(false);

  // Draft state, keyed by group id via the parent's selection so switching
  // groups remounts this component with fresh initial values.
  const [name, setName] = useState(group.name);
  const [description, setDescription] = useState(group.description);
  const [libraryIds, setLibraryIds] = useState<number[] | null>(group.library_ids);
  const [qualityPreset, setQualityPreset] = useState<PlaybackQualityPreset>(
    playbackQualityPresetFromValue(group.max_playback_quality),
  );
  const [downloadAllowed, setDownloadAllowed] = useState(group.download_allowed);
  const [transcodeAllowed, setTranscodeAllowed] = useState(group.download_transcode_allowed);
  const [videoTranscodeAllowed, setVideoTranscodeAllowed] = useState(group.transcode_allowed);
  const [audioTranscodeAllowed, setAudioTranscodeAllowed] = useState(group.audio_transcode_allowed);
  const [maxStreams, setMaxStreams] = useState(group.max_streams);
  const [maxTranscodes, setMaxTranscodes] = useState(group.max_transcodes);
  // null while a custom bitrate box holds no valid value; Save stays disabled.
  const [maxRemoteStreamBitrateKbps, setMaxRemoteStreamBitrateKbps] = useState<number | null>(
    group.max_remote_stream_bitrate_kbps,
  );
  const [maxLocalStreamBitrateKbps, setMaxLocalStreamBitrateKbps] = useState<number | null>(
    group.max_local_stream_bitrate_kbps,
  );
  const bitrateLimitsValid =
    maxRemoteStreamBitrateKbps !== null && maxLocalStreamBitrateKbps !== null;
  const [permissions, setPermissions] = useState<string[] | null>(group.allowed_permissions);
  const [requestsAllowed, setRequestsAllowed] = useState(group.requests_allowed);
  const [isDefault, setIsDefault] = useState(group.is_default);

  const allPermissions = permissions === null;

  function setPermissionAllowed(permission: string, allowed: boolean) {
    const current = permissions ?? ASSIGNABLE_PERMISSIONS.map((entry) => entry.value);
    const next = allowed
      ? Array.from(new Set([...current, permission]))
      : current.filter((value) => value !== permission);
    setPermissions(next);
  }

  async function save() {
    if (busy.current || conflict) return;
    if (maxRemoteStreamBitrateKbps === null || maxLocalStreamBitrateKbps === null) return;
    busy.current = true;
    setError("");
    const body: AccessGroupInput = {
      name: name.trim(),
      description: description.trim(),
      library_ids: libraryIds,
      max_playback_quality: playbackQualityValueFromPreset(qualityPreset),
      download_allowed: downloadAllowed,
      // The transcode toggle is disabled (not reset) when downloads are off,
      // so clamp it here to avoid saving a contradictory record.
      download_transcode_allowed: downloadAllowed && transcodeAllowed,
      transcode_allowed: videoTranscodeAllowed,
      audio_transcode_allowed: audioTranscodeAllowed,
      max_streams: maxStreams,
      max_transcodes: maxTranscodes,
      max_remote_stream_bitrate_kbps: maxRemoteStreamBitrateKbps,
      max_local_stream_bitrate_kbps: maxLocalStreamBitrateKbps,
      allowed_permissions: permissions,
      requests_allowed: requestsAllowed,
      is_default: isDefault,
    };
    try {
      setEditor(await updateGroup.mutateAsync({ editor, body }));
      onSaved();
    } catch (err) {
      failed(err);
    } finally {
      busy.current = false;
    }
  }

  async function remove() {
    if (busy.current || conflict) return;
    busy.current = true;
    setError("");
    try {
      await deleteGroup.mutateAsync(editor);
      setConfirmDelete(false);
      onDeleted();
    } catch (err) {
      failed(err);
    } finally {
      busy.current = false;
    }
  }

  return (
    <div className="space-y-5">
      {error && <p role="alert">{error}</p>}
      {conflict && (
        <div>
          Your draft is preserved. Reload the current group before submitting again.{" "}
          <Button disabled={reloading} onClick={() => void reload()}>
            Reload current group
          </Button>
        </div>
      )}
      <div className="surface-panel space-y-4 rounded-2xl border-0 p-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="group-name">Name</Label>
            <Input id="group-name" value={name} onChange={(event) => setName(event.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="group-description">Description</Label>
            <Input
              id="group-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="Optional"
            />
          </div>
        </div>
        <ToggleRow
          label="Default for new users"
          description={
            group.is_default
              ? "Newly created accounts are placed in this group automatically. To change this, make another group the default."
              : "Newly created accounts are placed in this group automatically. Existing users are never moved."
          }
          checked={isDefault}
          onCheckedChange={setIsDefault}
          disabled={group.is_default}
        />
      </div>

      <section className="surface-panel space-y-4 rounded-2xl border-0 p-5">
        <h2 className="text-sm font-semibold">Libraries &amp; playback</h2>
        <LibraryAccessSelector
          libraries={libraries.data ?? []}
          value={libraryIds}
          onChange={setLibraryIds}
        />
        <div className="space-y-2">
          <Label htmlFor="group-quality">Maximum playback quality</Label>
          <Select
            value={qualityPreset}
            onValueChange={(value) => setQualityPreset(value as PlaybackQualityPreset)}
          >
            <SelectTrigger id="group-quality" className="w-full sm:w-72">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PLAYBACK_QUALITY_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label} — {option.description}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <StreamBitrateLimitField
            id="group-stream-bitrate"
            label="Max remote stream bitrate"
            hint="Applies to new remote streams."
            value={maxRemoteStreamBitrateKbps}
            onChange={setMaxRemoteStreamBitrateKbps}
          />
          <StreamBitrateLimitField
            id="group-local-stream-bitrate"
            label="Max local stream bitrate"
            hint="Applies to new local streams."
            value={maxLocalStreamBitrateKbps}
            onChange={setMaxLocalStreamBitrateKbps}
          />
        </div>
      </section>

      <section className="surface-panel space-y-3 rounded-2xl border-0 p-5">
        <h2 className="text-sm font-semibold">Downloads &amp; requests</h2>
        <ToggleRow
          label="Allow downloads"
          description="Members may download items to their devices."
          checked={downloadAllowed}
          onCheckedChange={setDownloadAllowed}
        />
        <ToggleRow
          label="Allow transcoded downloads"
          description="Members may download converted versions, not just the original file."
          checked={transcodeAllowed}
          onCheckedChange={setTranscodeAllowed}
          disabled={!downloadAllowed}
        />
        <ToggleRow
          label="Allow media requests"
          description="Members may request titles that aren't in the library yet."
          checked={requestsAllowed}
          onCheckedChange={setRequestsAllowed}
        />
      </section>

      <section className="surface-panel space-y-4 rounded-2xl border-0 p-5">
        <h2 className="text-sm font-semibold">Concurrent streams</h2>
        <div className="grid gap-4 sm:grid-cols-2">
          <LimitField
            id="group-streams"
            label="Max streams"
            hint="0 = unlimited"
            value={maxStreams}
            onChange={setMaxStreams}
          />
          <LimitField
            id="group-transcodes"
            label="Max transcodes"
            hint="0 = unlimited"
            value={maxTranscodes}
            onChange={setMaxTranscodes}
          />
        </div>
        <ToggleRow
          label="Allow video transcoding"
          description="Members may play items that need server-side video conversion."
          checked={videoTranscodeAllowed}
          onCheckedChange={setVideoTranscodeAllowed}
        />
        <ToggleRow
          label="Allow audio transcoding"
          description="Members may play items that need audio conversion without video encoding."
          checked={audioTranscodeAllowed}
          onCheckedChange={setAudioTranscodeAllowed}
        />
      </section>

      <section className="surface-panel space-y-3 rounded-2xl border-0 p-5">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-sm font-semibold">Permissions</h2>
            <p className="text-muted-foreground mt-0.5 text-xs">
              A member also needs the permission granted on their own account.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground text-xs">Allow all</span>
            <Switch
              checked={allPermissions}
              onCheckedChange={(checked) =>
                setPermissions(checked ? null : ASSIGNABLE_PERMISSIONS.map((entry) => entry.value))
              }
              aria-label="Allow all permissions"
            />
          </div>
        </div>
        {!allPermissions && (
          <div className="space-y-2">
            {ASSIGNABLE_PERMISSIONS.map((permission) => (
              <ToggleRow
                key={permission.value}
                label={permission.label}
                description={permission.description}
                checked={permissions?.includes(permission.value) ?? false}
                onCheckedChange={(checked) => setPermissionAllowed(permission.value, checked)}
              />
            ))}
          </div>
        )}
      </section>

      <AccessGroupMembers groupId={group.id} />

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-col gap-1">
          <Button
            type="button"
            variant="ghost"
            className="text-destructive hover:text-destructive"
            onClick={() => setConfirmDelete(true)}
            disabled={group.is_default}
          >
            <Trash2 className="size-4" />
            Delete group
          </Button>
          {group.is_default && (
            <p className="text-muted-foreground text-xs">
              The default group can’t be deleted. Make another group the default first.
            </p>
          )}
        </div>
        <Button
          type="button"
          onClick={save}
          disabled={
            updateGroup.isPending ||
            deleteGroup.isPending ||
            conflict ||
            reloading ||
            !bitrateLimitsValid
          }
        >
          {updateGroup.isPending ? "Saving..." : "Save changes"}
        </Button>
      </div>

      <AlertDialog
        open={confirmDelete}
        onOpenChange={(open) => {
          if (!busy.current) setConfirmDelete(open);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{group.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              {group.member_count > 0
                ? `${group.member_count} ${
                    group.member_count === 1 ? "member" : "members"
                  } will move to no group and fall back to the built-in defaults. Their own restrictions are unchanged.`
                : "Members will move to no group and fall back to built-in defaults. This can't be undone."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {error && <p role="alert">{error}</p>}
          {conflict && (
            <Button disabled={reloading} onClick={() => void reload()}>
              Reload current group
            </Button>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteGroup.isPending || reloading}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                void remove();
              }}
              disabled={deleteGroup.isPending || conflict || reloading}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

interface ToggleRowProps {
  label: string;
  description: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}

/** Read-only list of a group's members, each linking to their user page. */
function AccessGroupMembers({ groupId }: { groupId: number | string }) {
  const users = useAdminUsers();
  const members = (users.data ?? [])
    .filter((user) => user.role !== "admin" && String(user.access_group_id) === String(groupId))
    .sort((a, b) => a.username.localeCompare(b.username));
  return (
    <section className="surface-panel space-y-3 rounded-2xl border-0 p-5" aria-label="Members">
      <div>
        <h2 className="text-sm font-semibold">Members</h2>
        <p className="text-muted-foreground mt-0.5 text-xs">
          Change a member&apos;s group from their user page.
        </p>
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
      {users.isSuccess && members.length === 0 && (
        <p className="text-muted-foreground text-sm">No members yet.</p>
      )}
      {members.length > 0 && (
        <ul className="divide-border divide-y text-sm">
          {members.map((member) => (
            <li key={member.id} className="flex items-center justify-between gap-3 py-2">
              <Link to={`/admin/users/${member.id}`} className="font-medium hover:underline">
                {member.username}
              </Link>
              <span className="text-muted-foreground truncate">{member.email}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function ToggleRow({ label, description, checked, onCheckedChange, disabled }: ToggleRowProps) {
  return (
    <div className="border-border flex items-center justify-between gap-4 rounded-lg border px-3 py-2.5">
      <div className="min-w-0">
        <p className="text-sm font-medium">{label}</p>
        <p className="text-muted-foreground text-xs">{description}</p>
      </div>
      <Switch
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
        aria-label={label}
      />
    </div>
  );
}

interface LimitFieldProps {
  id: string;
  label: string;
  hint: string;
  value: number;
  onChange: (value: number) => void;
}

function StreamBitrateLimitField({
  id,
  label,
  hint,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  value: number | null;
  onChange: (kbps: number | null) => void;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <StreamBitrateLimitInput id={id} label={label} value={value} onValueChange={onChange} />
      <p className="text-muted-foreground text-xs">{hint}</p>
    </div>
  );
}

function LimitField({ id, label, hint, value, onChange }: LimitFieldProps) {
  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between">
        <Label htmlFor={id}>{label}</Label>
        <span className="text-muted-foreground text-xs">{hint}</span>
      </div>
      <Input
        id={id}
        type="number"
        min={0}
        value={value}
        onChange={(event) => {
          const raw = event.target.value;
          if (raw === "") {
            if (event.target.validity.badInput) return;
            onChange(0);
            return;
          }
          if (!/^\d+$/.test(raw)) return;
          const next = Number(raw);
          if (Number.isSafeInteger(next)) onChange(next);
        }}
      />
    </div>
  );
}
