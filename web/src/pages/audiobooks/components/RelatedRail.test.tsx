import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

import { UICustomizationContext } from "@/contexts/uiCustomizationContext";
import { RelatedRail } from "./RelatedRail";

const items = [{ content_id: "book-1", title: "Book One", poster_url: "/cover.jpg" }];

describe("RelatedRail", () => {
  it("keeps square cover geometry by default", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RelatedRail heading="Related" items={items} />
      </MemoryRouter>,
    );

    expect(markup).toContain("aspect-square");
    expect(markup).not.toContain("aspect-[2/3]");
  });

  it("can render portrait poster geometry for ebook rails", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RelatedRail heading="Related" items={items} coverAspect="poster" />
      </MemoryRouter>,
    );

    expect(markup).toContain("aspect-[2/3]");
    expect(markup).not.toContain("aspect-square");
  });

  it("encodes related item links", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RelatedRail
          heading="Related"
          items={[{ content_id: "ebook 1/isbn:978", title: "Book One" }]}
          coverAspect="poster"
        />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/ebook%201%2Fisbn%3A978"');
  });

  it("uses compact widths and hides caption rows for artwork-only cards", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
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
          <RelatedRail
            heading="Related"
            items={[
              {
                content_id: "book-1",
                title: "Book One",
                poster_url: "/cover.jpg",
                subtitle: "Book metadata",
              },
            ]}
          />
        </UICustomizationContext.Provider>
      </MemoryRouter>,
    );

    expect(markup).toContain("w-[120px]");
    expect(markup).not.toContain(">Book One</div>");
    expect(markup).not.toContain("Book metadata");
  });
});
