// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import { renderLogin } from "./loginPage.test-support";
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    loading: false,
    setupLoading: false,
    setupRequired: false,
    user: null,
    providers: [
      { id: "oauth-fixture", installation_id: 3, mode: "oauth", display_name: "Fixture provider" },
    ],
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Sign in" }),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: vi.fn(async () => ({ enabled: false })),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
afterEach(cleanup);
it("starts provider login with a top-level GET to v2 and preserves the encoded local destination", () => {
  renderLogin("/login?redirect=%2Fme%3Ftab%3Dsettings");
  // Not a form post: the CSP's form-action 'self' would block the redirect to the provider.
  const link = screen.getByRole("link", { name: "Fixture provider" });
  expect(link.closest("form")).toBeNull();
  expect(link.getAttribute("href")).toBe("/api/v2/auth/oauth/3/start?next=%2Fme%3Ftab%3Dsettings");
});
