import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Library, LibraryUnsupportedFileGroup } from "@/api/types";
import { UnsupportedFilesSection } from "./UnsupportedFilesSection";

const mocks = vi.hoisted(() => ({
  useFiles: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useUnsupportedLibraryFiles: (...args: unknown[]) => mocks.useFiles(...args),
}));

const libraries = [
  { id: 1, name: "Movies", paths: ["/media/movies/"] },
  { id: 2, name: "Shows", paths: ["/media/tv"] },
] as Library[];

function group(overrides: Partial<LibraryUnsupportedFileGroup>): LibraryUnsupportedFileGroup {
  return {
    library_id: 1,
    library_name: "Movies",
    directory_path: "/media/movies/Ronin (1998)/VIDEO_TS",
    reason: "dvd_vob",
    message: "DVD VOB files are not cataloged; remux the DVD title to a single file",
    file_count: 3,
    file_names: ["VIDEO_TS.VOB", "VTS_01_1.VOB", "VTS_01_2.VOB"],
    first_seen_at: "2026-10-01T10:00:00Z",
    last_seen_at: "2026-10-04T10:00:00Z",
    ...overrides,
  };
}

function listing(groups: LibraryUnsupportedFileGroup[], extra: Record<string, unknown> = {}) {
  return {
    data: { pages: [{ groups, total: groups.length }] },
    isError: false,
    refetch: vi.fn(),
    isRefetching: false,
    hasNextPage: false,
    fetchNextPage: vi.fn(),
    isFetchingNextPage: false,
    ...extra,
  };
}

