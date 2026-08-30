import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { UICustomizationContext } from "@/contexts/uiCustomizationContext";
import { RecommendationGridSkeleton } from "./SectionSkeletons";

describe("RecommendationGridSkeleton", () => {
  it("matches compact artwork-only recommendation cards", () => {
    const markup = renderToStaticMarkup(
      <UICustomizationContext.Provider
        value={{
          cardPresentation: { poster_size: "compact", caption: "artwork" },
          cardPresentationSource: "profile_client",
          primaryMenu: null,
          primaryMenuSource: "default",
          shortcuts: { items: [] },
          isSupported: true,
          supportsAtomicShortcuts: true,
          isLoading: false,
          isUnavailable: false,
        }}
      >
        <RecommendationGridSkeleton count={2} />
      </UICustomizationContext.Provider>,
    );

    expect(markup).toContain("flex gap-4 overflow-hidden");
    expect(markup).toContain("w-[120px] shrink-0 sm:w-[140px] lg:w-[160px]");
    expect(markup).not.toContain("mt-1.5 h-4 w-3/4");
  });
});
