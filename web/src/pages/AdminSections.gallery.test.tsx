import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AdminSections from "./AdminSections";

const mocks = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 7, name: "Movies", type: "movies" }] }),
  fetchAdminLibraries: async () => [{ id: 7, name: "Movies" }],
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({ data: [] }),
  useImportTraktCollection: () => ({ mutateAsync: vi.fn() }),
}));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));
vi.mock("@/lib/recipes", () => ({ fetchRecipeCatalog: async () => ({ categories: {} }) }));
vi.mock("@/components/RecipeGallery/RecipeParamFields", () => ({ default: () => null }));
vi.mock("@/components/RecipeGallery/RecipeGalleryModal", () => ({
  default: ({
    open,
    onPick,
  }: {
    open: boolean;
    onPick: (def: unknown, preset: unknown) => void;
  }) =>
    open ? (
      <button
        onClick={() =>
          onPick(
            { type: "recently_added", presets: [] },
            {
              key: "ra",
              display_name: "Recently Added",
              icon: "",
              description_short: "Latest",
              default_params: {},
            },
          )
        }
      >
        Choose Recently Added
      </button>
    ) : null,
}));

let creates: Array<Record<string, unknown>>;
let finishCreate: () => void;

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  creates = [];
  mocks.request.mockImplementation(
    async (
      operation: string,
      args: { body?: Record<string, unknown>; onResponse?: (response: Response) => void } = {},
    ) => {
      args.onResponse?.(new Response(null, { headers: { ETag: '"rev-1"' } }));
      if (operation === "GET /api/v2/admin/sections/capabilities")
        return { available: true, reset_profiles: false, preview: true };
      if (operation === "GET /api/v2/admin/sections/order") return { ordered_ids: [] };
      if (operation === "GET /api/v2/admin/sections") return { items: [] };
      if (operation === "POST /api/v2/admin/sections") {
        creates.push(args.body!);
        await new Promise<void>((resolve) => {
          finishCreate = resolve;
        });
        return { id: "created", ...args.body };
      }
      throw new Error(`Unexpected ${operation}`);
    },
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function openGalleryConfig(tab: "Home" | "Library") {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <AdminSections />
    </QueryClientProvider>,
  );
  if (tab === "Library") {
    fireEvent.mouseDown(await screen.findByRole("tab", { name: "Library" }), { button: 0 });
  }
  const gallery = await screen.findByRole("button", { name: "Add from Gallery" });
  await waitFor(() => expect(gallery).toBeEnabled());
  await userEvent.click(gallery);
  await userEvent.click(await screen.findByRole("button", { name: "Choose Recently Added" }));
}

describe("admin Home rows page", () => {
  it("is titled Home rows and says who sees the rows", async () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <AdminSections />
      </QueryClientProvider>,
    );

    expect(await screen.findByRole("heading", { level: 1, name: "Home rows" })).toBeInTheDocument();
    expect(
      screen.getByText(
        "The rows everyone sees on Home and on library pages. Profiles can still hide, rename or reorder them.",
      ),
    ).toBeInTheDocument();
  });
});

describe("admin section gallery", () => {
  it("creates one Home row from the Home tab, without offering library bulk apply", async () => {
    await openGalleryConfig("Home");
    expect(screen.queryByLabelText(/apply to all libraries/i)).toBeNull();

    await userEvent.dblClick(screen.getByRole("button", { name: "Add section" }));
    expect(creates).toEqual([expect.objectContaining({ scope: "home" })]);
    await act(async () => finishCreate());
    await waitFor(() => expect(screen.queryByRole("button", { name: "Add section" })).toBeNull());
    expect(creates).toHaveLength(1);
  });

  it("offers library bulk apply on the Library tab", async () => {
    await openGalleryConfig("Library");
    expect(screen.getByLabelText(/apply to all libraries/i)).toBeInTheDocument();
  });
});
