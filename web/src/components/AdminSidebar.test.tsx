import type { SettingsHealthItem } from "@/hooks/admin/useSettingsHealth";
import type { BuildInfo } from "@/hooks/queries/admin/system";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SEARCH_SHORTCUT_LABEL } from "@/lib/keyboardShortcut";
import AdminSidebar from "./AdminSidebar";

interface MockBuildInfoResult {
  data?: BuildInfo;
  isPending: boolean;
  isError: boolean;
}

const mockUseServerBranding = vi.fn(() => ({
  serverName: "Silo",
  loginSubtitle: "Sign in with an existing account.",
}));
const defaultBuildInfo: BuildInfo = {
  display: "b4c5aae1+dirty",
  revision: "b4c5aae18aa653725ac697b29a05eac797576008",
  dirty: true,
  vcs_time: "2026-04-05T22:24:40Z",
  build_number: 411,
  built_at: "2026-08-19T19:45:00Z",
  available: true,
};
const mockUseBuildInfo = vi.fn<() => MockBuildInfoResult>(() => ({
  data: defaultBuildInfo,
  isPending: false,
  isError: false,
}));
const mockUseAdminSessions = vi.fn(() => ({ data: [] }));
const mockUseAdminPluginInstallations = vi.fn(() => ({ data: [] }));
const mockUseAdminRequestCounts = vi.fn<() => { data?: { needs_approval: number } }>(() => ({
  data: { needs_approval: 0 },
}));
const mockUsePolicyCapability = vi.fn(() => ({
  data: {
    enabled: true,
    editor_available: true,
    decision_types: [],
    generation: 1,
  },
}));

vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => mockUseServerBranding(),
}));

vi.mock("@/hooks/queries/admin/system", () => ({
  useBuildInfo: () => mockUseBuildInfo(),
}));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminSessions: () => mockUseAdminSessions(),
}));

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => mockUseAdminPluginInstallations(),
}));

vi.mock("@/hooks/queries/admin/policy", () => ({
  usePolicyCapability: () => mockUsePolicyCapability(),
}));

vi.mock("@/hooks/queries/admin/requests", () => ({
  useAdminRequestCounts: () => mockUseAdminRequestCounts(),
}));

const mockPrefetchSettings = vi.fn();
vi.mock("@/hooks/queries/admin/settings", () => ({
  usePrefetchAdminServerSettings: () => mockPrefetchSettings,
}));

const mockUseSettingsAttention = vi.fn<() => SettingsHealthItem[]>(() => []);
vi.mock("@/hooks/admin/useSettingsHealth", () => ({
  useSettingsAttention: () => mockUseSettingsAttention(),
}));

function renderSidebar(path = "/admin") {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[path]}>
      <AdminSidebar />
    </MemoryRouter>,
  );
}

function renderLiveSidebar(path = "/admin") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AdminSidebar />
    </MemoryRouter>,
  );
}

function stubLocalStorage() {
  const memory = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => memory.get(key) ?? null,
    setItem: (key: string, value: string) => memory.set(key, value),
    removeItem: (key: string) => memory.delete(key),
  });
  return memory;
}

