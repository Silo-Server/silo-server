import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";

import { useAdminNavTopics } from "./useAdminNavTopics";

const KEY = "silo.admin-nav.open-topics";

afterEach(() => localStorage.clear());

// The desktop sidebar and the phone drawer each run the hook at once.
it("keeps a choice made in one sidebar when the other toggles a different topic", () => {
  const desktop = renderHook(() => useAdminNavTopics(null));
  const drawer = renderHook(() => useAdminNavTopics(null));

  act(() => drawer.result.current.toggle("playback"));
  act(() => desktop.result.current.toggle("server"));

  expect(JSON.parse(localStorage.getItem(KEY) ?? "{}")).toEqual({ playback: true, server: true });
});

it("opens the topic holding the current page and remembers other toggles", () => {
  const { result } = renderHook(() => useAdminNavTopics("server"));
  expect(result.current.isOpen("server")).toBe(true);
  expect(result.current.isOpen("playback")).toBe(false);

  act(() => result.current.toggle("playback"));
  expect(result.current.isOpen("playback")).toBe(true);
  expect(JSON.parse(localStorage.getItem(KEY) ?? "{}")).toEqual({ playback: true });
});
