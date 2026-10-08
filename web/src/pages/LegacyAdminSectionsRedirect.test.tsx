import { cleanup, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it } from "vitest";

import LegacyAdminSectionsRedirect from "./LegacyAdminSectionsRedirect";

afterEach(cleanup);

function renderAt(entry: string) {
  const router = createMemoryRouter(
    [
      { path: "/admin", element: <h1>Admin dashboard</h1> },
      { path: "/admin/sections", element: <LegacyAdminSectionsRedirect /> },
      { path: "/admin/home-rows", element: <h1>Home rows</h1> },
    ],
    { initialEntries: ["/admin", entry], initialIndex: 1 },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe("LegacyAdminSectionsRedirect", () => {
  it("sends old Sections bookmarks to Home rows", async () => {
    const router = renderAt("/admin/sections");

    expect(await screen.findByRole("heading", { name: "Home rows" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/home-rows");
  });

  it("keeps the query string and hash", async () => {
    const router = renderAt("/admin/sections?page=7#row-3");

    await screen.findByRole("heading", { name: "Home rows" });
    expect(router.state.location.search).toBe("?page=7");
    expect(router.state.location.hash).toBe("#row-3");
  });

  it("replaces the old address so Back skips it", async () => {
    const router = renderAt("/admin/sections?page=home");

    await screen.findByRole("heading", { name: "Home rows" });
    expect(router.state.historyAction).toBe("REPLACE");

    await router.navigate(-1);
    expect(await screen.findByRole("heading", { name: "Admin dashboard" })).toBeInTheDocument();
  });
});
