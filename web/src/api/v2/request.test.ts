import { beforeEach, describe, expect, expectTypeOf, it, vi } from "vitest";

import getCurrentUserOk from "../../../../contracts/api/v2/fixtures/get_current_user_ok.json";
import getSetupStatusOk from "../../../../contracts/api/v2/fixtures/get_setup_status_ok.json";
import listAdminUsersOk from "../../../../contracts/api/v2/fixtures/list_admin_users_ok.json";
import listProgressOk from "../../../../contracts/api/v2/fixtures/list_progress_ok.json";
import updateProfileOk from "../../../../contracts/api/v2/fixtures/update_profile_ok.json";
import authenticationRequired from "../../../../contracts/api/v2/fixtures/authentication_required.json";
import notAcceptable from "../../../../contracts/api/v2/fixtures/not_acceptable.json";
import unsupportedMediaType from "../../../../contracts/api/v2/fixtures/unsupported_media_type.json";
import validationFailedBody from "../../../../contracts/api/v2/fixtures/validation_failed_body.json";
import profileVerificationRequired from "../../../../contracts/api/v2/fixtures/profile_verification_required.json";

import type { AdminUser, User } from "../types";
import {
  onProfileUnverified,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "../client";
import {
  V2ProblemError,
  V2TransportError,
  problemId,
  v2,
  type V2Body,
  type V2Result,
} from "./request";

const JSON_HEADERS = { "Content-Type": "application/json" };
const PROBLEM_HEADERS = { "Content-Type": "application/problem+json" };

function json(body: unknown, status = 200, headers: Record<string, string> = JSON_HEADERS) {
  return new Response(JSON.stringify(body), { status, headers });
}

function lastRequest(fetchMock: ReturnType<typeof vi.fn<typeof fetch>>) {
  const call = fetchMock.mock.calls[fetchMock.mock.calls.length - 1];
  if (!call) throw new Error("fetch was not called");
  return {
    url: String(call[0]),
    init: call[1] as RequestInit & { headers: Record<string, string> },
  };
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken(null);
  setRefreshToken(null);
  setProfileId(null);
  setProfileToken(null);
  onProfileUnverified(null);
});

