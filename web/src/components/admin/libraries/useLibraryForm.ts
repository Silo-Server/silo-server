import { useMemo, useState } from "react";

import type { CreateLibraryRequest, Library } from "@/api/types";
import {
  useCreateLibrary,
  useLibraryProviders,
  useSetLibraryProviders,
  useUpdateLibrary,
} from "@/hooks/queries/admin/libraries";
import { useAdminPlugins } from "@/hooks/queries/admin/plugins";

export type LevelChainItem = {
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

export interface LibraryFormErrors {
  name?: string;
  paths?: string;
}

export interface UseLibraryFormOptions {
  library: Library | null;
  onClose?: () => void;
  onSaved?: (library: Library) => void;
  resetAfterCreate?: boolean;
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

export function contentLevelLabel(level: string): string {
  return level
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
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

export function useLibraryForm({
  library,
  onClose,
  onSaved,
  resetAfterCreate = false,
}: UseLibraryFormOptions) {
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
  const [submitAttempted, setSubmitAttempted] = useState(false);

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

  const allErrors = useMemo<LibraryFormErrors>(() => {
    const next: LibraryFormErrors = {};
    if (!name.trim()) next.name = "Give this library a name.";
    if (!paths.some((p) => p.trim())) next.paths = "Add at least one folder to scan.";
    return next;
  }, [name, paths]);
  const errors: LibraryFormErrors = submitAttempted ? allErrors : {};

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

  function mergeBrowsedPaths(selectedPaths: string[]) {
    const merged = [...paths.filter((path) => path.trim())];
    for (const selectedPath of selectedPaths) {
      if (!merged.includes(selectedPath)) {
        merged.push(selectedPath);
      }
    }
    setPaths(merged.length > 0 ? merged : [""]);
  }

  function handleTypeChange(newType: string) {
    setType(newType);
    if (!library) {
      setLevelChains(buildDefaultLevelChains(metadataProviders, newType));
      setChainDirty(true);
    }
  }

  function reorderLevel(level: string, items: LevelChainItem[]) {
    setLevelChains({ ...activeLevelChains, [level]: items });
    setChainDirty(true);
  }

  function toggleLevelProvider(level: string, index: number) {
    setLevelChains((prev) => {
      const source = prev[level] ?? activeLevelChains[level] ?? [];
      const updated = [...source];
      updated[index] = { ...updated[index]!, enabled: !updated[index]!.enabled };
      return { ...prev, [level]: updated };
    });
    setChainDirty(true);
  }

  function finishCreate(created: Library) {
    onSaved?.(created);
    if (resetAfterCreate) {
      setName("");
      setPaths([""]);
      setSubmitAttempted(false);
    } else {
      onClose?.();
    }
  }

  function submit(): { ok: boolean; errors: LibraryFormErrors } {
    setSubmitAttempted(true);
    if (allErrors.name || allErrors.paths) {
      return { ok: false, errors: allErrors };
    }

    const body: CreateLibraryRequest = {
      name: name.trim(),
      paths: paths.filter((p) => p.trim()),
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
      return { ok: true, errors: {} };
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
    return { ok: true, errors: {} };
  }

  return {
    library,
    name,
    setName,
    paths,
    updatePath,
    addPath,
    removePath,
    mergeBrowsedPaths,
    type,
    handleTypeChange,
    enabled,
    setEnabled,
    metadataLanguage,
    setMetadataLanguage,
    chapterThumbnailsEnabled,
    setChapterThumbnailsEnabled,
    introDetectionEnabled,
    setIntroDetectionEnabled,
    contentLevels: contentLevelsForType(type),
    activeLevelChains,
    reorderLevel,
    toggleLevelProvider,
    hasMetadataProviders: metadataProviders.length > 0,
    errors,
    isPending,
    submit,
  };
}

export type LibraryFormController = ReturnType<typeof useLibraryForm>;
