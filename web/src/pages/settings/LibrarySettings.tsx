import { useEffect, useId, useState, type ReactNode } from "react";
import type { LibraryPlaybackPreference, Profile, UserLibrary } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
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
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import {
  useDeleteLibraryPlaybackPreference,
  useLibraryPlaybackPreferences,
  useSetLibraryPlaybackPreference,
} from "@/hooks/queries/libraryPlaybackPreferences";
import {
  DISABLED_LIBRARY_IDS_SETTING_KEY,
  LIBRARY_ORDER_SETTING_KEY,
  applyLibraryOrder,
  parseDisabledLibraryIDs,
  parseLibraryOrder,
  serializeDisabledLibraryIDs,
  serializeLibraryOrder,
  useAvailableUserLibraries,
} from "@/hooks/queries/libraries";
import {
  useDeleteDeviceSetting,
  useEffectiveSettings,
  useSetting,
  useSetDeviceSetting,
  useSetSetting,
} from "@/hooks/queries/settings";
import {
  LIBRARY_PAGE_STATE_SETTING_KEY,
  REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY,
} from "@/hooks/queries/libraryPageState";
import {
  buildInheritedLanguageLabel,
  buildInheritedShowForcedSubtitlesLabel,
  buildInheritedSubtitleLanguageLabel,
  buildInheritedSubtitleModeLabel,
  buildLibraryPlaybackRequest,
  buildLibraryPlaybackSummaryFromState,
  createLibraryPlaybackEditorState,
  getProfileDefaultForcedSubtitlesHint,
  getProfileDefaultLanguageHint,
  getProfileDefaultSubtitleLanguageHint,
  getProfileDefaultSubtitleModeHint,
  hasLibraryPlaybackOverride,
  INHERIT_VALUE,
  LANGUAGE_OPTIONS,
  type LibraryPlaybackEditorState,
  NONE_VALUE,
  ORIGINAL_LANGUAGE_LABEL,
  ORIGINAL_LANGUAGE_VALUE,
  SUBTITLE_MODE_OPTIONS,
} from "./libraryPlaybackPreferences";
import { toast } from "sonner";
import { ChevronDown, ChevronRight, Eye, EyeOff, GripVertical, RotateCcw } from "lucide-react";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  KeyboardSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type { DragStartEvent, DragEndEvent } from "@dnd-kit/core";
import {
  SortableContext,
  verticalListSortingStrategy,
  useSortable,
  arrayMove,
} from "@dnd-kit/sortable";
import { sortableKeyboardCoordinates } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

function sortLibrariesByOrder(libraries: UserLibrary[], ids: number[]) {
  const selected = new Set(ids);
  return libraries.filter((library) => selected.has(library.id)).map((library) => library.id);
}

function RememberLibraryPageStateSetting({ profileId }: { profileId: string }) {
  const { data: effective = {} } = useEffectiveSettings(profileId, [
    REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY,
  ]);
  const setDeviceSetting = useSetDeviceSetting();
  const deleteDeviceSetting = useDeleteDeviceSetting();
  const rememberLibraryPages =
    effective[REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY]?.effective_value !== "false";
  const pending = setDeviceSetting.isPending || deleteDeviceSetting.isPending;

  async function handleChange(checked: boolean) {
    try {
      if (checked) {
        await deleteDeviceSetting.mutateAsync({ key: REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY });
      } else {
        await deleteDeviceSetting.mutateAsync({ key: LIBRARY_PAGE_STATE_SETTING_KEY });
        await setDeviceSetting.mutateAsync({
          key: REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY,
          value: "false",
        });
      }
      toast.success("Library page preference saved");
    } catch {
      toast.error("Failed to save library page preference");
    }
  }

  return (
    <SettingRow
      label="Remember library pages"
      description="Return each library to the last tab, sort, and filters used on this profile and device."
      control={(id) => (
        <Switch
          id={id}
          checked={rememberLibraryPages}
          disabled={pending}
          onCheckedChange={handleChange}
        />
      )}
    />
  );
}

function PlaybackField({
  label,
  value,
  disabled,
  hint,
  onChange,
  children,
}: {
  label: string;
  value: string;
  disabled?: boolean;
  hint?: string;
  onChange: (value: string) => void;
  children: ReactNode;
}) {
  const controlId = useId();

  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={controlId} className="text-muted-foreground text-xs font-medium">
        {label}
      </Label>
      <Select value={value} onValueChange={onChange} disabled={disabled}>
        <SelectTrigger id={controlId} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>{children}</SelectContent>
      </Select>
      {hint && <p className="text-muted-foreground/70 text-[11px] leading-tight">{hint}</p>}
    </div>
  );
}

