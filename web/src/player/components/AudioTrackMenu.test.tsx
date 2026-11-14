// @vitest-environment jsdom

import { act, fireEvent, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AudioTrackMenu } from "./AudioTrackMenu";
import { PROVISIONAL_INVENTORY_TIMEOUT_MS } from "../utils/inventoryProvenance";
import { resolveAppliedAudioIndex } from "../utils/audioTrackMatch";

function renderMenu(tracks: Parameters<typeof AudioTrackMenu>[0]["tracks"]) {
  return render(
    createElement(AudioTrackMenu, {
      tracks,
      activeIndex: 0,
      onSelect: () => {},
      currentPosition: 0,
      open: true,
      onOpenChange: () => {},
      hideTrigger: true,
    }),
  );
}

describe("AudioTrackMenu", () => {
  it("joins the languages array into a multi-language descriptor", () => {
    renderMenu([
      {
        title: "English / French / Spanish",
        languages: ["en", "fr", "es"],
        codec: "eac3",
        layout: "5.1",
      },
    ]);
    expect(screen.getByText("English/French/Spanish · 5.1")).toBeTruthy();
  });

  it("falls back to the single language tag when languages is absent", () => {
    renderMenu([{ title: "French DTS", language: "fra", codec: "dts" }]);
    expect(screen.getByText("French")).toBeTruthy();
  });

  it("omits the language segment when no language resolves", () => {
    renderMenu([{ title: "Commentary", codec: "ac3", layout: "2.0" }]);
    expect(screen.getByText("2.0")).toBeTruthy();
  });

  it("keeps the trigger enabled with a single track and opens to that entry", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "eac3", channels: 6, default: true }],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
      }),
    );

    const trigger = screen.getByRole("button", { name: "Audio tracks" });
    expect(trigger).not.toHaveAttribute("aria-disabled");
    expect(trigger).not.toHaveClass("cursor-default");
    expect(trigger).not.toHaveClass("opacity-40");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu")).toBeNull();

    fireEvent.click(trigger);

    expect(trigger).toHaveAttribute("aria-expanded", "true");
    const entry = screen.getByRole("menuitem");
    expect(entry).toHaveClass("text-blue-400");
    expect(entry).toHaveTextContent("English");
    expect(entry).toHaveTextContent("EAC3");
    expect(entry).toHaveTextContent("5.1");
    expect(entry).toHaveTextContent("Default");
    expect(entry).toHaveTextContent("\u2713");
  });

  it("locks the menu shut while the session inventory is switching", () => {
    const onSelect = vi.fn();
    const tracks = [
      { title: "English", codec: "eac3", channels: 6, default: true },
      { title: "French", codec: "aac", channels: 2 },
    ];
    render(
      createElement(AudioTrackMenu, {
        tracks,
        activeIndex: 0,
        onSelect,
        currentPosition: 0,
        locked: true,
      }),
    );

    const trigger = screen.getByRole("button", { name: "Audio tracks" });
    expect(trigger).toBeDisabled();
    expect(trigger).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("closes an already-open menu when the inventory is locked", () => {
    const onSelect = vi.fn();
    const tracks = [
      { title: "English", codec: "eac3", channels: 6, default: true },
      { title: "French", codec: "aac", channels: 2 },
    ];
    const props = {
      tracks,
      activeIndex: 0,
      onSelect,
      currentPosition: 0,
    };
    const view = render(createElement(AudioTrackMenu, { ...props, open: true, hideTrigger: true }));
    expect(screen.getAllByRole("menuitem")).toHaveLength(2);

    view.rerender(
      createElement(AudioTrackMenu, { ...props, open: true, hideTrigger: true, locked: true }),
    );
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("renders nothing when there are no tracks", () => {
    const { container } = render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: -1,
        onSelect: () => {},
        currentPosition: 0,
      }),
    );
    expect(container.firstChild).toBeNull();
    expect(screen.queryByRole("button", { name: "Audio tracks" })).toBeNull();
  });

  it("shows an empty applied inventory as no selection, not row 0", () => {
    // The marker is what the session carries when the committed selection has
    // no row in the applied inventory. It must render no active row while the
    // menu itself still renders the inventory.
    const marker = resolveAppliedAudioIndex([], [], null);
    expect(marker).toBe(-1);

    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "eac3", channels: 6 }],
        activeIndex: marker < 0 ? null : marker,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: true,
      }),
    );

    const entry = screen.getByRole("menuitem");
    expect(entry).toHaveTextContent("English");
    expect(entry).not.toHaveClass("text-blue-400");
    expect(entry).not.toHaveTextContent("\u2713");
  });

  it("keeps multiple-track selection behavior unchanged", () => {
    const onSelect = vi.fn();
    render(
      createElement(AudioTrackMenu, {
        tracks: [
          { title: "English", codec: "eac3", channels: 6, default: true },
          { title: "French", codec: "aac", channels: 2 },
        ],
        activeIndex: 0,
        onSelect,
        currentPosition: 0,
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Audio tracks" }));
    const entries = screen.getAllByRole("menuitem");
    expect(entries).toHaveLength(2);
    const [firstEntry, secondEntry] = entries;
    if (!firstEntry || !secondEntry) {
      throw new Error("expected two menu entries");
    }
    expect(firstEntry).toHaveClass("text-blue-400");
    expect(secondEntry).not.toHaveClass("text-blue-400");

    fireEvent.click(secondEntry);
    expect(onSelect).toHaveBeenCalledWith(1, 0);
  });

  it("collapses identical descriptors and selects the first entry's original slot", () => {
    const onSelect = vi.fn();
    const tracks = [
      ...Array.from({ length: 7 }, (_, i) => ({
        title: `Track ${i}`,
        codec: "aac",
        channels: 2,
        language: `l${i}`,
        index: i,
      })),
      // Same descriptor at two container indexes (7 and 9), as a probed
      // multi-language release can carry. The first wins.
      { title: "English AC3", codec: "ac3", channels: 6, language: "en", index: 7 },
      { title: "Commentary", codec: "ac3", channels: 2, language: "en", index: 8 },
      { title: "English AC3", codec: "ac3", channels: 6, language: "en", index: 9 },
    ];

    render(
      createElement(AudioTrackMenu, {
        tracks,
        activeIndex: 7,
        onSelect,
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: true,
      }),
    );

    const entries = screen.getAllByRole("menuitem");
    // One duplicate collapsed: 10 tracks become 9 rows.
    expect(entries).toHaveLength(9);
    // The retained English entry keeps inventory slot 7, and its active state
    // still matches the plan's selection.
    const retained = entries[7]!;
    expect(retained).toHaveTextContent("English AC3");
    expect(retained).toHaveClass("text-blue-400");

    fireEvent.click(retained);
    expect(onSelect).toHaveBeenCalledWith(7, 0);
  });

  it("does not collapse tracks that differ in language", () => {
    renderMenu([
      { title: "English", codec: "ac3", channels: 6, language: "en" },
      { title: "English", codec: "ac3", channels: 6, language: "es" },
    ]);

    expect(screen.getAllByRole("menuitem")).toHaveLength(2);
  });

  it("marks the menu unverified while the inventory is provisional", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "eac3", channels: 6 }],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: true,
        provisional: true,
      }),
    );

    expect(screen.getByText("Unverified")).toBeTruthy();
  });

  it("hides the unverified marker once the inventory is verified", () => {
    renderMenu([{ title: "English", codec: "eac3", channels: 6 }]);

    expect(screen.queryByText("Unverified")).toBeNull();
  });

  it("shows discovery state when tracks are empty but inventory is provisional", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: true,
      }),
    );

    expect(screen.getByText("Discovering audio tracks…")).toBeTruthy();
    expect(screen.getByText("Unverified")).toBeTruthy();
  });

  it("shows failure state when discovery failed and tracks are empty", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: true,
        failed: true,
      }),
    );

    expect(screen.getByText("Track discovery failed")).toBeTruthy();
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
  });

  it("shows declared track rows immediately and marks them unverified", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "aac", channels: 2 }],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: true,
      }),
    );

    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.getByText("From release listing")).toBeTruthy();
    expect(screen.getByText("Unverified")).toBeTruthy();
  });

  it("swaps declared rows for probe evidence when it arrives", () => {
    const tracks = [{ title: "English", codec: "aac", channels: 2 }];
    const props = {
      tracks,
      activeIndex: 0,
      onSelect: () => {},
      currentPosition: 0,
      open: true,
      onOpenChange: () => {},
      hideTrigger: false,
      provisional: true,
    };
    const view = render(createElement(AudioTrackMenu, props));
    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.getByText("From release listing")).toBeTruthy();

    view.rerender(createElement(AudioTrackMenu, { ...props, provisional: false }));

    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.queryByText("From release listing")).toBeNull();
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.queryByText("Unverified")).toBeNull();
  });

  it("shows failure banner even when declared tracks are present", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "aac", channels: 2 }],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: true,
        failed: true,
      }),
    );

    expect(screen.getByText("Showing available tracks — discovery did not complete")).toBeTruthy();
    expect(screen.getByText("English")).toBeTruthy();
  });

  it("does not claim discovery failed above a verified, populated list", () => {
    // Regression: a timed-out poll sets `failed` while a plan/catalog refresh
    // has already supplied the confident, correct list. The failure notice must
    // not qualify a verified list.
    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "eac3", channels: 2 }],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: false,
        failed: true,
      }),
    );

    expect(screen.queryByText("Showing available tracks — discovery did not complete")).toBeNull();
    expect(screen.queryByText("Track discovery failed")).toBeNull();
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.getByText("English")).toBeTruthy();
  });

  it("shows the empty state, not a failure, for a verified empty list with a stale failed flag", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: false,
        failed: true,
      }),
    );

    expect(screen.getByText("No audio tracks available")).toBeTruthy();
    expect(screen.queryByText("Track discovery failed")).toBeNull();
  });

  it("does not show the discovery spinner over a verified list while the poll is still owed", () => {
    // A refreshed plan can supply the confident list while the deferred poll
    // is still pending; `pending` alone must not claim discovery in that state.
    render(
      createElement(AudioTrackMenu, {
        tracks: [{ title: "English", codec: "eac3", channels: 2 }],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: false,
        pending: true,
      }),
    );

    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.queryByText("Unverified")).toBeNull();
    expect(screen.getByText("English")).toBeTruthy();
  });

  it("renders explicit empty state when tracks are empty and open while verified", () => {
    render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        provisional: false,
        failed: false,
      }),
    );

    expect(screen.getByText("No audio tracks available")).toBeTruthy();
  });

  it("returns null when tracks are empty and closed while verified", () => {
    const { container } = render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: false,
        onOpenChange: () => {},
        provisional: false,
        failed: false,
      }),
    );

    expect(container.firstChild).toBeNull();
  });
});

