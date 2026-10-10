import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { CalmPage } from "./CalmPage";

afterEach(cleanup);

describe("CalmPage", () => {
  it("is a page's own h1 with its buttons in the header", () => {
    render(
      <CalmPage
        heading="page"
        title="Collections"
        subtitle="What viewers see on each library's Collections tab."
        actions={<button type="button">New collection</button>}
      >
        <p>List</p>
      </CalmPage>,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Collections" })).toBeInTheDocument();
    expect(screen.getByRole("banner")).toContainElement(
      screen.getByRole("button", { name: "New collection" }),
    );
    expect(screen.getByRole("banner").parentElement).toHaveClass("max-w-[1000px]");
    expect(screen.getByRole("banner").parentElement).not.toHaveClass("pb-24");
  });

  it("is an h2 under a parent page and leaves room for a docked bar", () => {
    render(
      <CalmPage heading="section" title="Home Screen" subtitle="Your rows." padBottom>
        <p>List</p>
      </CalmPage>,
    );
    expect(screen.getByRole("heading", { level: 2, name: "Home Screen" })).toBeInTheDocument();
    expect(screen.getByRole("banner").parentElement).toHaveClass("pb-24");
  });
});
