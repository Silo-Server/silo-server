// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import WatchPartyInvite from "./WatchPartyInvite";

vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

const IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15";
const IPAD = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15";
const ANDROID = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/140.0";
const DESKTOP = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0";

function Hub() {
  const { pathname, search } = useLocation();
  return <p>Hub at {pathname + search}</p>;
}

function mount(entry: string, ua: string, touchPoints = 0) {
  vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(ua);
  // jsdom doesn't implement maxTouchPoints.
  Object.defineProperty(window.navigator, "maxTouchPoints", {
    configurable: true,
    value: touchPoints,
  });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/rooms/join" element={<WatchPartyInvite />} />
        <Route path="/rooms" element={<Hub />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("offers the app on iPhone with the silo://watch-party link", () => {
  mount("/rooms/join?token=abc%2B1", IPHONE);
  const link = screen.getByRole("link", { name: /open in the silo app/i });
  const server = encodeURIComponent(window.location.origin);
  expect(link.getAttribute("href")).toBe(`silo://watch-party?server=${server}&token=abc%2B1`);
});

it("continues to the hub with the invite query intact", () => {
  mount("/rooms/join?token=abc", IPHONE);
  fireEvent.click(screen.getByRole("link", { name: /continue in the browser/i }));
  expect(screen.getByText("Hub at /rooms?token=abc")).toBeTruthy();
});

it("treats iPadOS Safari's desktop user agent as iOS", () => {
  mount("/rooms/join?token=abc", IPAD, 5);
  expect(screen.getByRole("link", { name: /open in the silo app/i })).toBeTruthy();
});

it.each([
  ["desktop", DESKTOP, 0],
  ["a Mac", IPAD, 0],
  // silo-android doesn't register the watch-party host yet.
  ["Android", ANDROID, 5],
])("forwards straight to the hub on %s", (_name, ua, touchPoints) => {
  mount("/rooms/join?token=abc", ua, touchPoints);
  expect(screen.getByText("Hub at /rooms?token=abc")).toBeTruthy();
  expect(screen.queryByRole("link", { name: /open in the silo app/i })).toBeNull();
});

it("forwards to the hub when the link has no token", () => {
  mount("/rooms/join", IPHONE);
  expect(screen.getByText("Hub at /rooms")).toBeTruthy();
});
