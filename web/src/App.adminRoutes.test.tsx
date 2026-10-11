import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { Outlet, type createMemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

let initialEntry = "/";
let appRouter: ReturnType<typeof createMemoryRouter> | null = null;

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

// A signed-in admin on a chosen profile, so the admin gate lets the
// navigation through; the session itself is not under test.
vi.mock("@/hooks/useAuth", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useAuth")>("@/hooks/useAuth");
  const auth = {
    user: { id: 1, username: "alex", role: "admin" },
    profile: { id: "profile-1", name: "Alex" },
    loading: false,
    setupLoading: false,
    setupRequired: false,
    pendingPasswordChange: false,
    isImpersonating: false,
  };
  return {
    ...actual,
    AuthProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
    useAuth: () => auth,
    useOptionalAuth: () => auth,
  };
});
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => true }));

vi.mock("@/components/AdminLayout", () => ({ default: () => <Outlet /> }));
vi.mock("@/pages/AdminHomeRows", () => ({ default: () => <div>Sections page</div> }));

import App from "@/App";

describe("App admin Home rows routes", () => {
  beforeEach(() => {
    appRouter = null;
  });

  it("serves the Sections page at /admin/sections", async () => {
    initialEntry = "/admin/sections";

    render(<App />);

    expect(await screen.findByText("Sections page")).toBeInTheDocument();
    expect(appRouter?.state.location.pathname).toBe("/admin/sections");
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
