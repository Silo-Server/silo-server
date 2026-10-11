import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { setAccessToken, setProfileId } from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { useHomeLayoutExport } from "./useHomeLayoutExport";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  hidden: [] as number[],
  libraryPrefsFail: false,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ profile: { id: "p1" } }) }));
vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: ({ keys }: { keys: readonly string[] }) => {
    if (!keys.includes(SETTING_KEYS.UI_DISABLED_LIBRARY_IDS)) return { data: {}, isLoading: false };
    if (mocks.libraryPrefsFail) {
      return { data: undefined, isLoading: false, error: new Error("settings down") };
    }
    return {
      data: { [SETTING_KEYS.UI_DISABLED_LIBRARY_IDS]: { value: mocks.hidden, source: "profile" } },
      isLoading: false,
    };
  },
}));

const LIBRARIES = [
  { id: "7", name: "Movies", type: "movies", sort_order: 0 },
  { id: "8", name: "Shows", type: "tvshows", sort_order: 1 },
  { id: "9", name: "Kids", type: "movies", sort_order: 2 },
];

/** The libraries the account may open; null is an unrestricted account. */
let allowed: number[] | null;
let reads: Array<string | undefined>;

// The server's library gate (viewerCanAccessLibrary): an unrestricted account
// is refused the libraries the profile hid, and a restricted one everything
// outside its allowed set minus the hidden ones.
function canOpen(id: number) {
  if (mocks.hidden.includes(id)) return false;
  return allowed === null || allowed.includes(id);
}

function notFound() {
  return new V2ProblemError("listProfileSectionOverrides", {
    type: "not_found",
    instance: "/api/v2/profile/sections",
    title: "Not Found",
    detail: "Library not found",
    status: 404,
  });
}

let blobs: Blob[];

beforeEach(() => {
  vi.clearAllMocks();
  setAccessToken("token");
  setProfileId("p1");
  mocks.hidden = [];
  mocks.libraryPrefsFail = false;
  allowed = null;
  reads = [];
  blobs = [];
  vi.stubGlobal(
    "URL",
    Object.assign(URL, {
      createObjectURL: (blob: Blob) => {
        blobs.push(blob);
        return "blob:layout";
      },
      revokeObjectURL: vi.fn(),
    }),
  );
  mocks.request.mockImplementation(
    async (operation: string, args: { query?: Record<string, unknown> } = {}) => {
      switch (operation) {
        case "GET /api/v2/user/libraries":
          // Unrestricted accounts get every enabled library back, hidden or
          // not; restricted ones get only what they may open.
          return {
            items: LIBRARIES.filter((library) => allowed === null || canOpen(Number(library.id))),
          };
        case "GET /api/v2/system/identity":
          return { server_id: "server-1" };
        case "GET /api/v2/profile/sections": {
          const libraryId = args.query?.library_id as string | undefined;
          reads.push(libraryId);
          if (libraryId && !canOpen(Number(libraryId))) throw notFound();
          return {
            items: [{ section_id: `row-${libraryId ?? "home"}`, hidden: true }],
          };
        }
      }
      throw new Error(`Unexpected ${operation}`);
    },
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setProfileId(null);
  setAccessToken(null);
});

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

async function exportLayout() {
  const { result } = renderHook(() => useHomeLayoutExport(), { wrapper });
  await waitFor(() => expect(result.current.ready).toBe(true));
  await act(() => result.current.run());
  return result.current;
}

async function exportedFile() {
  expect(toast.error).not.toHaveBeenCalled();
  expect(blobs).toHaveLength(1);
  return JSON.parse(await blobs[0]!.text()) as {
    libraries: Array<{ id: number }>;
    pages: Array<{ scope: string; library_id?: number; overrides: unknown[] }>;
  };
}

describe("useHomeLayoutExport", () => {
  it("exports Home and the shown library pages when an unrestricted profile hides a library", async () => {
    mocks.hidden = [8];
    const exporter = await exportLayout();

    const file = await exportedFile();
    expect(reads).toEqual([undefined, "7", "9"]);
    expect(file.libraries.map((library) => library.id)).toEqual([7, 9]);
    expect(file.pages.map((page) => page.library_id ?? page.scope)).toEqual(["home", 7, 9]);
    expect(file.pages.every((page) => page.overrides.length === 1)).toBe(true);
    expect(exporter.libraries?.map((library) => library.id)).toEqual([7, 9]);
    expect(toast.success).toHaveBeenCalledWith("Home layout exported");
  });

  it("exports a restricted profile's allowed libraries except the one it hid", async () => {
    allowed = [7, 9];
    mocks.hidden = [9];
    await exportLayout();

    const file = await exportedFile();
    expect(reads).toEqual([undefined, "7"]);
    expect(file.libraries.map((library) => library.id)).toEqual([7]);
    expect(file.pages.map((page) => page.library_id ?? page.scope)).toEqual(["home", 7]);
  });

  it("still fails the whole export when a library page read fails", async () => {
    // A library the list offers but the server refuses (disabled between the
    // two reads) keeps its access check; the export reports it instead of
    // saving a file with that page missing.
    allowed = null;
    const listed = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation(async (operation: string, args: never) => {
      if (operation === "GET /api/v2/profile/sections" && JSON.stringify(args).includes('"9"')) {
        throw notFound();
      }
      return listed(operation, args);
    });
    await exportLayout();

    expect(blobs).toHaveLength(0);
    expect(toast.error).toHaveBeenCalledWith("Failed to export the home layout: Library not found");
  });

  it("doesn't export when the profile's hidden libraries can't be read", async () => {
    mocks.libraryPrefsFail = true;
    await exportLayout();

    expect(blobs).toHaveLength(0);
    expect(reads).toEqual([]);
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("which libraries"));
  });
});
