import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import RequestPosterCard from "./RequestPosterCard";
import type { RequestMediaResult } from "@/api/types";

const requestable: RequestMediaResult = {
  media_type: "movie",
  tmdb_id: 42,
  title: "Test Movie",
  availability: "missing",
  request: { requestable: true },
};

describe("RequestPosterCard (discover variant)", () => {
  it("renders the hover Request button when onRequest is provided", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          isSubmitting={false}
          onRequest={() => {}}
        />
      </MemoryRouter>,
    );
    expect(markup).toContain("Request");
  });

  it("does not render the hover Request button when onRequest is omitted", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("rounded-full bg-white");
  });
});
