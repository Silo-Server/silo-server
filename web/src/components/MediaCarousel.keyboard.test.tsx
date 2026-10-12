import { useState } from "react";
import { createPortal } from "react-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";

const embla = vi.hoisted(() => ({
  api: undefined as
    | undefined
    | {
        rootNode: () => HTMLElement;
        internalEngine: () => { slideRegistry: number[][] };
        scrollTo: (index: number) => void;
      },
}));

// Embla measures layout jsdom does not have; tests that check scrolling hand
// in a stand-in API.
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaApi: embla.api,
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));

import MediaCarousel from "./MediaCarousel";

const titles = ["Heat", "Ronin", "Collateral", "Thief", "Drive"];

/** A card shaped like the real ones: a poster link, then a menu button. */
function Card({ title }: { title: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <a href={`/item/${title}`}>{title}</a>
      <button type="button" aria-label={`More for ${title}`} onClick={() => setOpen(true)}>
        ⋯
      </button>
      {open &&
        createPortal(
          <div role="menu">
            <button type="button" role="menuitem" autoFocus>
              Mark {title} watched
            </button>
          </div>,
          document.body,
        )}
    </div>
  );
}

function renderRow({ dir, windowed = false }: { dir?: "rtl"; windowed?: boolean } = {}) {
  return render(
    <MemoryRouter>
      <div dir={dir}>
        <button type="button">Before</button>
        <MediaCarousel
          title="Recently Added"
          windowing={windowed ? { initialCount: 2, placeholderClassName: "w-[140px]" } : undefined}
        >
          {titles.map((title) => (
            <Card key={title} title={title} />
          ))}
        </MediaCarousel>
        <button type="button">After</button>
      </div>
    </MemoryRouter>,
  );
}

const card = (title: string) => screen.getByRole("link", { name: title });

describe("MediaCarousel keyboard navigation", () => {
  beforeEach(() => {
    embla.api = undefined;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("labels the row's list by its heading", () => {
    renderRow();

    expect(screen.getByRole("list", { name: "Recently Added" })).toBeInTheDocument();
  });

  it("makes the row one Tab stop on its first card", async () => {
    const user = userEvent.setup();
    renderRow();

    screen.getByRole("button", { name: "Before" }).focus();
    await user.tab();
    expect(card("Heat")).toHaveFocus();
    // The active card's own controls stay reachable, then Tab leaves the row.
    await user.tab();
    expect(screen.getByRole("button", { name: "More for Heat" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "After" })).toHaveFocus();
  });

  it("moves card to card with Left and Right, and to the ends with Home and End", async () => {
    const user = userEvent.setup();
    renderRow();

    await user.click(card("Heat"));
    await user.keyboard("{ArrowRight}");
    expect(card("Ronin")).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(card("Collateral")).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(card("Ronin")).toHaveFocus();
    await user.keyboard("{End}");
    expect(card("Drive")).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(card("Drive")).toHaveFocus();
    await user.keyboard("{Home}");
    expect(card("Heat")).toHaveFocus();
  });

  it("moves to the neighbouring card from a card's other controls", async () => {
    const user = userEvent.setup();
    renderRow();

    screen.getByRole("button", { name: "More for Ronin" }).focus();
    await user.keyboard("{ArrowRight}");

    expect(card("Collateral")).toHaveFocus();
  });

  it("remembers the last focused card when Tab comes back", async () => {
    const user = userEvent.setup();
    renderRow();

    await user.click(card("Heat"));
    await user.keyboard("{ArrowRight}{ArrowRight}");
    await user.tab();
    await user.tab();
    expect(screen.getByRole("button", { name: "After" })).toHaveFocus();

    await user.tab({ shift: true });
    expect(screen.getByRole("button", { name: "More for Collateral" })).toHaveFocus();
    await user.tab({ shift: true });
    expect(card("Collateral")).toHaveFocus();
    await user.tab({ shift: true });
    expect(screen.getByRole("button", { name: "Before" })).toHaveFocus();
  });

  it("makes a clicked card the row's Tab stop", async () => {
    const user = userEvent.setup();
    renderRow();

    await user.click(card("Thief"));
    screen.getByRole("button", { name: "Before" }).focus();
    await user.tab();

    expect(card("Thief")).toHaveFocus();
  });

  it("flips Left and Right in a right-to-left layout", async () => {
    const user = userEvent.setup();
    renderRow({ dir: "rtl" });

    await user.click(card("Ronin"));
    await user.keyboard("{ArrowLeft}");
    expect(card("Collateral")).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(card("Ronin")).toHaveFocus();
  });

  it("leaves arrow keys inside an open card menu alone", async () => {
    const user = userEvent.setup();
    renderRow();

    await user.click(screen.getByRole("button", { name: "More for Heat" }));
    const item = screen.getByRole("menuitem", { name: "Mark Heat watched" });
    expect(item).toHaveFocus();
    await user.keyboard("{ArrowRight}{End}");

    expect(item).toHaveFocus();
  });

  it("keeps a control re-enabled in an inactive card out of the Tab order", async () => {
    const user = userEvent.setup();
    function Row({ ready }: { ready: boolean }) {
      return (
        <MemoryRouter>
          <button type="button">Before</button>
          <MediaCarousel title="Recently Added">
            {titles.map((title) => (
              <div key={title}>
                <a href={`/item/${title}`}>{title}</a>
                {/* Same element either way, as the card menu renders it. */}
                <button type="button" disabled={!ready}>
                  More for {title}
                </button>
              </div>
            ))}
          </MediaCarousel>
          <button type="button">After</button>
        </MemoryRouter>
      );
    }
    const { rerender } = render(<Row ready={false} />);
    rerender(<Row ready />);

    screen.getByRole("button", { name: "Before" }).focus();
    await user.tab();
    await user.tab();
    expect(screen.getByRole("button", { name: "More for Heat" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "After" })).toHaveFocus();
  });

  it("ignores modified arrows such as Alt+Left (browser back)", async () => {
    const user = userEvent.setup();
    renderRow();

    await user.click(card("Ronin"));
    await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");

    expect(card("Ronin")).toHaveFocus();
  });

  it("reaches cards that have not mounted yet in a windowed row", async () => {
    vi.stubGlobal("requestIdleCallback", () => 1);
    vi.stubGlobal("cancelIdleCallback", () => {});
    const user = userEvent.setup();
    renderRow({ windowed: true });
    expect(screen.queryByRole("link", { name: "Drive" })).not.toBeInTheDocument();

    await user.click(card("Heat"));
    await user.keyboard("{End}");

    expect(card("Drive")).toHaveFocus();
  });

  it("scrolls the row to a card that is outside the viewport", async () => {
    const scrollTo = vi.fn();
    const root = document.createElement("div");
    root.getBoundingClientRect = () => ({ left: 0, right: 300 }) as DOMRect;
    embla.api = {
      rootNode: () => root,
      internalEngine: () => ({ slideRegistry: [[0, 1], [2, 3], [4]] }),
      scrollTo,
    };
    const user = userEvent.setup();
    const { container } = renderRow();
    container.querySelectorAll<HTMLElement>("li.embla__slide").forEach((slide, index) => {
      slide.getBoundingClientRect = () =>
        ({ left: index * 150, right: index * 150 + 140 }) as DOMRect;
    });

    await user.click(card("Heat"));
    await user.keyboard("{ArrowRight}");
    expect(scrollTo).not.toHaveBeenCalled();
    await user.keyboard("{ArrowRight}");
    expect(scrollTo).toHaveBeenCalledWith(1);
  });
});
