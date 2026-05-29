import { useCallback, useEffect, useRef, useState } from "react";
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
import { useLibraryPageStatePreference } from "@/hooks/queries/libraryPageState";
import {
  applySavedLibraryPageSearchParams,
  hasLibraryPageSearchParams,
  parseLibraryPageState,
  serializeLibraryPageSearchParams,
  updateLibraryPageSearchParams,
} from "./libraryPageSearchParams";

export default function LibraryPage() {
  const { libraryId } = useParams<{ libraryId: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: libraries, isLoading } = useUserLibraries();
  const {
    isLoading: libraryPageStateLoading,
    preference: libraryPageStatePreference,
    rememberEnabled: rememberLibraryPageState,
    saveLibrarySearch,
  } = useLibraryPageStatePreference();
  const savedStateHydratedLibraryIdRef = useRef<number | null>(null);
  const applyingSavedSearchParamsRef = useRef<string | null>(null);

  const id = Number(libraryId);
  const library = libraries?.find((l) => l.id === id);
  const libraryType = library?.type ?? "";
  const savedLibrarySearch =
    Number.isFinite(id) && id > 0
      ? libraryPageStatePreference.libraries[String(id)]?.search
      : undefined;
  const { activeTab, browseType, queryDefinition } = parseLibraryPageState(
    searchParams,
    libraryType,
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

  useEffect(() => {
    if (
      !libraryType ||
      !Number.isFinite(id) ||
      id <= 0 ||
      libraryPageStateLoading ||
      !rememberLibraryPageState ||
      savedStateHydratedLibraryIdRef.current === id
    ) {
      return;
    }

    savedStateHydratedLibraryIdRef.current = id;
    if (
      savedLibrarySearch == null ||
      hasLibraryPageSearchParams(searchParams) ||
      savedLibrarySearch === serializeLibraryPageSearchParams(searchParams)
    ) {
      return;
    }

    const nextSearchParams = applySavedLibraryPageSearchParams(searchParams, savedLibrarySearch);
    if (nextSearchParams.toString() !== searchParams.toString()) {
      applyingSavedSearchParamsRef.current = nextSearchParams.toString();
      setSearchParams(nextSearchParams, { replace: true });
    }
  }, [
    id,
    libraryPageStateLoading,
    libraryType,
    rememberLibraryPageState,
    savedLibrarySearch,
    searchParams,
    setSearchParams,
  ]);

  useEffect(() => {
    if (!libraryType) {
      return;
    }

    const normalizedSearchParams = updateLibraryPageSearchParams(
      searchParams,
      { activeTab, browseType, queryDefinition },
      libraryType,
    );

    if (normalizedSearchParams.toString() !== searchParams.toString()) {
      setSearchParams(normalizedSearchParams, { replace: true });
    }
  }, [activeTab, browseType, queryDefinition, libraryType, searchParams, setSearchParams]);

  useEffect(() => {
    if (
      !libraryType ||
      !Number.isFinite(id) ||
      id <= 0 ||
      libraryPageStateLoading ||
      !rememberLibraryPageState
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

    const search = serializeLibraryPageSearchParams(normalizedSearchParams);
    if (applyingSavedSearchParamsRef.current != null) {
      if (applyingSavedSearchParamsRef.current === normalizedSearchParams.toString()) {
        applyingSavedSearchParamsRef.current = null;
      }
      return;
    }
    if (savedLibrarySearch !== search) {
      saveLibrarySearch(id, search);
    }
  }, [
    activeTab,
    browseType,
    id,
    libraryPageStateLoading,
    libraryType,
    queryDefinition,
    rememberLibraryPageState,
    saveLibrarySearch,
    savedLibrarySearch,
    searchParams,
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

  const handleBrowseTypeChange = (nextBrowseType: "series" | "episode") => {
    const nextSearchParams = updateLibraryPageSearchParams(
      searchParams,
      {
        activeTab: "library",
        browseType: nextBrowseType,
        queryDefinition,
      },
      libraryType,
    );
    setSearchParams(nextSearchParams);
  };

  if (isLoading) {
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
        <LibraryHeader libraryName={library.name} overlay={useOverlay} />
        <TabsContent value="recommended" className="mt-0">
          <LibraryRecommended libraryId={id} onHeroStateChange={handleHeroStateChange} />
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
