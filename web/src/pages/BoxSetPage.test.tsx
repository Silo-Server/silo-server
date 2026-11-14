import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ useBoxSet: vi.fn() }));

vi.mock("@/hooks/queries/boxSets", () => ({
  useBoxSet: (...args: unknown[]) => mocks.useBoxSet(...args),
}));

vi.mock("@/components/ItemGrid", () => ({
  default: ({ items }: { items: { title: string }[] }) => (
    <ol>
      {items.map((item) => (
        <li key={item.title}>{item.title}</li>
      ))}
    </ol>
  ),
}));

vi.mock("@/components/PageBack", () => ({ default: () => null }));
vi.mock("@/hooks/useTitleArt", () => ({ useShowTitleArt: () => false }));

import { boxSetNextMovie } from "@/lib/boxSets";
import BoxSetPage from "./BoxSetPage";

const movie = (contentId: string, title: string, played: boolean) => ({
  content_id: contentId,
  title,
  type: "movie" as const,
  user_state: { played, is_favorite: false, in_watchlist: false },
});

const boxSet = {
  id: "1241",
  library_id: "3",
  title: "Harry Potter Collection",
  overview: "The boy wizard's years at Hogwarts.",
  movie_count: 3,
  played_count: 1,
  first_year: 2001,
  last_year: 2004,
  items: [
    movie("m-671", "Philosopher's Stone", true),
    movie("m-672", "Chamber of Secrets", false),
    movie("m-673", "Prisoner of Azkaban", false),
  ],
};

function renderPage(path = "/box-sets/1241?library_id=3") {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/box-sets/:id" element={<BoxSetPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("BoxSetPage", () => {
  beforeEach(() => {
    mocks.useBoxSet.mockReset();
    mocks.useBoxSet.mockReturnValue({ data: boxSet, isLoading: false });
  });

  it("reads the box set of the library in the link", () => {
    renderPage();
    expect(mocks.useBoxSet).toHaveBeenCalledWith("1241", "3");
  });

  it("lists the movies in collection order and continues with the first unwatched one", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "Harry Potter Collection" })).toBeInTheDocument();
    expect(screen.getByText("Box set")).toBeInTheDocument();
    expect(screen.getByText("3 films")).toBeInTheDocument();
    expect(screen.getByText("2001 – 2004")).toBeInTheDocument();
    expect(screen.getAllByRole("listitem").map((item) => item.textContent)).toEqual([
      "Philosopher's Stone",
      "Chamber of Secrets",
      "Prisoner of Azkaban",
    ]);
    expect(screen.getByRole("link", { name: /Continue · Chamber of Secrets/ })).toHaveAttribute(
      "href",
      "/watch/m-672?libraryId=3",
    );
  });

  it("starts over from the first movie once every movie is watched", () => {
    const watched = boxSet.items.map((item) => ({
      ...item,
      user_state: { ...item.user_state, played: true },
    }));
    expect(boxSetNextMovie(watched)?.content_id).toBe("m-671");
  });

  it("says the box set is unavailable when the library has none by that id", () => {
    mocks.useBoxSet.mockReturnValue({ data: undefined, isLoading: false, error: undefined });
    renderPage();
    expect(screen.getByText("This box set isn't available")).toBeInTheDocument();
  });
});
