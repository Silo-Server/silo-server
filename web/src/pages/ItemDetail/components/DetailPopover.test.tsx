import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import DetailPopover, { DETAIL_POPOVER_CLOSE_ATTR } from "./DetailPopover";

function renderPopover(onSelect = vi.fn()) {
  render(
    <>
      <DetailPopover trigger={<button type="button">Audio</button>}>
        <button type="button" onClick={onSelect}>
          English
        </button>
        <button type="button">French</button>
        <button type="button" {...{ [DETAIL_POPOVER_CLOSE_ATTR]: "" }}>
          Done
        </button>
      </DetailPopover>
      <button type="button">Outside</button>
    </>,
  );
}

describe("DetailPopover", () => {
  it("links the trigger to a named dialog and moves focus into it", () => {
    renderPopover();
    const trigger = screen.getByRole("button", { name: "Audio" });

    fireEvent.click(trigger);

    const dialog = screen.getByRole("dialog", { name: "Audio" });
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(trigger).toHaveAttribute("aria-controls", dialog.id);
    expect(screen.getByRole("button", { name: "English" })).toHaveFocus();
  });

  it("stays open through a selection and through page scroll", () => {
    const onSelect = vi.fn();
    renderPopover(onSelect);
    const trigger = screen.getByRole("button", { name: "Audio" });

    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("button", { name: "English" }));
    expect(onSelect).toHaveBeenCalledOnce();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fireEvent.scroll(screen.getByRole("dialog"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fireEvent.scroll(window);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("closes for a control that opts in, and restores trigger focus", () => {
    renderPopover();
    const trigger = screen.getByRole("button", { name: "Audio" });

    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("closes on Escape and restores trigger focus", () => {
    renderPopover();
    const trigger = screen.getByRole("button", { name: "Audio" });
    fireEvent.click(trigger);

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("requests closure on Escape without mutating controlled open state", () => {
    const onOpenChange = vi.fn();
    render(
      <DetailPopover
        trigger={<button type="button">Audio</button>}
        open
        onOpenChange={onOpenChange}
      >
        <button type="button">English</button>
      </DetailPopover>,
    );

    fireEvent.keyDown(document, { key: "Escape" });

    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(screen.getByRole("dialog", { name: "Audio", hidden: true })).toBeVisible();
  });

  it("closes when focus moves outside", () => {
    renderPopover();
    fireEvent.click(screen.getByRole("button", { name: "Audio" }));

    fireEvent.focusIn(screen.getByRole("button", { name: "Outside" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
