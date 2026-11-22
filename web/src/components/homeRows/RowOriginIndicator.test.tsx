import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { EditSession, HomeRow, HomeRowsAdapter, Surface } from "@/lib/homeRows/types";
import { RowLine } from "./RowLine";
import { AddRowDialog } from "./addRow/AddRowDialog";

afterEach(cleanup);

function makeRow(overrides: Partial<HomeRow> = {}): HomeRow {
  return {
    id: "row-1",
    title: "Collections",
    sectionType: "collection_posters",
    config: { collections: [{ library_collection_id: "c1" }] },
    itemLimit: 20,
    hero: false,
    shown: true,
    own: false,
    ...overrides,
  };
}

function makeAdapter(surface: Surface, row: HomeRow): HomeRowsAdapter {
  return {
    surface,
    page: { kind: "home" },
    pages: [{ ref: { kind: "home" }, label: "Home" }],
    setPage: vi.fn(),
    status: "ready",
    error: null,
    canEdit: true,
    rows: [row],
    pending: false,
    conflict: null,
    reload: vi.fn(async () => {}),
    canReorder: true,
    orderToken: undefined,
    reorder: vi.fn(async () => {}),
    setShown: vi.fn(async () => {}),
    setHero: vi.fn(async () => {}),
    capabilities: { draftPreview: false, libraryCopies: false },
    create: vi.fn(async () => ({ newIds: [] })),
    openEdit: vi.fn(async () => ({
      row,
      token: row,
      kindLocked: surface === "profile" && !row.own,
    })),
    reloadEdit: vi.fn(async () => ({
      row,
      token: row,
      kindLocked: surface === "profile" && !row.own,
    })),
    save: vi.fn(async () => {}),
    peek: () => null,
    collections: { options: [], loading: false, failed: false, href: "/collections" },
  };
}

function renderEditDialog(surface: Surface, row: HomeRow) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const session: EditSession = {
    row,
    token: row,
    kindLocked: surface === "profile" && !row.own,
  };
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AddRowDialog
          adapter={makeAdapter(surface, row)}
          catalog={undefined}
          libraries={[]}
          session={session}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Home row origin indicators", () => {
  it("tags server-managed rows as Server and profile rows as Yours on Settings > Home Screen, and omits both on admin", () => {
    const { unmount } = render(
      <ul>
        <RowLine
          row={makeRow({ own: false })}
          surface="profile"
          page={{ kind: "home" }}
          pageLabel="Home"
          description={["1 collection"]}
          peek={null}
          onShownChange={vi.fn()}
          menu={null}
        />
        <RowLine
          row={makeRow({ id: "row-2", title: "My Picks", own: true })}
          surface="profile"
          page={{ kind: "home" }}
          pageLabel="Home"
          description={["1 collection"]}
          peek={null}
          onShownChange={vi.fn()}
          menu={null}
        />
      </ul>,
    );

    expect(screen.getAllByText("Server").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Yours").length).toBeGreaterThan(0);
    unmount();

    render(
      <ul>
        <RowLine
          row={makeRow({ own: false })}
          surface="admin"
          page={{ kind: "home" }}
          pageLabel="Home"
          description={["1 collection"]}
          peek={null}
          onShownChange={vi.fn()}
          menu={null}
        />
      </ul>,
    );

    expect(screen.queryByText("Server")).not.toBeInTheDocument();
    expect(screen.queryByText("Yours")).not.toBeInTheDocument();
  });

  it("shows Server + note or Yours in AddRowDialog on the profile surface and omits both on admin", () => {
    const serverRow = makeRow({ own: false });
    const { unmount } = renderEditDialog("profile", serverRow);
    expect(screen.getByText("Server")).toBeInTheDocument();
    expect(screen.getByText(/Managed by this server/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Change/i })).not.toBeInTheDocument();
    unmount();

    const ownRow = makeRow({ own: true });
    const { unmount: unmountOwn } = renderEditDialog("profile", ownRow);
    expect(screen.getByText("Yours")).toBeInTheDocument();
    expect(screen.queryByText("Server")).not.toBeInTheDocument();
    expect(screen.queryByText(/Managed by this server/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Change/i })).toBeInTheDocument();
    unmountOwn();

    renderEditDialog("admin", serverRow);
    expect(screen.queryByText("Server")).not.toBeInTheDocument();
    expect(screen.queryByText("Yours")).not.toBeInTheDocument();
    expect(screen.queryByText(/Managed by this server/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Change/i })).toBeInTheDocument();
  });
});
