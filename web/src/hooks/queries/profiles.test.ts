import { beforeEach, describe, expect, it, vi } from "vitest";

import createProfileCreated from "../../../../contracts/api/v2/fixtures/create_profile_created.json";
import uploadProfileAvatarOk from "../../../../contracts/api/v2/fixtures/upload_profile_avatar_ok.json";

import {
  captureProfileRequestContext,
  captureProfileRequestContextAs,
  setAccessToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import { createProfile, uploadProfileAvatar } from "./profiles";

const JSON_HEADERS = { "Content-Type": "application/json" };

function json(body: unknown, status = 200, headers: Record<string, string> = JSON_HEADERS) {
  return new Response(JSON.stringify(body), { status, headers });
}

function requestOf(fetchMock: ReturnType<typeof vi.fn<typeof fetch>>, index: number) {
  const call = fetchMock.mock.calls[index];
  if (!call) throw new Error(`fetch call ${index} missing`);
  return {
    url: String(call[0]),
    init: call[1] as RequestInit & { headers: Record<string, string> },
  };
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("tok-user");
  setProfileId("p-owner");
  setProfileToken(null);
});

describe("profile queries on the v2 contract", () => {
  it("creates a profile as an explicit profile without touching the active one", async () => {
    setProfileToken("pvt-child");
    const fetchMock = vi.fn<typeof fetch>(async () =>
      json(createProfileCreated, 201, { ...JSON_HEADERS, Location: "/api/v2/profiles/p-kid" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const primary = captureProfileRequestContextAs("p-primary", "pvt-primary");
    if (!primary) throw new Error("no session");
    await createProfile({ name: "Kid" }, primary);

    const headers = requestOf(fetchMock, 0).init.headers;
    expect(headers["X-Profile-Id"]).toBe("p-primary");
    expect(headers["X-Profile-Token"]).toBe("pvt-primary");
    expect(captureProfileRequestContext()).toMatchObject({
      profileId: "p-owner",
      profileToken: "pvt-child",
    });
  });

  it("uploads an avatar as a multipart form and projects the profile", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => json(uploadProfileAvatarOk));
    vi.stubGlobal("fetch", fetchMock);

    const file = new File(["png"], "avatar.png", { type: "image/png" });
    const profile = await uploadProfileAvatar("p-owner", file);

    const request = requestOf(fetchMock, 0);
    expect(request.url).toBe("/api/v2/profiles/p-owner/avatar");
    expect(request.init.method).toBe("PUT");
    expect(request.init.body).toBeInstanceOf(FormData);
    expect((request.init.body as FormData).get("avatar")).toBeInstanceOf(File);
    expect(request.init.headers["Content-Type"]).toBeUndefined();
    expect(profile.avatar_source).toBe("upload");
    expect(profile.avatar_url).toBe(uploadProfileAvatarOk.avatar_url);
  });
});
