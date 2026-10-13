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

// A signed-in account with a selected profile, so the route gates let the
// navigation through; the session itself is not under test.
vi.mock("@/hooks/useAuth", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useAuth")>("@/hooks/useAuth");
  const auth = {
    user: { id: 1, username: "alex", role: "user" },
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

vi.mock("@/components/Layout", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/pages/SettingsLayout", () => ({ default: () => <Outlet /> }));
vi.mock("@/pages/settings/HomeScreenSettings", () => ({
  default: () => <div>Home screen settings</div>,
}));

import App from "@/App";

describe("App routes", () => {
  beforeEach(() => {
    appRouter = null;
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
