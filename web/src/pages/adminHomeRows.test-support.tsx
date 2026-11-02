import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { expect, vi } from "vitest";
import AdminHomeRows from "./AdminHomeRows";

/** A stored admin row as the fake `v2` answers it. */
export type Row = {
  id: string;
  title: string;
  scope: string;
  library_id: string | null;
  position: number;
  section_type: string;
  item_limit: number;
  featured: boolean;
  enabled: boolean;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
};
export type Args = {
  query?: { scope?: string; library_id?: string };
  path?: { id: string };
  headers?: Record<string, string>;
  body?: Record<string, unknown>;
  onResponse?: (response: Response) => void;
};
export type Write = { operation: string; args: Args };

export function stored(id: string, overrides: Partial<Row> = {}): Row {
  return {
    id,
    title: `Row ${id}`,
    scope: "home",
    library_id: null,
    position: 0,
    section_type: "recently_added",
    item_limit: 20,
    featured: false,
    enabled: true,
    config: {},
    created_at: "2026-09-05T00:00:00Z",
    updated_at: "2026-09-05T00:00:00Z",
    ...overrides,
  };
}

/** Radix menus and dialogs measure themselves; jsdom has no ResizeObserver. */
export function stubResizeObserver() {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
}

/** The rows of the page a request names: Home, or one library page. */
export function pageRows(rows: Row[], args: Args) {
  const scope = args.query?.scope ?? "home";
  return rows.filter(
    (row) => row.scope === scope && (scope === "home" || row.library_id === args.query?.library_id),
  );
}

/**
 * Answers the reads Home rows makes from `rows`, listing `here` (by default the
 * requested page's rows). Returns undefined for any other operation.
 */
export function readSections(
  operation: string,
  args: Args,
  rows: Row[],
  { here = pageRows(rows, args), preview = false, resetProfiles = false } = {},
) {
  switch (operation) {
    case "GET /api/v2/admin/sections/capabilities":
      return { available: true, reset_profiles: resetProfiles, preview };
    case "GET /api/v2/admin/sections/order":
      return {
        scope: args.query?.scope ?? "home",
        library_id: args.query?.library_id ?? null,
        ordered_ids: here.map((row) => row.id),
      };
    case "GET /api/v2/admin/sections":
      return { items: here.map((row) => ({ ...row })) };
    case "GET /api/v2/admin/sections/{id}":
      return { ...rows.find((row) => row.id === args.path?.id)! };
  }
  return undefined;
}

/** Applies the fields a PATCH sends to the row it names. */
export function patchRow(rows: Row[], args: Args) {
  const changes = Object.fromEntries(
    Object.entries(args.body ?? {}).filter(([, value]) => value !== undefined),
  );
  return rows.map((row) => (row.id === args.path?.id ? { ...row, ...changes } : row));
}

export const newClient = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

export function renderAdminHomeRows(entry = "/admin/sections", client = newClient()) {
  const view = render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={client}>
        <AdminHomeRows />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return { client, unmount: view.unmount };
}

/** Waits until the page has its rows and capabilities, so Add row is enabled. */
export async function waitForAddRow() {
  const add = await screen.findByRole("button", { name: "Add row" });
  await waitFor(() => expect(add).toBeEnabled());
  return add;
}
