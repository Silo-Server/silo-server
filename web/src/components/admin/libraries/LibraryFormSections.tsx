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
import { cn } from "@/lib/utils";
import { extraKindGroupLabel, PROVIDER_TRAILER_KINDS } from "@/lib/extraKinds";
import { LANGUAGES } from "@/player/utils/languageNames";

import { LIBRARY_TYPES } from "./libraryTypes";
import {
  REALTIME_MONITORING_SETTINGS_PATH,
  realtimeMonitoringNeedsAttention,
  realtimeMonitoringStatusText,
} from "./realtimeMonitoring";
import { contentLevelLabel } from "./useLibraryForm";
import type { LevelChainItem, LibraryFormController } from "./useLibraryForm";

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
  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <Label htmlFor="library-name">Name</Label>
        <Input
          id="library-name"
          value={form.name}
          onChange={(e) => form.setName(e.target.value)}
          placeholder="e.g. Movies"
          aria-invalid={form.errors.name ? true : undefined}
        />
        {form.errors.name ? <p className="text-destructive text-xs">{form.errors.name}</p> : null}
      </div>
      <div className="space-y-1.5">
        <Label>Type</Label>
        <div
          className="grid grid-cols-3 gap-2 sm:grid-cols-5"
          role="radiogroup"
          aria-label="Library type"
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
            Changing the type of an existing library may require a full rescan to rematch items.
          </p>
        ) : null}
      </div>
      <SettingCard
        htmlFor="library-enabled-switch"
        title="Enabled"
        description="Disabled libraries are hidden from browsing and skipped by scans."
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
  const [browserOpen, setBrowserOpen] = useState(false);

  return (
    <div className="space-y-3">
      <div className="space-y-2">
        {form.paths.map((path, i) => (
          <div key={i} className="flex items-center gap-1.5">
            <FolderOpen className="text-muted-foreground/60 size-4 shrink-0" />
            <PathAutocompleteInput
              value={path}
              onValueChange={(value) => form.updatePath(i, value)}
              placeholder="/mnt/media/movies"
              aria-invalid={form.errors.paths ? true : undefined}
            />
            {form.paths.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-destructive size-9 shrink-0"
                onClick={() => form.removePath(i)}
                title="Remove folder"
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
          <FolderSearch className="mr-1 size-3.5" /> Browse
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={form.addPath}>
          <Plus className="mr-1 size-3.5" /> Add Path
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
        Real-time monitoring is turned off server-wide. Turn it on in{" "}
        <Link
          to={REALTIME_MONITORING_SETTINGS_PATH}
          className="text-foreground font-medium underline underline-offset-2"
        >
          Settings → Library &amp; Metadata
        </Link>
        .
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
        Status: {realtimeMonitoringStatusText(entry)}
      </p>
    );
  }

  return (
    <SettingCard
      htmlFor="realtime-monitoring-switch"
      title="Real-time monitoring"
      description="Scan this library automatically when its files change."
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

export function MetadataFields({ form }: { form: LibraryFormController }) {
  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <Label>Metadata Language</Label>
        <Select value={form.metadataLanguage} onValueChange={form.setMetadataLanguage}>
          <SelectTrigger className="w-full sm:w-64">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {LANGUAGES.map((lang) => (
              <SelectItem key={lang.code} value={lang.code}>
                {lang.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          Preferred language for titles, summaries, and artwork fetched from providers.
        </p>
      </div>
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-0.5">
          <Label htmlFor="auto-translate-metadata">Auto-translate descriptions</Label>
          <p className="text-muted-foreground text-xs">
            When providers have no translation for this library&apos;s language, translate
            descriptions with AI after each refresh. Requires AI description translation in Admin
            Settings → AI.
          </p>
        </div>
        <Switch
          id="auto-translate-metadata"
          checked={form.autoTranslateMetadata}
          onCheckedChange={form.setAutoTranslateMetadata}
        />
      </div>
      {form.settingSupport.trailers && (
        <div className="space-y-1.5">
          <Label>Trailer &amp; extras types</Label>
          <p className="text-muted-foreground text-xs">
            Video types fetched from metadata providers during refresh. Uncheck everything to
            disable remote trailers for this library.
          </p>
          <div
            className="grid grid-cols-1 gap-1.5 sm:grid-cols-2"
            role="group"
            aria-label="Trailer and extras types"
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
          <Label>Provider Priority</Label>
          <p className="text-muted-foreground mb-3 text-xs">
            Providers are asked in order from top to bottom. Uncheck a provider to skip it for that
            level.
          </p>
          {form.chainLoading ? (
            <div className="border-border bg-surface text-muted-foreground flex items-center justify-center gap-2 rounded-xl border border-dashed p-4 text-xs">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Loading providers…
            </div>
          ) : form.hasMetadataProviders ? (
            form.contentLevels.map((level) => (
              <ProviderLevelSection
                key={level}
                level={level}
                items={form.activeLevelChains[level] ?? []}
                onReorder={(newItems) => form.reorderLevel(level, newItems)}
                onToggleEnabled={(index) => form.toggleLevelProvider(level, index)}
              />
            ))
          ) : (
            <p className="border-border bg-surface text-muted-foreground rounded-xl border border-dashed p-4 text-center text-xs">
              No metadata provider plugins are installed. Install one under Admin → Plugins to fetch
              artwork and descriptions.
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
}: {
  form: LibraryFormController;
  chapterThumbnailsSupported: boolean;
}) {
  return (
    <div className="space-y-3">
      {form.settingSupport.chapterThumbnails && (
        <SettingCard
          htmlFor="chapter-thumbnails-switch"
          title="Generate chapter thumbnails"
          description="Stores chapter preview images in the configured public asset S3 bucket. Chapter markers and chapter menus still work without thumbnails."
          footer={
            !chapterThumbnailsSupported ? (
              <p className="text-warning text-xs">
                Public asset S3 storage is required before this can be enabled.
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
      {form.settingSupport.introDetection && (
        <SettingCard htmlFor="intro-detection-switch" {...markerDetectionCopy(form.settingSupport)}>
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
function markerDetectionCopy(support: {
  creditsOnlyDetection: boolean;
  movieCreditsDetection: boolean;
}): { title: string; description: string } {
  if (support.creditsOnlyDetection) {
    return {
      title: "Detect credits markers (best effort)",
      description:
        "Looks for end credits in movies in this library, from embedded chapters and the picture near the end. Some movies get no credits marker, or one that starts late. Needs Detect credits on in server settings.",
    };
  }
  const episodes =
    "Runs background audio analysis for episodes in this library. Embedded intro and credits chapters are used when available.";
  const kinds =
    "Detect intros and Detect credits in server settings choose which markers it finds.";
  return {
    title: "Detect intro and credits markers",
    description: support.movieCreditsDetection
      ? `${episodes} Movies get end credits only, on a best-effort basis. ${kinds}`
      : `${episodes} ${kinds}`,
  };
}
