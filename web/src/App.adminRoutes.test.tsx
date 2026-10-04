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

// A signed-in admin, so the admin gate lets the navigation through; the
// session itself is not under test.
vi.mock("@/hooks/useAuth", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useAuth")>("@/hooks/useAuth");
  const auth = {
    user: { id: 1, username: "alex", role: "admin" },
    profile: null,
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
vi.mock("@/pages/AdminSections", () => ({ default: () => <div>Home rows page</div> }));

import App from "@/App";

describe("App admin Home rows routes", () => {
  beforeEach(() => {
    appRouter = null;
  });

  it("serves the Home rows page at /admin/home-rows", async () => {
    initialEntry = "/admin/home-rows";

    render(<App />);

    expect(await screen.findByText("Home rows page")).toBeInTheDocument();
    expect(appRouter?.state.location.pathname).toBe("/admin/home-rows");
  });

  it("sends the old /admin/sections links to Home rows, keeping the query", async () => {
    initialEntry = "/admin/sections?page=page-2#rows";

    render(<App />);

    expect(await screen.findByText("Home rows page")).toBeInTheDocument();
    expect(appRouter?.state.location).toMatchObject({
      pathname: "/admin/home-rows",
      search: "?page=page-2",
      hash: "#rows",
    });
    // Replaced, not pushed: Back must not return to the old URL and bounce again.
    expect(appRouter?.state.historyAction).toBe("REPLACE");
  });
});
