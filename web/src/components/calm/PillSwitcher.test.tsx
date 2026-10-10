import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PillSwitcher } from "./PillSwitcher";

afterEach(cleanup);

const OPTIONS = [
  { value: "all", label: "All libraries" },
  { value: "7", label: "Movies", separated: true },
  { value: "9", label: "Kids" },
];

describe("PillSwitcher", () => {
  it("presses the current pill and reports a different one", async () => {
    const onChange = vi.fn();
    render(
      <PillSwitcher
        label="Library"
        options={OPTIONS}
        value="7"
        onChange={onChange}
        summary="12 collections"
      />,
    );
    expect(screen.getByRole("group", { name: "Library" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Movies" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("12 collections")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Movies" }));
    expect(onChange).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Kids" }));
    expect(onChange).toHaveBeenCalledWith("9");
  });

  it("can't be used while disabled", () => {
    render(
      <PillSwitcher label="Library" options={OPTIONS} value="all" onChange={vi.fn()} disabled />,
    );
    for (const option of OPTIONS)
      expect(screen.getByRole("button", { name: option.label })).toBeDisabled();
  });

  it("shows a pill's count after its name and can turn one pill off", async () => {
    const onChange = vi.fn();
    render(
      <PillSwitcher
        label="Library"
        options={[
          { value: "all", label: "All libraries", disabled: true },
          { value: "7", label: "Movies", count: 14 },
        ]}
        value="7"
        onChange={onChange}
      />,
    );
    expect(screen.getByRole("button", { name: "Movies 14" })).toBeEnabled();
    const all = screen.getByRole("button", { name: "All libraries" });
    expect(all).toBeDisabled();
    await userEvent.click(all);
    expect(onChange).not.toHaveBeenCalled();
  });
});

const options = Array.from({ length: 12 }, (_, index) => ({
  value: `page-${index}`,
  label: `Page ${index}`,
}));

/** jsdom has no layout: give the strip a 300px window onto 1200px of pills. */
function stubLayout(scrollLeft = 0) {
  const sizes = { clientWidth: 300, scrollWidth: 1200 };
  for (const [key, value] of Object.entries(sizes)) {
    vi.spyOn(HTMLElement.prototype, key as keyof typeof sizes, "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.getAttribute("role") === "group" ? value : 0;
    });
  }
  let left = scrollLeft;
  vi.spyOn(HTMLElement.prototype, "scrollLeft", "get").mockImplementation(() => left);
  vi.spyOn(HTMLElement.prototype, "scrollLeft", "set").mockImplementation((value: number) => {
    left = Math.max(0, Math.min(value, 900));
  });
  return { left: () => left };
}

const originalScrollBy = HTMLElement.prototype.scrollBy;

afterEach(() => {
  vi.restoreAllMocks();
  HTMLElement.prototype.scrollBy = originalScrollBy;
});

describe("PillSwitcher scrolling", () => {
  it("hides the scrollbar and fades only the edge with more past it", () => {
    stubLayout();
    render(<PillSwitcher label="Page" options={options} value="page-0" onChange={() => {}} />);

    const group = screen.getByRole("group", { name: "Page" });
    expect(group).toHaveClass("[scrollbar-width:none]", "[&::-webkit-scrollbar]:hidden");
    expect(group.className).toContain(
      "[mask-image:linear-gradient(to_right,black_calc(100%_-_var(--strip-fade,2rem))",
    );
    expect(group.className).not.toContain("transparent_var(--strip-hold,0px),black");
  });

  it("pages the strip from the chevron", () => {
    stubLayout();
    const scrollBy = vi.fn();
    HTMLElement.prototype.scrollBy = scrollBy;
    const { container } = render(
      <PillSwitcher label="Page" options={options} value="page-0" onChange={() => {}} />,
    );

    // Only toward the edge with more. CSS shows it for a mouse only, and it stays
    // out of the tab order and the accessibility tree: Tab reaches every pill.
    const chevrons = container.querySelectorAll("button[aria-hidden]");
    expect(chevrons).toHaveLength(1);
    expect(chevrons[0]).toHaveAttribute("tabindex", "-1");
    fireEvent.click(chevrons[0]!);
    expect(scrollBy).toHaveBeenCalledWith(expect.objectContaining({ left: 225 }));
  });

  it("scrolls the pressed pill into view", () => {
    const strip = stubLayout();
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
      this: HTMLElement,
    ) {
      const pressed = this.getAttribute("aria-pressed") === "true";
      // The pressed pill sits 1000px into the content; the strip starts at 0.
      const left = pressed ? 1000 - strip.left() : 0;
      return { left, width: pressed ? 80 : 300 } as DOMRect;
    });
    render(<PillSwitcher label="Page" options={options} value="page-11" onChange={() => {}} />);

    // 1000 + 80 - 300 + the 64px kept clear of the fade, clamped to the end.
    expect(strip.left()).toBe(844);
  });
});
