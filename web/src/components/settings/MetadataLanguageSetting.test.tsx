// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ORIGINAL_METADATA_LANGUAGE } from "@/lib/metadataLanguagePreferences";
import { MetadataLanguageSetting } from "./MetadataLanguageSetting";

const languageOptions = [
  { value: "en", label: "English" },
  { value: "ja", label: "Japanese" },
  { value: "no", label: "Norwegian" },
];

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
