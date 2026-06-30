import { useEffect, useId, useState } from "react";
import type { FormEvent } from "react";
import type {
  AdminACLCondition,
  AdminACLPolicy,
  AdminAccessGroup,
  AdminAccessGroupDetail,
  AdminAccessGroupWriteRequest,
} from "@/api/types";
import {
  useAdminAccessGroup,
  useAdminAccessGroups,
  useCreateAdminAccessGroup,
  useDeleteAdminAccessGroup,
  useUpdateAdminAccessGroup,
} from "@/hooks/queries/admin/users";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info, Pencil, Plus, Shield, Trash2 } from "lucide-react";
import {
  ACCESS_GROUP_ACTION_CATEGORIES,
  ACCESS_GROUP_ACTIONS,
  ACCESS_GROUP_PRESETS,
  accessGroupActionLabel,
  accessGroupSlugFromName,
  buildAccessGroupRuleDrafts,
  type AccessGroupAction,
} from "./adminAccessGroupsModel";

const MEDIA_TYPE_OPTIONS = [
  {
    value: "movie",
    label: "Movies",
    description: "Applies this group only to movie library items.",
  },
  {
    value: "series",
    label: "Series",
    description: "Applies this group only to series and episode library items.",
  },
  {
    value: "audiobook",
    label: "Audiobooks",
    description: "Applies this group only to audiobook library items.",
  },
  {
    value: "ebook",
    label: "Ebooks",
    description: "Applies this group only to ebook library items.",
  },
] as const;

const PLAYBACK_QUALITY_OPTIONS = ["480P", "720P", "1080P", "2160P", "4320P"] as const;

const LIBRARY_ACCESS_HELP =
  "Limits the group to selected libraries. All libraries means this group can apply everywhere.";
const MEDIA_TYPES_HELP =
  "Limits matching rules to selected media types. Leave all off to apply the rules to every media type.";
const POLICY_LIMIT_HELP =
  "Limits from multiple groups cascade; the most permissive explicit value wins.";
const MAX_QUALITY_HELP =
  "Caps the highest playback quality this group can receive. Any leaves quality unrestricted.";
const MAX_STREAMS_HELP =
  "Maximum simultaneous playback sessions allowed by this group. Zero or blank means no explicit group limit.";
const MAX_TRANSCODES_HELP =
  "Maximum simultaneous transcode sessions allowed by this group. Zero or blank means no explicit group limit.";
const MAX_PROFILES_HELP =
  "Maximum profiles allowed on a user account through this group. Blank inherits the normal default.";

const supportedActions = new Set<string>(ACCESS_GROUP_ACTIONS.map((item) => item.action));

