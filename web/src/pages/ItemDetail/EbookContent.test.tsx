import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { MemoryRouter } from "react-router";
import type { FileVersion, ItemDetail } from "@/api/types";
import EbookContent from "./EbookContent";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  useEbookReaderProgress: vi.fn(),
  useWatchedStateMutation: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
}));

vi.mock("@/hooks/queries/items", () => ({
  useWatchedStateMutation: mocks.useWatchedStateMutation,
}));

vi.mock("@/hooks/useAmbientColor", () => ({
  useAmbientColor: vi.fn(),
}));

vi.mock("@/hooks/queries/ebookReaderProgress", () => ({
  useEbookReaderProgress: mocks.useEbookReaderProgress,
}));

vi.mock("@/components/PageBack", () => ({
  default: () => <div />,
}));

vi.mock("@/components/MediaLocations", () => ({
  default: ({
    title,
    versions,
    emptyMessage,
    summaryBuilder,
  }: {
    title: string;
    versions: FileVersion[];
    emptyMessage: string;
    summaryBuilder?: (version: FileVersion) => string;
  }) => (
    <div>
      <span>{title}</span>
      <span>{versions.length}</span>
      <span>{emptyMessage}</span>
      {versions.map((version) => (
        <span key={version.file_id}>{summaryBuilder?.(version)}</span>
      ))}
    </div>
  ),
}));

vi.mock("@/components/DownloadVersionPicker", () => ({
  default: ({ versions, title }: { versions: FileVersion[]; title: string }) => (
    <div>
      <span>download picker</span>
      <span>{title}</span>
      <span>{versions.length}</span>
    </div>
  ),
}));

vi.mock("@/pages/audiobooks/components/RelatedRail", () => ({
  RelatedRail: ({
    heading,
    items,
    coverAspect,
  }: {
    heading: string;
    coverAspect?: "square" | "poster";
    items: Array<{ content_id: string; title: string; subtitle?: string; highlight?: boolean }>;
  }) => (
    <section>
      <h2>{heading}</h2>
      {coverAspect && <span>aspect:{coverAspect}</span>}
      {items.map((item) => (
        <div key={item.content_id}>
          <span>{item.title}</span>
          {item.subtitle && <span>{item.subtitle}</span>}
          {item.highlight && <span>Current</span>}
        </div>
      ))}
    </section>
  ),
}));

vi.mock("./DetailHero", () => ({
  default: ({
    title,
    context,
    crewLine,
    actions,
    genres,
    genreHref,
  }: {
    title: string;
    context?: ReactNode;
    crewLine?: ReactNode;
    actions?: ReactNode;
    genres?: string[];
    genreHref?: (genre: string) => string;
  }) => (
    <div>
      <span>{title}</span>
      <span>{context}</span>
      {crewLine}
      {genres?.map((genre) => (
        <a key={genre} href={genreHref?.(genre) ?? "#"}>
          {genre}
        </a>
      ))}
      {actions}
    </div>
  ),
}));

vi.mock("./components/MetadataBadges", () => ({
  default: () => <div />,
}));

vi.mock("./components/ScoreRow", () => ({
  default: () => <div />,
}));

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    file_path: overrides.file_path ?? "/books/book.epub",
    resolution: overrides.resolution ?? "",
    codec_video: overrides.codec_video ?? "",
    codec_audio: overrides.codec_audio ?? "",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "epub",
    file_size: overrides.file_size ?? 1234,
    duration: overrides.duration ?? 0,
    bitrate: overrides.bitrate ?? 0,
    file_name: overrides.file_name ?? "Book.epub",
    audio_tracks: overrides.audio_tracks,
    video_tracks: overrides.video_tracks,
    subtitle_tracks: overrides.subtitle_tracks,
  };
}

