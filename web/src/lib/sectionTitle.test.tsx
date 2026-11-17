import type { ReactNode } from "react";
import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AppIntlProvider, createAppIntl } from "@/i18n";
import { useSectionTitle } from "./sectionTitle";

const spanish = createAppIntl("es", {
  "home.section.continueWatching": [{ type: 0, value: "Seguir viendo" }],
});

describe("useSectionTitle", () => {
  it("translates a keyed title in the provider's language", () => {
    const wrapper = ({ children }: { children: ReactNode }) => (
      <AppIntlProvider intl={spanish}>{children}</AppIntlProvider>
    );
    const sectionTitle = renderHook(() => useSectionTitle(), { wrapper }).result.current;

    expect(
      sectionTitle({ title: "Continue Watching", title_key: "home.section.continueWatching" }),
    ).toBe("Seguir viendo");
  });
});
