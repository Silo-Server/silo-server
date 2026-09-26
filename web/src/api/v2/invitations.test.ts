import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  captureInvitationAuthority,
  createAdminInvitation,
  resendAdminInvitation,
  revokeAdminInvitation,
  listAdminInvitationsPage,
  getAdminInvitation,
} from "./invitations";
const row = { id: "9007199254740993", email: "invitee@example.invalid", status: "pending" };
const result = {
  invitation: row,
  claim_url: "https://example.invalid/invite/synthetic",
  delivery_status: "failed_or_unknown",
};
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("preserves false, explicit empty libraries, omitted inheritance, and exact string IDs", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => json(result, 201));
  vi.stubGlobal("fetch", fetch);
  expect(
    (
      await createAdminInvitation({
        email: row.email,
        role: "user",
        create_profile: false,
        show_tour: false,
        library_ids: [],
      })
    ).delivery_status,
  ).toBe("failed_or_unknown");
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({
    email: row.email,
    role: "user",
    create_profile: false,
    show_tour: false,
    library_ids: [],
  });
  await createAdminInvitation({
    email: row.email,
    role: "user",
    create_profile: true,
    show_tour: true,
  });
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({
    email: row.email,
    role: "user",
    create_profile: true,
    show_tour: true,
  });
  await resendAdminInvitation(row.id);
  expect(String(fetch.mock.calls[2]![0])).toBe(`/api/v2/admin/invitations/${row.id}/resend`);
  fetch.mockResolvedValue(new Response(null, { status: 204 }));
  await revokeAdminInvitation(row.id);
  expect(String(fetch.mock.calls[3]![0])).toBe(`/api/v2/admin/invitations/${row.id}`);
});
it("never refreshes or replays any mutation on401", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () =>
    json(
      {
        type: "https://silo.example/problems/authentication_required",
        title: "Unauthorized",
        status: 401,
      },
      401,
    ),
  );
  vi.stubGlobal("fetch", fetch);
  for (const call of [
    () =>
      createAdminInvitation({
        email: row.email,
        role: "user",
        create_profile: true,
        show_tour: true,
      }),
    () => resendAdminInvitation(row.id),
    () => revokeAdminInvitation(row.id),
  ]) {
    fetch.mockClear();
    await expect(call()).rejects.toMatchObject({ status: 401 });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).not.toContain("refresh");
  }
});
it("rejects stale captured authority before effects and after delayed results", async () => {
  const context = captureInvitationAuthority();
  setProfileId("child");
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  await expect(resendAdminInvitation(row.id, context)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
  setProfileId("owner");
  fetch.mockImplementation(async () => {
    setProfileId("child");
    return json(result, 201);
  });
  await expect(
    createAdminInvitation({
      email: row.email,
      role: "user",
      create_profile: true,
      show_tour: true,
    }),
  ).rejects.toThrow();
});
it("requires valid pagination and delivery outcomes without fallback POSTs", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  for (const page of [undefined, { has_more: true }, { has_more: true, next_cursor: "same" }]) {
    fetch.mockResolvedValue(json({ items: [], page }));
    await expect(listAdminInvitationsPage("same")).rejects.toThrow("pagination");
  }
  fetch.mockResolvedValue(json({ items: [row], page: { has_more: false } }));
  expect((await listAdminInvitationsPage()).items[0]?.id).toBe(row.id);
  fetch.mockResolvedValue(json({ ...result, claim_url: "" }, 201));
  await expect(
    createAdminInvitation({
      email: row.email,
      role: "user",
      create_profile: true,
      show_tour: true,
    }),
  ).rejects.toThrow("may have been created");
  for (const invalid of [
    { ...result, claim_url: 7 },
    { ...result, invitation: { id: 7 } },
  ]) {
    fetch.mockResolvedValue(json(invalid, 201));
    await expect(
      createAdminInvitation({
        email: row.email,
        role: "user",
        create_profile: true,
        show_tour: true,
      }),
    ).rejects.toThrow("may have been created");
  }
  fetch.mockResolvedValue(json(row));
  expect((await getAdminInvitation(row.id)).id).toBe(row.id);
});
