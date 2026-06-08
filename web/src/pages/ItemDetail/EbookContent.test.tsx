import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { MemoryRouter } from "react-router";
import type { FileVersion, ItemDetail } from "@/api/types";
import EbookContent from "./EbookContent";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
}));

vi.mock("@/hooks/useAmbientColor", () => ({
  useAmbientColor: vi.fn(),
}));

vi.mock("@/components/PageBack", () => ({
  default: () => <div />,
}));

vi.mock("@/components/MediaLocations", () => ({
  default: ({
    title,
    versions,
    emptyMessage,
  }: {
    title: string;
    versions: FileVersion[];
    emptyMessage: string;
  }) => (
    <div>
      <span>{title}</span>
      <span>{versions.length}</span>
      <span>{emptyMessage}</span>
    </div>
  ),
}));

vi.mock("@/components/DownloadVersionPicker", () => ({
  default: ({
    versions,
    title,
  }: {
    versions: FileVersion[];
    title: string;
  }) => (
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
  }: {
    heading: string;
    items: Array<{ content_id: string; title: string; subtitle?: string; highlight?: boolean }>;
  }) => (
    <section>
      <h2>{heading}</h2>
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
  }: {
    title: string;
    context?: ReactNode;
    crewLine?: ReactNode;
    actions?: ReactNode;
  }) => (
    <div>
      <span>{title}</span>
      <span>{context}</span>
      {crewLine}
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
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [
      { name: "Becky Chambers", job: "Author" },
      { name: "A Narrator Should Not Appear", job: "Narrator" },
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
  });

  it("renders ebook authors without audiobook narrator credits", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem()} />
      </MemoryRouter>,
    );

    expect(markup).toContain("Ebook");
    expect(markup).toContain("By");
    expect(markup).toContain("Becky Chambers");
    expect(markup).not.toContain("A Narrator Should Not Appear");
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
        <EbookContent item={makeEbookItem()} />
      </MemoryRouter>,
    );

    expect(markup).toContain("Read");
    expect(markup).toContain("/reader/ebook/ebook-1?file_id=1");

    markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent item={makeEbookItem({ versions: [] })} />
      </MemoryRouter>,
    );
    expect(markup).not.toContain("Read");
  });

  it("renders ebook series and related rails from ebook detail extension", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EbookContent
          item={makeEbookItem({
            ebook: {
              authors: [{ name: "Becky Chambers" }],
              publisher: "Tor",
              series: {
                name: "Monk and Robot",
                entries: [
                  {
                    content_id: "ebook-1",
                    title: "A Psalm for the Wild-Built",
                    series_index: 1,
                  },
                  {
                    content_id: "ebook-2",
                    title: "A Prayer for the Crown-Shy",
                    series_index: 2,
                  },
                ],
              },
              related: {
                also_by_author: [{ content_id: "ebook-3", title: "The Long Way", year: 2014 }],
                similar: [{ content_id: "ebook-4", title: "All Systems Red", year: 2017 }],
              },
            },
          })}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("In Monk and Robot");
    expect(markup).toContain("A Prayer for the Crown-Shy");
    expect(markup).toContain("Book 2");
    expect(markup).toContain("Also by Becky Chambers");
    expect(markup).toContain("The Long Way");
    expect(markup).toContain("You might also like");
    expect(markup).toContain("All Systems Red");
  });
});
