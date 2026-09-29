import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RequestMediaDetail, RequestMediaSeason } from "@/api/types";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  loaded: { data: undefined as RequestMediaDetail | undefined, isFetching: false, isError: false },
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useCreateMediaRequest: () => ({ mutate: mocks.create, isPending: false }),
  useRequestMediaDetail: () => mocks.loaded,
}));

import { RequestSeasonsDialog } from "./RequestSeasonsDialog";

const season = (n: number, overrides: Partial<RequestMediaSeason> = {}): RequestMediaSeason => ({
  season_number: n,
  episode_count: 10,
  air_date: "2020-01-01",
  availability: "missing",
  requested: false,
  ...overrides,
});

const series = (overrides: Partial<RequestMediaDetail>): RequestMediaDetail => ({
  media_type: "series",
  tmdb_id: 42,
  title: "Example",
  availability: "missing",
  request: { requestable: true },
  seasons: [],
  ...overrides,
});

function open(detail?: RequestMediaDetail) {
  render(
    <RequestSeasonsDialog
      open
      onOpenChange={() => {}}
      tmdbID={42}
      title="Example"
      detail={detail}
    />,
  );
  return screen.getByRole("dialog");
}

describe("RequestSeasonsDialog", () => {
  beforeEach(() => {
    mocks.create.mockReset();
    mocks.loaded = { data: undefined, isFetching: false, isError: false };
  });

  it("leaves an untouched pick to the server", () => {
    const dialog = open(series({ seasons: [season(1), season(2, { availability: "available" })] }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Request Season 1" }));
    expect(mocks.create.mock.calls[0]![0]).not.toHaveProperty("seasons");
  });

  it("requests a series with nothing aired whole", () => {
    const dialog = open(series({ seasons: [season(1, { air_date: "2999-01-01" })] }));
    expect(within(dialog).getByText(/covers the whole series/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Request series" }));
    expect(mocks.create.mock.calls[0]![0]).not.toHaveProperty("seasons");
  });

  it("asks for the upcoming seasons of a series in the library", () => {
    const dialog = open(
      series({
        availability: "available",
        seasons: [season(1, { availability: "available" }), season(2, { air_date: "2999-01-01" })],
      }),
    );
    expect(within(dialog).getByText("Pick the upcoming seasons to request.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Request" })).toBeDisabled();
    fireEvent.click(within(dialog).getByRole("switch", { name: "Season 2" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Request Season 2" }));
    expect(mocks.create.mock.calls[0]![0]).toMatchObject({ seasons: [2] });
  });

  it("describes why a season can't be picked", () => {
    const dialog = open(series({ seasons: [season(1, { availability: "available" })] }));
    const toggle = within(dialog).getByRole("switch", { name: "Season 1" });
    expect(toggle).toBeDisabled();
    expect(toggle).toHaveAccessibleDescription(/In library/);
  });

  it("waits for a refetch before sending from loaded detail", () => {
    mocks.loaded = { data: series({ seasons: [season(1)] }), isFetching: true, isError: false };
    const dialog = open();
    expect(within(dialog).getByRole("button", { name: /Request/ })).toBeDisabled();
  });
});
