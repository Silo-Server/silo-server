// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ORIGINAL_METADATA_LANGUAGE } from "@/lib/metadataLanguagePreferences";
import { MetadataLanguageSetting } from "./MetadataLanguageSetting";

const languageOptions = [
  { value: "en", label: "English" },
  { value: "ja", label: "Japanese" },
  { value: "no", label: "Norwegian" },
];

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;
window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.scrollIntoView ??= () => {};

describe("MetadataLanguageSetting", () => {
  afterEach(cleanup);

  it("shows a source-language exception and removes only that rule", () => {
    const onOverridesChange = vi.fn();
    render(
      <MetadataLanguageSetting
        fallback="en"
        overrides={{ no: ORIGINAL_METADATA_LANGUAGE, ja: "en" }}
        languageOptions={languageOptions}
        onFallbackChange={vi.fn()}
        onOverridesChange={onOverridesChange}
      />,
    );

    expect(screen.getByText("Norwegian")).toBeTruthy();
    expect(screen.getByText("Japanese")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove Norwegian exception" }));

    expect(onOverridesChange).toHaveBeenCalledWith({ ja: "en" });
  });

  it("keeps the add action disabled until an original language is chosen", () => {
    render(
      <MetadataLanguageSetting
        fallback={ORIGINAL_METADATA_LANGUAGE}
        overrides={{}}
        languageOptions={languageOptions}
        onFallbackChange={vi.fn()}
        onOverridesChange={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: "Add exception" }).hasAttribute("disabled")).toBe(
      true,
    );
    expect(screen.getByLabelText("Original language for new exception")).toBeTruthy();
  });

  it("shows a new exception immediately while its save is still pending", async () => {
    const user = userEvent.setup();
    const onOverridesChange = vi.fn(() => new Promise<void>(() => {}));
    render(
      <MetadataLanguageSetting
        fallback="en"
        overrides={{}}
        languageOptions={languageOptions}
        onFallbackChange={vi.fn()}
        onOverridesChange={onOverridesChange}
      />,
    );

    await user.click(screen.getByLabelText("Original language for new exception"));
    await user.click(screen.getByRole("option", { name: "Norwegian" }));
    await user.click(screen.getByRole("button", { name: "Add exception" }));

    expect(screen.getByLabelText("Metadata language for Norwegian")).toHaveTextContent(
      "Original language",
    );
    expect(onOverridesChange).toHaveBeenCalledWith({ no: ORIGINAL_METADATA_LANGUAGE });
  });

  it("rolls back an optimistic exception when its save fails", async () => {
    const user = userEvent.setup();
    let rejectSave: (reason: Error) => void = () => {};
    const onOverridesChange = vi.fn(
      () =>
        new Promise<void>((_, reject) => {
          rejectSave = reject;
        }),
    );
    render(
      <MetadataLanguageSetting
        fallback="en"
        overrides={{}}
        languageOptions={languageOptions}
        onFallbackChange={vi.fn()}
        onOverridesChange={onOverridesChange}
      />,
    );

    await user.click(screen.getByLabelText("Original language for new exception"));
    await user.click(screen.getByRole("option", { name: "Norwegian" }));
    await user.click(screen.getByRole("button", { name: "Add exception" }));
    expect(screen.getByLabelText("Metadata language for Norwegian")).toBeInTheDocument();

    rejectSave(new Error("save failed"));

    await waitFor(() =>
      expect(screen.queryByLabelText("Metadata language for Norwegian")).not.toBeInTheDocument(),
    );
  });

  it("keeps a custom target selectable when it is outside the advisory catalog", () => {
    render(
      <MetadataLanguageSetting
        fallback="en"
        overrides={{ no: "pt-BR" }}
        languageOptions={languageOptions}
        onFallbackChange={vi.fn()}
        onOverridesChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Metadata language for Norwegian").textContent).toContain(
      "Portuguese",
    );
  });
});
