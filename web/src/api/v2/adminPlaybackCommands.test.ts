import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
  StaleApiRequestContextError,
} from "@/api/client";
import {
  allocateAdminPlaybackCommand,
  captureAdminPlaybackCommandAuthority,
  LATEST_SEQUENCE_HEADER,
  latestSequenceOf,
  observeAdminPlaybackSequence,
  sendAdminPlaybackCommand,
} from "./adminPlaybackCommands";

function response(body: unknown, status = 202, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
      ...headers,
    },
  });
}

const staleProblem = {
  type: "https://siloserver.org/docs/api/v2/problems/conflict",
  title: "Conflict",
  status: 409,
  detail: "The command sequence is behind the session's latest applied command.",
};

const receipt = {
  command_id: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  sequence: 7,
  outcome: "applied",
  delivery: "dispatched",
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

it("allocates a rising sequence per session and a fresh command id per call", () => {
  const first = allocateAdminPlaybackCommand("s-1");
  const second = allocateAdminPlaybackCommand("s-1");
  const other = allocateAdminPlaybackCommand("s-2");
  expect(second.sequence).toBeGreaterThan(first.sequence);
  expect(first.command_id).not.toBe(second.command_id);
  expect(first.command_id).toMatch(/^[0-9a-f-]{36}$/);
  expect(other.sequence).toBeGreaterThan(0);
});

it("sends each action with the exact identity body and never a terminate", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => response(receipt));
  vi.stubGlobal("fetch", fetch);
  const identity = { command_id: receipt.command_id, sequence: 7 };
  for (const action of ["pause", "resume", "stop"] as const) {
    fetch.mockClear();
    const out = await sendAdminPlaybackCommand({ sessionId: "s-1", action, identity });
    expect(out).toEqual(receipt);
    expect(String(fetch.mock.calls[0]![0])).toContain(`/api/v2/admin/sessions/s-1/${action}`);
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual(identity);
  }
  fetch.mockClear();
  await sendAdminPlaybackCommand({
    sessionId: "s-1",
    action: "message",
    identity,
    message: "  Movie night ends at nine  ",
    title: "Heads up",
  });
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({
    ...identity,
    message: "Movie night ends at nine",
    title: "Heads up",
  });
  await expect(
    sendAdminPlaybackCommand({ sessionId: "s-1", action: "message", identity, message: " " }),
  ).rejects.toThrow("Message is required");
  await expect(
    sendAdminPlaybackCommand({
      sessionId: "s-1",
      action: "terminate" as unknown as "stop",
      identity,
    }),
  ).rejects.toThrow("Unsupported session command");
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("surfaces a replayed receipt and refuses a mismatched command identity", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementationOnce(async () => response({ ...receipt, outcome: "replayed" }, 200))
    .mockImplementationOnce(async () => response({ ...receipt, command_id: "other" }, 202));
  vi.stubGlobal("fetch", fetch);
  const identity = { command_id: receipt.command_id, sequence: 7 };
  const replayed = await sendAdminPlaybackCommand({ sessionId: "s-1", action: "pause", identity });
  expect(replayed.outcome).toBe("replayed");
  await expect(
    sendAdminPlaybackCommand({ sessionId: "s-1", action: "pause", identity }),
  ).rejects.toThrow("different command identity");
});

it("throws the stale and conflict problems without retrying", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(async () => response(staleProblem, 409));
  vi.stubGlobal("fetch", fetch);
  await expect(
    sendAdminPlaybackCommand({
      sessionId: "s-1",
      action: "resume",
      identity: { command_id: receipt.command_id, sequence: 3 },
    }),
  ).rejects.toMatchObject({ status: 409, problemType: "conflict" });
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("allocates above the latest sequence another administrator applied", async () => {
  // Another browser, with a clock far ahead of this one, already applied a
  // command at a sequence this allocator's clock will not reach for a while.
  const ahead = Date.now() + 60 * 60 * 1000;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementationOnce(async () =>
      response(staleProblem, 409, { [LATEST_SEQUENCE_HEADER]: String(ahead) }),
    )
    .mockImplementation(async () => response(receipt));
  vi.stubGlobal("fetch", fetch);
  const refused = allocateAdminPlaybackCommand("s-skew");
  expect(refused.sequence).toBeLessThan(ahead);
  const failure = await sendAdminPlaybackCommand({
    sessionId: "s-skew",
    action: "pause",
    identity: refused,
  }).catch((error: unknown) => error);
  expect(latestSequenceOf(failure)).toBe(ahead);
  // The refusal raised the floor: the next click lands above the winner and
  // the same intent is applied.
  const next = allocateAdminPlaybackCommand("s-skew");
  expect(next.sequence).toBeGreaterThan(ahead);
  expect(allocateAdminPlaybackCommand("s-other").sequence).toBeLessThan(ahead);
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("ignores a floor that is not a positive integer or is behind the allocator", () => {
  const before = allocateAdminPlaybackCommand("s-floor").sequence;
  observeAdminPlaybackSequence("s-floor", 1);
  observeAdminPlaybackSequence("s-floor", -5);
  observeAdminPlaybackSequence("s-floor", Number.NaN);
  expect(allocateAdminPlaybackCommand("s-floor").sequence).toBeGreaterThan(before);
  expect(latestSequenceOf(new Error("plain"))).toBeNull();
});

it("refuses a superseded authority before and after the request", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => {
    setProfileId("someone-else");
    return response(receipt);
  });
  vi.stubGlobal("fetch", fetch);
  const context = captureAdminPlaybackCommandAuthority();
  await expect(
    sendAdminPlaybackCommand(
      {
        sessionId: "s-1",
        action: "pause",
        identity: { command_id: receipt.command_id, sequence: 7 },
      },
      context,
    ),
  ).rejects.toBeInstanceOf(StaleApiRequestContextError);
  await expect(
    sendAdminPlaybackCommand(
      {
        sessionId: "s-1",
        action: "pause",
        identity: { command_id: receipt.command_id, sequence: 8 },
      },
      context,
    ),
  ).rejects.toBeInstanceOf(StaleApiRequestContextError);
  expect(fetch).toHaveBeenCalledTimes(1);
});
