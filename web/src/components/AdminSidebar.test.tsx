import type { BuildInfo } from "@/hooks/queries/admin/system";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
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

function renderSidebar(embedded = false) {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={["/admin"]}>
      <AdminSidebar embedded={embedded} />
    </MemoryRouter>,
  );
}

describe("AdminSidebar", () => {
  beforeEach(() => {
    mockUsePolicyCapability.mockReturnValue({
      data: {
        enabled: true,
        editor_available: true,
        decision_types: [],
        generation: 1,
      },
    });
  });

  it("shows how many requests need approval on the Requests entry", () => {
    mockUseAdminRequestCounts.mockReturnValueOnce({ data: { needs_approval: 3 } });
    const markup = renderSidebar();
    const requestsLink = markup.match(/<a[^>]*href="\/admin\/requests"[^>]*>.*?<\/a>/)?.[0];

    expect(requestsLink).toContain('<span aria-hidden="true">3</span>');
    expect(requestsLink).toContain(", 3 need approval");
  });

  it("leaves the Requests entry plain when nothing needs approval", () => {
    const markup = renderSidebar();
    const requestsLink = markup.match(/<a[^>]*href="\/admin\/requests"[^>]*>.*?<\/a>/)?.[0];

    expect(requestsLink).toBeDefined();
    expect(requestsLink).not.toContain("need approval");
  });

  it("keeps settings as one sidebar destination", () => {
    const markup = renderSidebar();
    const settingsLinks = markup.match(/href="\/admin\/settings[^"]*"/g) ?? [];

    expect(settingsLinks).toEqual(['href="/admin/settings"']);
    expect(markup).not.toContain("/admin/settings?tab=");
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

    const markup = renderSidebar();

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
