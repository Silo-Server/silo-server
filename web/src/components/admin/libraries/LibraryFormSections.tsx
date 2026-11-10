import { useState } from "react";
import type { ReactNode } from "react";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  FolderOpen,
  FolderSearch,
  Loader2,
  Plus,
  Trash2,
} from "lucide-react";
import { Link } from "react-router";

import FolderBrowser from "@/components/FolderBrowser";
import PathAutocompleteInput from "@/components/PathAutocompleteInput";
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
import { useLibraryRealtimeMonitoring } from "@/hooks/queries/admin/libraries";
import { useAppLanguage, useT, type Translate } from "@/i18n";
import { cn } from "@/lib/utils";
import { extraKindGroupLabel, PROVIDER_TRAILER_KINDS } from "@/lib/extraKinds";
import { languageOptions } from "@/player/utils/languageNames";

import { BOX_SET_MIN_MOVIE_OPTIONS, LIBRARY_TYPES } from "./libraryTypes";
import {
  REALTIME_MONITORING_SETTINGS_PATH,
  realtimeMonitoringNeedsAttention,
  realtimeMonitoringStatusText,
} from "./realtimeMonitoring";
import { contentLevelLabel } from "./useLibraryForm";
import type { LevelChainItem, LibraryFormController } from "./useLibraryForm";
import { LibraryTranslatePanel } from "./LibraryTranslatePanel";

// An example folder path, which is data in every language.
const FOLDER_PATH_PLACEHOLDER = "/mnt/media/movies";

// A library's metadata_language is a bare ISO 639-1 code; the server rejects
// regional tags such as pt-BR, so the picker leaves them out.
function libraryMetadataLanguages() {
  return languageOptions().filter((lang) => /^[a-z]{2}$/.test(lang.code));
}

function SettingCard({
  htmlFor,
  title,
  description,
  children,
  footer,
}: {
  htmlFor?: string;
  title: string;
  description: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <div className="border-border bg-surface rounded-xl border p-3.5">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <Label htmlFor={htmlFor}>{title}</Label>
          <p className="text-muted-foreground text-xs leading-relaxed">{description}</p>
          {footer}
        </div>
        {children}
      </div>
    </div>
  );
}

export function GeneralFields({
  form,
  posterSlot,
}: {
  form: LibraryFormController;
  posterSlot?: ReactNode;
}) {
  const t = useT();
  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <Label htmlFor="library-name">{t("web.admin.common.libraryEditor.general.name")}</Label>
        <Input
          id="library-name"
          value={form.name}
          onChange={(e) => form.setName(e.target.value)}
          placeholder={t("web.admin.common.libraryEditor.general.namePlaceholder")}
          aria-invalid={form.errors.name ? true : undefined}
        />
        {form.errors.name ? <p className="text-destructive text-xs">{form.errors.name}</p> : null}
      </div>
      <div className="space-y-1.5">
        <Label>{t("web.admin.common.libraryEditor.general.type")}</Label>
        <div
          className="grid grid-cols-3 gap-2 sm:grid-cols-5"
          role="radiogroup"
          aria-label={t("web.admin.common.libraryEditor.general.typeGroup")}
        >
          {LIBRARY_TYPES.map(({ value, label, icon: Icon }) => {
            const selected = form.type === value;
            return (
              <button
                key={value}
                type="button"
                role="radio"
                aria-checked={selected}
                onClick={() => form.handleTypeChange(value)}
                className={cn(
                  "flex flex-col items-center gap-1.5 rounded-xl border px-2 py-3 transition-colors duration-150",
                  selected
                    ? "border-primary/50 bg-primary/10 text-foreground"
                    : "border-border bg-surface text-muted-foreground hover:bg-surface-hover hover:text-foreground",
                )}
              >
                <Icon
                  className={cn("size-5", selected ? "text-primary" : "text-muted-foreground")}
                />
                <span className="text-[11px] font-medium">{label}</span>
              </button>
            );
          })}
        </div>
        {form.library && form.type !== form.library.type ? (
          <p className="text-warning text-xs">
            {t("web.admin.common.libraryEditor.general.typeChangeWarning")}
          </p>
        ) : null}
      </div>
      <SettingCard
        htmlFor="library-enabled-switch"
        title={t("web.admin.common.libraryEditor.general.enabled")}
        description={t("web.admin.common.libraryEditor.general.enabledDescription")}
      >
        <Switch
          id="library-enabled-switch"
          checked={form.enabled}
          onCheckedChange={form.setEnabled}
        />
      </SettingCard>
      {posterSlot}
    </div>
  );
}

