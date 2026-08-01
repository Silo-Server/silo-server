import { useEffect, useId, useState, type ReactNode } from "react";
import type { UserLibrary } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { LanguageSelect } from "@/components/settings/LanguageSelect";
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
  applyLibraryOrder,
  normalizeLibraryIDs,
  useAvailableUserLibraries,
  useLibraryDisplayPreferences,
} from "@/hooks/queries/libraries";
import {
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
} from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { ApiClientError } from "@/api/client";
import {
  buildInheritedLanguageLabel,
  buildInheritedShowForcedSubtitlesLabel,
  buildInheritedSubtitleLanguageLabel,
  buildInheritedSubtitleModeLabel,
  buildLibraryPlaybackMutations,
  buildLibraryPlaybackSummaryFromState,
  createLibraryPlaybackEditorState,
  getProfileDefaultForcedSubtitlesHint,
  getProfileDefaultLanguageHint,
  getProfileDefaultSubtitleLanguageHint,
  getProfileDefaultSubtitleModeHint,
  hasLibraryPlaybackOverride,
  INHERIT_VALUE,
  LIBRARY_PLAYBACK_KEYS,
  libraryScope,
  type LibraryPlaybackEditorState,
  NONE_VALUE,
  SUBTITLE_MODE_OPTIONS,
} from "./libraryPlaybackPreferences";
import { namedLanguageOptionsFor, type SettingOption } from "@/lib/languageOptions";
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

/** Both library page state keys are profile+device scoped in the contract. */
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

/** Visibility and order are profile-wide in the contract (no device scope). */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

// The canonical DELETE answers 404 when nothing was stored at that scope,
// which for a reset flow means "already done", not a failure.
async function clearIgnoringUnset(clear: ReturnType<typeof useClearSettingValue>, key: SettingKey) {
  try {
    await clear.mutateAsync({ key, identity: DEVICE_SCOPE });
  } catch (error) {
    if (error instanceof ApiClientError && error.status === 404) {
      return;
    }
    throw error;
  }
}

