import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { MemoryRouter } from "react-router";
import type { FileVersion, ItemDetail } from "@/api/types";
import EbookContent from "./EbookContent";
import { makeFileVersion, makeItemDetail } from "./itemDetail.test-support";

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

const makeVersion = (overrides: Partial<FileVersion> = {}) =>
  makeFileVersion({
    file_path: "/books/book.epub",
    resolution: "",
    codec_video: "",
    codec_audio: "",
    container: "epub",
    file_size: 1234,
    file_name: "Book.epub",
    ...overrides,
  });

function makeEbookItem(overrides: Partial<ItemDetail> = {}) {
  return makeItemDetail("ebook", {
    title: "A Psalm for the Wild-Built",
    original_title: "",
    year: 2021,
    overview: "An ebook overview",
    tagline: "",
    crew: [
      { name: "Becky Chambers", job: "Author", person_id: "author-1" },
      { name: "A Narrator Should Not Appear", job: "Narrator", person_id: "narrator-1" },
    ],
    studios: ["Tor"],
    series_id: "",
    series_title: "",
    season_number: null,
    episode_number: null,
    episode_count: null,
    air_date: null,
    is_specials: false,
    versions: [makeVersion()],
    ...overrides,
  });
}

function renderEbook(item = makeEbookItem(), libraryId?: number) {
  return renderToStaticMarkup(
    <MemoryRouter>
      <EbookContent item={item} libraryId={libraryId} />
    </MemoryRouter>,
  );
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
    let markup = renderEbook();
    expect(markup).toContain("Download");

    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });
    markup = renderEbook();
    expect(markup).not.toContain("Download");

    mocks.useAuth.mockReturnValue({ user: { download_allowed: true } });
    markup = renderEbook(makeEbookItem({ versions: [] }));
    expect(markup).not.toContain("Download");
  });

  it("shows read action for ebook files even when downloads are disabled", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });

    let markup = renderEbook(makeEbookItem(), 12);

    expect(markup).toContain("Read");
    expect(markup).toContain("/reader/ebook/ebook-1?file_id=1&amp;libraryId=12");

    markup = renderEbook(makeEbookItem({ versions: [] }));
    expect(markup).not.toContain("/reader/ebook/");
  });

  it("does not show read action when ebook files are not reader-supported", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: true } });

    const markup = renderEbook(
      makeEbookItem({
        versions: [makeVersion({ container: "docx", file_name: "Book.docx" })],
      }),
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

    const markup = renderEbook(
      makeEbookItem({
        versions: [
          makeVersion({ file_id: 1, container: "pdf", file_name: "Book.pdf" }),
          makeVersion({ file_id: 2, container: "epub", file_name: "Book.epub" }),
        ],
      }),
    );

    expect(markup).toContain("Continue");
    expect(markup).toContain("42%");
    expect(markup).not.toContain(">Read<");
    expect(markup).toContain("/reader/ebook/ebook-1?file_id=1");
  });

  it("prefers EPUB for the read action when multiple ebook files exist", () => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: false } });

    const markup = renderEbook(
      makeEbookItem({
        versions: [
          makeVersion({ file_id: 1, container: "pdf", file_name: "Book.pdf" }),
          makeVersion({ file_id: 2, container: "epub", file_name: "Book.epub" }),
        ],
      }),
    );

    expect(markup).toContain("/reader/ebook/ebook-1?file_id=2");
  });
});
