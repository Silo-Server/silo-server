import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

import { RelatedRail } from "./RelatedRail";

describe("RelatedRail", () => {
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