function makeEbookItem(
  overrides: Partial<ItemDetail & { type: "ebook" }> = {},
): ItemDetail & { type: "ebook" } {
  return {
    content_id: "ebook-1",
    type: "ebook",
    title: "A Psalm for the Wild-Built",
    original_title: "",
    year: 2021,
    overview: "An ebook overview",
    tagline: "",
    runtime: 0,
    content_rating: "",
    genres: [],
    rating_imdb: null,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    ratings: [],
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [
      { name: "Becky Chambers", job: "Author", person_id: "author-1" },
      { name: "A Narrator Should Not Appear", job: "Narrator", person_id: "narrator-1" },
    ],
    studios: ["Tor"],
    networks: [],
    countries: [],
    release_date: null,
    first_air_date: null,
    last_air_date: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    season_count: null,
    series_id: "",
    series_title: "",
    season_number: null,
    episode_number: null,
    episode_count: null,
    air_date: null,
    is_specials: false,
    versions: [makeVersion()],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
  };
}

describe("EbookContent", () => {
  beforeEach(() => {
    mocks.useAuth.mockReset();
    mocks.useAuth.mockReturnValue({ user: { download_allowed: true } });
    mocks.useEbookReaderProgress.mockReset();
    mocks.useEbookReaderProgress.mockReturnValue({ data: null });
    mocks.useWatchedStateMutation.mockReset();
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
  });

  it("only shows download action when downloads are allowed and files exist", () => {
    let markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem()} />
      </MemoryRouter>,
    );
    expect(markup).toContain("Download");

    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });
    markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem()} />
      </MemoryRouter>,
    );
    expect(markup).not.toContain("Download");

    mocks.useAuth.mockReturnValue({ user: { download_allowed: true } });
    markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem({ versions: [] })} />
      </MemoryRouter>,
    );
    expect(markup).not.toContain("Download");
  });

  it("shows read action for ebook files even when downloads are disabled", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });

    let markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem()} libraryId={12} />
      </MemoryRouter>,
    );

    expect(markup).toContain("Read");
    expect(markup).toContain("/reader/ebook/ebook-1?file_id=1&amp;libraryId=12");

    markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem({ versions: [] })} />
      </MemoryRouter>,
    );
    expect(markup).not.toContain("/reader/ebook/");
  });

  it("shows the mark read toggle backed by the watched mutation", () => {
    const mutate = vi.fn();
    mocks.useWatchedStateMutation.mockReturnValue({ mutate, isPending: false });
    const item = makeEbookItem();
    const { rerender } = render(
      <MemoryRouter>
        <EbookContent item={item} />
      </MemoryRouter>,
    );

    expect(mocks.useWatchedStateMutation).toHaveBeenCalledWith(item);
    fireEvent.click(screen.getByRole("button", { name: "Mark Read" }));
    expect(mutate).toHaveBeenLastCalledWith(true);

    rerender(
      <MemoryRouter>
        <EbookContent
          item={makeEbookItem({
            user_data: { played: true, position_seconds: 0, duration_seconds: 0 },
          })}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Mark Unread" }));
    expect(mutate).toHaveBeenLastCalledWith(false);
    expect(mutate).toHaveBeenCalledTimes(2);
  });

  it("does not show read action when ebook files are not reader-supported", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: true } });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent
          item={makeEbookItem({
            versions: [makeVersion({ container: "docx", file_name: "Book.docx" })],
          })}
        />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("/reader/ebook/");
    expect(markup).toContain("Download");
  });

  it("continues from the saved reader file when progress points at another format", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });
    mocks.useEbookReaderProgress.mockReturnValue({
      data: {
        file_id: 1,
        location: "pdf-location",
        progress: 0.42,
      },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent
          item={makeEbookItem({
            versions: [
              makeVersion({ file_id: 1, container: "pdf", file_name: "Book.pdf" }),
              makeVersion({ file_id: 2, container: "epub", file_name: "Book.epub" }),
            ],
          })}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Continue");
    expect(markup).toContain("42%");
    expect(markup).not.toContain(">Read<");
    expect(markup).toContain("/reader/ebook/ebook-1?file_id=1");
  });

  it("prefers EPUB for the read action when multiple ebook files exist", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent
          item={makeEbookItem({
            versions: [
              makeVersion({ file_id: 1, container: "pdf", file_name: "Book.pdf" }),
              makeVersion({ file_id: 2, container: "epub", file_name: "Book.epub" }),
            ],
          })}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("/reader/ebook/ebook-1?file_id=2");
  });
});
