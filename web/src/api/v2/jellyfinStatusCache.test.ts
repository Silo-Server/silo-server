import { afterEach, beforeEach, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, setAccessToken, setProfileId } from "@/api/client";
import type { JellyfinCompatStatus, JellyfinCompatOperationStatus } from "@/api/types";
import { applyJellyfinCompatOperationUpdate } from "@/components/RealtimeEventsProvider";
import { jellyfinCompatStatusKey } from "./jellyfinStatusCache";
import { adminKeys } from "@/hooks/queries/keys";

let client: QueryClient;
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin-session");
  setProfileId("primary");
  client = new QueryClient();
});
afterEach(() => client.clear());
const initial = {
  enabled: true,
  web_state: "installing",
  prerequisites: [],
} as unknown as JellyfinCompatStatus;
const progress = {
  id: "install",
  kind: "install",
  state: "running",
  started_at: "2026-09-06T00:00:00.000Z",
  phase: "building",
  progress_percent: 60,
} satisfies JellyfinCompatOperationStatus;

it("publishes live progress to the actual scoped status key only", () => {
  const authority = captureProfileRequestContext()!;
  const active = jellyfinCompatStatusKey(authority);
  const foreign = jellyfinCompatStatusKey({ ...authority, profileId: "other" });
  client.setQueryData(active, initial);
  client.setQueryData(foreign, initial);
  client.setQueryData(adminKeys.jellyfinCompatStatus(), initial);
  applyJellyfinCompatOperationUpdate(client, progress, authority);
  expect(client.getQueryData(active)).toMatchObject({ operation: progress });
  expect(client.getQueryData(foreign)).toEqual(initial);
  expect(client.getQueryData(adminKeys.jellyfinCompatStatus())).toEqual(initial);
});
it("refuses an event from a replaced socket authority", () => {
  const old = captureProfileRequestContext()!;
  setProfileId("replacement");
  const active = jellyfinCompatStatusKey(captureProfileRequestContext()!);
  client.setQueryData(active, initial);
  client.setQueryData(jellyfinCompatStatusKey(old), initial);
  applyJellyfinCompatOperationUpdate(client, progress, old);
  expect(client.getQueryData(active)).toEqual(initial);
  expect(client.getQueryData(jellyfinCompatStatusKey(old))).toEqual(initial);
});
it("invalidates only the matching authority on completion", () => {
  const authority = captureProfileRequestContext()!;
  const active = jellyfinCompatStatusKey(authority),
    foreign = jellyfinCompatStatusKey({ ...authority, profileId: "other" });
  client.setQueryData(active, initial);
  client.setQueryData(foreign, initial);
  applyJellyfinCompatOperationUpdate(client, { ...progress, state: "succeeded" }, authority);
  expect(client.getQueryState(active)?.isInvalidated).toBe(true);
  expect(client.getQueryState(foreign)?.isInvalidated).toBe(false);
});
