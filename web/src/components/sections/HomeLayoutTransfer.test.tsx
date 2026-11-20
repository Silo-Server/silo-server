import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { HomeLayoutImportDialog } from "./HomeLayoutTransfer";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  savedPuts: [] as Array<{ query?: unknown; body?: unknown }>,
}));

vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("@/lib/recipes", () => ({
  fetchRecipeCatalog: vi.fn(async () => ({ categories: { discovery: [] } })),
}));
vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { role: "user" } }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function renderDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<HomeLayoutImportDialog onClose={vi.fn()} libraries={[]} />, { wrapper });
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.savedPuts = [];
  setAccessToken("token");
  setProfileId("p1");
  mocks.request.mockImplementation(
    async (operation: string, args: { query?: unknown; body?: unknown } = {}) => {
      switch (operation) {
        case "GET /api/v2/system/identity":
          return { server_id: "srv-1" };
        case "GET /api/v2/profile/sections/baseline":
          return {
            section_tokens: ["tok-continue", "tok-collections"],
            visible_sections: [
              {
                id: "default-continue-watching",
                section_type: "continue_watching",
                title: "Continue Watching",
                token: "tok-continue",
              },
              {
                id: "s-collections",
                section_type: "collection_posters",
                title: "Collections",
                token: "tok-collections",
              },
            ],
          };
        case "GET /api/v2/profile/sections":
          return { items: [] };
        case "PUT /api/v2/profile/sections":
          mocks.savedPuts.push(args);
          return {};
      }
      throw new Error(`Unexpected ${operation}`);
    },
  );
});

afterEach(() => {
  cleanup();
  setProfileId(null);
  setAccessToken(null);
});

describe("HomeLayoutImportDialog", () => {
  it("prechecks server rows added after export and hides them on import", async () => {
    renderDialog();

    const file = {
      format: "silo-home-layout",
      version: 1,
      exported_at: "2026-11-10T00:00:00.000Z",
      server_id: "srv-1",
      libraries: [],
      pages: [
        {
          scope: "home",
          overrides: [],
          server_section_tokens: ["tok-continue"],
        },
      ],
    };
    fireEvent.change(screen.getByLabelText("Layout file"), {
      target: { value: JSON.stringify(file) },
    });

    const collectionsBox = await screen.findByRole("checkbox", { name: "Collections" });
    expect(collectionsBox).toHaveAttribute("data-state", "checked");
    expect(screen.queryByRole("checkbox", { name: "Continue Watching" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.savedPuts).toHaveLength(1));
    expect(mocks.savedPuts[0]?.body).toEqual({
      overrides: [
        expect.objectContaining({
          section_id: "s-collections",
          hidden: true,
        }),
      ],
    });
  });

  it("imports a file from before tokens existed as before, without offering rows to hide", async () => {
    renderDialog();

    const olderFile = {
      format: "silo-home-layout",
      version: 1,
      exported_at: "2026-10-01T00:00:00.000Z",
      server_id: "srv-1",
      libraries: [],
      pages: [{ scope: "home", overrides: [] }],
    };
    fireEvent.change(screen.getByLabelText("Layout file"), {
      target: { value: JSON.stringify(olderFile) },
    });

    const importButton = await screen.findByRole("button", { name: "Import" });
    await waitFor(() => expect(importButton).toBeEnabled());
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(mocks.request).not.toHaveBeenCalledWith(
      "GET /api/v2/profile/sections/baseline",
      expect.anything(),
    );

    fireEvent.click(importButton);
    await waitFor(() => expect(mocks.savedPuts).toHaveLength(1));
    expect(mocks.savedPuts[0]?.body).toEqual({ overrides: [] });
  });

  it("still imports the file's rows when the server baseline can't be read", async () => {
    const answer = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation(async (operation: string, args?: unknown) => {
      if (operation === "GET /api/v2/profile/sections/baseline") throw new Error("unavailable");
      return answer(operation, args);
    });
    renderDialog();

    const file = {
      format: "silo-home-layout",
      version: 1,
      exported_at: "2026-11-10T00:00:00.000Z",
      server_id: "srv-1",
      libraries: [],
      pages: [
        {
          scope: "home",
          overrides: [{ section_id: "default-continue-watching", position: 1 }],
          server_section_tokens: ["tok-continue"],
        },
      ],
    };
    fireEvent.change(screen.getByLabelText("Layout file"), {
      target: { value: JSON.stringify(file) },
    });

    const importButton = await screen.findByRole("button", { name: "Import" });
    await waitFor(() => expect(importButton).toBeEnabled());
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    fireEvent.click(importButton);
    await waitFor(() => expect(mocks.savedPuts).toHaveLength(1));
    expect(mocks.savedPuts[0]?.body).toEqual({
      overrides: [
        expect.objectContaining({ section_id: "default-continue-watching", position: 1 }),
      ],
    });
  });
});
