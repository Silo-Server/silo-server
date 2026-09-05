import { beforeEach, describe, expect, it, vi } from "vitest";

import createProfileCreated from "../../../../contracts/api/v2/fixtures/create_profile_created.json";
import listHouseholdSessionsOk from "../../../../contracts/api/v2/fixtures/list_household_sessions_ok.json";
import listProfilesOk from "../../../../contracts/api/v2/fixtures/list_profiles_ok.json";
import uploadProfileAvatarOk from "../../../../contracts/api/v2/fixtures/upload_profile_avatar_ok.json";
import verifyProfilePinOk from "../../../../contracts/api/v2/fixtures/verify_profile_pin_ok.json";
import verifyProfilePinWrong from "../../../../contracts/api/v2/fixtures/verify_profile_pin_wrong.json";
import deleteProfilePrimaryProtected from "../../../../contracts/api/v2/fixtures/delete_profile_primary_protected.json";

import { setAccessToken, setProfileId } from "@/api/client";
import { V2ProblemError, v2 } from "@/api/v2/request";
import {
  createProfile,
  listHouseholdSessions,
  listProfiles,
  uploadProfileAvatar,
  verifyProfilePIN,
} from "./profiles";

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
});

describe("profile queries on the v2 contract", () => {
  it("lists profiles with numeric library ids and the upload capability", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => json(listProfilesOk));
    vi.stubGlobal("fetch", fetchMock);

    const list = await listProfiles();

    expect(requestOf(fetchMock, 0).url).toBe("/api/v2/profiles");
    expect(list.avatar_upload_enabled).toBe(listProfilesOk.avatar_upload_enabled);
    expect(list.profiles).toHaveLength(listProfilesOk.items.length);
    expect(list.profiles[0]).toMatchObject({
      id: "p-owner",
      name: "Laura",
      is_primary: true,
      allowed_library_ids: [3],
    });
  });

  it("creates a profile with the v2 body and projects the created profile", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () =>
      json(createProfileCreated, 201, { ...JSON_HEADERS, Location: "/api/v2/profiles/p-kid" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const created = await createProfile({
      name: "Kid",
      is_child: true,
      allowed_library_ids: ["3"],
      max_playback_quality: "1080p",
    });

    const request = requestOf(fetchMock, 0);
    expect(request.url).toBe("/api/v2/profiles");
    expect(request.init.method).toBe("POST");
    expect(JSON.parse(String(request.init.body))).toEqual({
      name: "Kid",
      is_child: true,
      allowed_library_ids: ["3"],
      max_playback_quality: "1080p",
    });
    expect(created.id).toBe(createProfileCreated.id);
    expect(created.allowed_library_ids).toEqual(
      createProfileCreated.allowed_library_ids.map(Number),
    );
  });

  it("verifies a PIN and passes the token through; a wrong PIN is a plain false", async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(json(verifyProfilePinOk))
      .mockResolvedValueOnce(json(verifyProfilePinWrong));
    vi.stubGlobal("fetch", fetchMock);

    const ok = await verifyProfilePIN("p-owner", "1234");
    expect(ok).toEqual(verifyProfilePinOk);
    expect(ok.profile_token).toBe("pvt_fixture");
    const request = requestOf(fetchMock, 0);
    expect(request.url).toBe("/api/v2/profiles/p-owner/verify-pin");
    expect(JSON.parse(String(request.init.body))).toEqual({ pin: "1234" });

    const wrong = await verifyProfilePIN("p-owner", "0000");
    expect(wrong.valid).toBe(false);
    expect(wrong.profile_token).toBeUndefined();
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

  it("projects household playback sessions onto the shared session shape", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => json(listHouseholdSessionsOk));
    vi.stubGlobal("fetch", fetchMock);

    const sessions = await listHouseholdSessions();

    expect(requestOf(fetchMock, 0).url).toBe("/api/v2/profiles/household/sessions");
    expect(sessions).toHaveLength(listHouseholdSessionsOk.items.length);
    const first = listHouseholdSessionsOk.items[0];
    if (!first) throw new Error("fixture has no sessions");
    expect(sessions[0]).toMatchObject({
      session_id: first.id,
      user_id: Number(first.user_id),
      media_file_id: Number(first.media_file_id),
      profile_name: first.profile_name,
      position_seconds: first.position_seconds,
    });
    expect(sessions[0]).not.toHaveProperty("id");
  });

  it("surfaces a protected primary profile as a problem", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () =>
      json(deleteProfilePrimaryProtected, deleteProfilePrimaryProtected.status, {
        "Content-Type": "application/problem+json",
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      v2("DELETE /api/v2/profiles/{id}", { path: { id: "p-owner" } }),
    ).rejects.toBeInstanceOf(V2ProblemError);
  });
});