describe("v2 request boundary", () => {
  it("decodes the committed 2xx fixtures and sends the contract headers", async () => {
    setAccessToken("tok-user");
    setProfileId("p-owner");
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url === "/api/v2/system/setup") return json(getSetupStatusOk);
      if (url === "/api/v2/account/me") return json(getCurrentUserOk);
      if (url === "/api/v2/admin/users") return json(listAdminUsersOk);
      if (url.startsWith("/api/v2/progress?")) return json(listProgressOk);
      if (url === "/api/v2/profiles/p-owner") return json(updateProfileOk);
      throw new Error(`unexpected ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const setup = await v2("GET /api/v2/system/setup");
    expect(setup.needs_setup).toBe(false);

    const me = await v2("GET /api/v2/account/me");
    expect(me).toEqual(getCurrentUserOk);
    expect(me.role).toBe("user");
    expectTypeOf(me.id).toEqualTypeOf<string>();

    const users = await v2("GET /api/v2/admin/users");
    expect(users.items).toHaveLength(listAdminUsersOk.items.length);
    expect(users.items[0]?.effective_policy.library_ids).toEqual(["3"]);

    const progress = await v2("GET /api/v2/progress", {
      query: { status: "in_progress", limit: 1, library_id: undefined },
    });
    expect(progress.items[0]?.media_item_id).toBe("movie-8f2c1a");
    expect(progress.page?.has_more).toBe(true);
    expect(progress.page?.next_cursor).toBe(listProgressOk.page.next_cursor);
    const progressRequest = lastRequest(fetchMock);
    expect(progressRequest.url).toBe("/api/v2/progress?status=in_progress&limit=1");
    expect(progressRequest.init.method).toBe("GET");
    expect(progressRequest.init.headers).toMatchObject({
      Accept: "application/json",
      Authorization: "Bearer tok-user",
      "X-Profile-Id": "p-owner",
      "X-Silo-Client": "Silo Web",
      "X-Silo-Client-Family": "web",
    });
    expect(progressRequest.init.headers["X-Silo-Client-Version"]).toMatch(/\S/);
    expect(progressRequest.init.body).toBeUndefined();

    const profile = await v2("PATCH /api/v2/profiles/{id}", {
      path: { id: "p-owner" },
      body: { name: "Laura" },
    });
    expect(profile).toEqual(updateProfileOk);
    expect(fetchMock.mock.calls.every(([input]) => !String(input).startsWith("/api/v1"))).toBe(
      true,
    );
  });

  it("sends explicit null on the wire and omits absent PATCH members", async () => {
    setAccessToken("tok-user");
    const fetchMock = vi.fn<typeof fetch>(async () => json(updateProfileOk));
    vi.stubGlobal("fetch", fetchMock);

    await v2("PATCH /api/v2/profiles/{id}", {
      path: { id: "p owner/1" },
      body: { avatar: null, auto_skip_intro: true },
    });

    const { url, init } = lastRequest(fetchMock);
    expect(url).toBe("/api/v2/profiles/p%20owner%2F1");
    expect(init.method).toBe("PATCH");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(init.body).toBe('{"avatar":null,"auto_skip_intro":true}');
    const wire = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(wire.avatar).toBeNull();
    expect("name" in wire).toBe(false);
    expect("pin" in wire).toBe(false);
  });

  it("throws V2ProblemError for a 401 authentication_required problem", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => json(authenticationRequired, 401, PROBLEM_HEADERS)),
    );

    const err = await v2("GET /api/v2/account/me").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(V2ProblemError);
    const problem = err as V2ProblemError;
    expect(problem.operationId).toBe("getCurrentUser");
    expect(problem.status).toBe(401);
    expect(problem.problemType).toBe("authentication_required");
    expect(problem.problem).toEqual(authenticationRequired);
    expect(problem.message).toBe(authenticationRequired.detail);
  });

  it("carries the field errors of a 422 validation_failed problem", async () => {
    setAccessToken("tok-user");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => json(validationFailedBody, 422, PROBLEM_HEADERS)),
    );

    const err = await v2("PATCH /api/v2/profiles/{id}", {
      path: { id: "p-owner" },
      body: { name: "" },
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(V2ProblemError);
    const problem = (err as V2ProblemError).problem;
    expect(problemId(problem)).toBe("validation_failed");
    expect(problem.errors?.length).toBeGreaterThan(0);
    expect(problem.errors?.[0]).toMatchObject({
      location: expect.stringMatching(/^body\./),
      code: expect.any(String),
      detail: expect.any(String),
    });
  });

  it("decodes 406 and 415 problems", async () => {
    setAccessToken("tok-user");
    let call = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => {
        call += 1;
        return call === 1
          ? json(notAcceptable, 406, PROBLEM_HEADERS)
          : json(unsupportedMediaType, 415, PROBLEM_HEADERS);
      }),
    );

    await expect(v2("GET /api/v2/system/setup")).rejects.toMatchObject({
      name: "V2ProblemError",
      status: 406,
      problemType: "not_acceptable",
    });
    await expect(
      v2("PATCH /api/v2/profiles/{id}", { path: { id: "p-owner" }, body: {} }),
    ).rejects.toMatchObject({
      name: "V2ProblemError",
      status: 415,
      problemType: "unsupported_media_type",
    });
  });

  it("reports a non-JSON body as a transport error, not a problem", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(
        async () =>
          new Response("<html><body>502 Bad Gateway</body></html>", {
            status: 502,
            headers: { "Content-Type": "text/html" },
          }),
      ),
    );

    const err = await v2("GET /api/v2/system/setup").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(V2TransportError);
    expect(err).not.toBeInstanceOf(V2ProblemError);
    expect((err as V2TransportError).status).toBe(502);
    expect((err as V2TransportError).operationId).toBe("getSetupStatus");
  });

  it("rethrows a network failure unchanged", async () => {
    const failure = new TypeError("Failed to fetch");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => {
        throw failure;
      }),
    );

    await expect(v2("GET /api/v2/system/setup")).rejects.toBe(failure);
  });

  it("refreshes once and retries after a 401 with an expired token", async () => {
    setAccessToken("expired");
    setRefreshToken("refresh-1");
    setProfileId("p-owner");
    setProfileToken("pin-1");

    let protectedCalls = 0;
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url === "/api/v1/auth/refresh") {
        return json({ access_token: "fresh", refresh_token: "refresh-2", expires_in: 3600 });
      }
      protectedCalls += 1;
      return protectedCalls === 1
        ? json(authenticationRequired, 401, PROBLEM_HEADERS)
        : json(getCurrentUserOk);
    });
    vi.stubGlobal("fetch", fetchMock);

    const me = await v2("GET /api/v2/account/me");
    expect(me.username).toBe("laura");

    const v2Calls = fetchMock.mock.calls.filter(([input]) => String(input).startsWith("/api/v2/"));
    expect(v2Calls).toHaveLength(2);
    const first = v2Calls[0]?.[1]?.headers as Record<string, string>;
    const retry = v2Calls[1]?.[1]?.headers as Record<string, string>;
    expect(first).toMatchObject({ Authorization: "Bearer expired", "X-Profile-Id": "p-owner" });
    expect(retry).toMatchObject({
      Authorization: "Bearer fresh",
      "X-Profile-Id": "p-owner",
      "X-Profile-Token": "pin-1",
      Accept: "application/json",
    });
    expect(localStorage.getItem("refresh_token")).toBe("refresh-2");
  });

  it("does not retry a 401 when there is no refresh token", async () => {
    setAccessToken("expired");
    const fetchMock = vi.fn<typeof fetch>(async () =>
      json(authenticationRequired, 401, PROBLEM_HEADERS),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(v2("GET /api/v2/account/me")).rejects.toBeInstanceOf(V2ProblemError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("clears the active PIN when the profile authority is rejected", async () => {
    setAccessToken("tok-user");
    setProfileId("p-owner");
    setProfileToken("pin-1");
    const unverified = vi.fn();
    onProfileUnverified(unverified);
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => json(profileVerificationRequired, 403, PROBLEM_HEADERS)),
    );

    await expect(v2("GET /api/v2/progress")).rejects.toMatchObject({
      problemType: "profile_verification_required",
    });
    expect(unverified).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("profile_token")).toBeNull();
  });

  it("clears the active PIN when a multipart upload is refused for the profile", async () => {
    setAccessToken("tok-user");
    setProfileId("p-owner");
    setProfileToken("pin-1");
    const unverified = vi.fn();
    onProfileUnverified(unverified);
    const fetchMock = vi.fn<typeof fetch>(async () =>
      json(profileVerificationRequired, 403, PROBLEM_HEADERS),
    );
    vi.stubGlobal("fetch", fetchMock);

    const poster = new Blob(["png"], { type: "image/png" });
    await expect(
      v2("PUT /api/v2/libraries/{id}/poster", { path: { id: "7" }, form: { poster } }),
    ).rejects.toMatchObject({ problemType: "profile_verification_required" });

    const { url, init } = lastRequest(fetchMock);
    expect(url).toBe("/api/v2/libraries/7/poster");
    expect(init.method).toBe("PUT");
    expect(init.body).toBeInstanceOf(FormData);
    expect((init.body as FormData).get("poster")).toBeInstanceOf(Blob);
    expect(init.headers["Content-Type"]).toBeUndefined();
    expect(unverified).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("profile_token")).toBeNull();
  });
});

describe("v2 type separation", () => {
  it("keeps v1 mirror types out of v2 parameters", () => {
    type Me = V2Result<"GET /api/v2/account/me">;
    type AdminUsers = V2Result<"GET /api/v2/admin/users">;

    const v1User: User = {
      id: 1,
      username: "laura",
      email: "laura@example.test",
      role: "user",
      permissions: [],
      download_allowed: true,
    };
    const v1UserWithStringId = { ...v1User, id: "1" };
    const v1AdminUsers: AdminUser[] = [];

    function acceptsMe(_me: Me): void {}
    function acceptsAdminUsers(_users: AdminUsers): void {}

    // @ts-expect-error a v1 User is not a v2 Account: the id type differs.
    acceptsMe(v1User);
    // @ts-expect-error a structural look-alike without the v2 brand is rejected.
    acceptsMe(v1UserWithStringId);
    // @ts-expect-error the v1 AdminUser[] is not the branded v2 collection.
    acceptsAdminUsers({ items: v1AdminUsers });

    expectTypeOf<Me["role"]>().toEqualTypeOf<"admin" | "user">();
    expectTypeOf<Me>().not.toMatchTypeOf<User>();
    expectTypeOf(v1User).not.toMatchTypeOf<Me>();

    // Query and body inference: unknown members and v1-only shapes are
    // rejected. Never called; the compiler is the assertion.
    function typeOnly() {
      // @ts-expect-error offset is not a listProgress parameter.
      void v2("GET /api/v2/progress", { query: { offset: 50 } });
      // @ts-expect-error allowed_library_ids is string[] on the v2 contract.
      const v1Ids: V2Body<"PATCH /api/v2/profiles/{id}"> = { allowed_library_ids: [1] };
      void v1Ids;
      // @ts-expect-error a path parameter is required.
      void v2("PATCH /api/v2/profiles/{id}", { body: {} });
      // @ts-expect-error getSetupStatus takes no body.
      void v2("GET /api/v2/system/setup", { body: {} });
      // @ts-expect-error unknown operations do not compile.
      void v2("GET /api/v2/nope");
    }
    expect(typeof typeOnly).toBe("function");
  });
});
