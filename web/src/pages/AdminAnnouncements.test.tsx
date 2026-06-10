import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Announcement } from "@/api/types";

// Radix ScrollArea requires ResizeObserver — polyfill for jsdom
global.ResizeObserver = class ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// ─── Mock data ───────────────────────────────────────────────────────────────

const ANNOUNCEMENTS: Announcement[] = [
  {
    id: 1,
    title: "Welcome",
    body: "Hello everyone",
    audience: { all: true },
    created_at: "2026-01-01T00:00:00Z",
  },
  {
    id: 2,
    title: "User notice",
    body: "For some users",
    audience: { user_ids: [10, 20] },
    created_at: "2026-01-02T00:00:00Z",
  },
  {
    id: 3,
    title: "Library alert",
    body: "For Movies lib",
    audience: { library_ids: [5] },
    created_at: "2026-01-03T00:00:00Z",
  },
];

// ─── Hook mocks ───────────────────────────────────────────────────────────────

const createAnnouncementMutateMock = vi.fn();
const deleteAnnouncementMutateMock = vi.fn();
const useAnnouncementsMock = vi.fn();

vi.mock("@/hooks/queries/notifications", () => ({
  useAnnouncements: () => useAnnouncementsMock(),
  useCreateAnnouncement: () => ({
    mutate: createAnnouncementMutateMock,
    isPending: false,
  }),
  useDeleteAnnouncement: () => ({
    mutate: deleteAnnouncementMutateMock,
    isPending: false,
  }),
}));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({
    data: [
      { id: 10, username: "alice", email: "alice@example.com" },
      { id: 20, username: "bob", email: "bob@example.com" },
    ],
  }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 5, name: "Movies" },
      { id: 6, name: "TV Shows" },
    ],
  }),
}));

// ─── Helpers ─────────────────────────────────────────────────────────────────

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

// We import AdminAnnouncements after mocks are declared (vi.mock is hoisted)
import AdminAnnouncements from "./AdminAnnouncements";

function renderPage() {
  const qc = makeQueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <AdminAnnouncements />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe("AdminAnnouncements", () => {
  beforeEach(() => {
    createAnnouncementMutateMock.mockReset();
    deleteAnnouncementMutateMock.mockReset();
    useAnnouncementsMock.mockReturnValue({
      data: ANNOUNCEMENTS,
      isLoading: false,
    });
  });

  // ── 1. Table renders rows with audience summaries ──────────────────────────

  it("renders rows with audience summaries: Everyone, N user(s), Libraries: names", () => {
    renderPage();

    expect(screen.getByText("Welcome")).toBeInTheDocument();
    expect(screen.getByText("User notice")).toBeInTheDocument();
    expect(screen.getByText("Library alert")).toBeInTheDocument();

    expect(screen.getByText("Everyone")).toBeInTheDocument();
    expect(screen.getByText("2 user(s)")).toBeInTheDocument();
    expect(screen.getByText("Libraries: Movies")).toBeInTheDocument();
  });

  // ── 2a. Submit disabled with empty title ───────────────────────────────────

  it("Publish button is disabled when title is empty", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: /new announcement/i }));

    const publishBtn = screen.getByRole("button", { name: /publish/i });
    expect(publishBtn).toBeDisabled();
  });

  // ── 2b. Submit disabled when Specific users selected but none checked ───────

  it("Publish button is disabled when Specific users mode has no users checked", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: /new announcement/i }));

    // Type a title so that title validation passes
    await user.type(screen.getByLabelText(/title/i), "Test announcement");

    // Select "Specific users" radio
    await user.click(screen.getByRole("radio", { name: /specific users/i }));

    // No users are checked → submit should still be disabled
    const publishBtn = screen.getByRole("button", { name: /publish/i });
    expect(publishBtn).toBeDisabled();
  });

  // ── 3. Submitting Everyone announcement calls create with correct payload ───

  it("submitting an Everyone announcement calls create mutate with audience:{all:true} and no expires_at key", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: /new announcement/i }));
    await user.type(screen.getByLabelText(/title/i), "Hello");
    await user.type(screen.getByLabelText(/body/i), "World");
    // audience mode is Everyone by default

    await user.click(screen.getByRole("button", { name: /publish/i }));

    expect(createAnnouncementMutateMock).toHaveBeenCalledOnce();
    const [payload] = createAnnouncementMutateMock.mock.calls[0] as [Record<string, unknown>, ...unknown[]];
    expect(payload.title).toBe("Hello");
    expect(payload.body).toBe("World");
    expect(payload.audience).toEqual({ all: true });
    expect(Object.keys(payload)).not.toContain("expires_at");
  });

  // ── 4. Libraries mode → audience:{library_ids:[id]} only ──────────────────

  it("selecting Libraries mode and one library produces audience:{library_ids:[id]} only", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: /new announcement/i }));
    await user.type(screen.getByLabelText(/title/i), "Lib notice");

    await user.click(screen.getByRole("radio", { name: /libraries/i }));

    // Check "Movies" checkbox
    await user.click(screen.getByRole("checkbox", { name: /movies/i }));

    await user.click(screen.getByRole("button", { name: /publish/i }));

    expect(createAnnouncementMutateMock).toHaveBeenCalledOnce();
    const [payload] = createAnnouncementMutateMock.mock.calls[0] as [Record<string, unknown>, ...unknown[]];
    const audience = payload.audience as Record<string, unknown>;
    expect(audience).toEqual({ library_ids: [5] });
    expect(Object.keys(audience)).not.toContain("all");
    expect(Object.keys(audience)).not.toContain("user_ids");
  });

  // ── 5. Delete confirm calls delete mutate with the row id ─────────────────

  it("clicking delete then confirming calls useDeleteAnnouncement.mutate with the announcement id", async () => {
    const user = userEvent.setup();
    renderPage();

    // Click delete button on the first row (id=1 "Welcome")
    const deleteBtn = screen.getByRole("button", { name: /delete announcement "Welcome"/i });
    await user.click(deleteBtn);

    // Confirm dialog should appear — click the destructive confirm button
    // The ConfirmDialog renders an AlertDialog; find the "Delete" action button
    const confirmBtn = screen.getByRole("button", { name: /^delete$/i });
    await user.click(confirmBtn);

    expect(deleteAnnouncementMutateMock).toHaveBeenCalledWith(1);
  });

  // ── 6. Empty state renders when no announcements ───────────────────────────

  it("shows an empty state message when there are no announcements", () => {
    useAnnouncementsMock.mockReturnValue({ data: [], isLoading: false });

    renderPage();

    expect(screen.getByText(/no announcements yet/i)).toBeInTheDocument();
  });
});
