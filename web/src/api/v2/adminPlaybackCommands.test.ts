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
  latestSequenceOf,
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

it("allocates above the latest sequence another administrator applied", async () => {
  // Another browser, with a clock far ahead of this one, already applied a
  // command at a sequence this allocator's clock will not reach for a while.
  const ahead = Date.now() + 60 * 60 * 1000;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementationOnce(async () =>
      response(staleProblem, 409, { "X-Silo-Latest-Sequence": String(ahead) }),
    )
    .mockImplementationOnce(async () =>
      response(staleProblem, 409, { "X-Silo-Latest-Sequence": String(ahead - 30 * 60 * 1000) }),
    );
  vi.stubGlobal("fetch", fetch);
  const refused = allocateAdminPlaybackCommand("s-skew");
  expect(refused.sequence).toBeLessThan(ahead);
  const failure = await sendAdminPlaybackCommand({
    sessionId: "s-skew",
    action: "pause",
    identity: refused,
  }).catch((error: unknown) => error);
  expect(failure).toMatchObject({ status: 409, problemType: "conflict" });
  expect(latestSequenceOf(failure)).toBe(ahead);
  expect(latestSequenceOf(new Error("plain"))).toBeNull();
  // The refusal raises the next allocation above the applied command.
  const next = allocateAdminPlaybackCommand("s-skew");
  expect(next.sequence).toBeGreaterThan(ahead);
  expect(allocateAdminPlaybackCommand("s-other").sequence).toBeLessThan(ahead);
  // A delayed refusal with a lower floor must not move this browser backward.
  await expect(
    sendAdminPlaybackCommand({ sessionId: "s-skew", action: "pause", identity: next }),
  ).rejects.toMatchObject({ status: 409, problemType: "conflict" });
  expect(allocateAdminPlaybackCommand("s-skew").sequence).toBeGreaterThan(next.sequence);
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("repeats a command whose outcome is unknown with the same identity, and only that command", async () => {
  const noReply = {
    type: "https://siloserver.org/docs/api/v2/problems/dependency_unavailable",
    title: "Service Unavailable",
    status: 503,
    detail: "The server holding this playback session did not answer in time.",
  };
  // 503: the replica holding the lane did not answer; 200/202: a receipt.
  const statuses = [503, 200, 503, 202, 503, 202, 503, 202];
  const bodies: Array<{ command_id: string; sequence: number }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>().mockImplementation(async (_url, init) => {
      const body = JSON.parse(String(init?.body));
      bodies.push(body);
      const status = statuses.shift()!;
      return status === 503 ? response(noReply, 503) : response({ ...receipt, ...body }, status);
    }),
  );
  const pause = { sessionId: "s-retry", action: "pause" } as const;
  const send = (request: Parameters<typeof sendAdminPlaybackCommand>[0]) =>
    sendAdminPlaybackCommand(request).catch((error: unknown) => error);

  // The owner did not answer: the repeat carries the same identity.
  await send(pause);
  await send(pause);
  expect(bodies[1]).toEqual(bodies[0]);

  // That answer settled it; the next pause is a new command.
  await send(pause);
  expect(bodies[2]!.command_id).not.toBe(bodies[0]!.command_id);
  expect(bodies[2]!.sequence).toBeGreaterThan(bodies[0]!.sequence);

  // A different command for the session is not a repeat of the unknown one.
  await send({ ...pause, action: "resume" });
  expect(bodies[3]!.command_id).not.toBe(bodies[2]!.command_id);

  // Nor is the same command under another profile.
  await send(pause);
  setProfileId("someone-else");
  await send(pause);
  expect(bodies[5]!.command_id).not.toBe(bodies[4]!.command_id);

  // Switching away and back installs the profile's proof again: also a change.
  await send(pause);
  setProfileId("owner");
  setProfileToken(null);
  setProfileId("someone-else");
  setProfileToken(null);
  await send(pause);
  expect(bodies[7]!.command_id).not.toBe(bodies[6]!.command_id);
});

it("does not reopen a command that another request already settled", async () => {
  const noReply = {
    type: "https://siloserver.org/docs/api/v2/problems/dependency_unavailable",
    title: "Service Unavailable",
    status: 503,
  };
  const bodies: Array<{ command_id: string; sequence: number }> = [];
  let answerLate!: () => void;
  const late = new Promise<void>((resolve) => (answerLate = resolve));
  const plan = [
    async () => response(noReply, 503),
    // Two pages repeat the unknown pause at once: one is answered, then the
    // other's 503 arrives late.
    async () => {
      await late;
      return response(noReply, 503);
    },
    async (body: object) => response({ ...receipt, ...body }, 200),
    async (body: object) => response({ ...receipt, ...body }, 202),
  ];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>().mockImplementation(async (_url, init) => {
      const body = JSON.parse(String(init?.body));
      bodies.push(body);
      return plan.shift()!(body);
    }),
  );
  const pause = { sessionId: "s-late", action: "pause" } as const;
  const send = () => sendAdminPlaybackCommand(pause).catch((error: unknown) => error);

  await send();
  const lateRepeat = send();
  await send();
  answerLate();
  await lateRepeat;
  expect(bodies[1]).toEqual(bodies[0]);
  expect(bodies[2]).toEqual(bodies[0]);

  await send();
  expect(bodies[3]!.command_id).not.toBe(bodies[0]!.command_id);
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
