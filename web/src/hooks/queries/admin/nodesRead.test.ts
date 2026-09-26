import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
} from "@/api/client";
import { fetchAdminNodes } from "./nodes";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => vi.unstubAllGlobals());
it("reads every signed page without parsing IDs or dropping empty advertised hashes", async () => {
  const first = {
    id: "9007199254740993",
    advertised_capabilities_hash: "",
    capabilities: { marker: "stored" },
  };
  const second = { id: "9007199254740994" };
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ items: [first], page: { has_more: true, next_cursor: "signed-next" } }),
        { headers: { "Content-Type": "application/json" } },
      ),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ items: [second], page: { has_more: false } }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const nodes = await fetchAdminNodes();
  expect(nodes).toMatchObject([first, second]);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(fetchMock.mock.calls[1]?.[0]).toBe("/api/v2/admin/nodes?limit=200&cursor=signed-next");
  for (const call of fetchMock.mock.calls)
    expect(call[1]?.headers).toMatchObject({
      "X-Profile-Id": "profile-a",
      "X-Profile-Token": "pin-a",
    });
});
it("stops paging after authority changes during body decoding", async () => {
  let finish!: (body: string) => void;
  let start!: () => void;
  const reading = new Promise<void>((resolve) => {
    start = resolve;
  });
  const response = new Response(null, { headers: { "Content-Type": "application/json" } });
  vi.spyOn(response, "text").mockImplementation(() => {
    start();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);
  const pending = fetchAdminNodes();
  const rejected = expect(pending).rejects.toBeInstanceOf(StaleApiRequestContextError);
  await reading;
  setProfileId("profile-b");
  finish(JSON.stringify({ items: [], page: { has_more: true, next_cursor: "next" } }));
  await rejected;
  expect(fetchMock).toHaveBeenCalledOnce();
});
