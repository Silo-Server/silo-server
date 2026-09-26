import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";
import {
  buildLibraryCollectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "./catalogSearchParams";

const mocks = vi.hoisted(() => ({
  useCatalogWindow: vi.fn(),
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mocks.useCatalogWindow(...args),
  useCatalogFilters: () => ({ data: undefined, isLoading: false }),
  useCatalogMetadataFilters: () => ({ data: undefined, isLoading: false }),
}));
vi.mock("@/hooks/queries/people", () => ({
  usePersonSearch: () => ({ data: undefined, isLoading: false, isError: false }),
}));
vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => ({ discoveryEnabled: false, isResolving: false }),
}));
vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: () => ({ data: undefined, isLoading: false }),
}));
vi.mock("@/components/ItemGrid", () => ({ default: () => <div data-testid="item-grid" /> }));
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({ default: () => null }));

import Catalog from "./Catalog";

function catalogProblem(status: number) {
  return new V2ProblemError("queryCatalog", {
    type: `https://silo.example/problems/${status === 404 ? "not_found" : "internal_error"}`,
    title: status === 404 ? "Not Found" : "Internal Server Error",
    status,
    detail: status === 404 ? "Catalog source not found" : "The catalog is unavailable.",
    instance: "/api/v2/catalog/query",
  });
}

function catalogWindow(error: Error | null, sourceError: Error | null = error) {
  return {
    data: { title: undefined, totalItems: 0, pages: new Map() },
    isLoading: false,
    isError: error !== null,
    isPlaceholderData: false,
    error,
    sourceError,
    refetch: vi.fn(),
  };
}

function renderCatalog(href: string) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[href]}>
        <Catalog />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  document.title = "Silo";
});
afterEach(() => {
  cleanup();
  mocks.useCatalogWindow.mockReset();
});

it("gives a collection the viewer cannot reach a not-available page", () => {
  mocks.useCatalogWindow.mockReturnValue(catalogWindow(catalogProblem(404)));

  renderCatalog(buildUserCollectionCatalogHref("collection-1", "Weekend picks"));

  expect(
    screen.getByRole("heading", { level: 1, name: "This collection isn't available" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "All collections" })).toHaveAttribute(
    "href",
    "/collections",
  );
  expect(screen.queryByText("Could not load catalog results.")).not.toBeInTheDocument();
  expect(screen.queryByTestId("item-grid")).not.toBeInTheDocument();
  expect(document.title).toContain("Not found");
});

it("titles a collection by its name even when the result is already known on mount", () => {
  mocks.useCatalogWindow.mockReturnValue({
    ...catalogWindow(null),
    data: { title: "Weekend picks", totalItems: 0, pages: new Map() },
  });

  renderCatalog(buildUserCollectionCatalogHref("collection-1", "Weekend picks"));

  expect(document.title).toContain("Weekend picks");
});

it("treats a missing library collection the same way", () => {
  mocks.useCatalogWindow.mockReturnValue(catalogWindow(catalogProblem(404)));

  renderCatalog(buildLibraryCollectionCatalogHref("collection-2", "Staff picks", 4));

  expect(
    screen.getByRole("heading", { level: 1, name: "This collection isn't available" }),
  ).toBeInTheDocument();
});

it("keeps a loaded collection when only a later page answers 404", () => {
  mocks.useCatalogWindow.mockReturnValue(catalogWindow(catalogProblem(404), null));

  renderCatalog(buildUserCollectionCatalogHref("collection-1", "Weekend picks"));

  expect(screen.queryByText("This collection isn't available")).not.toBeInTheDocument();
  expect(screen.getByText("Could not load catalog results.")).toBeInTheDocument();
});

it("keeps the retry panel for a collection that failed to load", () => {
  mocks.useCatalogWindow.mockReturnValue(catalogWindow(catalogProblem(500)));

  renderCatalog(buildUserCollectionCatalogHref("collection-1", "Weekend picks"));

  expect(screen.getByText("Could not load catalog results.")).toBeInTheDocument();
  expect(screen.queryByText("This collection isn't available")).not.toBeInTheDocument();
});

it("leaves non-collection sources on the catalog's own error handling", () => {
  mocks.useCatalogWindow.mockReturnValue(catalogWindow(catalogProblem(404)));

  renderCatalog("/catalog?source=favorites");

  expect(screen.queryByText("This collection isn't available")).not.toBeInTheDocument();
  expect(screen.getByText("Could not load catalog results.")).toBeInTheDocument();
});
