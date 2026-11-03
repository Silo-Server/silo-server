import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { Outlet, type createMemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

let initialEntry = "/";
let appRouter: ReturnType<typeof createMemoryRouter> | null = null;
const session = vi.hoisted(() => ({ role: "user" as "user" | "admin" }));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    // App builds a data router from the real history; start it at the entry
    // under test instead.
    createBrowserRouter: ((routes: Parameters<typeof actual.createMemoryRouter>[0]) => {
      appRouter = actual.createMemoryRouter(routes, { initialEntries: [initialEntry] });
      return appRouter;
    }) as typeof actual.createBrowserRouter,
  };
});

// A signed-in account (a regular user or an admin, per test) with a selected
// profile, so the route gates let the navigation through; the session itself
// is not under test.
vi.mock("@/hooks/useAuth", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useAuth")>("@/hooks/useAuth");
  const authFor = (role: string) => ({
    user: { id: 1, username: "alex", role },
    profile: { id: "profile-1", name: "Alex" },
    loading: false,
    setupLoading: false,
    setupRequired: false,
    pendingPasswordChange: false,
    isImpersonating: false,
  });
  const auths = { user: authFor("user"), admin: authFor("admin") };
  return {
    ...actual,
    AuthProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
    useAuth: () => auths[session.role],
    useOptionalAuth: () => auths[session.role],
  };
});
vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => session.role === "admin",
}));

vi.mock("@/components/Layout", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/pages/SettingsLayout", () => ({ default: () => <Outlet /> }));
vi.mock("@/pages/settings/HomeScreenSettings", () => ({
  default: () => <div>Home screen settings</div>,
}));
vi.mock("@/components/AdminLayout", () => ({ default: () => <Outlet /> }));
vi.mock("@/pages/AdminHomeRows", () => ({ default: () => <div>Sections page</div> }));

import App from "@/App";

describe("App routes", () => {
  beforeEach(() => {
    appRouter = null;
    session.role = "user";
  });

  it("redirects the retired profile home editor to Settings > Home Screen", async () => {
    initialEntry = "/profile/customize-home";

    render(<App />);

    expect(await screen.findByText("Home screen settings")).toBeInTheDocument();
    expect(appRouter?.state.location.pathname).toBe("/settings/home-screen");
    // Replaced, not pushed: Back must not return to the old URL and bounce again.
    expect(appRouter?.state.historyAction).toBe("REPLACE");
  });
});

describe("App admin Home rows routes", () => {
  beforeEach(() => {
    appRouter = null;
    session.role = "admin";
  });

  it("sends /admin/home-rows links to Sections, keeping the query", async () => {
    initialEntry = "/admin/home-rows?page=page-2#rows";

    render(<App />);

    expect(await screen.findByText("Sections page")).toBeInTheDocument();
    expect(appRouter?.state.location).toMatchObject({
      pathname: "/admin/sections",
      search: "?page=page-2",
      hash: "#rows",
    });
    // Replaced, not pushed: Back must not return to the old URL and bounce again.
    expect(appRouter?.state.historyAction).toBe("REPLACE");
  });
});
