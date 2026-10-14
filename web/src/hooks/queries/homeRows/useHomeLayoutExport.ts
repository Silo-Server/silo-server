import { useState } from "react";
import { toast } from "sonner";
import { captureProfileRequestContext } from "@/api/client";
import { v2, V2ProblemError } from "@/api/v2/request";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import {
  HOME_LAYOUT_MAX_LENGTH,
  buildHomeLayoutFile,
  type HomeLayoutScope,
} from "@/lib/homeLayoutTransfer";
import { SETTING_KEYS } from "@/lib/settingsContract";

const HOME_PREFERENCE_KEYS = [SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS] as const;
// Page reads during export run this many at a time.
const READ_BATCH = 4;

async function readInBatches<T, R>(items: readonly T[], read: (item: T) => Promise<R>) {
  const results: R[] = [];
  for (let start = 0; start < items.length; start += READ_BATCH) {
    results.push(...(await Promise.all(items.slice(start, start + READ_BATCH).map(read))));
  }
  return results;
}

/** The query that addresses one page's saved overrides. */
export function layoutPageQuery(scope: HomeLayoutScope, libraryId?: number) {
  return { scope, library_id: libraryId ? String(libraryId) : undefined };
}

export function layoutProblemMessage(error: unknown): string {
  if (error instanceof V2ProblemError) {
    return error.problem.detail?.trim() || error.problem.title || "request failed";
  }
  return error instanceof Error ? error.message : "request failed";
}

function downloadJson(fileName: string, blob: Blob) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

export interface HomeLayoutExport {
  /** Downloads this profile's Home and library page layouts as one file. */
  run(): Promise<void>;
  running: boolean;
  /** The libraries and Home preferences have loaded. */
  ready: boolean;
  /**
   * The libraries the layout covers, once loaded; the import plans against
   * them too. These are the libraries this profile shows: the server refuses
   * section reads and writes for one the profile hid.
   */
  libraries: ReturnType<typeof useUserLibraries>["data"];
}

/** Export of the profile's Home and library page layouts (#1705), started from More. */
export function useHomeLayoutExport(): HomeLayoutExport {
  const librariesQuery = useUserLibraries();
  // Until the profile's hidden libraries load, the list still includes them.
  const libraries = librariesQuery.isLoading ? undefined : librariesQuery.data;
  const homePreferences = useEffectiveSettings({ keys: HOME_PREFERENCE_KEYS });
  // Only a value this profile chose travels; an inherited one would be
  // pinned on the importing profile.
  const hideWatched = homePreferences.data?.[SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS];
  const hideWatchedItems =
    hideWatched?.source === "profile" ? hideWatched.value === true : undefined;
  const [running, setRunning] = useState(false);

  async function run() {
    if (!libraries) return;
    if (homePreferences.isError) {
      toast.error(
        "Couldn't read this profile's Home preferences, so the export would be incomplete. Reload the page and try again.",
      );
      return;
    }
    if (librariesQuery.error) {
      toast.error(
        "Couldn't read which libraries this profile shows, so the export would fail. Reload the page and try again.",
      );
      return;
    }
    // Every read goes to the profile active now, even if it changes mid-export.
    const profileContext = captureProfileRequestContext();
    if (!profileContext) {
      toast.error("Choose a profile before exporting its home layout.");
      return;
    }
    setRunning(true);
    try {
      const identity = await v2("GET /api/v2/system/identity");
      const sources = [
        { scope: "home" as const, libraryId: undefined },
        ...libraries.map((library) => ({ scope: "library" as const, libraryId: library.id })),
      ];
      const pages = await readInBatches(sources, async (source) => {
        const result = await v2("GET /api/v2/profile/sections", {
          query: layoutPageQuery(source.scope, source.libraryId),
          profileContext,
        });
        return { ...source, overrides: result.items };
      });
      const exportedAt = new Date();
      const file = buildHomeLayoutFile({
        serverId: identity.server_id,
        exportedAt,
        libraries,
        hideWatchedItems,
        pages,
      });
      const blob = new Blob([`${JSON.stringify(file, null, 2)}\n`], { type: "application/json" });
      if (blob.size > HOME_LAYOUT_MAX_LENGTH) {
        toast.error(
          "This home layout is too large to export. Remove some custom sections and try again.",
        );
        return;
      }
      downloadJson(`silo-home-layout-${exportedAt.toISOString().slice(0, 10)}.json`, blob);
      toast.success("Home layout exported");
    } catch (error) {
      toast.error(`Failed to export the home layout: ${layoutProblemMessage(error)}`);
    } finally {
      setRunning(false);
    }
  }

  return {
    run,
    running,
    ready: Boolean(libraries) && !homePreferences.isLoading,
    libraries,
  };
}
