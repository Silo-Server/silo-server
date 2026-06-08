import { useMemo, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  FolderOpen,
  Plus,
  Trash2,
} from "lucide-react";

import type { CreateLibraryRequest, Library } from "@/api/types";
import { Button } from "@/components/ui/button";
import FolderBrowser from "@/components/FolderBrowser";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import PathAutocompleteInput from "@/components/PathAutocompleteInput";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  useCreateLibrary,
  useLibraryProviders,
  useSetLibraryProviders,
  useUpdateLibrary,
} from "@/hooks/queries/admin/libraries";
import { useAdminPlugins } from "@/hooks/queries/admin/plugins";
import { cn } from "@/lib/utils";
import { LANGUAGES } from "@/player/utils/languageNames";

type LevelChainItem = {
  plugin_installation_id: number;
  capability_id: string;
  provider_slug: string;
  enabled: boolean;
};

type MetadataProvider = {
  plugin_installation_id: number;
  capability_id: string;
  slug: string;
  defaultPriority: Record<string, number>;
};

export interface LibraryFormProps {
  library: Library | null;
  chapterThumbnailsSupported: boolean;
  onClose?: () => void;
  onSaved?: (library: Library) => void;
  resetAfterCreate?: boolean;
  submitLabel?: string;
  savingLabel?: string;
  extraContent?: ReactNode;
}

export function contentLevelsForType(libraryType: string): string[] {
  switch (libraryType) {
    case "series":
      return ["series", "season", "episode"];
    case "movies":
      return ["movie"];
    case "mixed":
      return ["movie", "series", "season", "episode", "audiobook", "ebook"];
    case "audiobooks":
      return ["audiobook"];
    case "ebooks":
    case "ebook":
      return ["ebook"];
    case "podcasts":
      return ["podcast", "podcast_episode"];
    default:
      return [];
  }
}

function buildDefaultLevelChains(
  metadataProviders: MetadataProvider[],
  libraryType: string,
): Record<string, LevelChainItem[]> {
  const defaultChain: Record<string, LevelChainItem[]> = {};
  for (const level of contentLevelsForType(libraryType)) {
    const sorted = [...metadataProviders].sort((a, b) => {
      const pa = a.defaultPriority[level] ?? 0;
      const pb = b.defaultPriority[level] ?? 0;
      if ((pa === 0) !== (pb === 0)) return pa === 0 ? 1 : -1;
      return pa - pb;
    });
    defaultChain[level] = sorted.map((provider) => ({
      plugin_installation_id: provider.plugin_installation_id,
      capability_id: provider.capability_id,
      provider_slug: provider.slug,
      enabled: (provider.defaultPriority[level] ?? 0) > 0,
    }));
  }
  return defaultChain;
}

function buildLevelChainsFromServer(
  currentChain: {
    levels?: Record<
      string,
      Array<{
        plugin_installation_id: number;
        capability_id: string;
        provider_slug: string;
        enabled: boolean;
      }>
    >;
  } | null,
  metadataProviders: MetadataProvider[],
  libraryType: string,
): Record<string, LevelChainItem[]> {
  const mapped: Record<string, LevelChainItem[]> = {};
  if (currentChain?.levels) {
    for (const [level, entries] of Object.entries(currentChain.levels)) {
      mapped[level] = entries.map((entry) => ({
        plugin_installation_id: entry.plugin_installation_id,
        capability_id: entry.capability_id,
        provider_slug: entry.provider_slug,
        enabled: entry.enabled,
      }));
    }
  }

  const defaults = buildDefaultLevelChains(metadataProviders, libraryType);
  for (const level of contentLevelsForType(libraryType)) {
    if (!mapped[level] || mapped[level].length === 0) {
      mapped[level] = defaults[level] ?? [];
    }
  }
  return mapped;
}

