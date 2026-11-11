import { ArrowLeft, Ellipsis, Trash2 } from "lucide-react";
import { useRef, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";

import { getAccessGroup, type AccessGroupEditor as GroupEditor } from "@/api/v2/accessGroups";
import { isRequestEditorConflict } from "@/api/v2/adminRequests";
import { V2ProblemError } from "@/api/v2/request";
import type { AccessGroup, AccessGroupInput } from "@/api/types";
import { EditorConflict } from "@/components/admin/EditorConflict";
import { RequestLimitFields } from "@/components/admin/RequestLimitFields";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
import { Textarea } from "@/components/ui/textarea";
import {
  useAccessGroups,
  useDeleteAccessGroup,
  useUpdateAccessGroup,
} from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useRequestGroupLimit,
  useRequestSettings,
  useUpdateRequestGroupLimit,
} from "@/hooks/queries/admin/requests";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useT, type MessageKey } from "@/i18n";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";
import {
  describeInheritedValue,
  formatRequestApproval,
  formatRequestQuota,
  hasRequestLimitErrors,
  requestLimitBody,
  requestLimitChanges,
  requestLimitDraft,
  requestLimitErrors,
  resolveRequestTerms,
} from "@/lib/requestAccess";
import { TOUCH_HIT_AREA, TOUCH_TARGETS } from "@/lib/touchTargets";
import { cn } from "@/lib/utils";
import { useStagedDraft } from "@/pages/admin-settings/useStagedDraft";
import { parseWholeNumber } from "@/pages/admin-users/detail/access/policySources";
import {
  PLAYBACK_QUALITY_OPTIONS,
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
  type PlaybackQualityPreset,
} from "@/lib/playback-quality";

import { LimitField, StreamBitrateLimitField, ToggleRow } from "./AccessGroupFields";
import { AccessGroupMembers } from "./AccessGroupMembers";
import { AccessGroupSaveBar } from "./AccessGroupSaveBar";

// Tailwind's lg breakpoint: the editor's bottom button row from here up, the
// pinned save bar below. Tests have no matchMedia and get the button row.
const DESKTOP_QUERY = "(min-width: 1024px)";

// Menu rows tall enough to tap on a phone.
const MENU_ITEM_CLASS = "min-h-11";

// The two assignable permissions (mirrors auth.assignablePermissions). A group
// mask of `null` means "all assignable"; a list narrows to those named.
const ASSIGNABLE_PERMISSIONS: Array<{
  value: string;
  label: MessageKey;
  description: MessageKey;
}> = [
  {
    value: PERMISSION_METADATA_CURATION,
    label: "web.admin.access.groups.permissions.metadataCuration",
    description: "web.admin.access.groups.permissions.metadataCurationDescription",
  },
  {
    value: PERMISSION_MARKER_EDIT,
    label: "web.admin.access.groups.permissions.markerEdit",
    description: "web.admin.access.groups.permissions.markerEditDescription",
  },
];

/**
 * The body a save would send for the group as stored, built with the same
 * normalization as the editor's (quality preset, clamped transcoded
 * downloads), so an untouched draft compares equal to it.
 */
function storedGroupBody(group: AccessGroup): AccessGroupInput {
  return {
    name: group.name.trim(),
    description: group.description.trim(),
    library_ids: group.library_ids,
    max_playback_quality: playbackQualityValueFromPreset(
      playbackQualityPresetFromValue(group.max_playback_quality),
    ),
    download_allowed: group.download_allowed,
    download_transcode_allowed: group.download_allowed && group.download_transcode_allowed,
    transcode_allowed: group.transcode_allowed,
    audio_transcode_allowed: group.audio_transcode_allowed,
    max_streams: group.max_streams,
    max_transcodes: group.max_transcodes,
    max_remote_stream_bitrate_kbps: group.max_remote_stream_bitrate_kbps,
    max_local_stream_bitrate_kbps: group.max_local_stream_bitrate_kbps,
    max_profiles: group.max_profiles,
    allowed_permissions: group.allowed_permissions,
    requests_allowed: group.requests_allowed,
    is_default: group.is_default,
  };
}

function sameGroupBody(a: AccessGroupInput, b: AccessGroupInput): boolean {
  return (Object.keys(b) as Array<keyof AccessGroupInput>).every(
    (key) => JSON.stringify(a[key]) === JSON.stringify(b[key]),
  );
}

