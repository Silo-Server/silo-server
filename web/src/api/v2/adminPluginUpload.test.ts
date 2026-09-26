import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "../client";
import { V2ProblemError } from "./request";
import { uploadAdminPlugin } from "./adminPluginUpload";

const installation = {
  id: "31",
  plugin_id: "org.example.up",
  version: "2.0.0",
  install_path: "/plugins/up",
  enabled: true,
  kind: "plugin",
  update_policy: "auto",
  source_kind: "external",
  updates_paused: false,
  capabilities: [],
  global_config_schema: [],
  user_config_schema: [],
  routes: [],
  assets: [],
  metadata: {},
  global_configs: [],
  auth_bindings: [],
  task_bindings: [],
  created_at: "2026-09-07T00:00:00.000Z",
  updated_at: "2026-09-07T00:00:00.000Z",
};
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (type: string, status: number) =>
  new Response(
    JSON.stringify({
      type: `https://silo.dev/problems/${type}`,
      title: type,
      status,
      detail: "synthetic",
      instance: "synthetic",
    }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
const session = (id: string, size: number, chunk: number, received: number) =>
  json(
    {
      upload_id: id,
      filename: "p.zip",
      size_bytes: size,
      chunk_size: chunk,
      total_chunks: Math.ceil(size / chunk),
      received_chunks: received,
      received_bytes: Math.min(size, received * chunk),
      complete: received * chunk >= size,
      expires_at: "2026-09-07T02:00:00.000Z",
    },
    received === 0 ? 201 : 200,
  );
let authority: ProfileRequestContextSnapshot;
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
  setProfileToken("pin-a");
  authority = captureProfileRequestContext()!;
});
afterEach(() => vi.unstubAllGlobals());

it("sends a small archive as one multipart request under captured authority", async () => {
  const fetchMock = vi.fn().mockResolvedValue(json(installation, 201));
  vi.stubGlobal("fetch", fetchMock);
  const result = await uploadAdminPlugin({
    file: new File(["PK\x03\x04"], "p.zip"),
    profileContext: authority,
  });
  expect(result.plugin_id).toBe("org.example.up");
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe("/api/v2/admin/plugins/uploads");
  expect(init.method).toBe("POST");
  expect(init.body).toBeInstanceOf(FormData);
  expect((init.body as FormData).get("archive")).toBeInstanceOf(Blob);
  expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("profile-a");
});

it("uploads a large archive in ordered octet-stream chunks, completes, and reports progress", async () => {
  const size = 1024 * 1024 + 10;
  const chunk = 512 * 1024;
  const puts: string[] = [];
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    if (url === "/api/v2/admin/plugins/uploads/chunked") return session("s1", size, chunk, 0);
    if (url.includes("/chunks/")) {
      puts.push(
        url +
          " " +
          (init.headers as Record<string, string>)["Content-Type"] +
          " " +
          (init.body as Blob).size,
      );
      return session("s1", size, chunk, puts.length);
    }
    if (url.endsWith("/complete")) return json(installation, 201);
    throw new Error("unexpected " + url);
  });
  vi.stubGlobal("fetch", fetchMock);
  const percents: number[] = [];
  const result = await uploadAdminPlugin({
    file: new File([new Uint8Array(size)], "p.zip"),
    profileContext: authority,
    onProgress: (p) => percents.push(p.percent),
  });
  expect(result.id).toBe("31");
  expect(puts).toEqual([
    `/api/v2/admin/plugins/uploads/chunked/s1/chunks/0 application/octet-stream ${chunk}`,
    `/api/v2/admin/plugins/uploads/chunked/s1/chunks/1 application/octet-stream ${chunk}`,
    "/api/v2/admin/plugins/uploads/chunked/s1/chunks/2 application/octet-stream 10",
  ]);
  expect(percents).toEqual([0, 50, 100, 100]);
  expect(fetchMock).toHaveBeenCalledTimes(5);
});

it("halves the chunk size after a 413 on the first chunk, cancelling the first session", async () => {
  const size = 600 * 1024;
  const sessions: number[] = [];
  const deletes: string[] = [];
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    if (url === "/api/v2/admin/plugins/uploads/chunked") {
      const body = JSON.parse(String(init.body)) as { chunk_size: number };
      sessions.push(body.chunk_size);
      return session(`s${sessions.length}`, size, body.chunk_size, 0);
    }
    if (init.method === "DELETE") {
      deletes.push(url);
      return new Response(null, { status: 204 });
    }
    if (url.includes("/s1/chunks/")) return problem("payload_too_large", 413);
    if (url.includes("/s2/chunks/")) {
      const index = Number(url.split("/").pop());
      return session("s2", size, sessions[1]!, index + 1);
    }
    if (url.endsWith("/complete")) return json(installation, 201);
    throw new Error("unexpected " + url);
  });
  vi.stubGlobal("fetch", fetchMock);
  await uploadAdminPlugin({
    file: new File([new Uint8Array(size)], "p.zip"),
    profileContext: authority,
  });
  expect(sessions).toEqual([512 * 1024, 256 * 1024]);
  expect(deletes).toEqual(["/api/v2/admin/plugins/uploads/chunked/s1"]);
});

it("does not replay a chunk after 401 and refuses once the authority changed", async () => {
  const size = 600 * 1024;
  const fetchMock = vi.fn(async (url: string) => {
    if (url === "/api/v2/admin/plugins/uploads/chunked") return session("s1", size, 512 * 1024, 0);
    if (url.includes("/chunks/")) return problem("authentication_required", 401);
    if (url.endsWith("/chunked/s1")) return new Response(null, { status: 204 });
    throw new Error("unexpected " + url);
  });
  vi.stubGlobal("fetch", fetchMock);
  await expect(
    uploadAdminPlugin({
      file: new File([new Uint8Array(size)], "p.zip"),
      profileContext: authority,
    }),
  ).rejects.toBeInstanceOf(V2ProblemError);
  const chunkCalls = fetchMock.mock.calls.filter(([url]) => String(url).includes("/chunks/"));
  expect(chunkCalls).toHaveLength(1);
  setProfileToken("pin-b");
  fetchMock.mockClear();
  await expect(
    uploadAdminPlugin({ file: new File(["x"], "p.zip"), profileContext: authority }),
  ).rejects.toBeInstanceOf(StaleApiRequestContextError);
  expect(fetchMock).not.toHaveBeenCalled();
});
