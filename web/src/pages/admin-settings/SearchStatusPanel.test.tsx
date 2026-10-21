import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SearchStatusPanel } from "./SearchStatusPanel";

const catalogSearchStatusMock = vi.fn();

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCatalogSearchStatus: () => catalogSearchStatusMock(),
}));

const STATUS = {
  active_provider: "postgres",
  configured_provider: "postgres",
  degraded: false,
  meilisearch: {
    configured: false,
    healthy: false,
    circuit_state: "closed",
    binary_quantized: false,
    semantic_enabled: false,
  },
  index: {
    active_index_uid: "",
    schema_version: 3,
    expected_schema_version: 3,
    rebuild_required: false,
    document_count: 0,
    vector_document_count: 0,
    pending_events: 0,
    dead_lettered_events: 0,
  },
};

function renderPanel() {
  return render(
    <MemoryRouter>
      <SearchStatusPanel />
    </MemoryRouter>,
  );
}

describe("SearchStatusPanel", () => {
  beforeEach(() => {
    catalogSearchStatusMock.mockReset();
  });

  it("reports a failed status request instead of leaving the skeletons up", () => {
    catalogSearchStatusMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("search status unavailable"),
    });

    renderPanel();

    expect(
      screen.getByText("Couldn't load search status: search status unavailable"),
    ).toBeInTheDocument();
    // The maintenance tasks are exactly what an admin wants when the index is
    // unhealthy enough for the status endpoint to fail, so they stay reachable.
    expect(screen.getByRole("link", { name: "Rebuild index" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Automatic maintenance" })).toBeInTheDocument();
  });

  it("shows a stale compatible index as Meilisearch keyword-only", () => {
    catalogSearchStatusMock.mockReturnValue({
      data: {
        ...STATUS,
        active_provider: "meilisearch",
        configured_provider: "meilisearch",
        degraded: true,
        degraded_reason: "Search index rebuild required; using Meilisearch keyword search",
        meilisearch: { ...STATUS.meilisearch, configured: true, healthy: true },
        index: { ...STATUS.index, active_index_uid: "search-index", rebuild_required: true },
      },
      isLoading: false,
      isError: false,
      error: null,
    });

    renderPanel();

    expect(screen.getByText("Meilisearch (keyword only)")).toBeInTheDocument();
    expect(
      screen.getByText("Search index rebuild required; using Meilisearch keyword search"),
    ).toBeInTheDocument();
    expect(screen.getByText("rebuild required")).toBeInTheDocument();
  });
});
