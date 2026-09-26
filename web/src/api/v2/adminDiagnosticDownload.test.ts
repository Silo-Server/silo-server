import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
} from "../client";
import { V2ProblemError } from "./request";
import { fetchAdminDiagnosticReportBundle } from "./adminDiagnosticDownload";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => vi.unstubAllGlobals());
it("downloads raw bytes with captured authority and an encoded identifier", async () => {
  const response = new Response("bundle", { headers: { "Content-Type": "application/gzip" } });
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);
  const blob = await fetchAdminDiagnosticReportBundle("report /1");
  expect(blob.size).toBe(6);
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(fetchMock.mock.calls[0]?.[0]).toBe(
    "/api/v2/admin/diagnostics/reports/report%20%2F1/download",
  );
  expect(fetchMock.mock.calls[0]?.[1]?.headers).toMatchObject({
    Accept: "application/gzip, application/problem+json",
    Authorization: "Bearer test-admin",
    "X-Profile-Id": "profile-a",
    "X-Profile-Token": "pin-a",
  });
});
it("discards a body that completes after a profile switch", async () => {
  let finish!: (blob: Blob) => void;
  let reading!: () => void;
  const started = new Promise<void>((resolve) => {
    reading = resolve;
  });
  const response = new Response(null, { headers: { "Content-Type": "application/gzip" } });
  vi.spyOn(response, "blob").mockImplementation(() => {
    reading();
    return new Promise<Blob>((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const pending = fetchAdminDiagnosticReportBundle("report-1");
  const rejected = expect(pending).rejects.toBeInstanceOf(StaleApiRequestContextError);
  await started;
  setProfileId("profile-b");
  finish(new Blob(["bundle"]));
  await rejected;
});
it("decodes a problem without offering its body as a bundle", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "https://silo.example/problems/not_found",
          title: "Not found",
          status: 404,
        }),
        { status: 404, headers: { "Content-Type": "application/problem+json" } },
      ),
    ),
  );
  await expect(fetchAdminDiagnosticReportBundle("missing")).rejects.toBeInstanceOf(V2ProblemError);
});
it("rejects a JSON success such as a legacy presigned URL response", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response('{"download_url":"https://storage.example.test/report"}', {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  await expect(fetchAdminDiagnosticReportBundle("report-1")).rejects.toThrow(
    "unexpected diagnostic report format",
  );
});