function contentLevelLabel(level: string): string {
  return level
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function buildProviderChainBody(activeLevelChains: Record<string, LevelChainItem[]>) {
  return {
    levels: Object.fromEntries(
      Object.entries(activeLevelChains).map(([level, items]) => [
        level,
        items.map((item, i) => ({
          plugin_installation_id: item.plugin_installation_id,
          capability_id: item.capability_id,
          priority: i,
          enabled: item.enabled,
        })),
      ]),
    ),
  };
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

export function LibraryForm({
  library,
  chapterThumbnailsSupported,
  onClose,
  onSaved,
  resetAfterCreate = false,
  submitLabel = "Save",
  savingLabel = "Saving...",
  extraContent,
}: LibraryFormProps) {
  const [name, setName] = useState(library?.name ?? "");
  const [paths, setPaths] = useState<string[]>(library?.paths?.length ? library.paths : [""]);
  const [type, setType] = useState(library?.type ?? "movies");
  const [enabled, setEnabled] = useState(library?.enabled ?? true);
  const [metadataLanguage, setMetadataLanguage] = useState(library?.metadata_language ?? "en");
  const [chapterThumbnailsEnabled, setChapterThumbnailsEnabled] = useState(
    library?.chapter_thumbnails_enabled ?? false,
  );
  const [introDetectionEnabled, setIntroDetectionEnabled] = useState(
    library?.intro_detection_enabled ?? false,
  );
  const [levelChains, setLevelChains] = useState<Record<string, LevelChainItem[]>>({});
  const [chainDirty, setChainDirty] = useState(false);
  const [browserOpen, setBrowserOpen] = useState(false);

  const createMutation = useCreateLibrary();
  const updateMutation = useUpdateLibrary();
  const setChainMutation = useSetLibraryProviders();
  const { installations } = useAdminPlugins();
  const { data: currentChain } = useLibraryProviders(library?.id ?? null);

  const metadataProviders = useMemo(() => {
    const result: MetadataProvider[] = [];
    for (const inst of installations) {
      if (!inst.enabled) continue;
      for (const cap of inst.capabilities ?? []) {
        if (cap.type === "metadata_provider.v1") {
          const dp =
            (cap.metadata?.default_priority as Record<string, number>) ??
            ((cap.metadata?.metadata as Record<string, unknown>)?.default_priority as Record<
              string,
              number
            >) ??
            {};
          result.push({
            plugin_installation_id: inst.id,
            capability_id: cap.id,
            slug: cap.display_name || cap.id,
            defaultPriority: dp,
          });
        }
      }
    }
    return result;
  }, [installations]);

  const isPending =
    createMutation.isPending || updateMutation.isPending || setChainMutation.isPending;

  const defaultLevelChains = useMemo(
    () => buildDefaultLevelChains(metadataProviders, type),
    [metadataProviders, type],
  );
  const resolvedLevelChains = useMemo(() => {
    if (!library) {
      return defaultLevelChains;
    }
    if (currentChain === undefined) {
      return levelChains;
    }
    return buildLevelChainsFromServer(currentChain, metadataProviders, type);
  }, [currentChain, defaultLevelChains, levelChains, library, metadataProviders, type]);
  const activeLevelChains = chainDirty ? levelChains : resolvedLevelChains;

  function updatePath(index: number, value: string) {
    const next = [...paths];
    next[index] = value;
    setPaths(next);
  }

  function addPath() {
    setPaths([...paths, ""]);
  }

  function removePath(index: number) {
    setPaths(paths.filter((_, i) => i !== index));
  }

  function handleBrowseSelect(selectedPaths: string[]) {
    const merged = [...paths.filter((path) => path.trim())];
    for (const selectedPath of selectedPaths) {
      if (!merged.includes(selectedPath)) {
        merged.push(selectedPath);
      }
    }
    setPaths(merged.length > 0 ? merged : [""]);
    setBrowserOpen(false);
  }

  function handleTypeChange(newType: string) {
    setType(newType);
    if (!library) {
      setLevelChains(buildDefaultLevelChains(metadataProviders, newType));
      setChainDirty(true);
    }
  }

  function finishCreate(created: Library) {
    onSaved?.(created);
    if (resetAfterCreate) {
      setName("");
      setPaths([""]);
    } else {
      onClose?.();
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const filteredPaths = paths.filter((p) => p.trim());
    if (filteredPaths.length === 0) return;

    const body: CreateLibraryRequest = {
      name,
      paths: filteredPaths,
      type,
      enabled,
      metadata_language: metadataLanguage,
      chapter_thumbnails_enabled: chapterThumbnailsEnabled,
      intro_detection_enabled: introDetectionEnabled,
    };

    if (library) {
      updateMutation.mutate(
        { id: library.id, body },
        {
          onSuccess: () => {
            if (chainDirty) {
              setChainMutation.mutate(
                {
                  id: library.id,
                  body: buildProviderChainBody(activeLevelChains),
                },
                { onSuccess: () => onClose?.() },
              );
            } else {
              onClose?.();
            }
          },
        },
      );
      return;
    }

    createMutation.mutate(body, {
      onSuccess: (created) => {
        if (chainDirty) {
          setChainMutation.mutate(
            {
              id: created.id,
              body: buildProviderChainBody(activeLevelChains),
            },
            { onSuccess: () => finishCreate(created) },
          );
        } else {
          finishCreate(created);
        }
      },
    });
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div className="grid grid-cols-[1fr_auto] items-end gap-3">
        <div className="space-y-1.5">
          <Label>Name</Label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="flex items-center gap-2 pb-0.5">
          <Switch id="enabled-switch" checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor="enabled-switch" className="text-muted-foreground text-xs">
            Enabled
          </Label>
        </div>
      </div>
      <div className="space-y-1.5">
        <Label>Paths</Label>
        {paths.map((p, i) => (
          <div key={i} className="flex gap-1">
            <PathAutocompleteInput
              value={p}
              onValueChange={(value) => updatePath(i, value)}
              placeholder="/mnt/media/movies"
              required
            />
            {paths.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-9 w-9 shrink-0"
                onClick={() => removePath(i)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            )}
          </div>
        ))}
        <div className="flex gap-1">
          <Button type="button" variant="outline" size="sm" onClick={addPath}>
            <Plus className="mr-1 h-3.5 w-3.5" /> Add Path
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => setBrowserOpen(true)}>
            <FolderOpen className="mr-1 h-3.5 w-3.5" /> Browse
          </Button>
        </div>
        <FolderBrowser
          open={browserOpen}
          onOpenChange={setBrowserOpen}
          onSelect={handleBrowseSelect}
          existingPaths={paths.filter((path) => path.trim())}
        />
      </div>
      <div className="grid grid-cols-2 items-end gap-3">
        <div className="space-y-1.5">
          <Label>Type</Label>
          <Select value={type} onValueChange={handleTypeChange}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="movies">Movies</SelectItem>
              <SelectItem value="series">Series</SelectItem>
              <SelectItem value="mixed">Mixed</SelectItem>
              <SelectItem value="audiobooks">Audiobooks</SelectItem>
              <SelectItem value="ebooks">Ebooks</SelectItem>
              <SelectItem value="podcasts">Podcasts</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label>Metadata Language</Label>
          <Select value={metadataLanguage} onValueChange={setMetadataLanguage}>
            <SelectTrigger>
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
        </div>
      </div>
      <div className="rounded-xl border border-white/10 bg-white/5 p-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <Label htmlFor="chapter-thumbnails-switch">Generate chapter thumbnails</Label>
            <p className="text-xs text-white/60">
              Stores chapter preview images in the configured public asset S3 bucket. Chapter
              markers and chapter menus still work without thumbnails.
            </p>
            {!chapterThumbnailsSupported ? (
              <p className="text-xs text-amber-300">
                Public asset S3 storage is required before this can be enabled.
              </p>
            ) : null}
          </div>
          <Switch
            id="chapter-thumbnails-switch"
            checked={chapterThumbnailsEnabled}
            disabled={!chapterThumbnailsSupported}
            onCheckedChange={setChapterThumbnailsEnabled}
          />
        </div>
      </div>
      <div className="rounded-xl border border-white/10 bg-white/5 p-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <Label htmlFor="intro-detection-switch">Detect intro markers</Label>
            <p className="text-xs text-white/60">
              Runs background audio analysis for episodes in this library. Embedded intro chapters
              are used when available.
            </p>
          </div>
          <Switch
            id="intro-detection-switch"
            checked={introDetectionEnabled}
            onCheckedChange={setIntroDetectionEnabled}
          />
        </div>
      </div>
      {extraContent}

      {contentLevelsForType(type).length > 0 && (
        <div className="mt-4 border-t border-white/10 pt-4">
          <h3 className="mb-3 text-sm font-semibold text-white">Metadata Providers</h3>
          {contentLevelsForType(type).map((level) => {
            const items = activeLevelChains[level] ?? [];
            return (
              <ProviderLevelSection
                key={level}
                level={level}
                items={items}
                onReorder={(newItems) => {
                  setLevelChains({ ...activeLevelChains, [level]: newItems });
                  setChainDirty(true);
                }}
                onToggleEnabled={(index) => {
                  setLevelChains((prev) => {
                    const source = prev[level] ?? activeLevelChains[level] ?? [];
                    const updated = [...source];
                    updated[index] = { ...updated[index]!, enabled: !updated[index]!.enabled };
                    return { ...prev, [level]: updated };
                  });
                  setChainDirty(true);
                }}
              />
            );
          })}
        </div>
      )}

      <Button type="submit" className="w-full" disabled={isPending}>
        {isPending ? savingLabel : submitLabel}
      </Button>
    </form>
  );
}
