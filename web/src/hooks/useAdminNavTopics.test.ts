import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";

import { useAdminNavTopics, useCloseAdminNavTopicsOnExit } from "./useAdminNavTopics";

const KEY = "silo.admin-nav.open-topics";

afterEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});

// The desktop sidebar and the phone drawer each run the hook at once.
it("keeps a choice made in one sidebar when the other toggles a different topic", () => {
  const desktop = renderHook(() => useAdminNavTopics(null));
  const drawer = renderHook(() => useAdminNavTopics(null));

  act(() => drawer.result.current.toggle("playback"));
  act(() => desktop.result.current.toggle("server"));

  expect(JSON.parse(sessionStorage.getItem(KEY) ?? "{}")).toEqual({ playback: true, server: true });
});

it("opens the topic holding the current page and remembers other toggles", () => {
  const { result } = renderHook(() => useAdminNavTopics("server"));
  expect(result.current.isOpen("server")).toBe(true);
  expect(result.current.isOpen("playback")).toBe(false);

  act(() => result.current.toggle("playback"));
  expect(result.current.isOpen("playback")).toBe(true);
  expect(JSON.parse(sessionStorage.getItem(KEY) ?? "{}")).toEqual({ playback: true });
});

it("keeps the open topics while the admin layout stays mounted, reloads included", () => {
  sessionStorage.setItem(KEY, JSON.stringify({ server: true }));
  renderHook(() => useCloseAdminNavTopicsOnExit());

  const { result } = renderHook(() => useAdminNavTopics("libraries"));
  expect(result.current.isOpen("server")).toBe(true);
});

it("closes every topic once the admin layout unmounts", () => {
  sessionStorage.setItem(KEY, JSON.stringify({ server: true }));
  localStorage.setItem(KEY, JSON.stringify({ logs: true }));
  const layout = renderHook(() => useCloseAdminNavTopicsOnExit());

  layout.unmount();

  expect(sessionStorage.getItem(KEY)).toBeNull();
  expect(localStorage.getItem(KEY)).toBeNull();
  const { result } = renderHook(() => useAdminNavTopics(null));
  expect(result.current.isOpen("server")).toBe(false);
});