function SortableLibraryCard({ id, children }: { id: number; children: React.ReactNode }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id,
  });

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <div ref={setNodeRef} style={style} className="flex gap-2">
      <button
        type="button"
        aria-label="Drag to reorder"
        className="hover:bg-surface-hover mt-4 cursor-grab touch-none self-start rounded-md p-1 transition-colors"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="text-muted-foreground h-4 w-4" />
      </button>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function LibraryCard({
  library,
  enabled,
  visibilityDisabled = false,
  preference,
  profile,
  onToggleVisibility,
}: {
  library: UserLibrary;
  enabled: boolean;
  visibilityDisabled?: boolean;
  preference: LibraryPlaybackPreference | null;
  profile: Profile;
  onToggleVisibility: (checked: boolean) => void;
}) {
  const controlId = useId();
  const setPlaybackPreference = useSetLibraryPlaybackPreference();
  const deletePlaybackPreference = useDeleteLibraryPlaybackPreference();
  const [expanded, setExpanded] = useState(false);
  const [editorState, setEditorState] = useState(() =>
    createLibraryPlaybackEditorState(preference),
  );

  useEffect(() => {
    // Keep the inline editor aligned with cached mutations and profile switches.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setEditorState(createLibraryPlaybackEditorState(preference));
  }, [preference, profile.id]);

  const playbackPending = setPlaybackPreference.isPending || deletePlaybackPreference.isPending;
  const summaryText = buildLibraryPlaybackSummaryFromState(editorState);
  const hasOverride = hasLibraryPlaybackOverride(editorState);

  function savePlaybackState(
    nextState: LibraryPlaybackEditorState,
    rollbackState: LibraryPlaybackEditorState,
  ) {
    setEditorState(nextState);

    const onError = () => {
      setEditorState(rollbackState);
      toast.error("Failed to update playback defaults");
    };

    if (!hasLibraryPlaybackOverride(nextState)) {
      deletePlaybackPreference.mutate(library.id, {
        onError,
        onSuccess: () => setExpanded(false),
      });
      return;
    }

    setPlaybackPreference.mutate(
      {
        libraryId: library.id,
        body: buildLibraryPlaybackRequest(nextState),
      },
      { onError },
    );
  }

  function handlePlaybackChange(field: keyof LibraryPlaybackEditorState, value: string) {
    const rollbackState = editorState;
    const nextState = { ...editorState, [field]: value };
    savePlaybackState(nextState, rollbackState);
  }

  function handleReset() {
    savePlaybackState(createLibraryPlaybackEditorState(null), editorState);
  }

  return (
    <div className="surface-panel overflow-hidden rounded-[1.5rem] border-0 transition-colors">
      <div className="flex flex-col gap-4 px-4 py-4 sm:px-5">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-sm font-medium">{library.name}</span>
            <Badge variant="outline" className="shrink-0 text-[11px] capitalize">
              {library.type}
            </Badge>
            {hasOverride && (
              <Badge variant="secondary" className="shrink-0 text-[11px]">
                Custom
              </Badge>
            )}
          </div>
          <p className="text-muted-foreground mt-0.5 text-xs leading-relaxed">{summaryText}</p>
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="text-muted-foreground hover:text-foreground justify-start gap-1 self-start text-xs"
            onClick={() => setExpanded((current) => !current)}
          >
            {expanded ? (
              <ChevronDown className="size-3.5" />
            ) : (
              <ChevronRight className="size-3.5" />
            )}
            <span>{expanded ? "Hide playback overrides" : "Edit playback overrides"}</span>
          </Button>
          <div className="surface-panel-subtle flex items-center justify-between rounded-[1rem] px-3 py-2 sm:min-w-[180px]">
            <Label htmlFor={controlId} className="text-xs font-medium">
              Visible in navigation
            </Label>
            <Switch
              id={controlId}
              checked={enabled}
              disabled={visibilityDisabled}
              onCheckedChange={onToggleVisibility}
            />
          </div>
        </div>
      </div>

      {expanded && (
        <div className="border-border/50 bg-muted/20 border-t px-4 py-4 sm:px-5">
          <p className="text-muted-foreground mb-3 text-xs">
            Override your profile&apos;s playback defaults for this library. Changes save
            automatically.
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            <PlaybackField
              label="Spoken language"
              value={editorState.audioLanguage}
              disabled={playbackPending}
              hint={getProfileDefaultLanguageHint(profile.language)}
              onChange={(value) => handlePlaybackChange("audioLanguage", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedLanguageLabel(profile.language)}
              </SelectItem>
              <SelectItem value={ORIGINAL_LANGUAGE_VALUE}>{ORIGINAL_LANGUAGE_LABEL}</SelectItem>
              {LANGUAGE_OPTIONS.map((language) => (
                <SelectItem key={language.code} value={language.code}>
                  {language.label}
                </SelectItem>
              ))}
            </PlaybackField>

            <PlaybackField
              label="Subtitle language"
              value={editorState.subtitleLanguage}
              disabled={playbackPending}
              hint={getProfileDefaultSubtitleLanguageHint(profile.subtitle_language)}
              onChange={(value) => handlePlaybackChange("subtitleLanguage", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedSubtitleLanguageLabel(profile.subtitle_language)}
              </SelectItem>
              <SelectItem value={NONE_VALUE}>None</SelectItem>
              {LANGUAGE_OPTIONS.map((language) => (
                <SelectItem key={language.code} value={language.code}>
                  {language.label}
                </SelectItem>
              ))}
            </PlaybackField>

            <PlaybackField
              label="Subtitle behavior"
              value={editorState.subtitleMode}
              disabled={playbackPending}
              hint={getProfileDefaultSubtitleModeHint(profile.subtitle_mode)}
              onChange={(value) => handlePlaybackChange("subtitleMode", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedSubtitleModeLabel(profile.subtitle_mode)}
              </SelectItem>
              {SUBTITLE_MODE_OPTIONS.map((mode) => (
                <SelectItem key={mode.value} value={mode.value}>
                  {mode.label}
                </SelectItem>
              ))}
            </PlaybackField>

            <PlaybackField
              label="Forced subtitles"
              value={editorState.showForcedSubtitles}
              disabled={playbackPending}
              hint={getProfileDefaultForcedSubtitlesHint(profile.show_forced_subtitles)}
              onChange={(value) => handlePlaybackChange("showForcedSubtitles", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedShowForcedSubtitlesLabel(profile.show_forced_subtitles)}
              </SelectItem>
              <SelectItem value="on">On</SelectItem>
              <SelectItem value="off">Off</SelectItem>
            </PlaybackField>
          </div>

          {hasOverride && (
            <div className="mt-3 flex justify-end">
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="text-muted-foreground hover:text-foreground gap-1.5 text-xs"
                disabled={playbackPending}
                onClick={handleReset}
              >
                <RotateCcw className="size-3" />
                Reset to profile defaults
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default function LibrarySettings() {
  const { data: libraries, isLoading: librariesLoading } = useAvailableUserLibraries();
  const { data: disabledSetting, isLoading: disabledSettingLoading } = useSetting(
    DISABLED_LIBRARY_IDS_SETTING_KEY,
  );
  const { data: orderSetting, isLoading: orderSettingLoading } =
    useSetting(LIBRARY_ORDER_SETTING_KEY);
  const { profile: currentProfile, isLoading: profileLoading } = useCurrentProfile();
  const { data: playbackPreferences, isLoading: playbackPrefsLoading } =
    useLibraryPlaybackPreferences({
      enabled: !!currentProfile,
    });
  const setSetting = useSetSetting();
  const [disabledLibraryIDs, setDisabledLibraryIDs] = useState<number[]>([]);
  const [orderedLibraries, setOrderedLibraries] = useState<UserLibrary[]>([]);
  const [activeId, setActiveId] = useState<number | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  useEffect(() => {
    const parsedIDs = parseDisabledLibraryIDs(disabledSetting);
    // Keep the editable local order in sync with the latest saved setting.
    // This supports optimistic updates and rollback without changing behavior.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setDisabledLibraryIDs(libraries ? sortLibrariesByOrder(libraries, parsedIDs) : parsedIDs);
  }, [disabledSetting, libraries]);

  useEffect(() => {
    if (!libraries) return;
    const order = parseLibraryOrder(orderSetting);
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setOrderedLibraries(order.length > 0 ? applyLibraryOrder(libraries, order) : libraries);
  }, [libraries, orderSetting]);

  function saveDisabledLibraries(nextDisabledLibraryIDs: number[], rollbackIDs: number[]) {
    setDisabledLibraryIDs(nextDisabledLibraryIDs);
    setSetting.mutate(
      {
        key: DISABLED_LIBRARY_IDS_SETTING_KEY,
        value: serializeDisabledLibraryIDs(nextDisabledLibraryIDs),
      },
      {
        onError: () => {
          setDisabledLibraryIDs(rollbackIDs);
          toast.error("Failed to update library visibility");
        },
      },
    );
  }

  function handleLibraryToggle(libraryId: number, enabled: boolean) {
    if (!libraries) return;

    const current = disabledLibraryIDs;
    const next = enabled
      ? current.filter((id) => id !== libraryId)
      : sortLibrariesByOrder(libraries, [...current, libraryId]);

    saveDisabledLibraries(next, current);
  }

  function handleEnableAll() {
    saveDisabledLibraries([], disabledLibraryIDs);
  }

  function handleDisableAll() {
    if (!libraries) return;
    saveDisabledLibraries(
      libraries.map((library) => library.id),
      disabledLibraryIDs,
    );
  }

  function handleDragStart(event: DragStartEvent) {
    setActiveId(event.active.id as number);
  }

  function handleDragEnd(event: DragEndEvent) {
    setActiveId(null);
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = orderedLibraries.findIndex((l) => l.id === active.id);
    const newIndex = orderedLibraries.findIndex((l) => l.id === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    const next = arrayMove(orderedLibraries, oldIndex, newIndex);
    const prev = orderedLibraries;
    setOrderedLibraries(next);
    setSetting.mutate(
      {
        key: LIBRARY_ORDER_SETTING_KEY,
        value: serializeLibraryOrder(next.map((l) => l.id)),
      },
      {
        onError: () => {
          setOrderedLibraries(prev);
          toast.error("Failed to update library order");
        },
      },
    );
  }

  function handleDragCancel() {
    setActiveId(null);
  }

  const activeLibrary = activeId != null ? orderedLibraries.find((l) => l.id === activeId) : null;

  if (
    librariesLoading ||
    disabledSettingLoading ||
    orderSettingLoading ||
    profileLoading ||
    playbackPrefsLoading
  ) {
    return <div className="text-muted-foreground pt-4">Loading libraries...</div>;
  }

  if (!libraries || libraries.length === 0) {
    return (
      <div className="text-muted-foreground pt-4">
        No libraries are available for this account right now.
      </div>
    );
  }

  if (!currentProfile) {
    return <div className="text-muted-foreground pt-4">Choose a profile to manage libraries.</div>;
  }

  const displayLibraries = orderedLibraries.length > 0 ? orderedLibraries : libraries;
  const visibleCount = libraries.filter(
    (library) => !disabledLibraryIDs.includes(library.id),
  ).length;
  const preferencesByLibraryId = new Map(
    (playbackPreferences ?? []).map((preference) => [preference.library_id, preference]),
  );

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Libraries</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Toggle which libraries appear in your navigation and customize playback defaults per
          library.
        </p>
      </div>

      <SettingsGroup
        title="Browsing"
        description="These preferences apply to this profile on the current device."
      >
        <RememberLibraryPageStateSetting profileId={currentProfile.id} />
      </SettingsGroup>

      <div className="surface-panel-subtle flex flex-col gap-4 rounded-[1.4rem] p-4 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-muted-foreground text-sm leading-relaxed">
          <span className="text-foreground font-medium">{visibleCount}</span> of {libraries.length}{" "}
          libraries visible for this profile.
        </p>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Button
            size="sm"
            variant="outline"
            className="gap-1.5 sm:min-w-[120px]"
            onClick={handleEnableAll}
            disabled={setSetting.isPending || visibleCount === libraries.length}
          >
            <Eye className="size-3.5" />
            Show all
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="gap-1.5 sm:min-w-[120px]"
            onClick={handleDisableAll}
            disabled={setSetting.isPending || visibleCount === 0}
          >
            <EyeOff className="size-3.5" />
            Hide all
          </Button>
        </div>
      </div>

      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
        onDragCancel={handleDragCancel}
      >
        <SortableContext
          items={displayLibraries.map((l) => l.id)}
          strategy={verticalListSortingStrategy}
        >
          <div className="space-y-3">
            {displayLibraries.map((library) => {
              const enabled = !disabledLibraryIDs.includes(library.id);
              return (
                <SortableLibraryCard key={`${currentProfile.id}:${library.id}`} id={library.id}>
                  <LibraryCard
                    library={library}
                    enabled={enabled}
                    visibilityDisabled={setSetting.isPending}
                    preference={preferencesByLibraryId.get(library.id) ?? null}
                    profile={currentProfile}
                    onToggleVisibility={(checked) => handleLibraryToggle(library.id, checked)}
                  />
                </SortableLibraryCard>
              );
            })}
          </div>
        </SortableContext>
        <DragOverlay>
          {activeLibrary ? (
            <div className="surface-panel flex items-center gap-3 rounded-[1.5rem] px-4 py-4 shadow-lg">
              <GripVertical className="text-muted-foreground h-4 w-4" />
              <span className="text-sm font-medium">{activeLibrary.name}</span>
              <Badge variant="outline" className="text-[11px] capitalize">
                {activeLibrary.type}
              </Badge>
            </div>
          ) : null}
        </DragOverlay>
      </DndContext>
    </div>
  );
}
