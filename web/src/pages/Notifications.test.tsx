// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

// ---------------------------------------------------------------------------
// Hoisted mutable mock state
// ---------------------------------------------------------------------------
const mocks = vi.hoisted(() => ({
  listPages: [] as Array<{ items: Array<{ id: number; title: string; body: string; category: string; created_at: string; read_at?: string; link?: string }> }>,
  hasNextPage: false,
  fetchNextPage: vi.fn(),
  markReadMutate: vi.fn(),
  dismissMutate: vi.fn(),
  unreadCount: 0,
  listFilters: {} as { unread?: boolean; category?: string },
  userRole: "user" as string,
}));

vi.mock("@/hooks/queries/notifications", () => ({
  useNotificationsList: (filters: { unread?: boolean; category?: string } = {}) => {
    mocks.listFilters = filters;
    return {
      data: { pages: mocks.listPages },
      hasNextPage: mocks.hasNextPage,
      fetchNextPage: mocks.fetchNextPage,
      isLoading: false,
    };
  },
  useMarkRead: () => ({
    mutate: mocks.markReadMutate,
    isPending: false,
  }),
  useDismissNotification: () => ({
    mutate: mocks.dismissMutate,
    isPending: false,
  }),
  useUnreadCount: () => ({ data: mocks.unreadCount }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    user: { id: 1, username: "alice", role: mocks.userRole },
  }),
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: () => undefined,
}));

// ---------------------------------------------------------------------------
import Notifications from "./Notifications";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function makeNotification(
  id: number,
  overrides: Partial<{ title: string; body: string; category: string; read_at: string; link: string }> = {},
) {
  return {
    id,
    user_id: 1,
    category: "system" as const,
    type: "test",
    title: `Title ${id}`,
    body: `Body ${id}`,
    created_at: new Date(Date.now() - 60_000).toISOString(),
    ...overrides,
  };
}

async function mount(root: Root, container: HTMLElement, ui: ReactNode) {
  await act(async () => {
    root.render(ui);
    await Promise.resolve();
  });
  return container;
}

function wrap(ui: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>
  );
}

// ---------------------------------------------------------------------------
describe("Notifications page", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    mocks.listPages = [];
    mocks.hasNextPage = false;
    mocks.fetchNextPage.mockReset();
    mocks.markReadMutate.mockReset();
    mocks.dismissMutate.mockReset();
    mocks.unreadCount = 0;
    mocks.listFilters = {};
    mocks.userRole = "user";

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  // -------------------------------------------------------------------------
  // Test 1: renders rows from two pages; unread row gets data-unread="true"
  // -------------------------------------------------------------------------
  it("renders rows with titles/bodies from two pages; unread row gets data-unread", async () => {
    mocks.listPages = [
      { items: [makeNotification(1), makeNotification(2, { read_at: new Date().toISOString() })] },
      { items: [makeNotification(3)] },
    ];

    const el = await mount(root, container, wrap(<Notifications />));

    expect(el.textContent).toContain("Title 1");
    expect(el.textContent).toContain("Body 1");
    expect(el.textContent).toContain("Title 2");
    expect(el.textContent).toContain("Title 3");

    const unreadRows = el.querySelectorAll('[data-unread="true"]');
    // Rows 1 and 3 are unread; row 2 has read_at set
    expect(unreadRows.length).toBe(2);
  });

  // -------------------------------------------------------------------------
  // Test 2: admin tab hidden for non-admin; shown for admin
  // -------------------------------------------------------------------------
  it("hides Admin tab for non-admin users", async () => {
    mocks.userRole = "user";
    mocks.listPages = [{ items: [] }];

    const el = await mount(root, container, wrap(<Notifications />));

    const buttons = Array.from(el.querySelectorAll("button")).map((b) => b.textContent ?? "");
    expect(buttons.some((t) => t.includes("Admin"))).toBe(false);
  });

  it("shows Admin tab for admin users", async () => {
    mocks.userRole = "admin";
    mocks.listPages = [{ items: [] }];

    const el = await mount(root, container, wrap(<Notifications />));

    const buttons = Array.from(el.querySelectorAll("button")).map((b) => b.textContent ?? "");
    expect(buttons.some((t) => t.includes("Admin"))).toBe(true);
  });

  // -------------------------------------------------------------------------
  // Test 3: switching to Requests tab calls useNotificationsList with {category:"request"}
  // -------------------------------------------------------------------------
  it("switching to Requests tab filters by category:request", async () => {
    mocks.listPages = [{ items: [] }];

    const el = await mount(root, container, wrap(<Notifications />));

    // Default: no category filter
    expect(mocks.listFilters.category).toBeUndefined();

    // Find and click the Requests tab
    const requestsBtn = Array.from(el.querySelectorAll("button")).find((b) =>
      b.textContent?.trim() === "Requests",
    );
    expect(requestsBtn).toBeDefined();

    await act(async () => {
      requestsBtn!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    expect(mocks.listFilters.category).toBe("request");
  });

  // -------------------------------------------------------------------------
  // Test 4: mark-on-view: called once with unread ids; re-render does not repeat
  // -------------------------------------------------------------------------
  it("calls markRead.mutate once with unread ids from page 1; ref guard prevents repeat", async () => {
    const readAt = new Date().toISOString();
    mocks.listPages = [
      {
        items: [
          makeNotification(1), // unread
          makeNotification(2, { read_at: readAt }), // already read
        ],
      },
    ];

    const el = await mount(root, container, wrap(<Notifications />));

    expect(mocks.markReadMutate).toHaveBeenCalledTimes(1);
    expect(mocks.markReadMutate).toHaveBeenCalledWith({ ids: [1] });

    // Force a re-render by toggling a tab back and forth; mutate must NOT be called again
    const allBtn = Array.from(el.querySelectorAll("button")).find((b) =>
      b.textContent?.trim() === "All",
    );
    if (allBtn) {
      await act(async () => {
        allBtn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        await Promise.resolve();
      });
    }

    expect(mocks.markReadMutate).toHaveBeenCalledTimes(1);
  });

  // -------------------------------------------------------------------------
  // Test 5: dismiss button calls dismiss mutate with the row id
  // -------------------------------------------------------------------------
  it("dismiss button calls dismissMutate with the notification id", async () => {
    mocks.listPages = [{ items: [makeNotification(42)] }];

    const el = await mount(root, container, wrap(<Notifications />));

    const dismissBtn = el.querySelector('[aria-label="Dismiss"]');
    expect(dismissBtn).not.toBeNull();

    await act(async () => {
      dismissBtn!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    expect(mocks.dismissMutate).toHaveBeenCalledWith(42);
  });

  // -------------------------------------------------------------------------
  // Test 6: "Load more" visible when hasNextPage; calls fetchNextPage
  // -------------------------------------------------------------------------
  it("shows Load more button when hasNextPage and calls fetchNextPage", async () => {
    mocks.hasNextPage = true;
    mocks.listPages = [{ items: [makeNotification(1)] }];

    const el = await mount(root, container, wrap(<Notifications />));

    const loadMoreBtn = Array.from(el.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("Load more"),
    );
    expect(loadMoreBtn).toBeDefined();

    await act(async () => {
      loadMoreBtn!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
  });
});