describe("AdminSidebar", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  beforeEach(() => {
    stubLocalStorage();
    mockUseSettingsAttention.mockReturnValue([]);
    mockUsePolicyCapability.mockReturnValue({
      data: {
        enabled: true,
        editor_available: true,
        decision_types: [],
        generation: 1,
      },
    });
  });

  it("shows how many requests need approval on the queue, and on Requests while it is closed", () => {
    mockUseAdminRequestCounts.mockReturnValue({ data: { needs_approval: 3 } });
    const queue = renderSidebar("/admin/requests").match(
      /<a[^>]*href="\/admin\/requests"[^>]*>.*?<\/a>/,
    )?.[0];
    expect(queue).toContain('<span aria-hidden="true">3</span>');
    expect(queue).toContain(", 3 need approval");

    renderLiveSidebar("/admin");
    expect(screen.getByRole("button", { name: /^Requests, 3 need approval/ })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    mockUseAdminRequestCounts.mockReturnValue({ data: { needs_approval: 0 } });
  });

  it("leaves Requests plain when nothing needs approval", () => {
    renderLiveSidebar("/admin");

    expect(screen.getByRole("button", { name: "Requests" })).not.toHaveTextContent("approval");
  });

  it("opens the topic holding the current page and marks the page", () => {
    renderLiveSidebar("/admin/settings/playback");

    expect(screen.getByRole("button", { name: "Playback" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    const playback = screen.getByRole("list", { name: "Playback" });
    expect(within(playback).getByRole("link", { name: "Settings" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(within(playback).getByRole("link", { name: "Settings" })).toHaveAttribute(
      "href",
      "/admin/settings/playback",
    );
    expect(screen.getByRole("button", { name: "Integrations" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  it("opens and closes a topic on click and remembers the choice", () => {
    const view = renderLiveSidebar("/admin");
    const integrations = screen.getByRole("button", { name: "Integrations" });

    fireEvent.click(integrations);
    expect(integrations).toHaveAttribute("aria-expanded", "true");
    expect(
      within(screen.getByRole("list", { name: "Integrations" })).getByRole("link", {
        name: "AI Services",
      }),
    ).toHaveAttribute("href", "/admin/settings/ai");

    view.unmount();
    renderLiveSidebar("/admin/users");
    expect(screen.getByRole("button", { name: "Integrations" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("reopens the current page's topic even after it was closed there", () => {
    renderLiveSidebar("/admin/libraries");
    fireEvent.click(screen.getByRole("button", { name: "Libraries" }));
    expect(screen.getByRole("button", { name: "Libraries" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    cleanup();

    renderLiveSidebar("/admin/collections");
    expect(screen.getByRole("button", { name: "Libraries" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("marks a broken setting on its page and on its closed topic", () => {
    mockUseSettingsAttention.mockReturnValue([
      {
        id: "transcoding",
        label: "Transcoding",
        state: "warn",
        stateText: "Restart pending",
        detail: "Saved changes apply after a restart",
        action: { label: "Fix", page: "playback" },
      },
      {
        id: "email",
        label: "Email",
        state: "off",
        stateText: "Not set up",
        detail: "Invites and resets can't send",
        action: { label: "Set up", page: "notifications" },
      },
    ]);
    renderLiveSidebar("/admin");

    expect(
      screen.getByRole("button", { name: /^Playback, Transcoding restart pending/ }),
    ).toBeInTheDocument();
    // Setup nobody has done yet is the Dashboard's to mention, not a dot.
    expect(screen.getByRole("button", { name: "Integrations" })).toBeInTheDocument();
  });

  it("fetches what a settings page needs once the pointer is on its link", () => {
    mockPrefetchSettings.mockClear();
    renderLiveSidebar("/admin/libraries");
    const libraries = screen.getByRole("list", { name: "Libraries" });

    fireEvent.pointerEnter(within(libraries).getByRole("link", { name: "Collections" }));
    expect(mockPrefetchSettings).not.toHaveBeenCalled();

    fireEvent.pointerEnter(within(libraries).getByRole("link", { name: "Settings" }));
    expect(mockPrefetchSettings).toHaveBeenCalledTimes(1);
  });

  it("puts the admin search at the top of the desktop sidebar", () => {
    const onSearch = vi.fn();
    render(
      <MemoryRouter initialEntries={["/admin"]}>
        <AdminSidebar onSearch={onSearch} />
      </MemoryRouter>,
    );

    const search = screen.getByRole("button", { name: "Search admin sections" });
    expect(search).toHaveTextContent(SEARCH_SHORTCUT_LABEL);
    fireEvent.click(search);
    expect(onSearch).toHaveBeenCalledTimes(1);
  });

  it("leaves search out of the phone drawer, whose header has its own", () => {
    render(
      <MemoryRouter initialEntries={["/admin"]}>
        <AdminSidebar embedded />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Search admin sections" })).not.toBeInTheDocument();
  });

  it("lists home rows as Home Sections under Libraries", () => {
    const markup = renderSidebar("/admin/sections");

    expect(markup).toContain('href="/admin/sections"');
    expect(markup).toContain(">Home Sections<");
    expect(markup).not.toContain('href="/admin/home-rows"');
  });

  it("hides Policy navigation when the editor capability is unavailable", () => {
    mockUsePolicyCapability.mockReturnValueOnce({
      data: {
        enabled: true,
        editor_available: false,
        decision_types: [],
        generation: 1,
      },
    });

    // The topic is open on API Keys, so Policy would be listed beside it.
    const markup = renderSidebar("/admin/api-keys");

    expect(markup).toContain('href="/admin/api-keys"');
    expect(markup).not.toContain('href="/admin/policy"');
    expect(markup).not.toContain(">Policy<");
  });

  it("renders the build identifier in the footer", () => {
    const markup = renderSidebar();

    expect(markup).toContain(">Build<");
    expect(markup).toContain(">411 · b4c5aae1+dirty<");
    expect(markup).toContain('title="Built 2026-08-19T19:45:00Z"');
  });

  it("renders dev build when build metadata is missing", () => {
    mockUseBuildInfo.mockReturnValueOnce({
      data: {
        ...defaultBuildInfo,
        display: "unavailable",
        revision: "",
        dirty: false,
        vcs_time: "",
        build_number: 0,
        built_at: "",
        available: false,
      },
      isPending: false,
      isError: false,
    });

    const markup = renderSidebar();

    expect(markup).toContain(">dev build<");
  });

  it("falls back to the revision for builds without an ordered number", () => {
    const legacyBuildInfo = { ...defaultBuildInfo };
    delete legacyBuildInfo.build_number;
    delete legacyBuildInfo.built_at;
    mockUseBuildInfo.mockReturnValueOnce({
      data: legacyBuildInfo,
      isPending: false,
      isError: false,
    });

    const markup = renderSidebar();

    expect(markup).toContain(">b4c5aae1+dirty<");
  });

  it("renders load failed when the build info query errors", () => {
    mockUseBuildInfo.mockReturnValueOnce({
      data: undefined,
      isPending: false,
      isError: true,
    });

    const markup = renderSidebar();

    expect(markup).toContain(">load failed<");
  });

  it("renders loading while the build info query is pending", () => {
    mockUseBuildInfo.mockReturnValueOnce({
      data: undefined,
      isPending: true,
      isError: false,
    });

    const markup = renderSidebar();

    expect(markup).toContain(">loading...<");
  });
});