function RememberLibraryPageStateSetting() {
  const { data: effective } = useEffectiveSettings({
    keys: [SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE],
  });
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  // Contract default is true; only an explicit false disables the feature.
  const rememberLibraryPages =
    effective?.[SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE]?.value !== false;
  const pending = setValue.isPending || clearValue.isPending;

  async function handleChange(checked: boolean) {
    try {
      if (checked) {
        // Clear the device override so the setting inherits its default again.
        await clearIgnoringUnset(clearValue, SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE);
      } else {
        // Turning the feature off also discards the state saved so far.
        await clearIgnoringUnset(clearValue, SETTING_KEYS.UI_LIBRARY_PAGE_STATE);
        await setValue.mutateAsync({
          key: SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE,
          value: false,
          identity: DEVICE_SCOPE,
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

/** PlaybackField for open language values, with the shared "Other…" entry. */
function LanguageField({
  label,
  value,
  options,
  disabled,
  hint,
  onChange,
  children,
}: {
  label: string;
  value: string;
  options: readonly SettingOption[];
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
      <LanguageSelect
        id={controlId}
        value={value}
        options={options}
        disabled={disabled}
        className="w-full"
        onValueChange={onChange}
      >
        {children}
      </LanguageSelect>
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
  profileDefaults,
  onToggleVisibility,
}: {
  library: UserLibrary;
  enabled: boolean;
  visibilityDisabled?: boolean;
  /** The values this library falls back to, for the "Profile default" hints. */
  profileDefaults: {
    audioLanguage: string | null;
    subtitleLanguage: string | null;
    subtitleMode: string;
    showForcedSubtitles: boolean;
  };
  onToggleVisibility: (checked: boolean) => void;
}) {
  const controlId = useId();
  // Resolved with this library in context, so each key reports whether the
  // answer came from the library's own row or from a wider scope.
  const { data: effective } = useEffectiveSettings({
    keys: LIBRARY_PLAYBACK_KEYS,
    libraryIds: [library.id],
  });
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const [expanded, setExpanded] = useState(false);
  const [editorState, setEditorState] = useState(() => createLibraryPlaybackEditorState(effective));

  useEffect(() => {
    // Keep the inline editor aligned with the resolved values, which change on
    // a profile switch as well as after a save.
    setEditorState(createLibraryPlaybackEditorState(effective));
  }, [effective]);

  const playbackPending = setValue.isPending || clearValue.isPending;
  const summaryText = buildLibraryPlaybackSummaryFromState(editorState);
  const hasOverride = hasLibraryPlaybackOverride(editorState);
  const audioLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
    editorState.audioLanguage === INHERIT_VALUE || editorState.audioLanguage === NONE_VALUE
      ? undefined
      : editorState.audioLanguage,
    effective?.[SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE]?.suggested_values,
  );
  const subtitleLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
    editorState.subtitleLanguage === INHERIT_VALUE || editorState.subtitleLanguage === NONE_VALUE
      ? undefined
      : editorState.subtitleLanguage,
    effective?.[SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE]?.suggested_values,
  );

  /**
   * Applies an editor state as canonical per-key writes at profile_library.
   *
   * Each key moves independently: the legacy endpoint replaced one composite
   * row, so clearing one field meant re-sending the other three, and a
   * concurrent change to any of them was lost. One write per changed key has no
   * such coupling, and "inherit" is expressed by deleting that key's row rather
   * than by omitting a field from a composite body.
   */
  async function savePlaybackState(
    nextState: LibraryPlaybackEditorState,
    rollbackState: LibraryPlaybackEditorState,
  ) {
    setEditorState(nextState);
    const scope = libraryScope(library.id);

    try {
      for (const mutation of buildLibraryPlaybackMutations(nextState)) {
        if (mutation.value === undefined) {
          try {
            await clearValue.mutateAsync({ key: mutation.key, identity: scope });
          } catch (error) {
            // Nothing stored at this scope already inherits, which is the
            // state the clear asks for.
            if (!(error instanceof ApiClientError && error.status === 404)) throw error;
          }
          continue;
        }
        await setValue.mutateAsync({
          key: mutation.key,
          value: mutation.value,
          identity: scope,
        });
      }
      if (!hasLibraryPlaybackOverride(nextState)) {
        setExpanded(false);
      }
    } catch {
      setEditorState(rollbackState);
      toast.error("Failed to update playback defaults");
    }
  }

  function handlePlaybackChange(field: keyof LibraryPlaybackEditorState, value: string) {
    const rollbackState = editorState;
    const nextState = { ...editorState, [field]: value };
    void savePlaybackState(nextState, rollbackState);
  }

  function handleReset() {
    void savePlaybackState(
      {
        audioLanguage: INHERIT_VALUE,
        subtitleLanguage: INHERIT_VALUE,
        subtitleMode: INHERIT_VALUE,
        showForcedSubtitles: INHERIT_VALUE,
      },
      editorState,
    );
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
            <LanguageField
              label="Spoken language"
              value={editorState.audioLanguage}
              options={audioLanguageOptions}
              disabled={playbackPending}
              hint={getProfileDefaultLanguageHint(profileDefaults.audioLanguage)}
              onChange={(value) => handlePlaybackChange("audioLanguage", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedLanguageLabel(profileDefaults.audioLanguage ?? "")}
              </SelectItem>
            </LanguageField>

            <LanguageField
              label="Subtitle language"
              value={editorState.subtitleLanguage}
              options={subtitleLanguageOptions}
              disabled={playbackPending}
              hint={getProfileDefaultSubtitleLanguageHint(profileDefaults.subtitleLanguage)}
              onChange={(value) => handlePlaybackChange("subtitleLanguage", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedSubtitleLanguageLabel(profileDefaults.subtitleLanguage ?? "")}
              </SelectItem>
              <SelectItem value={NONE_VALUE}>None</SelectItem>
            </LanguageField>

            <PlaybackField
              label="Subtitle behavior"
              value={editorState.subtitleMode}
              disabled={playbackPending}
              hint={getProfileDefaultSubtitleModeHint(profileDefaults.subtitleMode)}
              onChange={(value) => handlePlaybackChange("subtitleMode", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedSubtitleModeLabel(profileDefaults.subtitleMode)}
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
              hint={getProfileDefaultForcedSubtitlesHint(profileDefaults.showForcedSubtitles)}
              onChange={(value) => handlePlaybackChange("showForcedSubtitles", value)}
            >
              <SelectItem value={INHERIT_VALUE}>
                {buildInheritedShowForcedSubtitlesLabel(profileDefaults.showForcedSubtitles)}
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
  const {
    disabledLibraryIDs: savedDisabledLibraryIDs,
    libraryOrder: savedLibraryOrder,
    isLoading: libraryPrefsLoading,
  } = useLibraryDisplayPreferences();
  const { profile: currentProfile, isLoading: profileLoading } = useCurrentProfile();
  // Resolved with no library in context, so these are exactly the values a
  // library inherits when it holds no override of its own — which is what the
  // "Profile default" hints on each card have to name.
  const { data: profileDefaultSettings, isLoading: playbackPrefsLoading } = useEffectiveSettings({
    keys: LIBRARY_PLAYBACK_KEYS,
    enabled: !!currentProfile,
  });
  const setSetting = useSetSettingValue();
  const [disabledLibraryIDs, setDisabledLibraryIDs] = useState<number[]>([]);
  const [orderedLibraries, setOrderedLibraries] = useState<UserLibrary[]>([]);
  const [activeId, setActiveId] = useState<number | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  useEffect(() => {
    // Keep the editable local order in sync with the latest saved setting.
    // This supports optimistic updates and rollback without changing behavior.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setDisabledLibraryIDs(
      libraries
        ? sortLibrariesByOrder(libraries, savedDisabledLibraryIDs)
        : savedDisabledLibraryIDs,
    );
  }, [savedDisabledLibraryIDs, libraries]);

  useEffect(() => {
    if (!libraries) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setOrderedLibraries(
      savedLibraryOrder.length > 0 ? applyLibraryOrder(libraries, savedLibraryOrder) : libraries,
    );
  }, [libraries, savedLibraryOrder]);

  function saveDisabledLibraries(nextDisabledLibraryIDs: number[], rollbackIDs: number[]) {
    setDisabledLibraryIDs(nextDisabledLibraryIDs);
    setSetting.mutate(
      {
        key: SETTING_KEYS.UI_DISABLED_LIBRARY_IDS,
        value: normalizeLibraryIDs(nextDisabledLibraryIDs),
        identity: PROFILE_SCOPE,
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
        key: SETTING_KEYS.UI_LIBRARY_ORDER,
        value: normalizeLibraryIDs(next.map((l) => l.id)),
        identity: PROFILE_SCOPE,
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

  if (librariesLoading || libraryPrefsLoading || profileLoading || playbackPrefsLoading) {
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
  const profileDefaults = {
    audioLanguage:
      (profileDefaultSettings?.[SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE]?.value as string | null) ??
      null,
    subtitleLanguage:
      (profileDefaultSettings?.[SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE]?.value as string | null) ??
      null,
    subtitleMode:
      (profileDefaultSettings?.[SETTING_KEYS.PLAYBACK_SUBTITLE_MODE]?.value as
        | string
        | undefined) ?? "auto",
    showForcedSubtitles:
      (profileDefaultSettings?.[SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES]?.value as
        | boolean
        | undefined) ?? true,
  };

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
        <RememberLibraryPageStateSetting />
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
                    profileDefaults={profileDefaults}
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
