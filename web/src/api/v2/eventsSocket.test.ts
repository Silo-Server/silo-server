import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import {
  captureEventsAuthority,
  isEventsAuthorityActive,
  mintEventsSocketTicket,
} from "./eventsSocket";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("session-access");
  setProfileId(null);
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("captures account-only authority and refuses it after profile selection", () => {
  const authority = captureEventsAuthority()!;
  expect(authority.profileId).toBe("");
  expect(isEventsAuthorityActive(authority)).toBe(true);
  setProfileId("selected");
  expect(isEventsAuthorityActive(authority)).toBe(false);
});
it("refuses dispatch after captured profile changes", async () => {
  setProfileId("first");
  const authority = captureEventsAuthority()!;
  setProfileId("second");
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(mintEventsSocketTicket(authority)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
it("refuses a delayed ticket after the login account changes", async () => {
  let finish!: (response: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const pending = mintEventsSocketTicket(captureEventsAuthority()!);
  setAccessToken("replacement-session");
  finish(
    new Response(JSON.stringify({ ticket: "a".repeat(43), protocol: "silo.events.v2" }), {
      headers: { "Content-Type": "application/json" },
    }),
  );
  await expect(pending).rejects.toThrow();
  expect(String(fetch.mock.calls[0]![0])).toBe("/api/v2/events/ws-ticket");
});