export function FolderFields({ form }: { form: LibraryFormController }) {
  const t = useT();
  const [browserOpen, setBrowserOpen] = useState(false);
  const browseLabel = t("web.admin.common.libraryEditor.folders.browse");
  const addPathLabel = t("web.admin.common.libraryEditor.folders.addPath");

  return (
    <div className="space-y-3">
      <div className="space-y-2">
        {form.paths.map((path, i) => (
          <div key={i} className="flex items-center gap-1.5">
            <FolderOpen className="text-muted-foreground/60 size-4 shrink-0" />
            <PathAutocompleteInput
              value={path}
              onValueChange={(value) => form.updatePath(i, value)}
              placeholder={FOLDER_PATH_PLACEHOLDER}
              aria-invalid={form.errors.paths ? true : undefined}
            />
            {form.paths.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-destructive size-9 shrink-0"
                onClick={() => form.removePath(i)}
                title={t("web.admin.common.libraryEditor.folders.remove")}
              >
                <Trash2 className="size-3.5" />
              </Button>
            )}
          </div>
        ))}
      </div>
      {form.errors.paths ? <p className="text-destructive text-xs">{form.errors.paths}</p> : null}
      <div className="flex gap-1.5">
        <Button type="button" variant="outline" size="sm" onClick={() => setBrowserOpen(true)}>
          <FolderSearch className="me-1 size-3.5" /> {browseLabel}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={form.addPath}>
          <Plus className="me-1 size-3.5" /> {addPathLabel}
        </Button>
      </div>
      <FolderBrowser
        open={browserOpen}
        onOpenChange={setBrowserOpen}
        onSelect={(selected) => {
          form.mergeBrowsedPaths(selected);
          setBrowserOpen(false);
        }}
        existingPaths={form.paths.filter((path) => path.trim())}
      />
    </div>
  );
}

/**
 * The library's real-time monitoring switch and the server's status line for
 * it. It stays out of FolderFields, which the setup wizard also renders, so
 * only the admin library editor fetches the monitoring status.
 */
export function RealtimeMonitoringFields({ form }: { form: LibraryFormController }) {
  const t = useT();
  const { data: status } = useLibraryRealtimeMonitoring();
  const serverOff = status?.server_enabled === false;
  const { library } = form;
  const entry = library
    ? status?.libraries.find((candidate) => candidate.library_id === library.id)
    : undefined;
  // The status describes the saved switch, so an unsaved change hides it
  // rather than contradicting it.
  const hasUnsavedChange = form.realtimeMonitoring !== (library?.realtime_monitoring ?? true);

  let footer: ReactNode = null;
  if (serverOff) {
    footer = (
      <p className="text-muted-foreground text-xs">
        {t("web.admin.common.libraryEditor.monitoring.serverOff", {
          link: (chunks) => (
            <Link
              to={REALTIME_MONITORING_SETTINGS_PATH}
              className="text-foreground font-medium underline underline-offset-2"
            >
              {chunks}
            </Link>
          ),
        })}
      </p>
    );
  } else if (entry && !hasUnsavedChange) {
    footer = (
      <p
        className={cn(
          "text-xs",
          realtimeMonitoringNeedsAttention(entry) ? "text-warning" : "text-muted-foreground",
        )}
      >
        {t("web.admin.common.libraryEditor.monitoringStatus.line", {
          status: realtimeMonitoringStatusText(entry),
        })}
      </p>
    );
  }

  return (
    <SettingCard
      htmlFor="realtime-monitoring-switch"
      title={t("web.admin.common.libraryEditor.monitoring.title")}
      description={t("web.admin.common.libraryEditor.monitoring.description")}
      footer={footer}
    >
      <Switch
        id="realtime-monitoring-switch"
        checked={form.realtimeMonitoring}
        disabled={serverOff}
        onCheckedChange={form.setRealtimeMonitoring}
      />
    </SettingCard>
  );
}

