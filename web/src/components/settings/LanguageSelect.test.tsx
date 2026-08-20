import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { LanguageSelect } from "@/components/settings/LanguageSelect";
import { SelectItem } from "@/components/ui/select";

// Radix Select reads element sizes via ResizeObserver, which jsdom does not
// provide, and opens through pointer capture, which jsdom also lacks.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const OPTIONS = [
  { value: "en", label: "English" },
  { value: "fr", label: "French" },
];

function renderSelect(props: Partial<Parameters<typeof LanguageSelect>[0]> = {}) {
  const onValueChange = vi.fn();
  render(
    <LanguageSelect
      aria-label="Spoken language"
      value="none"
      options={OPTIONS}
      onValueChange={onValueChange}
      {...props}
    >
      <SelectItem value="none">No preference</SelectItem>
    </LanguageSelect>,
  );
  return { onValueChange };
}

describe("LanguageSelect", () => {
  it("commits a typed tag through the Other entry", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSelect();

    await user.click(screen.getByRole("combobox", { name: "Spoken language" }));
    await user.click(screen.getByRole("option", { name: "Other…" }));

    const input = screen.getByRole("textbox", { name: "Language code" });
    await user.type(input, "is");
    // The preview names the language before anything is saved.
    expect(screen.getByText("Icelandic")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Use" }));
    expect(onValueChange).toHaveBeenCalledWith("is");
    // Committing closes the free-entry row.
    expect(screen.queryByRole("textbox", { name: "Language code" })).not.toBeInTheDocument();
  });

  it("refuses to commit an invalid tag and explains why", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSelect();

    await user.click(screen.getByRole("combobox", { name: "Spoken language" }));
    await user.click(screen.getByRole("option", { name: "Other…" }));

    const input = screen.getByRole("textbox", { name: "Language code" });
    await user.type(input, "not a language{Enter}");

    expect(onValueChange).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("Not a valid language tag");
    expect(screen.getByRole("button", { name: "Use" })).toBeDisabled();
  });

  it("keeps the current selection when Other is dismissed", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSelect({ value: "en" });

    await user.click(screen.getByRole("combobox", { name: "Spoken language" }));
    await user.click(screen.getByRole("option", { name: "Other…" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onValueChange).not.toHaveBeenCalled();
    expect(screen.getByRole("combobox", { name: "Spoken language" })).toHaveTextContent("English");
  });

  it("hides the Other entry when the value is constrained", async () => {
    const user = userEvent.setup();
    renderSelect({ allowOther: false });

    await user.click(screen.getByRole("combobox", { name: "Spoken language" }));
    expect(screen.queryByRole("option", { name: "Other…" })).not.toBeInTheDocument();
  });
});