/** The description is one line: line breaks typed or pasted on a phone become spaces. */
function singleLine(value: string): string {
  return value.replace(/\r\n|[\r\n]/g, " ");
}

interface AccessGroupEditorProps {
  initialEditor: GroupEditor;
  onSaved: () => void;
  onDeleted: () => void;
  /** Back to the list of groups. */
  onBack: () => void;
}

export function AccessGroupEditor({
  initialEditor,
  onSaved,
  onDeleted,
  onBack,
}: AccessGroupEditorProps) {
  const t = useT();
  const isDesktop = useMediaQuery(DESKTOP_QUERY, true);
  const [editor, setEditor] = useState(initialEditor);
  const group = editor.group;
  const busy = useRef(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [reloading, setReloading] = useState(false);
  function failed(err: unknown) {
    setError(err instanceof Error ? err.message : t("web.admin.access.groups.actionFailed"));
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
  // Deleting a group moves its members into the default group (server-side,
  // in the same transaction), so the confirmation names where they go.
  const allGroups = useAccessGroups();
  const defaultGroupName = (allGroups.data ?? []).find(
    (candidate) => candidate.is_default && String(candidate.id) !== String(group.id),
  )?.name;
  // The single-group read carries no member count; the list does.
  const memberCount = (allGroups.data ?? []).find(
    (candidate) => String(candidate.id) === String(group.id),
  )?.member_count;
  // The count is cached and can be stale (an account may have joined since the
  // list loaded), so a zero never drops the move warning.
  const deleteDescription = t("web.admin.access.groups.delete.description", {
    members: memberCount === undefined ? "unknown" : memberCount === 0 ? "none" : "some",
    count: memberCount ?? 0,
    count_text: memberCount ?? 0,
    destination: defaultGroupName ? "named" : "unnamed",
    default_group: defaultGroupName ?? "",
  });

  // Draft state, keyed by group id via the parent's selection so switching
  // groups remounts this component with fresh initial values. `discard` below
  // resets each of these to the stored group: keep the two lists in step.
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
  // Kept as typed so a cleared box is an unsaved edit; Save waits for a valid limit.
  const [maxProfilesText, setMaxProfilesText] = useState(String(group.max_profiles));
  const maxProfiles = parseWholeNumber(maxProfilesText, 1);
  const [permissions, setPermissions] = useState<string[] | null>(group.allowed_permissions);
  const [requestsAllowed, setRequestsAllowed] = useState(group.requests_allowed);
  const [isDefault, setIsDefault] = useState(group.is_default);
  // Bumped by Discard to remount the fields that keep their own copy of what
  // was typed (the custom bitrate boxes, the request limit's numbers).
  const [draftGeneration, setDraftGeneration] = useState(0);

  // The group's request approval and limit are their own record with their
  // own validator; Save writes them after the group when they changed.
  const groupLimit = useRequestGroupLimit(Number(group.id), editor.profileContext);
  const requestSettings = useRequestSettings();
  const requestLimit = useStagedDraft(groupLimit.data, requestLimitDraft, requestLimitChanges);
  const updateGroupLimit = useUpdateRequestGroupLimit();
  const [limitConflict, setLimitConflict] = useState(false);
  const [limitError, setLimitError] = useState("");
  const limitErrors = requestLimit.draft ? requestLimitErrors(requestLimit.draft) : {};
  const limitBlocksSave =
    requestLimit.changes > 0 && (limitConflict || hasRequestLimitErrors(limitErrors));
  const serverTerms = requestSettings.data
    ? resolveRequestTerms([], requestSettings.data)
    : undefined;

  async function reloadLimit() {
    const result = await groupLimit.refetch();
    if (result.data && !result.isError) {
      requestLimit.adopt(result.data);
      setLimitConflict(false);
      setLimitError("");
    } else {
      toast.error(t("web.admin.access.groups.reloadLimitFailed"));
    }
  }

  const allPermissions = permissions === null;

  function setPermissionAllowed(permission: string, allowed: boolean) {
    const current = permissions ?? ASSIGNABLE_PERMISSIONS.map((entry) => entry.value);
    const next = allowed
      ? Array.from(new Set([...current, permission]))
      : current.filter((value) => value !== permission);
    setPermissions(next);
  }

  // What Save would send, or null while a limit box holds no valid value.
  const draftBody: AccessGroupInput | null =
    maxRemoteStreamBitrateKbps === null ||
    maxLocalStreamBitrateKbps === null ||
    maxProfiles === null
      ? null
      : {
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
          max_profiles: maxProfiles,
          allowed_permissions: permissions,
          requests_allowed: requestsAllowed,
          is_default: isDefault,
        };
  // An invalid limit box is an edit too: the stored group always holds a valid one.
  const dirty =
    draftBody === null ||
    !sameGroupBody(draftBody, storedGroupBody(group)) ||
    requestLimit.changes > 0;

  async function save() {
    if (busy.current || conflict || limitBlocksSave) return;
    if (draftBody === null) return;
    busy.current = true;
    setError("");
    setLimitError("");
    const body = draftBody;
    // Each group write advances its revision and can collide with another
    // admin's edit, so a save that only changed the request limit, or a retry
    // after the limit failed, leaves the group alone.
    const groupChanged = !sameGroupBody(body, storedGroupBody(group));
    if (groupChanged) {
      try {
        setEditor(await updateGroup.mutateAsync({ editor, body }));
      } catch (err) {
        failed(err);
        busy.current = false;
        return;
      }
    }
    const { base: limitBase, draft: limitDraft } = requestLimit;
    try {
      if (requestLimit.changes > 0 && limitBase && limitDraft) {
        requestLimit.adopt(
          await updateGroupLimit.mutateAsync({
            limit: limitBase,
            body: requestLimitBody(limitDraft),
            profileContext: editor.profileContext,
          }),
        );
      }
      onSaved();
    } catch (err) {
      if (isRequestEditorConflict(err)) setLimitConflict(true);
      const reason = err instanceof Error ? err.message : t("web.admin.access.groups.saveFailed");
      setLimitError(
        groupChanged
          ? t("web.admin.access.groups.limitNotSavedAfterGroup", { reason })
          : t("web.admin.access.groups.limitNotSaved", { reason }),
      );
    } finally {
      busy.current = false;
    }
  }

  /** Throws the draft away: every field goes back to the group as stored. */
  function discard() {
    if (busy.current) return;
    setName(group.name);
    setDescription(group.description);
    setLibraryIds(group.library_ids);
    setQualityPreset(playbackQualityPresetFromValue(group.max_playback_quality));
    setDownloadAllowed(group.download_allowed);
    setTranscodeAllowed(group.download_transcode_allowed);
    setVideoTranscodeAllowed(group.transcode_allowed);
    setAudioTranscodeAllowed(group.audio_transcode_allowed);
    setMaxStreams(group.max_streams);
    setMaxTranscodes(group.max_transcodes);
    setMaxRemoteStreamBitrateKbps(group.max_remote_stream_bitrate_kbps);
    setMaxLocalStreamBitrateKbps(group.max_local_stream_bitrate_kbps);
    setMaxProfilesText(String(group.max_profiles));
    setPermissions(group.allowed_permissions);
    setRequestsAllowed(group.requests_allowed);
    setIsDefault(group.is_default);
    requestLimit.reset();
    setDraftGeneration((generation) => generation + 1);
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

  const saving = updateGroup.isPending || updateGroupLimit.isPending;
  const saveDisabled =
    updateGroup.isPending ||
    updateGroupLimit.isPending ||
    deleteGroup.isPending ||
    conflict ||
    reloading ||
    !bitrateLimitsValid ||
    maxProfiles === null ||
    limitBlocksSave;
  const saveLabel = saving
    ? t("web.admin.access.groups.saving")
    : t("web.admin.access.groups.save");
  // On a phone with unsaved edits the save messages move into the pinned bar,
  // next to the Save they are about.
  const barVisible = !isDesktop && dirty;

  const saveMessages = (
    <>
      {error && <p role="alert">{error}</p>}
      {conflict && (
        <div>
          {t("web.admin.access.groups.conflict.message", {
            reload: (chunks) => (
              <Button disabled={reloading} onClick={() => void reload()}>
                {chunks}
              </Button>
            ),
          })}
        </div>
      )}
    </>
  );
  const limitMessages = (
    <>
      {limitError && (
        <p role="alert" className="text-destructive text-sm">
          {limitError}
        </p>
      )}
      {limitConflict && <EditorConflict onReload={reloadLimit} />}
    </>
  );

  return (
    <>
      <div className="flex items-center justify-between gap-2">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-muted-foreground -ms-2 w-fit"
          onClick={onBack}
        >
          <ArrowLeft className="size-4" />
          {t("web.admin.access.groups.allGroups")}
        </Button>
        <div className="lg:hidden">
          {/* Not modal, like the users list menu, so the item can open a dialog. */}
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground data-[state=open]:bg-accent data-[state=open]:text-foreground -me-2 size-11 rounded-[10px]"
                aria-label={t("web.admin.users.detail.header.moreActions")}
              >
                <Ellipsis className="size-4" aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className={cn("w-64 max-w-[calc(100vw-2rem)]", TOUCH_TARGETS)}
            >
              <DropdownMenuItem
                className={cn(MENU_ITEM_CLASS, "flex-wrap")}
                variant="destructive"
                disabled={group.is_default}
                onSelect={() => setConfirmDelete(true)}
              >
                <Trash2 aria-hidden="true" />
                {t("web.admin.access.groups.delete.button")}
                {group.is_default && (
                  <span className="text-muted-foreground basis-full text-xs">
                    {t("web.admin.access.groups.delete.defaultLocked")}
                  </span>
                )}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      <div className="space-y-5">
        {!barVisible && saveMessages}
        <div className="surface-panel space-y-4 rounded-2xl border-0 p-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="group-name">{t("web.admin.access.groups.field.name")}</Label>
              <Input
                id="group-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="group-description">
                {t("web.admin.access.groups.field.description")}
              </Label>
              {isDesktop ? (
                <Input
                  id="group-description"
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  placeholder={t("web.admin.access.groups.field.descriptionPlaceholder")}
                />
              ) : (
                // Wraps a long description on a phone. It stays one line of
                // text, as the desktop input keeps it: Enter adds nothing and
                // pasted line breaks become spaces.
                <Textarea
                  id="group-description"
                  rows={2}
                  value={description}
                  onChange={(event) => setDescription(singleLine(event.target.value))}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") event.preventDefault();
                  }}
                  enterKeyHint="done"
                  placeholder={t("web.admin.access.groups.field.descriptionPlaceholder")}
                  className="field-sizing-content max-h-40 min-h-16 resize-none"
                />
              )}
            </div>
          </div>
          <ToggleRow
            label={t("web.admin.access.groups.default.label")}
            description={
              group.is_default
                ? t("web.admin.access.groups.default.descriptionLocked")
                : t("web.admin.access.groups.default.description")
            }
            checked={isDefault}
            onCheckedChange={setIsDefault}
            disabled={group.is_default}
          />
        </div>

        <section className="surface-panel space-y-4 rounded-2xl border-0 p-5">
          <h2 className="text-sm font-semibold">{t("web.admin.access.groups.playback.heading")}</h2>
          <LibraryAccessSelector
            libraries={libraries.data ?? []}
            value={libraryIds}
            onChange={setLibraryIds}
          />
          <div className="space-y-2">
            <Label htmlFor="group-quality">{t("web.admin.access.groups.playback.quality")}</Label>
            <Select
              value={qualityPreset}
              onValueChange={(value) => setQualityPreset(value as PlaybackQualityPreset)}
            >
              <SelectTrigger id="group-quality" className="w-full sm:w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent className={TOUCH_TARGETS}>
                {PLAYBACK_QUALITY_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {t("web.admin.access.groups.playback.qualityOption", {
                      label: option.label,
                      description: option.description,
                    })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <StreamBitrateLimitField
              key={`remote-${draftGeneration}`}
              id="group-stream-bitrate"
              label={t("web.admin.access.groups.playback.remoteBitrate")}
              hint={t("web.admin.access.groups.playback.remoteBitrateHint")}
              value={maxRemoteStreamBitrateKbps}
              onChange={setMaxRemoteStreamBitrateKbps}
            />
            <StreamBitrateLimitField
              key={`local-${draftGeneration}`}
              id="group-local-stream-bitrate"
              label={t("web.admin.access.groups.playback.localBitrate")}
              hint={t("web.admin.access.groups.playback.localBitrateHint")}
              value={maxLocalStreamBitrateKbps}
              onChange={setMaxLocalStreamBitrateKbps}
            />
          </div>
        </section>

        <section className="surface-panel space-y-3 rounded-2xl border-0 p-5">
          <h2 className="text-sm font-semibold">
            {t("web.admin.access.groups.downloads.heading")}
          </h2>
          <ToggleRow
            label={t("web.admin.access.groups.downloads.allow")}
            description={t("web.admin.access.groups.downloads.allowDescription")}
            checked={downloadAllowed}
            onCheckedChange={setDownloadAllowed}
          />
          <ToggleRow
            label={t("web.admin.access.groups.downloads.transcoded")}
            description={t("web.admin.access.groups.downloads.transcodedDescription")}
            checked={transcodeAllowed}
            onCheckedChange={setTranscodeAllowed}
            disabled={!downloadAllowed}
          />
        </section>

        <section
          className="surface-panel space-y-4 rounded-2xl border-0 p-5"
          aria-labelledby="group-requests-heading"
        >
          <div>
            <h2 id="group-requests-heading" className="text-sm font-semibold">
              {t("web.admin.access.groups.requests.heading")}
            </h2>
            <p className="text-muted-foreground mt-0.5 text-xs">
              {t("web.admin.access.groups.requests.intro", {
                link: (chunks) => (
                  <Link
                    to="/admin/settings/requests"
                    className={cn("text-foreground underline", TOUCH_HIT_AREA)}
                  >
                    {chunks}
                  </Link>
                ),
              })}
            </p>
          </div>
          <ToggleRow
            label={t("web.admin.access.groups.requests.allow")}
            description={t("web.admin.access.groups.requests.allowDescription")}
            checked={requestsAllowed}
            onCheckedChange={setRequestsAllowed}
          />
          {groupLimit.isError ? (
            <p role="alert" className="text-sm">
              {t("web.admin.access.groups.requests.loadFailed", {
                retry: (chunks) => (
                  <Button
                    variant="link"
                    className="h-auto p-0"
                    onClick={() => void groupLimit.refetch()}
                  >
                    {chunks}
                  </Button>
                ),
              })}
            </p>
          ) : requestLimit.draft ? (
            <RequestLimitFields
              key={draftGeneration}
              draft={requestLimit.draft}
              onChange={(next) => requestLimit.update(() => next)}
              subject="group"
              inheritLabel={t("web.admin.access.groups.requests.inheritLabel")}
              inherited={
                serverTerms
                  ? {
                      approval: describeInheritedValue(
                        formatRequestApproval(serverTerms.autoApprove),
                        serverTerms.approvalSource,
                        null,
                      ),
                      limit: describeInheritedValue(
                        formatRequestQuota(serverTerms.quota),
                        serverTerms.quotaSource,
                        null,
                      ),
                    }
                  : undefined
              }
              customSeed={
                serverTerms && !serverTerms.quota.unlimited
                  ? {
                      maxRequests: String(serverTerms.quota.max),
                      windowDays: String(serverTerms.quota.days),
                    }
                  : { maxRequests: "", windowDays: "" }
              }
              errors={limitErrors}
              disabled={updateGroupLimit.isPending}
            />
          ) : (
            <p className="text-muted-foreground text-sm">
              {t("web.admin.access.groups.requests.loading")}
            </p>
          )}
          {!barVisible && limitMessages}
        </section>

        <section className="surface-panel space-y-4 rounded-2xl border-0 p-5">
          <h2 className="text-sm font-semibold">{t("web.admin.access.groups.streams.heading")}</h2>
          <div className="grid gap-4 sm:grid-cols-2">
            <LimitField
              id="group-streams"
              label={t("web.admin.access.groups.streams.max")}
              hint={t("web.admin.access.groups.streams.zeroUnlimited")}
              value={maxStreams}
              onChange={setMaxStreams}
            />
            <LimitField
              id="group-transcodes"
              label={t("web.admin.access.groups.streams.maxTranscodes")}
              hint={t("web.admin.access.groups.streams.zeroUnlimited")}
              value={maxTranscodes}
              onChange={setMaxTranscodes}
            />
          </div>
          <ToggleRow
            label={t("web.admin.access.groups.streams.videoTranscode")}
            description={t("web.admin.access.groups.streams.videoTranscodeDescription")}
            checked={videoTranscodeAllowed}
            onCheckedChange={setVideoTranscodeAllowed}
          />
          <ToggleRow
            label={t("web.admin.access.groups.streams.audioTranscode")}
            description={t("web.admin.access.groups.streams.audioTranscodeDescription")}
            checked={audioTranscodeAllowed}
            onCheckedChange={setAudioTranscodeAllowed}
          />
        </section>

        <section className="surface-panel space-y-2 rounded-2xl border-0 p-5">
          <h2 className="text-sm font-semibold">{t("web.admin.access.groups.profiles.heading")}</h2>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <div className="flex items-baseline justify-between">
                <Label htmlFor="group-profiles">{t("web.admin.access.groups.profiles.max")}</Label>
                <span className="text-muted-foreground text-xs">
                  {t("web.admin.access.groups.profiles.atLeastOne")}
                </span>
              </div>
              <Input
                id="group-profiles"
                type="number"
                inputMode="numeric"
                min={1}
                step={1}
                value={maxProfilesText}
                aria-invalid={maxProfiles === null ? true : undefined}
                onChange={(event) => setMaxProfilesText(event.target.value)}
              />
            </div>
          </div>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.access.groups.profiles.help")}
          </p>
        </section>

        <section className="surface-panel space-y-3 rounded-2xl border-0 p-5">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-sm font-semibold">
                {t("web.admin.access.groups.permissions.heading")}
              </h2>
              <p className="text-muted-foreground mt-0.5 text-xs">
                {t("web.admin.access.groups.permissions.help")}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground text-xs">
                {t("web.admin.access.groups.permissions.allowAll")}
              </span>
              <Switch
                checked={allPermissions}
                onCheckedChange={(checked) =>
                  setPermissions(
                    checked ? null : ASSIGNABLE_PERMISSIONS.map((entry) => entry.value),
                  )
                }
                aria-label={t("web.admin.access.groups.permissions.allowAllLabel")}
              />
            </div>
          </div>
          {!allPermissions && (
            <div className="space-y-2">
              {ASSIGNABLE_PERMISSIONS.map((permission) => (
                <ToggleRow
                  key={permission.value}
                  label={t(permission.label)}
                  description={t(permission.description)}
                  checked={permissions?.includes(permission.value) ?? false}
                  onCheckedChange={(checked) => setPermissionAllowed(permission.value, checked)}
                />
              ))}
            </div>
          )}
        </section>

        <AccessGroupMembers groupId={group.id} />

        {/* Below lg, Delete is in the menu at the top and Save in the pinned bar. */}
        <div className="flex flex-wrap items-center justify-between gap-3 max-lg:hidden">
          <div className="flex flex-col gap-1">
            <Button
              type="button"
              variant="ghost"
              className="text-destructive hover:text-destructive"
              onClick={() => setConfirmDelete(true)}
              disabled={group.is_default}
            >
              <Trash2 className="size-4" />
              {t("web.admin.access.groups.delete.button")}
            </Button>
            {group.is_default && (
              <p className="text-muted-foreground text-xs">
                {t("web.admin.access.groups.delete.defaultLocked")}
              </p>
            )}
          </div>
          <Button type="button" onClick={save} disabled={saveDisabled}>
            {saveLabel}
          </Button>
        </div>

        {barVisible && (
          <AccessGroupSaveBar
            onDiscard={discard}
            onSave={() => void save()}
            saveDisabled={saveDisabled}
            discardDisabled={saving || deleteGroup.isPending || reloading}
            saveLabel={saveLabel}
            messages={
              error || conflict || limitError || limitConflict ? (
                <>
                  {saveMessages}
                  {limitMessages}
                </>
              ) : undefined
            }
          />
        )}

        <AlertDialog
          open={confirmDelete}
          onOpenChange={(open) => {
            if (!busy.current) setConfirmDelete(open);
          }}
        >
          <AlertDialogContent className={TOUCH_TARGETS}>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("web.admin.access.groups.delete.title", { name: group.name })}
              </AlertDialogTitle>
              <AlertDialogDescription>{deleteDescription}</AlertDialogDescription>
            </AlertDialogHeader>
            {error && <p role="alert">{error}</p>}
            {conflict && (
              <Button disabled={reloading} onClick={() => void reload()}>
                {t("web.admin.access.groups.conflict.reload")}
              </Button>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={deleteGroup.isPending || reloading}>
                {t("common.cancel")}
              </AlertDialogCancel>
              <AlertDialogAction
                onClick={(event) => {
                  event.preventDefault();
                  void remove();
                }}
                disabled={deleteGroup.isPending || conflict || reloading}
                className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              >
                {t("web.admin.access.groups.delete.confirm")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </>
  );
}
