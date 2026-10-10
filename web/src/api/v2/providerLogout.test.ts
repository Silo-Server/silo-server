import { afterEach, expect, it, vi } from "vitest";
import { PROVIDER_LOGOUT_LOOKUP_TIMEOUT_MS, endSessionWithProvider } from "./providerLogout";

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });

afterEach(() => vi.unstubAllGlobals());

function stubFetch(providerLogout: Response) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(
        `${init?.method ?? "GET"} ${url} ${new Headers(init?.headers).get("Authorization")}`,
      );
      return url.endsWith("/provider-logout")
        ? providerLogout
        : new Response(null, { status: 204 });
    }),
  );
  return calls;
}

it("asks for the end-session URL before logging out, then goes there", async () => {
  const calls = stubFetch(json(200, { end_session_url: "https://id.example.test/logout?x=1" }));
  const navigate = vi.fn();
  await endSessionWithProvider("tok", navigate);
  expect(calls).toEqual([
    "GET /api/v2/auth/provider-logout Bearer tok",
    "POST /api/v2/auth/logout Bearer tok",
  ]);
  expect(navigate).toHaveBeenCalledWith("https://id.example.test/logout?x=1");
});

it("only logs out of Silo when the provider offers no URL", async () => {
  const calls = stubFetch(json(200, { end_session_url: "" }));
  const navigate = vi.fn();
  await endSessionWithProvider("tok", navigate);
  expect(calls).toHaveLength(2);
  expect(navigate).not.toHaveBeenCalled();
});

it("still logs out when the server lacks the operation", async () => {
  const calls = stubFetch(
    json(404, { type: "about:blank", title: "Not found", status: 404, detail: "x" }),
  );
  const navigate = vi.fn();
  await endSessionWithProvider("tok", navigate);
  expect(calls[1]).toBe("POST /api/v2/auth/logout Bearer tok");
  expect(navigate).not.toHaveBeenCalled();
});

it("never navigates to a non-http URL", async () => {
  stubFetch(json(200, { end_session_url: "javascript:alert(1)" }));
  const navigate = vi.fn();
  await endSessionWithProvider("tok", navigate);
  expect(navigate).not.toHaveBeenCalled();
});

it("leaves the provider alone when asked to end only the Silo session", async () => {
  const calls = stubFetch(json(200, { end_session_url: "https://id.example.test/logout" }));
  const navigate = vi.fn();
  await endSessionWithProvider("tok", navigate, { withProvider: false });
  expect(calls).toEqual(["POST /api/v2/auth/logout Bearer tok"]);
  expect(navigate).not.toHaveBeenCalled();
});

it("logs out of Silo when the provider lookup hangs past its bound", async () => {
  const bound = new AbortController();
  const timeout = vi.spyOn(AbortSignal, "timeout").mockReturnValue(bound.signal);
  try {
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        calls.push(`${init?.method ?? "GET"} ${url}`);
        if (!url.endsWith("/provider-logout")) {
          return Promise.resolve(new Response(null, { status: 204 }));
        }
        return new Promise<Response>((_, reject) => {
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("", "TimeoutError")),
          );
        });
      }),
    );
    const navigate = vi.fn();
    const done = endSessionWithProvider("tok", navigate);
    expect(timeout).toHaveBeenCalledWith(PROVIDER_LOGOUT_LOOKUP_TIMEOUT_MS);
    expect(calls).toEqual(["GET /api/v2/auth/provider-logout"]);
    bound.abort();
    await done;
    expect(calls).toEqual(["GET /api/v2/auth/provider-logout", "POST /api/v2/auth/logout"]);
    expect(navigate).not.toHaveBeenCalled();
  } finally {
    timeout.mockRestore();
  }
});
