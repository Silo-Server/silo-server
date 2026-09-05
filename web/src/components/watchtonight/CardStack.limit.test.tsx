// @vitest-environment jsdom
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import CardStack from "./CardStack";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

describe("swipe session ending", () => {
  it("distinguishes a session limit from exhausted recommendations and offers restart", () => {
    const onReset = vi.fn();
    const { rerender } = render(
      <MemoryRouter>
        <CardStack
          cards={[]}
          hasMore={false}
          pagingLimited
          isFetching={false}
          onNeedMore={vi.fn()}
          onClose={vi.fn()}
          onReset={onReset}
        />
      </MemoryRouter>,
    );
    expect(screen.getByText(/end of this swipe session/)).toBeTruthy();
    expect(screen.queryByText(/seen everything/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Start Over" }));
    expect(onReset).toHaveBeenCalledOnce();
    rerender(
      <MemoryRouter>
        <CardStack
          cards={[]}
          hasMore={false}
          isFetching={false}
          onNeedMore={vi.fn()}
          onClose={vi.fn()}
          onReset={onReset}
        />
      </MemoryRouter>,
    );
    expect(screen.getByText(/seen everything/)).toBeTruthy();
  });
});
