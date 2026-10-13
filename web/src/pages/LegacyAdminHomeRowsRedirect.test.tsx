import { cleanup, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it } from "vitest";

import LegacyAdminHomeRowsRedirect from "./LegacyAdminHomeRowsRedirect";

afterEach(cleanup);

function renderAt(entry: string) {
  const router = createMemoryRouter(
    [
      { path: "/admin", element: <h1>Admin dashboard</h1> },
      { path: "/admin/home-rows", element: <LegacyAdminHomeRowsRedirect /> },
      { path: "/admin/sections", element: <h1>Sections</h1> },
    ],
    { initialEntries: ["/admin", entry], initialIndex: 1 },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe("LegacyAdminHomeRowsRedirect", () => {
  it("sends Home rows links to Sections", async () => {
    const router = renderAt("/admin/home-rows");

    expect(await screen.findByRole("heading", { name: "Sections" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/sections");
  });

  it("keeps the query string and hash", async () => {
    const router = renderAt("/admin/home-rows?page=7#row-3");

    await screen.findByRole("heading", { name: "Sections" });
    expect(router.state.location.search).toBe("?page=7");
    expect(router.state.location.hash).toBe("#row-3");
  });

  it("replaces the old address so Back skips it", async () => {
    const router = renderAt("/admin/home-rows?page=home");

    await screen.findByRole("heading", { name: "Sections" });
    expect(router.state.historyAction).toBe("REPLACE");

    await router.navigate(-1);
    expect(await screen.findByRole("heading", { name: "Admin dashboard" })).toBeInTheDocument();
  });
});
