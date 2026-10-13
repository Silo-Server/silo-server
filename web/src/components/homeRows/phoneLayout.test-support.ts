import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, vi } from "vitest";

/** A phone: under 1024px (docked bar, picker sheet) and under 640px. */
export function stubPhone() {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: query === "(max-width: 1023px)" || query === "(max-width: 639px)",
        media: query,
        addEventListener() {},
        removeEventListener() {},
      }) as unknown as MediaQueryList,
  );
}

/**
 * On a phone the page's buttons dock at the bottom, the page pills scroll
 * inside the page's width, and Add row lists one card per line.
 *
 * jsdom can't measure layout, so the width checks read the classes that do
 * the work; the real check is a walk at 390px in a browser.
 */
export async function expectPhoneLayout(pageName: string) {
  const dock = screen.getByRole("region", { name: "Page actions" });
  expect(within(dock).getByRole("button", { name: "More" })).toBeInTheDocument();
  const pills = screen.getByRole("group", { name: "Page" });
  expect(pills).toHaveClass("overflow-x-auto");
  expect(pills.parentElement).toHaveClass("max-sm:basis-full");
  // A column that can't shrink below the pills would push the list past the screen's edge.
  expect(pills.parentElement?.parentElement?.parentElement).toHaveClass(
    "grid-cols-[minmax(0,1fr)]",
  );

  await userEvent.click(within(dock).getByRole("button", { name: "Add row" }));
  const picker = await screen.findByRole("dialog", { name: `Add a row to ${pageName}` });
  expect(within(picker).getByRole("tablist")).toHaveAttribute("aria-orientation", "horizontal");
  const variants = within(picker).getByText("24 hours, 7 days or 30 days");
  expect(variants.closest("section")?.querySelector(".grid-cols-2")).toBeNull();
  expect(
    within(picker).queryByRole("button", { name: "Trending on this server, 7 days" }),
  ).toBeNull();
}
