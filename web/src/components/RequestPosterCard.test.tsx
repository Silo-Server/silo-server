import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { render, screen } from "@testing-library/react";
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
    // Must render an actual <button> with the "Request" label, not just any "Request"
    // substring (the /requests/... URL would match a naive includes check).
    expect(markup).toMatch(/<button[^>]*>[\s\S]*?Request[\s\S]*?<\/button>/);
  });

  it("contains the hover overlay inside the poster frame", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          isSubmitting={false}
          onRequest={() => {}}
        />
      </MemoryRouter>,
    );

    const overlay = screen.getByTestId("request-poster-hover-overlay");
    const posterFrame = overlay.closest(".media-card-image");

    expect(posterFrame).not.toBeNull();
  });

  it("does not render the hover Request button when onRequest is omitted", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );

    // The discover variant only contains one <button> (the hover Request action);
    // its absence is the strongest signal that the button was suppressed.
    expect(markup).not.toContain("<button");
  });

  it("shows the media type so same-title movies and series stay distinguishable", () => {
    const movieMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );
    const seriesMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, media_type: "series", tmdb_id: 43 }}
        />
      </MemoryRouter>,
    );

    expect(movieMarkup).toContain(">Movie<");
    expect(seriesMarkup).toContain(">Series<");
  });
});
