import { useMemo, useState } from "react";

import type { CreateLibraryRequest, Library, LibraryProviderChainResponse } from "@/api/types";
import {
  useCreateLibrary,
  useLibraryProviderDefaults,
  useLibraryProviders,
  useSetLibraryProviders,
  useUpdateLibrary,
} from "@/hooks/queries/admin/libraries";
import { PROVIDER_TRAILER_KINDS } from "@/lib/extraKinds";

export type LevelChainItem = {
  plugin_installation_id: number;
  capability_id: string;
  provider_slug: string;
  enabled: boolean;
};

// hasChainProviders reports whether the server's resolved provider chains (a
// library's saved chain merged with the server-computed defaults) expose any
// provider — used only to decide between the chain editor and the "install a
// plugin" empty state. Built-in host providers (NFO) appear in the server
// response without any plugin installation, so this gate must not depend on
// installed plugins.
export function hasChainProviders(chains: Record<string, LevelChainItem[]>): boolean {
  return Object.values(chains).some((items) => items.length > 0);
}

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
    case "manga":
      return ["manga"];
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

// levelChainsFromResponse converts a provider-chain API response (a library's
// saved chain, or the server-computed defaults for a library type) into the
// editor's per-level item lists, preserving server priority order.
export function levelChainsFromResponse(
  response: LibraryProviderChainResponse | null | undefined,
): Record<string, LevelChainItem[]> {
  const mapped: Record<string, LevelChainItem[]> = {};
  for (const [level, entries] of Object.entries(response?.levels ?? {})) {
    mapped[level] = [...entries]
      .sort((a, b) => a.priority - b.priority)
      .map((entry) => ({
        plugin_installation_id: entry.plugin_installation_id,
        capability_id: entry.capability_id,
        provider_slug: entry.provider_slug,
        enabled: entry.enabled,
      }));
  }
  return mapped;
}

// mergeChainWithDefaults fills content levels the saved chain doesn't cover
// (e.g. a library created before a level existed) with the server-computed
// defaults, without touching levels the chain already defines.
export function mergeChainWithDefaults(
  chain: Record<string, LevelChainItem[]>,
  defaults: Record<string, LevelChainItem[]>,
  libraryType: string,
): Record<string, LevelChainItem[]> {
  const merged = { ...chain };
  for (const level of contentLevelsForType(libraryType)) {
    if (!merged[level] || merged[level].length === 0) {
      merged[level] = defaults[level] ?? [];
    }
  }
  return merged;
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
  const [autoTranslateMetadata, setAutoTranslateMetadata] = useState(
    library?.auto_translate_metadata ?? false,
  );
  const [chapterThumbnailsEnabled, setChapterThumbnailsEnabled] = useState(
    library?.chapter_thumbnails_enabled ?? false,
  );
  const [introDetectionEnabled, setIntroDetectionEnabled] = useState(
    library?.intro_detection_enabled ?? false,
  );
  const [trailerKinds, setTrailerKinds] = useState<string[]>(
    library?.trailer_kinds ?? [...PROVIDER_TRAILER_KINDS],
  );
  const [levelChains, setLevelChains] = useState<Record<string, LevelChainItem[]>>({});
  const [chainDirty, setChainDirty] = useState(false);
  const [submitAttempted, setSubmitAttempted] = useState(false);

  const createMutation = useCreateLibrary();
  const updateMutation = useUpdateLibrary();
  const setChainMutation = useSetLibraryProviders();
  const { data: currentChain } = useLibraryProviders(library?.id ?? null);
  // The server computes default chains (same logic that seeds them on create),
  // so the form never re-derives defaults from plugin manifests client-side.
  const { data: providerDefaults, isLoading: defaultsLoading } = useLibraryProviderDefaults(type);

  const isPending =
    createMutation.isPending || updateMutation.isPending || setChainMutation.isPending;

  const defaultLevelChains = useMemo(
    () => levelChainsFromResponse(providerDefaults),
    [providerDefaults],
  );
  const resolvedLevelChains = useMemo(() => {
    if (!library) {
      return defaultLevelChains;
    }
    if (currentChain === undefined) {
      return levelChains;
    }
    return mergeChainWithDefaults(levelChainsFromResponse(currentChain), defaultLevelChains, type);
  }, [currentChain, defaultLevelChains, levelChains, library, type]);
  const activeLevelChains = chainDirty ? levelChains : resolvedLevelChains;
  // The chain editor has nothing truthful to show until the server chain (for
  // an existing library) and the type's defaults have arrived; local edits
  // always render immediately.
  const chainLoading =
    !chainDirty && (defaultsLoading || (library !== null && currentChain === undefined));

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
      // Drop any local chain edits: the new type's defaults come from the
      // server, and with a clean chain the create flow lets the server-seeded
      // chain stand instead of writing one back.
      setLevelChains({});
      setChainDirty(false);
    }
  }

  function toggleTrailerKind(kind: string) {
    setTrailerKinds((current) =>
      current.includes(kind) ? current.filter((k) => k !== kind) : [...current, kind],
    );
  }

  function reorderLevel(level: string, items: LevelChainItem[]) {
    setLevelChains({ ...activeLevelChains, [level]: items });
    setChainDirty(true);
  }

  function toggleLevelProvider(level: string, index: number) {
    const source = activeLevelChains[level] ?? [];
    const updated = [...source];
    updated[index] = { ...updated[index]!, enabled: !updated[index]!.enabled };
    setLevelChains({ ...activeLevelChains, [level]: updated });
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
      auto_translate_metadata: autoTranslateMetadata,
      chapter_thumbnails_enabled: chapterThumbnailsEnabled,
      intro_detection_enabled: introDetectionEnabled,
      trailer_kinds: trailerKinds,
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
    autoTranslateMetadata,
    setAutoTranslateMetadata,
    chapterThumbnailsEnabled,
    setChapterThumbnailsEnabled,
    introDetectionEnabled,
    setIntroDetectionEnabled,
    trailerKinds,
    toggleTrailerKind,
    contentLevels: contentLevelsForType(type),
    activeLevelChains,
    chainLoading,
    reorderLevel,
    toggleLevelProvider,
    hasMetadataProviders: hasChainProviders(activeLevelChains),
    errors,
    isPending,
    submit,
  };
}

export type LibraryFormController = ReturnType<typeof useLibraryForm>;
