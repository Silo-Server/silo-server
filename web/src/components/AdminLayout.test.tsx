import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAdminServerStatus: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerStatus: () => mocks.useAdminServerStatus(),
}));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: undefined }),
}));
vi.mock("@/hooks/queries/admin/policy", () => ({
  usePolicyCapability: () => ({ data: undefined }),
}));
vi.mock("@/components/AdminSidebar", () => ({ default: () => null }));
vi.mock("@/components/AdminSectionCommandDialog", () => ({
  AdminSectionCommandDialog: () => null,
}));
vi.mock("@/components/ServerActivity", () => ({ default: () => null }));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ isBackgroundBarVisible: false }),
}));
vi.mock("@/pages/audiobooks/player/audiobookPlaybackContext", () => ({
  useAudiobookPlaybackController: () => null,
}));

import AdminLayout from "./AdminLayout";

// The dashboard and the users page stand in for "any admin page that is not
// settings" — the shell is the only thing that renders the restart prompt, so
// both must show it.
function renderAdmin(initialPath = "/admin") {
  const router = createMemoryRouter(
    [
      {
        path: "/admin",
        element: <AdminLayout />,
        children: [
          { index: true, element: <h1>Admin dashboard</h1> },
          { path: "users", element: <h1>Admin users</h1> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  );

  return { router, ...render(<RouterProvider router={router} />) };
}

beforeEach(() => {
  mocks.useAdminServerStatus.mockReturnValue({ data: { restart_required: true } });
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === "(min-width: 64rem)",
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("AdminLayout search shortcut hint", () => {
  function stubUserAgent(value: string) {
    vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(value);
  }

  // The dialog opens on Cmd or Ctrl, so the advertised hint has to name the key
  // this keyboard actually has — a hardcoded ⌘ is a dead instruction on Windows
  // and Linux, which is most self-hosters.
  it("names Ctrl off Apple platforms", () => {
    stubUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36");
    renderAdmin();

    const [search] = screen.getAllByRole("button", { name: "Search admin sections" });
    expect(search).toHaveAttribute("title", "Search admin sections (Ctrl K)");
    expect(screen.getByText("Ctrl K")).toBeInTheDocument();
    expect(screen.queryByText(/⌘/)).not.toBeInTheDocument();
  });

  it("names the command glyph on Apple platforms", () => {
    stubUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15");
    renderAdmin();

    const [search] = screen.getAllByRole("button", { name: "Search admin sections" });
    expect(search).toHaveAttribute("title", "Search admin sections (⌘ K)");
    expect(screen.getByText("⌘ K")).toBeInTheDocument();
  });
});

describe("AdminLayout restart banner", () => {
  it("stays quiet while no restart is owed", () => {
    mocks.useAdminServerStatus.mockReturnValue({ data: { restart_required: false } });
    renderAdmin();

    expect(screen.getByRole("heading", { name: "Admin dashboard" })).toBeInTheDocument();
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });

  it("prompts on a page outside settings, above the routed page", () => {
    renderAdmin("/admin/users");

    const banner = screen.getByRole("status");
    const page = screen.getByRole("heading", { name: "Admin users" });

    expect(banner).toHaveTextContent("Restart required");
    // Node.DOCUMENT_POSITION_FOLLOWING: the page comes after the banner.
    expect(banner.compareDocumentPosition(page) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps a dismissal across admin navigation", async () => {
    const { router } = renderAdmin();

    await userEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();

    // The shell owns the banner, so moving between admin pages neither
    // resurrects the prompt nor loses the admin's "Later".
    await act(async () => {
      await router.navigate("/admin/users");
    });

    expect(screen.getByRole("heading", { name: "Admin users" })).toBeInTheDocument();
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });
});