describe("AudioTrackMenu discovery bound", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  function renderProvisional(onRetryDiscovery?: () => void) {
    return render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: true,
        provisional: true,
        onRetryDiscovery,
      }),
    );
  }

  it("replaces the unbounded spinner with an explicit retry when discovery stalls", () => {
    vi.useFakeTimers();
    renderProvisional();

    expect(screen.getByText("Discovering audio tracks…")).toBeTruthy();
    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });

    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.getByText("Audio tracks unavailable.")).toBeTruthy();
  });

  it("re-triggers discovery and shows progress again on retry", () => {
    vi.useFakeTimers();
    const onRetryDiscovery = vi.fn();
    renderProvisional(onRetryDiscovery);

    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });
    fireEvent.click(screen.getByRole("menuitem", { name: "Retry" }));

    expect(onRetryDiscovery).toHaveBeenCalledOnce();
    expect(screen.getByText("Discovering audio tracks…")).toBeTruthy();
    expect(screen.queryByText("Audio tracks unavailable.")).toBeNull();

    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });
    expect(screen.getByText("Audio tracks unavailable.")).toBeTruthy();
  });

  it("returns to live rows when the retried inventory lands", () => {
    vi.useFakeTimers();
    const tracks = [{ title: "English", codec: "aac", channels: 2 }];
    const props = {
      tracks: [],
      activeIndex: 0,
      onSelect: () => {},
      currentPosition: 0,
      open: true,
      onOpenChange: () => {},
      hideTrigger: true,
      provisional: true,
    };
    const view = render(createElement(AudioTrackMenu, props));
    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });
    expect(screen.getByText("Audio tracks unavailable.")).toBeTruthy();

    view.rerender(createElement(AudioTrackMenu, { ...props, tracks, provisional: false }));

    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.queryByText("Audio tracks unavailable.")).toBeNull();
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
  });

  it("offers the same retry beside a terminal discovery failure", () => {
    vi.useFakeTimers();
    const onRetryDiscovery = vi.fn();
    render(
      createElement(AudioTrackMenu, {
        tracks: [],
        activeIndex: 0,
        onSelect: () => {},
        currentPosition: 0,
        open: true,
        onOpenChange: () => {},
        hideTrigger: false,
        provisional: true,
        failed: true,
        onRetryDiscovery,
      }),
    );

    expect(screen.getByText("Track discovery failed")).toBeTruthy();
    fireEvent.click(screen.getByRole("menuitem", { name: "Retry" }));
    expect(onRetryDiscovery).toHaveBeenCalledOnce();
  });
});
