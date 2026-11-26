import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Link, MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { insightRefreshProps } from "./insightsHelpers";

function renderRegion(refreshing: boolean, onButton = () => {}) {
  return render(
    <MemoryRouter initialEntries={["/insights"]}>
      <Routes>
        <Route
          path="/insights"
          element={
            <div {...insightRefreshProps(refreshing)}>
              <Link to="/history">History</Link>
              <button type="button" onClick={onButton}>
                Sort
              </button>
            </div>
          }
        />
        <Route path="/history" element={<p>History page</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("insightRefreshProps", () => {
  afterEach(cleanup);

  // Enter on a focused link dispatches a click, so this is the keyboard path
  // as well as the mouse one; pointer-events alone would not stop it.
  it("keeps a link in a refreshing region from navigating, and leaves buttons live", () => {
    const onButton = vi.fn();
    renderRegion(true, onButton);

    fireEvent.click(screen.getByRole("link", { name: "History" }));
    fireEvent.click(screen.getByRole("button", { name: "Sort" }));

    expect(screen.queryByText("History page")).toBeNull();
    expect(onButton).toHaveBeenCalledOnce();
  });

  it("lets the same link navigate once the region is current", () => {
    renderRegion(false);

    fireEvent.click(screen.getByRole("link", { name: "History" }));

    expect(screen.getByText("History page")).toBeTruthy();
  });
});
