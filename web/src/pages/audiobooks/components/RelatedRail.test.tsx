import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

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
});
