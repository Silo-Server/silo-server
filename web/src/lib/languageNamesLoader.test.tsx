import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { formatLanguageWhenLoaded, useLanguageNamesLoaded } from "./languageNamesLoader";

function LanguageLabel({ value }: { value: string }) {
  useLanguageNamesLoaded();
  return <span>{formatLanguageWhenLoaded(value) ?? "pending"}</span>;
}

function InactiveLabel({ onRender }: { onRender: () => void }) {
  onRender();
  useLanguageNamesLoaded(false);
  return null;
}

describe("languageNamesLoader", () => {
  it("renders the bundled CLDR name once the name data loads", async () => {
    const inactiveRender = vi.fn();
    render(
      <>
        <LanguageLabel value="pt-BR" />
        <InactiveLabel onRender={inactiveRender} />
      </>,
    );

    expect(screen.getByText("pending")).toBeInTheDocument();
    expect(await screen.findByText("Brazilian Portuguese")).toBeInTheDocument();
    expect(formatLanguageWhenLoaded("sr-Latn")).toBe("Serbian (Latin)");
    expect(formatLanguageWhenLoaded(" ")).toBeNull();
    // A caller that shows no language names is not re-rendered by the load.
    expect(inactiveRender).toHaveBeenCalledTimes(1);
  });
});