describe("UnsupportedFilesSection", () => {
  beforeEach(() => {
    mocks.useFiles.mockReset();
  });

  it("shows the count while collapsed", () => {
    mocks.useFiles.mockReturnValue(
      listing([group({}), group({ directory_path: "/media/movies/Manhunter (1986)" })]),
    );

    render(<UnsupportedFilesSection libraries={libraries} />);

    const header = screen.getByRole("button", { name: /files not cataloged/i });
    expect(header).toHaveAttribute("aria-expanded", "false");
    expect(within(header).getByText("2")).toBeInTheDocument();
  });

  it("lists each folder under its library root and explains it on demand", async () => {
    mocks.useFiles.mockReturnValue(
      listing([
        group({}),
        group({
          library_id: 2,
          library_name: "Shows",
          directory_path: "/media/tv/Show/Season 01/BDMV/STREAM",
          reason: "disc_stream",
          message:
            "Blu-ray and AVCHD disc folders (BDMV/STREAM) are not cataloged; remux the disc title to a single file",
          file_count: 140,
          file_names: Array.from({ length: 100 }, (_, i) => `${String(i).padStart(5, "0")}.m2ts`),
        }),
        group({
          directory_path: "/elsewhere/Thief (1981)",
          reason: "disc_image",
          file_count: 1,
          file_names: ["Thief (1981).iso"],
        }),
      ]),
    );
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));

    const ronin = screen.getByText("Ronin (1998)/VIDEO_TS").closest("tr")!;
    expect(within(ronin).getByText("Movies")).toBeInTheDocument();
    expect(within(ronin).getByText("DVD VOB")).toBeInTheDocument();
    expect(within(ronin).getByText("3")).toBeInTheDocument();
    const show = screen.getByText("Show/Season 01/BDMV/STREAM").closest("tr")!;
    expect(within(show).getByText("Blu-ray or AVCHD folder")).toBeInTheDocument();
    // A folder outside its library's roots keeps its full path.
    expect(screen.getByText("/elsewhere/Thief (1981)")).toBeInTheDocument();
    expect(screen.queryByText(/remux the DVD title/)).not.toBeInTheDocument();

    const roninToggle = within(ronin).getByRole("button", { name: "Ronin (1998)/VIDEO_TS" });
    await user.click(roninToggle);

    expect(roninToggle).toHaveAttribute("aria-expanded", "true");
    const details = document.getElementById(roninToggle.getAttribute("aria-controls")!)!;
    expect(within(details).getByText("/media/movies/Ronin (1998)/VIDEO_TS")).toBeInTheDocument();
    expect(
      within(details).getByText(
        "DVD VOB files are not cataloged; remux the DVD title to a single file",
      ),
    ).toBeInTheDocument();
    for (const name of ["VIDEO_TS.VOB", "VTS_01_1.VOB", "VTS_01_2.VOB"]) {
      expect(within(details).getByText(name)).toBeInTheDocument();
    }
    expect(within(details).queryByText(/more$/)).not.toBeInTheDocument();

    // A folder with more files than names kept says how many are not listed.
    await user.click(within(show).getByRole("button", { name: "Show/Season 01/BDMV/STREAM" }));
    expect(screen.getByText("00099.m2ts")).toBeInTheDocument();
    expect(screen.getByText("and 40 more")).toBeInTheDocument();
  });

  it("opens and closes a folder from the keyboard", async () => {
    mocks.useFiles.mockReturnValue(listing([group({})]));
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));
    const toggle = screen.getByRole("button", { name: "Ronin (1998)/VIDEO_TS" });
    toggle.focus();

    await user.keyboard("{Enter}");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("VTS_01_1.VOB")).toBeInTheDocument();

    await user.keyboard(" ");
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("VTS_01_1.VOB")).not.toBeInTheDocument();
  });

  it("labels a folder against its own library's roots only", async () => {
    // The Shows root sits inside the Movies root. A Movies row under it must
    // not be labeled against the Shows root.
    const nested = [
      { id: 1, name: "Movies", paths: ["/media"] },
      { id: 2, name: "Shows", paths: ["/media/tv"] },
    ] as Library[];
    mocks.useFiles.mockReturnValue(
      listing([group({ directory_path: "/media/tv/Concert (2001)/VIDEO_TS" })]),
    );
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={nested} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));

    expect(screen.getByText("tv/Concert (2001)/VIDEO_TS")).toBeInTheDocument();
  });

  it("loads the next page on request", async () => {
    const fetchNextPage = vi.fn();
    mocks.useFiles.mockReturnValue(listing([group({})], { hasNextPage: true, fetchNextPage }));
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));
    await user.click(screen.getByRole("button", { name: "Load more" }));

    expect(fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it("keeps loaded folders and offers a retry when a refresh fails", async () => {
    const refetch = vi.fn();
    mocks.useFiles.mockReturnValue(listing([group({})], { isError: true, refetch }));
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    const header = screen.getByRole("button", { name: /files not cataloged/i });
    // The count still shows; the loaded rows are only out of date.
    expect(within(header).getByText("1")).toBeInTheDocument();
    await user.click(header);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "The list could not be refreshed. It shows what loaded before.",
    );
    expect(screen.getByText("Ronin (1998)/VIDEO_TS")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("offers a retry when the first load fails", async () => {
    const refetch = vi.fn();
    mocks.useFiles.mockReturnValue(listing([], { data: undefined, isError: true, refetch }));
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));

    expect(screen.getByRole("alert")).toHaveTextContent("The list could not be loaded.");
    expect(screen.queryByText("No skipped files found.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("says when nothing was skipped", async () => {
    mocks.useFiles.mockReturnValue(listing([]));
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));

    expect(screen.getByText("No skipped files found.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("passes the filter to the listing", async () => {
    mocks.useFiles.mockReturnValue(listing([group({})]));
    const user = userEvent.setup();

    render(<UnsupportedFilesSection libraries={libraries} />);
    await user.click(screen.getByRole("button", { name: /files not cataloged/i }));
    await user.type(screen.getByRole("textbox", { name: "Filter files not cataloged" }), "ronin");

    await vi.waitFor(() => expect(mocks.useFiles).toHaveBeenLastCalledWith({ search: "ronin" }));
  });
});