export default function AdminAccessGroups() {
  const { data: groups = [], isLoading } = useAdminAccessGroups();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingGroup, setEditingGroup] = useState<AdminAccessGroup | null>(null);
  const [viewingGroup, setViewingGroup] = useState<AdminAccessGroup | null>(null);
  const [deleteGroup, setDeleteGroup] = useState<AdminAccessGroup | null>(null);
  const deleteMutation = useDeleteAdminAccessGroup();

  if (isLoading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-10 w-full rounded-lg" />
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={deleteGroup !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteGroup(null);
        }}
        title="Delete access group"
        description={`Delete access group "${deleteGroup?.name}"? This action cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (deleteGroup) deleteMutation.mutate(deleteGroup.slug);
          setDeleteGroup(null);
        }}
      />
      <Dialog
        open={viewingGroup !== null}
        onOpenChange={(open) => {
          if (!open) setViewingGroup(null);
        }}
      >
        <DialogContent className="sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle>{viewingGroup?.name ?? "Access Group"}</DialogTitle>
          </DialogHeader>
          {viewingGroup && <AccessGroupDetailPanel group={viewingGroup} />}
        </DialogContent>
      </Dialog>

      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Access Groups</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Manage built-in and custom authorization groups.
          </p>
        </div>
        <Dialog
          open={dialogOpen}
          onOpenChange={(open) => {
            setDialogOpen(open);
            if (!open) setEditingGroup(null);
          }}
        >
          <DialogTrigger asChild>
            <Button
              size="sm"
              onClick={() => {
                setEditingGroup(null);
              }}
            >
              <Plus className="mr-1 h-4 w-4" /> Add Group
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-3xl">
            <DialogHeader>
              <DialogTitle>
                {editingGroup ? "Edit Access Group" : "Create Access Group"}
              </DialogTitle>
            </DialogHeader>
            <AccessGroupForm
              key={editingGroup?.slug ?? "new"}
              group={editingGroup}
              onClose={() => {
                setDialogOpen(false);
                setEditingGroup(null);
              }}
            />
          </DialogContent>
        </Dialog>
      </div>

      <div className="surface-panel overflow-x-auto rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Slug</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Members</TableHead>
              <TableHead>Description</TableHead>
              <TableHead className="w-24">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {groups.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-muted-foreground text-center">
                  No access groups found.
                </TableCell>
              </TableRow>
            )}
            {groups.map((group) => (
              <TableRow key={group.slug}>
                <TableCell className="font-medium">
                  <div className="flex items-center gap-2">
                    {group.protected && <Shield className="text-muted-foreground h-4 w-4" />}
                    <button
                      type="button"
                      className="hover:text-primary text-left hover:underline"
                      aria-label={`View ${group.name} access group`}
                      onClick={() => setViewingGroup(group)}
                    >
                      {group.name}
                    </button>
                  </div>
                </TableCell>
                <TableCell>
                  <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">
                    {group.slug}
                  </code>
                </TableCell>
                <TableCell>
                  <Badge variant={group.built_in ? "default" : "secondary"}>
                    {group.built_in ? "Built-in" : "Custom"}
                  </Badge>
                </TableCell>
                <TableCell>{group.member_count}</TableCell>
                <TableCell className="text-muted-foreground max-w-md text-sm">
                  {group.description}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      aria-label={`Edit access group ${group.name}`}
                      disabled={group.protected}
                      onClick={() => {
                        setEditingGroup(group);
                        setDialogOpen(true);
                      }}
                    >
                      <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      aria-label={`Delete access group ${group.name}`}
                      disabled={group.built_in || group.protected}
                      onClick={() => setDeleteGroup(group)}
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function AccessGroupDetailPanel({ group }: { group: AdminAccessGroup }) {
  const { data: detail, isLoading } = useAdminAccessGroup(group.slug);

  if (isLoading || !detail) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-20 w-full rounded-md" />
        <Skeleton className="h-32 w-full rounded-md" />
        <Skeleton className="h-32 w-full rounded-md" />
      </div>
    );
  }

  return (
    <div className="max-h-[72vh] space-y-5 overflow-y-auto pr-1">
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="border-border rounded-md border px-3 py-2">
          <div className="text-muted-foreground text-xs font-medium uppercase">Slug</div>
          <code className="mt-1 block font-mono text-sm">{detail.slug}</code>
        </div>
        <div className="border-border rounded-md border px-3 py-2">
          <div className="text-muted-foreground text-xs font-medium uppercase">Type</div>
          <div className="mt-1">
            <Badge variant={detail.built_in ? "default" : "secondary"}>
              {detail.built_in ? "Built-in" : "Custom"}
            </Badge>
          </div>
        </div>
        <div className="border-border rounded-md border px-3 py-2">
          <div className="text-muted-foreground text-xs font-medium uppercase">Members</div>
          <div className="mt-1 text-sm font-medium">{detail.member_count}</div>
        </div>
      </div>

      {detail.description && <p className="text-muted-foreground text-sm">{detail.description}</p>}

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">Members</h3>
        <div className="border-border overflow-hidden rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Username</TableHead>
                <TableHead>Email</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail.members.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className="text-muted-foreground text-center">
                    No members in this group.
                  </TableCell>
                </TableRow>
              ) : (
                detail.members.map((member) => (
                  <TableRow key={member.user_id}>
                    <TableCell className="font-medium">{member.username}</TableCell>
                    <TableCell>{member.email}</TableCell>
                    <TableCell>
                      <Badge variant={member.role === "admin" ? "default" : "secondary"}>
                        {member.role}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Badge variant={member.enabled ? "outline" : "destructive"}>
                        {member.enabled ? "Active" : "Disabled"}
                      </Badge>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">Rules</h3>
        <div className="border-border overflow-hidden rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Action</TableHead>
                <TableHead>Effect</TableHead>
                <TableHead>Resource</TableHead>
                <TableHead>Priority</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail.rules.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className="text-muted-foreground text-center">
                    No rules defined.
                  </TableCell>
                </TableRow>
              ) : (
                detail.rules.map((rule) => (
                  <TableRow key={rule.id}>
                    <TableCell>
                      <div className="font-medium">{accessGroupActionLabel(rule.action)}</div>
                      <div className="text-muted-foreground font-mono text-xs">{rule.action}</div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={rule.effect === "allow" ? "outline" : "destructive"}>
                        {rule.effect}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {rule.resource_type}:{rule.resource_id}
                    </TableCell>
                    <TableCell>{rule.priority}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </section>
    </div>
  );
}

function AccessGroupForm({
  group,
  onClose,
}: {
  group: AdminAccessGroup | null;
  onClose: () => void;
}) {
  const { data: detail, isLoading } = useAdminAccessGroup(group?.slug ?? null);
  const { data: libraries = [] } = useAdminLibraries();
  const createMutation = useCreateAdminAccessGroup();
  const updateMutation = useUpdateAdminAccessGroup();
  const [name, setName] = useState(group?.name ?? "");
  const [slug, setSlug] = useState(group?.slug ?? "");
  const [slugTouched, setSlugTouched] = useState(Boolean(group));
  const [description, setDescription] = useState(group?.description ?? "");
  const [selectedActions, setSelectedActions] = useState<AccessGroupAction[]>([]);
  const [libraryIDs, setLibraryIDs] = useState<number[] | null>(null);
  const [mediaTypes, setMediaTypes] = useState<string[]>([]);
  const [maxPlaybackQuality, setMaxPlaybackQuality] = useState("");
  const [maxStreams, setMaxStreams] = useState("");
  const [maxTranscodes, setMaxTranscodes] = useState("");
  const [maxProfiles, setMaxProfiles] = useState("");
  const nameId = useId();
  const slugId = useId();
  const descriptionId = useId();
  const maxPlaybackQualityId = useId();
  const maxStreamsId = useId();
  const maxTranscodesId = useId();
  const maxProfilesId = useId();
  const isPending = createMutation.isPending || updateMutation.isPending;

  useEffect(() => {
    if (!detail) return;
    setName(detail.name);
    setSlug(detail.slug);
    setDescription(detail.description);
    const actions = detail.rules
      .map((rule) => rule.action)
      .filter((action): action is AccessGroupAction => supportedActions.has(action));
    setSelectedActions(actions);
    const conditions = firstRuleConditions(detail);
    setLibraryIDs(conditions.library_ids ?? null);
    setMediaTypes(conditions.media_types ?? []);
    const policy = detail.policy ?? {};
    setMaxPlaybackQuality(policy.max_playback_quality ?? "");
    setMaxStreams(policy.max_streams != null ? String(policy.max_streams) : "");
    setMaxTranscodes(policy.max_transcodes != null ? String(policy.max_transcodes) : "");
    setMaxProfiles(policy.max_profiles != null ? String(policy.max_profiles) : "");
  }, [detail]);

  function handleNameChange(value: string) {
    setName(value);
    if (!group && !slugTouched) {
      setSlug(accessGroupSlugFromName(value));
    }
  }

  function toggleAction(action: AccessGroupAction, checked: boolean) {
    setSelectedActions((current) => {
      if (checked) {
        return current.includes(action) ? current : [...current, action];
      }
      return current.filter((value) => value !== action);
    });
  }

  function applyPreset(actions: readonly AccessGroupAction[]) {
    setSelectedActions(actions.filter((action) => supportedActions.has(action)));
  }

  function toggleMediaType(mediaType: string, checked: boolean) {
    setMediaTypes((current) => {
      if (checked) {
        return current.includes(mediaType) ? current : [...current, mediaType];
      }
      return current.filter((value) => value !== mediaType);
    });
  }

  function buildConditions(): AdminACLCondition {
    const conditions: AdminACLCondition = {};
    if (libraryIDs !== null) conditions.library_ids = libraryIDs;
    if (mediaTypes.length > 0) conditions.media_types = mediaTypes;
    return conditions;
  }

  function buildPolicy(): AdminACLPolicy {
    const policy: AdminACLPolicy = {};
    if (maxPlaybackQuality) policy.max_playback_quality = maxPlaybackQuality;
    if (maxStreams.trim() !== "") policy.max_streams = Number(maxStreams);
    if (maxTranscodes.trim() !== "") policy.max_transcodes = Number(maxTranscodes);
    if (maxProfiles.trim() !== "") policy.max_profiles = Number(maxProfiles);
    return policy;
  }

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    const body: AdminAccessGroupWriteRequest = {
      slug,
      name,
      description,
      policy: buildPolicy(),
      rules: buildAccessGroupRuleDrafts(selectedActions, buildConditions()),
    };
    if (group) {
      updateMutation.mutate({ slug: group.slug, body }, { onSuccess: onClose });
    } else {
      createMutation.mutate(body, { onSuccess: onClose });
    }
  }

  if (group && isLoading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-9 w-full rounded-md" />
        <Skeleton className="h-36 w-full rounded-md" />
      </div>
    );
  }

  return (
    <TooltipProvider delayDuration={150}>
      <form onSubmit={handleSubmit} className="flex max-h-[74vh] flex-col">
        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto pr-1">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor={nameId}>Name</Label>
              <Input
                id={nameId}
                value={name}
                onChange={(event) => handleNameChange(event.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor={slugId}>Slug</Label>
              <Input
                id={slugId}
                value={slug}
                onChange={(event) => {
                  setSlugTouched(true);
                  setSlug(accessGroupSlugFromName(event.target.value));
                }}
                disabled={Boolean(group)}
                required
              />
            </div>
          </div>
          <div className="space-y-2">
            <Label htmlFor={descriptionId}>Description</Label>
            <Input
              id={descriptionId}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
            />
          </div>

          <div className="border-border rounded-md border px-3 py-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <LabelWithHelp
                label="Actions"
                description="Choose the capabilities this group grants. Library and media filters below narrow where matching rules apply."
              />
              <div className="flex flex-wrap gap-2">
                {ACCESS_GROUP_PRESETS.map((preset) => (
                  <div key={preset.id} className="flex items-center gap-1">
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="h-8"
                          title={preset.description}
                          onClick={() => applyPreset(preset.actions)}
                        >
                          {preset.label}
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent side="top">{preset.description}</TooltipContent>
                    </Tooltip>
                    <HelpTooltip
                      label={`${preset.label} preset`}
                      description={preset.description}
                    />
                  </div>
                ))}
              </div>
            </div>
            <div className="mt-3 space-y-4">
              {ACCESS_GROUP_ACTION_CATEGORIES.map((category) => {
                const actions = ACCESS_GROUP_ACTIONS.filter(
                  (item) => item.category === category.id,
                );
                if (actions.length === 0) return null;
                return (
                  <div key={category.id} className="space-y-2">
                    <CategoryLabel label={category.label} description={category.description} />
                    <div className="grid gap-2 sm:grid-cols-2">
                      {actions.map((item) => (
                        <div
                          key={item.action}
                          className="border-border flex min-h-11 items-center justify-between rounded-md border px-3 py-2"
                        >
                          <div className="flex min-w-0 items-center gap-1.5">
                            <Label htmlFor={`acl-action-${item.action}`}>{item.label}</Label>
                            <HelpTooltip label={item.label} description={item.description} />
                          </div>
                          <Switch
                            id={`acl-action-${item.action}`}
                            checked={selectedActions.includes(item.action)}
                            onCheckedChange={(checked) => toggleAction(item.action, checked)}
                          />
                        </div>
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>

          <LibraryAccessSelector
            libraries={libraries}
            value={libraryIDs}
            onChange={setLibraryIDs}
            labelAccessory={
              <HelpTooltip label="Library Access" description={LIBRARY_ACCESS_HELP} />
            }
          />

          <div className="border-border rounded-md border px-3 py-3">
            <LabelWithHelp label="Media Types" description={MEDIA_TYPES_HELP} />
            <div className="mt-3 grid gap-2 sm:grid-cols-2">
              {MEDIA_TYPE_OPTIONS.map((item) => (
                <div
                  key={item.value}
                  className="border-border flex min-h-11 items-center justify-between rounded-md border px-3 py-2"
                >
                  <div className="flex min-w-0 items-center gap-1.5">
                    <Label htmlFor={`acl-media-${item.value}`}>{item.label}</Label>
                    <HelpTooltip label={item.label} description={item.description} />
                  </div>
                  <Switch
                    id={`acl-media-${item.value}`}
                    checked={mediaTypes.includes(item.value)}
                    onCheckedChange={(checked) => toggleMediaType(item.value, checked)}
                  />
                </div>
              ))}
            </div>
          </div>

          <div className="border-border rounded-md border px-3 py-3">
            <LabelWithHelp label="Policy Limits" description={POLICY_LIMIT_HELP} />
            <div className="mt-3 grid gap-3 sm:grid-cols-4">
              <div className="space-y-2">
                <LabelWithHelp
                  label="Max Quality"
                  description={MAX_QUALITY_HELP}
                  htmlFor={maxPlaybackQualityId}
                />
                <Select
                  value={maxPlaybackQuality || "none"}
                  onValueChange={(value) => setMaxPlaybackQuality(value === "none" ? "" : value)}
                >
                  <SelectTrigger id={maxPlaybackQualityId}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">Any</SelectItem>
                    {PLAYBACK_QUALITY_OPTIONS.map((quality) => (
                      <SelectItem key={quality} value={quality}>
                        {quality}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <LabelWithHelp
                  label="Max Streams"
                  description={MAX_STREAMS_HELP}
                  htmlFor={maxStreamsId}
                />
                <Input
                  id={maxStreamsId}
                  type="number"
                  min={0}
                  value={maxStreams}
                  onChange={(event) => setMaxStreams(event.target.value)}
                />
              </div>
              <div className="space-y-2">
                <LabelWithHelp
                  label="Max Transcodes"
                  description={MAX_TRANSCODES_HELP}
                  htmlFor={maxTranscodesId}
                />
                <Input
                  id={maxTranscodesId}
                  type="number"
                  min={0}
                  value={maxTranscodes}
                  onChange={(event) => setMaxTranscodes(event.target.value)}
                />
              </div>
              <div className="space-y-2">
                <LabelWithHelp
                  label="Max Profiles"
                  description={MAX_PROFILES_HELP}
                  htmlFor={maxProfilesId}
                />
                <Input
                  id={maxProfilesId}
                  type="number"
                  min={1}
                  value={maxProfiles}
                  onChange={(event) => setMaxProfiles(event.target.value)}
                />
              </div>
            </div>
          </div>
        </div>

        <div className="border-border mt-5 flex justify-end gap-2 border-t pt-4">
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={isPending}>
            {group ? "Save Group" : "Create Group"}
          </Button>
        </div>
      </form>
    </TooltipProvider>
  );
}

function LabelWithHelp({
  label,
  description,
  htmlFor,
}: {
  label: string;
  description: string;
  htmlFor?: string;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      <HelpTooltip label={label} description={description} />
    </div>
  );
}

function CategoryLabel({ label, description }: { label: string; description: string }) {
  return (
    <div className="text-muted-foreground flex items-center gap-1.5 text-xs font-medium uppercase">
      <span>{label}</span>
      <HelpTooltip label={label} description={description} />
    </div>
  );
}

function HelpTooltip({ label, description }: { label: string; description: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
          aria-label={`${label} help: ${description}`}
          title={description}
        >
          <Info className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="top">{description}</TooltipContent>
    </Tooltip>
  );
}

function firstRuleConditions(detail: AdminAccessGroupDetail): AdminACLCondition {
  return detail.rules[0]?.conditions ?? {};
}
