import { startTransition, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import type { QueryDefinition } from "@/api/types";
import { Tabs, TabsContent } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import LibraryHeader from "@/components/LibraryHeader";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import LibraryRecommended from "./LibraryRecommended";
import LibraryBrowse from "./LibraryBrowse";
import LibraryCollections from "./LibraryCollections";
import {
  libraryPageStateWriteRetryDelay,
  useLibraryPageStatePreference,
} from "@/hooks/queries/libraryPageState";
import {
  applySavedLibraryPageSearchParams,
  hasLibraryPageSearchParams,
  parseLibraryPageState,
  serializeLibraryPageSearchParams,
  updateLibraryPageSearchParams,
  type LibraryBrowseType,
} from "./libraryPageSearchParams";

const LIBRARY_SAVE_RETRY_DELAYS_MS = [2_000, 5_000] as const;

interface LibrarySaveRetry {
  key: string;
  failures: number;
  ready: boolean;
  timeout?: ReturnType<typeof setTimeout>;
}

interface HydratedLibrarySearch {
  ownerKey: string | null;
  libraryId: number;
  search: string;
}

export default function LibraryPage() {
  const { libraryId } = useParams<{ libraryId: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: libraries, isLoading } = useUserLibraries();
  const {
    ownerKey: libraryPageStateOwnerKey,
    isLoading: libraryPageStateLoading,
    preference: libraryPageStatePreference,
    rememberEnabled: rememberLibraryPageState,
    saveLibrarySearch,
  } = useLibraryPageStatePreference();
  const [savedStateHydratedKey, setSavedStateHydratedKey] = useState<string | null>(null);
  const [hydratedLibrarySearch, setHydratedLibrarySearch] = useState<HydratedLibrarySearch | null>(
    null,
  );
  const applyingSavedSearchParamsRef = useRef<string | null>(null);
  const applyingSavedSearchParamsKeyRef = useRef<string | null>(null);
  const submittedLibrarySearchRef = useRef<{ key: string } | null>(null);
  const librarySaveRetryRef = useRef<LibrarySaveRetry | null>(null);
  const [librarySaveRetryNonce, setLibrarySaveRetryNonce] = useState(0);

  useEffect(() => {
    return () => {
      const retry = librarySaveRetryRef.current;
      if (retry?.timeout !== undefined) {
        clearTimeout(retry.timeout);
      }
    };
  }, []);

  const id = Number(libraryId);
  const libraryPageStateKey = `${libraryPageStateOwnerKey ?? "none"}:${id}`;
  const library = libraries?.find((l) => l.id === id);
  const libraryType = library?.type ?? "";
  const savedLibrarySearch =
    Number.isFinite(id) && id > 0
      ? libraryPageStatePreference.libraries[String(id)]?.search
      : undefined;
  const currentLibrarySearch = serializeLibraryPageSearchParams(searchParams);
  const hasInheritedHydratedSearch =
    hydratedLibrarySearch !== null &&
    hydratedLibrarySearch.ownerKey !== libraryPageStateOwnerKey &&
    hydratedLibrarySearch.libraryId === id &&
    hydratedLibrarySearch.search === currentLibrarySearch;
  const hasUnhydratedLibraryState =
    Boolean(libraryType) &&
    Number.isFinite(id) &&
    id > 0 &&
    savedStateHydratedKey !== libraryPageStateKey;
  const shouldApplySavedLibrarySearch =
    hasUnhydratedLibraryState &&
    !libraryPageStateLoading &&
    ((hasInheritedHydratedSearch && !rememberLibraryPageState) ||
      (rememberLibraryPageState &&
        (!hasLibraryPageSearchParams(searchParams) || hasInheritedHydratedSearch) &&
        ((savedLibrarySearch != null && savedLibrarySearch !== currentLibrarySearch) ||
          hasInheritedHydratedSearch)));
  const shouldWaitForSavedLibrarySearch =
    hasUnhydratedLibraryState &&
    libraryPageStateLoading &&
    (!hasLibraryPageSearchParams(searchParams) || hasInheritedHydratedSearch);
  const searchParamsKey = searchParams.toString();
  const { activeTab, browseType, queryDefinition } = useMemo(
    () => parseLibraryPageState(new URLSearchParams(searchParamsKey), libraryType),
    [libraryType, searchParamsKey],
  );

  // Tracks whether LibraryRecommended is currently rendering a hero banner.
  // We can't derive this from layout metadata alone — a featured section can
  // still resolve to error/empty, in which case no hero is actually on screen
  // and overlay-mode header styling would be unreadable (especially on light
  // themes, where overlay assumes a dark backdrop behind it).
  const [hasRenderedHero, setHasRenderedHero] = useState(false);
  const handleHeroStateChange = useCallback((rendered: boolean) => {
    setHasRenderedHero(rendered);
  }, []);

  useDocumentTitle(library?.name ?? "Library");

  /* eslint-disable react-hooks/set-state-in-effect -- hydration provenance is synchronized with router state */
  useEffect(() => {
    if (
      applyingSavedSearchParamsRef.current === null &&
      hydratedLibrarySearch?.ownerKey === libraryPageStateOwnerKey &&
      hydratedLibrarySearch.libraryId === id &&
      hydratedLibrarySearch.search !== currentLibrarySearch
    ) {
      // The URL no longer matches what this profile hydrated, so a later
      // profile switch must treat it as an explicit user choice.
      setHydratedLibrarySearch(null);
    }
  }, [currentLibrarySearch, hydratedLibrarySearch, id, libraryPageStateOwnerKey]);

  useEffect(() => {
    if (
      !libraryType ||
      !Number.isFinite(id) ||
      id <= 0 ||
      libraryPageStateLoading ||
      savedStateHydratedKey === libraryPageStateKey ||
      !shouldApplySavedLibrarySearch
    ) {
      return;
    }

    const nextSearchParams = applySavedLibraryPageSearchParams(
      searchParams,
      rememberLibraryPageState ? (savedLibrarySearch ?? "") : "",
    );
    const hydratedSearch = serializeLibraryPageSearchParams(nextSearchParams);
    const nextHydratedLibrarySearch: HydratedLibrarySearch = {
      ownerKey: libraryPageStateOwnerKey,
      libraryId: id,
      search: hydratedSearch,
    };

    if (nextSearchParams.toString() === searchParams.toString()) {
      // The saved search already matches the URL, so there is nothing to
      // navigate to and the page can leave the skeleton immediately.
      setSavedStateHydratedKey(libraryPageStateKey);
      setHydratedLibrarySearch(nextHydratedLibrarySearch);
      return;
    }

    applyingSavedSearchParamsRef.current = hydratedSearch;
    applyingSavedSearchParamsKeyRef.current = libraryPageStateKey;
    // react-router wraps setSearchParams in a transition. Marking hydration as
    // done urgently would commit first, dropping the skeleton while
    // `searchParams` still read the pre-navigation URL — long enough for the
    // default Recommended tab to mount and fire its section queries before the
    // transition lands the saved tab. Keeping all three updates in one
    // transition commits the marker and the URL together.
    startTransition(() => {
      setSavedStateHydratedKey(libraryPageStateKey);
      setHydratedLibrarySearch(nextHydratedLibrarySearch);
      setSearchParams(nextSearchParams, { replace: true });
    });
  }, [
    id,
    libraryPageStateKey,
    libraryPageStateLoading,
    libraryPageStateOwnerKey,
    libraryType,
    rememberLibraryPageState,
    savedStateHydratedKey,
    savedLibrarySearch,
    searchParams,
    setSearchParams,
    shouldApplySavedLibrarySearch,
  ]);
  /* eslint-enable react-hooks/set-state-in-effect */

  useEffect(() => {
    if (!libraryType || shouldApplySavedLibrarySearch) {
      return;
    }

    if (applyingSavedSearchParamsKeyRef.current !== libraryPageStateKey) {
      applyingSavedSearchParamsRef.current = null;
      applyingSavedSearchParamsKeyRef.current = libraryPageStateKey;
    }

    const normalizedSearchParams = updateLibraryPageSearchParams(
      searchParams,
      { activeTab, browseType, queryDefinition },
      libraryType,
    );

    if (normalizedSearchParams.toString() !== searchParams.toString()) {
      setSearchParams(normalizedSearchParams, { replace: true });
    }
  }, [
    activeTab,
    browseType,
    id,
    libraryPageStateKey,
    queryDefinition,
    libraryType,
    searchParams,
    setSearchParams,
    shouldApplySavedLibrarySearch,
  ]);

  useEffect(() => {
    if (
      !libraryType ||
      !Number.isFinite(id) ||
      id <= 0 ||
      libraryPageStateLoading ||
      !rememberLibraryPageState ||
      shouldApplySavedLibrarySearch
    ) {
      return;
    }

    const normalizedSearchParams = updateLibraryPageSearchParams(
      searchParams,
      { activeTab, browseType, queryDefinition },
      libraryType,
    );
    if (normalizedSearchParams.toString() !== searchParams.toString()) {
      return;
    }

    const canonicalSearch = serializeLibraryPageSearchParams(normalizedSearchParams);
    const retryKey = `${libraryPageStateKey}:${canonicalSearch}`;
    const pendingRetry = librarySaveRetryRef.current;
    if (pendingRetry !== null && pendingRetry.key !== retryKey) {
      if (pendingRetry.timeout !== undefined) {
        clearTimeout(pendingRetry.timeout);
      }
      librarySaveRetryRef.current = null;
    }
    if (applyingSavedSearchParamsRef.current != null) {
      if (applyingSavedSearchParamsRef.current === canonicalSearch) {
        applyingSavedSearchParamsRef.current = null;
      }
      return;
    }
    // Mutation state and cache invalidation can rerender before the effective
    // read catches up. Treat one canonical URL as one logical submission.
    const submitted = submittedLibrarySearchRef.current;
    if (savedLibrarySearch === canonicalSearch) {
      const retry = librarySaveRetryRef.current;
      if (retry?.key === retryKey) {
        if (retry.timeout !== undefined) {
          clearTimeout(retry.timeout);
        }
        librarySaveRetryRef.current = null;
      }
      if (submitted?.key === retryKey) {
        submittedLibrarySearchRef.current = null;
      }
      return;
    }
    if (submitted?.key === retryKey) {
      return;
    }
    const retry = librarySaveRetryRef.current;
    if (retry?.key === retryKey) {
      if (!retry.ready) {
        return;
      }
      retry.ready = false;
    }

    const nextSubmission = { key: retryKey };
    submittedLibrarySearchRef.current = nextSubmission;
    void saveLibrarySearch(id, canonicalSearch).then(
      () => {
        const completedRetry = librarySaveRetryRef.current;
        if (completedRetry?.key === retryKey) {
          if (completedRetry.timeout !== undefined) {
            clearTimeout(completedRetry.timeout);
          }
          librarySaveRetryRef.current = null;
        }
      },
      (error: unknown) => {
        if (submittedLibrarySearchRef.current !== nextSubmission) {
          return;
        }
        submittedLibrarySearchRef.current = null;

        const previousRetry = librarySaveRetryRef.current;
        const failures = previousRetry?.key === retryKey ? previousRetry.failures : 0;
        if (failures >= LIBRARY_SAVE_RETRY_DELAYS_MS.length) {
          librarySaveRetryRef.current = {
            key: retryKey,
            failures,
            ready: false,
          };
          return;
        }

        const fallbackRetryDelay = LIBRARY_SAVE_RETRY_DELAYS_MS[failures];
        if (fallbackRetryDelay === undefined) {
          return;
        }
        const retryDelay = libraryPageStateWriteRetryDelay(error, fallbackRetryDelay);
        if (retryDelay === null) {
          librarySaveRetryRef.current = {
            key: retryKey,
            failures: LIBRARY_SAVE_RETRY_DELAYS_MS.length,
            ready: false,
          };
          return;
        }
        const nextRetry: LibrarySaveRetry = {
          key: retryKey,
          failures: failures + 1,
          ready: false,
        };
        nextRetry.timeout = setTimeout(() => {
          if (librarySaveRetryRef.current !== nextRetry) {
            return;
          }
          nextRetry.timeout = undefined;
          nextRetry.ready = true;
          setLibrarySaveRetryNonce((nonce) => nonce + 1);
        }, retryDelay);
        librarySaveRetryRef.current = nextRetry;
      },
    );
  }, [
    activeTab,
    browseType,
    id,
    libraryPageStateLoading,
    libraryPageStateKey,
    librarySaveRetryNonce,
    libraryType,
    queryDefinition,
    rememberLibraryPageState,
    saveLibrarySearch,
    savedLibrarySearch,
    searchParams,
    shouldApplySavedLibrarySearch,
  ]);

  const handleTabChange = (value: string) => {
    const nextSearchParams = updateLibraryPageSearchParams(
      searchParams,
      {
        activeTab:
          value === "library" ? "library" : value === "collections" ? "collections" : "recommended",
        browseType,
        queryDefinition,
      },
      libraryType,
    );
    setSearchParams(nextSearchParams);
  };

  const handleQueryDefinitionChange = (nextQueryDefinition: QueryDefinition) => {
    const nextSearchParams = updateLibraryPageSearchParams(
      searchParams,
      {
        activeTab: "library",
        browseType,
        queryDefinition: nextQueryDefinition,
      },
      libraryType,
    );
    setSearchParams(nextSearchParams);
  };

  const handleBrowseTypeChange = (
    nextBrowseType: LibraryBrowseType,
    nextQueryDefinition?: QueryDefinition,
  ) => {
    const nextSearchParams = updateLibraryPageSearchParams(
      searchParams,
      {
        activeTab: "library",
        browseType: nextBrowseType,
        queryDefinition: nextQueryDefinition ?? queryDefinition,
      },
      libraryType,
    );
    setSearchParams(nextSearchParams);
  };

  if (isLoading || shouldWaitForSavedLibrarySearch || shouldApplySavedLibrarySearch) {
    return (
      <div className="h-full px-4 py-4 sm:px-6 sm:py-6 lg:px-10 xl:px-12">
        <Skeleton className="mb-6 h-10 w-48" />
        <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7">
          {Array.from({ length: 21 }).map((_, i) => (
            <div key={i}>
              <Skeleton className="aspect-[2/3] w-full rounded-lg" />
              <Skeleton className="mt-2 h-4 w-3/4 rounded" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  if (!library) {
    return (
      <div className="text-muted-foreground flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
        <p>This library is hidden or unavailable for your account.</p>
        <Link to="/settings/libraries" className="text-primary text-sm font-medium hover:underline">
          Manage library visibility in Settings
        </Link>
      </div>
    );
  }

  const isRecommended = activeTab === "recommended";
  // Only use transparent-overlay styling when a hero is actually on screen.
  // Without it, overlay styling (white text, no backdrop) would be illegible
  // against the normal page background — especially on light themes.
  const useOverlay = isRecommended && hasRenderedHero;

  return (
    <div className="relative">
      <Tabs value={activeTab} onValueChange={handleTabChange}>
        <LibraryHeader libraryName={library.name} libraryType={libraryType} overlay={useOverlay} />
        <TabsContent value="recommended" className="mt-0">
          <LibraryRecommended
            libraryId={id}
            libraryType={libraryType}
            onHeroStateChange={handleHeroStateChange}
          />
        </TabsContent>
        <TabsContent value="library" className="mt-0">
          <div className="px-4 py-4 sm:px-6 sm:py-6 lg:px-10 xl:px-12">
            <LibraryBrowse
              libraryId={id}
              libraryType={libraryType || "mixed"}
              browseType={browseType}
              queryDefinition={queryDefinition}
              onBrowseTypeChange={handleBrowseTypeChange}
              onQueryDefinitionChange={handleQueryDefinitionChange}
            />
          </div>
        </TabsContent>
        <TabsContent value="collections" className="mt-0">
          <div className="px-4 py-4 sm:px-6 sm:py-6 lg:px-10 xl:px-12">
            <LibraryCollections libraryId={id} />
          </div>
        </TabsContent>
      </Tabs>
    </div>
  );
}
