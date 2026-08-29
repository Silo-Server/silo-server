import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation, useNavigationType } from "react-router";
import { afterEach, describe, expect, it } from "vitest";

import { resetNavigationHistory } from "@/lib/navigationHistory";
import DetailBreadcrumb from "./DetailBreadcrumb";

function RouteOutput() {
  const location = useLocation();
  return (
    <>
      <output aria-label="location">{location.pathname}</output>
      <output aria-label="navigation type">{useNavigationType()}</output>
    </>
  );
}

const segments = [
  { label: "The Series", href: "/item/series" },
  { label: "Season 1", href: "/item/season" },
  { label: "Episode 1" },
];

afterEach(() => {
  resetNavigationHistory();
  window.history.replaceState(null, "");
  delete document.documentElement.dataset.navigationDirection;
});

describe("DetailBreadcrumb", () => {
  it("renders every crumb, linking all but the current page", () => {
    render(
      <MemoryRouter initialEntries={["/item/episode"]}>
        <DetailBreadcrumb segments={segments} />
      </MemoryRouter>,
    );

    expect(screen.getByRole("link", { name: "The Series" })).toHaveAttribute(
      "href",
      "/item/series",
    );
    expect(screen.getByRole("link", { name: "Season 1" })).toHaveAttribute("href", "/item/season");
    expect(screen.queryByRole("link", { name: "Episode 1" })).not.toBeInTheDocument();
    expect(screen.getByText("Episode 1")).toHaveAttribute("aria-current", "page");
  });

  it("renders nothing without segments", () => {
    const { container } = render(
      <MemoryRouter>
        <DetailBreadcrumb segments={[]} />
      </MemoryRouter>,
    );

    expect(container).toBeEmptyDOMElement();
  });

  it("navigates backwards to a crumb this page load never recorded", () => {
    // Landing straight on the episode — a shared link, or a reload — leaves the
    // map empty, so the crumb pushes. The motion is still backwards.
    render(
      <MemoryRouter initialEntries={["/item/episode"]}>
        <DetailBreadcrumb segments={segments} />
        <RouteOutput />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Season 1" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/season");
    expect(screen.getByRole("status", { name: "navigation type" })).toHaveTextContent("PUSH");
    expect(document.documentElement.dataset.navigationDirection).toBe("back");
  });
});