function ProviderLevelSection({
  level,
  items,
  onReorder,
  onToggleEnabled,
}: {
  level: string;
  items: LevelChainItem[];
  onReorder: (items: LevelChainItem[]) => void;
  onToggleEnabled: (index: number) => void;
}) {
  // contentLevelLabel reads the interface language.
  useAppLanguage();
  const [collapsed, setCollapsed] = useState(false);

  const moveItem = (index: number, direction: -1 | 1) => {
    const newItems = [...items];
    const target = index + direction;
    if (target < 0 || target >= newItems.length) return;
    [newItems[index], newItems[target]] = [newItems[target]!, newItems[index]!];
    onReorder(newItems);
  };

  return (
    <div className="mb-3">
      <button
        type="button"
        onClick={() => setCollapsed(!collapsed)}
        className="text-primary hover:text-primary/80 mb-1.5 flex items-center gap-1.5 text-xs font-semibold tracking-wider uppercase"
      >
        {collapsed ? <ChevronRight className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
        {contentLevelLabel(level)}
      </button>
      {!collapsed && (
        <div className="flex flex-col gap-1">
          {items.map((item, i) => (
            <div
              key={`${item.plugin_installation_id}:${item.capability_id}`}
              className={cn(
                "flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm",
                item.enabled
                  ? "border-border bg-muted text-foreground"
                  : "border-border/50 bg-muted/30 text-muted-foreground",
              )}
            >
              <input
                type="checkbox"
                checked={item.enabled}
                onChange={() => onToggleEnabled(i)}
                className="h-3.5 w-3.5"
                style={{ accentColor: "var(--primary)" }}
              />
              <span className="flex-1 font-mono text-xs">{item.provider_slug}</span>
              <div className="flex gap-0.5">
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5"
                  disabled={i === 0}
                  onClick={() => moveItem(i, -1)}
                >
                  <ArrowUp className="h-2.5 w-2.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5"
                  disabled={i === items.length - 1}
                  onClick={() => moveItem(i, 1)}
                >
                  <ArrowDown className="h-2.5 w-2.5" />
                </Button>
              </div>
              <span className="text-muted-foreground/70 font-mono text-[10px]">{i + 1}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// The offered sizes, plus a saved size outside them so it stays selected.
function boxSetMinMovieOptions(current: number): number[] {
  return [...new Set<number>([...BOX_SET_MIN_MOVIE_OPTIONS, current])].sort((a, b) => a - b);
}

export function BoxSetFields({ form }: { form: LibraryFormController }) {
  const t = useT();
  return (
    <div className="border-border bg-surface rounded-xl border p-3.5">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <Label htmlFor="box-sets-enabled">
            {t("web.admin.common.libraryEditor.boxSets.title")}
          </Label>
          <p className="text-muted-foreground text-xs leading-relaxed">
            {t("web.admin.common.libraryEditor.boxSets.description")}
          </p>
        </div>
        <Switch
          id="box-sets-enabled"
          checked={form.boxSetsEnabled}
          onCheckedChange={form.setBoxSetsEnabled}
        />
      </div>
      {form.boxSetsEnabled && (
        <div className="border-border mt-3 space-y-3 border-t pt-3">
          <div className="space-y-1.5">
            <Label id="box-set-min-movies-label">
              {t("web.admin.common.libraryEditor.boxSets.minMovies")}
            </Label>
            <div
              role="radiogroup"
              aria-labelledby="box-set-min-movies-label"
              className="border-border inline-flex gap-0.5 rounded-full border p-0.5"
            >
              {boxSetMinMovieOptions(form.boxSetMinMovies).map((count) => {
                const selected = form.boxSetMinMovies === count;
                return (
                  <button
                    key={count}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    onClick={() => form.setBoxSetMinMovies(count)}
                    className={cn(
                      "rounded-full px-3 py-0.5 text-xs font-medium transition-colors",
                      selected
                        ? "bg-primary text-primary-foreground"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {count}
                  </button>
                );
              })}
            </div>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.common.libraryEditor.boxSets.minMoviesHelp")}
            </p>
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-0.5">
              <Label htmlFor="box-sets-in-search">
                {t("web.admin.common.libraryEditor.boxSets.inSearch")}
              </Label>
              <p className="text-muted-foreground text-xs">
                {t("web.admin.common.libraryEditor.boxSets.inSearchHelp")}
              </p>
            </div>
            <Switch
              id="box-sets-in-search"
              checked={form.boxSetsInSearch}
              onCheckedChange={form.setBoxSetsInSearch}
            />
          </div>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.common.libraryEditor.boxSets.lookupNote")}
          </p>
        </div>
      )}
    </div>
  );
}

export function MetadataFields({ form }: { form: LibraryFormController }) {
  const t = useT();
  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <Label>{t("web.admin.common.libraryEditor.metadata.language")}</Label>
        <Select value={form.metadataLanguage} onValueChange={form.setMetadataLanguage}>
          <SelectTrigger className="w-full sm:w-64">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {libraryMetadataLanguages().map((lang) => (
              <SelectItem key={lang.code} value={lang.code}>
                {lang.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.common.libraryEditor.metadata.languageHelp")}
        </p>
      </div>
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-0.5">
          <Label htmlFor="auto-translate-metadata">
            {t("web.admin.common.libraryEditor.metadata.autoTranslate")}
          </Label>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.common.libraryEditor.metadata.autoTranslateHelp")}
          </p>
        </div>
        <Switch
          id="auto-translate-metadata"
          checked={form.autoTranslateMetadata}
          onCheckedChange={form.setAutoTranslateMetadata}
        />
      </div>
      {form.library && <LibraryTranslatePanel libraryId={form.library.id} />}
      {form.settingSupport.boxSets && <BoxSetFields form={form} />}
      {form.settingSupport.trailers && (
        <div className="space-y-1.5">
          <Label>{t("web.admin.common.libraryEditor.metadata.trailerKinds")}</Label>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.common.libraryEditor.metadata.trailerKindsHelp")}
          </p>
          <div
            className="grid grid-cols-1 gap-1.5 sm:grid-cols-2"
            role="group"
            aria-label={t("web.admin.common.libraryEditor.metadata.trailerKindsGroup")}
          >
            {PROVIDER_TRAILER_KINDS.map((kind) => {
              const checked = form.trailerKinds.includes(kind);
              return (
                <label
                  key={kind}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm",
                    checked
                      ? "border-border bg-muted text-foreground"
                      : "border-border/50 bg-muted/30 text-muted-foreground",
                  )}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() => form.toggleTrailerKind(kind)}
                    className="h-3.5 w-3.5"
                    style={{ accentColor: "var(--primary)" }}
                  />
                  {extraKindGroupLabel(kind)}
                </label>
              );
            })}
          </div>
        </div>
      )}
      {form.contentLevels.length > 0 && (
        <div className="space-y-1.5">
          <Label>{t("web.admin.common.libraryEditor.metadata.providerPriority")}</Label>
          <p className="text-muted-foreground mb-3 text-xs">
            {t("web.admin.common.libraryEditor.metadata.providerPriorityHelp")}
          </p>
          {form.chainLoading ? (
            <div className="border-border bg-surface text-muted-foreground flex items-center justify-center gap-2 rounded-xl border border-dashed p-4 text-xs">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              {t("web.admin.common.libraryEditor.metadata.loadingProviders")}
            </div>
          ) : form.hasMetadataProviders ? (
            <>
              {form.hasLocalMetadata && (
                <div className="mb-3">
                  <SettingCard
                    htmlFor="local-metadata-switch"
                    title={t("web.admin.common.libraryEditor.metadata.localMetadata")}
                    description={t(
                      "web.admin.common.libraryEditor.metadata.localMetadataDescription",
                    )}
                  >
                    <Switch
                      id="local-metadata-switch"
                      checked={form.localMetadataEnabled}
                      onCheckedChange={form.toggleLocalMetadata}
                    />
                  </SettingCard>
                </div>
              )}
              {form.hasRemoteProviders ? (
                form.contentLevels.map((level) => (
                  <ProviderLevelSection
                    key={level}
                    level={level}
                    items={form.remoteLevelChains[level] ?? []}
                    onReorder={(newItems) => form.reorderLevel(level, newItems)}
                    onToggleEnabled={(index) => form.toggleLevelProvider(level, index)}
                  />
                ))
              ) : (
                <p className="border-border bg-surface text-muted-foreground rounded-xl border border-dashed p-4 text-center text-xs">
                  {t("web.admin.common.libraryEditor.metadata.noRemoteProviders")}
                </p>
              )}
            </>
          ) : (
            <p className="border-border bg-surface text-muted-foreground rounded-xl border border-dashed p-4 text-center text-xs">
              {t("web.admin.common.libraryEditor.metadata.noProviders")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

export function AdvancedFields({
  form,
  chapterThumbnailsSupported,
  trickplaySupported,
}: {
  form: LibraryFormController;
  chapterThumbnailsSupported: boolean;
  /** Undefined when the server does not offer seek previews. */
  trickplaySupported?: boolean;
}) {
  const t = useT();
  return (
    <div className="space-y-3">
      {form.settingSupport.chapterThumbnails && (
        <SettingCard
          htmlFor="chapter-thumbnails-switch"
          title={t("web.admin.common.libraryEditor.advanced.chapterThumbnails")}
          description={t("web.admin.common.libraryEditor.advanced.chapterThumbnailsDescription")}
          footer={
            !chapterThumbnailsSupported ? (
              <p className="text-warning text-xs">
                {t("web.admin.common.libraryEditor.advanced.chapterThumbnailsUnavailable")}
              </p>
            ) : null
          }
        >
          <Switch
            id="chapter-thumbnails-switch"
            checked={form.chapterThumbnailsEnabled}
            disabled={!chapterThumbnailsSupported}
            onCheckedChange={form.setChapterThumbnailsEnabled}
          />
        </SettingCard>
      )}
      {form.settingSupport.trickplay && trickplaySupported !== undefined && (
        <SettingCard
          htmlFor="trickplay-switch"
          title={t("web.admin.common.libraryEditor.advanced.trickplay")}
          description={t("web.admin.common.libraryEditor.advanced.trickplayDescription")}
          footer={
            !trickplaySupported ? (
              <p className="text-warning text-xs">
                {t("web.admin.common.libraryEditor.advanced.trickplayUnavailable")}
              </p>
            ) : null
          }
        >
          <Switch
            id="trickplay-switch"
            checked={form.trickplayEnabled}
            disabled={!trickplaySupported && !form.trickplayEnabled}
            onCheckedChange={form.setTrickplayEnabled}
          />
        </SettingCard>
      )}
      {form.settingSupport.introDetection && (
        <SettingCard
          htmlFor="intro-detection-switch"
          {...markerDetectionCopy(form.settingSupport, t)}
        >
          <Switch
            id="intro-detection-switch"
            checked={form.introDetectionEnabled}
            onCheckedChange={form.setIntroDetectionEnabled}
          />
        </SettingCard>
      )}
    </div>
  );
}

// markerDetectionCopy describes what local marker detection covers in a
// library: movies get best-effort end credits only, episodes get intros and
// credits, and mixed libraries hold both.
function markerDetectionCopy(
  support: {
    creditsOnlyDetection: boolean;
    movieCreditsDetection: boolean;
  },
  t: Translate,
): { title: string; description: string } {
  if (support.creditsOnlyDetection) {
    return {
      title: t("web.admin.common.libraryEditor.advanced.creditsTitle"),
      description: t("web.admin.common.libraryEditor.advanced.creditsDescription"),
    };
  }
  return {
    title: t("web.admin.common.libraryEditor.advanced.introCreditsTitle"),
    description: support.movieCreditsDetection
      ? t("web.admin.common.libraryEditor.advanced.introCreditsMixedDescription")
      : t("web.admin.common.libraryEditor.advanced.introCreditsDescription"),
  };
}
