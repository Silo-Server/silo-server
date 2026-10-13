import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

// Embla measures layout jsdom does not have; the header is what is under test.
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));

import MediaCarousel from "./MediaCarousel";

describe("MediaCarousel", () => {
  it("links Explore all to the row's page", () => {
    render(
      <MemoryRouter>
        <MediaCarousel title="Trending Movies" viewAllHref="/requests/discover/trending_movies">
          <div>Heat</div>
        </MediaCarousel>
      </MemoryRouter>,
    );

    const link = screen.getByRole("link", { name: "Explore all Trending Movies" });
    expect(link).toHaveAttribute("href", "/requests/discover/trending_movies");
    expect(link).toHaveTextContent("Explore all");
  });

  it("keeps the Explore all button for rows that navigate themselves", () => {
    const onViewAll = vi.fn();
    render(
      <MemoryRouter>
        <MediaCarousel title="Recently Added" onViewAll={onViewAll}>
          <div>Heat</div>
        </MediaCarousel>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Explore all" }));

    expect(onViewAll).toHaveBeenCalledOnce();
    expect(screen.queryByRole("link", { name: /Explore all/ })).not.toBeInTheDocument();
  });
});

describe("MediaCarousel windowing", () => {
  type IdleTask = () => void;
  let idleTasks: IdleTask[];
  let observers: Array<{
    callback: IntersectionObserverCallback;
    targets: Set<Element>;
    options?: IntersectionObserverInit;
  }>;

  beforeEach(() => {
    idleTasks = [];
    observers = [];
    vi.stubGlobal("requestIdleCallback", (task: IdleTask) => {
      idleTasks.push(task);
      return idleTasks.length;
    });
    vi.stubGlobal("cancelIdleCallback", () => {});
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        private readonly record: (typeof observers)[number];
        constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
          this.record = { callback, targets: new Set(), options };
          observers.push(this.record);
        }
        observe = (target: Element) => this.record.targets.add(target);
        unobserve = (target: Element) => this.record.targets.delete(target);
        disconnect = () => this.record.targets.clear();
        takeRecords = () => [];
      },
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const titles = Array.from({ length: 30 }, (_, index) => `Title ${index + 1}`);

  function renderRow(windowing = { initialCount: 5, placeholderClassName: "w-[140px] shrink-0" }) {
    return render(
      <MemoryRouter>
        <MediaCarousel title="Recently Added" windowing={windowing}>
          {titles.map((title) => (
            <div key={title} className="w-[140px] shrink-0">
              <a href={`/item/${title}`}>{title}</a>
            </div>
          ))}
        </MediaCarousel>
      </MemoryRouter>,
    );
  }

  function placeholders(container: HTMLElement) {
    return container.querySelectorAll("li[data-carousel-slide-index]");
  }

  it("mounts the first slides and keeps the rest as same-width placeholders", () => {
    const { container } = renderRow();

    expect(screen.getAllByRole("link")).toHaveLength(5);
    expect(container.querySelectorAll("li.embla__slide")).toHaveLength(30);
    const pending = placeholders(container);
    expect(pending).toHaveLength(25);
    expect(pending[0]).toHaveAttribute("aria-hidden", "true");
    expect(pending[0]?.firstElementChild).toHaveClass("w-[140px]", "shrink-0");
  });

  it("fills the row in idle batches until every card is mounted", () => {
    const { container } = renderRow();

    act(() => idleTasks.shift()?.());
    expect(screen.getAllByRole("link")).toHaveLength(13);

    while (idleTasks.length > 0) {
      act(() => idleTasks.shift()?.());
    }
    expect(screen.getAllByRole("link")).toHaveLength(30);
    expect(placeholders(container)).toHaveLength(0);
  });

  it("keeps each slide element when its card fills in", () => {
    const { container } = renderRow();
    const slide = container.querySelectorAll("li.embla__slide")[10];

    while (idleTasks.length > 0) {
      act(() => idleTasks.shift()?.());
    }

    expect(container.querySelectorAll("li.embla__slide")[10]).toBe(slide);
    expect(slide).toHaveTextContent("Title 11");
  });

  it("mounts placeholders that scroll near the viewport", () => {
    const { container } = renderRow();
    const observer = observers[0]!;
    const viewport = container.querySelector(".embla__viewport");
    expect(observer.options?.root).toBe(viewport);
    const target = container.querySelector('li[data-carousel-slide-index="11"]')!;
    expect(observer.targets.has(target)).toBe(true);

    act(() =>
      observer.callback(
        [{ isIntersecting: true, target } as unknown as IntersectionObserverEntry],
        {} as IntersectionObserver,
      ),
    );

    // Through the intersecting slide plus the buffer.
    expect(screen.getAllByRole("link")).toHaveLength(14);
  });

  it("mounts every card as soon as focus enters the row", () => {
    const { container } = renderRow();

    act(() => {
      (container.querySelector(".embla__viewport") as HTMLElement).focus();
    });

    expect(screen.getAllByRole("link")).toHaveLength(30);
    expect(placeholders(container)).toHaveLength(0);
  });

  it("renders every slide when the row fits the initial window", () => {
    const { container } = renderRow({ initialCount: 40, placeholderClassName: "w-[140px]" });

    expect(screen.getAllByRole("link")).toHaveLength(30);
    expect(placeholders(container)).toHaveLength(0);
    expect(idleTasks).toHaveLength(0);
  });

  it("does not re-render unchanged slides when the carousel itself re-renders", () => {
    const renders = vi.fn();
    function Card({ title }: { title: string }) {
      renders(title);
      return <div>{title}</div>;
    }
    const children = [<Card key="a" title="A" />, <Card key="b" title="B" />];
    const { rerender } = render(
      <MemoryRouter>
        <MediaCarousel title="Row">{children}</MediaCarousel>
      </MemoryRouter>,
    );
    expect(renders).toHaveBeenCalledTimes(2);

    rerender(
      <MemoryRouter>
        <MediaCarousel title="Renamed row">{children}</MediaCarousel>
      </MemoryRouter>,
    );

    expect(screen.getByRole("heading", { name: "Renamed row" })).toBeInTheDocument();
    expect(renders).toHaveBeenCalledTimes(2);
  });
});
