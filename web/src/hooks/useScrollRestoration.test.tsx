import { act, render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useScrollRestoration } from "./useScrollRestoration";

function Page() {
  useScrollRestoration();
  return null;
}

function renderRouter() {
  const router = createMemoryRouter([{ path: "/item/:id", element: <Page /> }], {
    initialEntries: ["/item/a"],
  });
  render(<RouterProvider router={router} />);
  return router;
}

describe("useScrollRestoration", () => {
  let scrollTo: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    scrollTo = vi.fn();
    vi.stubGlobal("scrollTo", scrollTo);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    { name: "a plain navigation", state: undefined, scrolls: true },
    { name: "a navigation that keeps the scroll", state: { preserveScroll: true }, scrolls: false },
  ])("$name to a new path", async ({ state, scrolls }) => {
    const router = renderRouter();
    scrollTo.mockClear();

    await act(() => router.navigate("/item/b", { state }));

    expect(scrollTo).toHaveBeenCalledTimes(scrolls ? 1 : 0);
  });
});
