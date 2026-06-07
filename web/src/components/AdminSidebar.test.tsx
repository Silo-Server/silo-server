import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import AdminSidebar from "./AdminSidebar";
import type { BuildInfo } from "@/hooks/queries/admin/system";

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
  available: true,
};
const mockUseBuildInfo = vi.fn<() => MockBuildInfoResult>(() => ({
  data: defaultBuildInfo,
  isPending: false,
  isError: false,
}));
const mockUseAdminSessions = vi.fn(() => ({ data: [] }));
const mockUseAdminPluginInstallations = vi.fn(() => ({ data: [] }));

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

function renderSidebar() {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={["/admin"]}>
      <AdminSidebar />
    </MemoryRouter>,
  );
}

describe("AdminSidebar", () => {
  it("includes a Sections link in the manage navigation", () => {
    const markup = renderSidebar();

    expect(markup).toContain('href="/admin/sections"');
    expect(markup).toContain(">Sections<");
  });

  it("includes a Maintenance link in the server navigation", () => {
    const markup = renderSidebar();

    expect(markup).toContain('href="/admin/maintenance"');
    expect(markup).toContain(">Maintenance<");
  });

  it("includes a Recommendations link in the server navigation", () => {
    const markup = renderSidebar();

    expect(markup).toContain('href="/admin/recommendations"');
    expect(markup).toContain(">Recommendations<");
  });

  it("includes a Marker History link in the users navigation", () => {
    const markup = renderSidebar();

    expect(markup).toContain('href="/admin/marker-history"');
    expect(markup).toContain(">Marker History<");
  });

  it("renders the build identifier in the footer", () => {
    const markup = renderSidebar();

    expect(markup).toContain(">Build<");
    expect(markup).toContain(">b4c5aae1+dirty<");
  });

  it("renders dev build when build metadata is missing", () => {
    mockUseBuildInfo.mockReturnValueOnce({
      data: {
        ...defaultBuildInfo,
        display: "unavailable",
        revision: "",
        dirty: false,
        vcs_time: "",
        available: false,
      },
      isPending: false,
      isError: false,
    });

    const markup = renderSidebar();

    expect(markup).toContain(">dev build<");
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
