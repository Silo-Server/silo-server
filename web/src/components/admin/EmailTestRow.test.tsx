import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));
import { TestEmailRow } from "./EmailTestRow";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
  toast.success.mockReset();
  toast.error.mockReset();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function prepare() {
  render(<TestEmailRow />);
  fireEvent.change(screen.getByLabelText("Test email recipient"), {
    target: { value: "recipient@example.test" },
  });
  return screen.getByRole("button", { name: "Send test" });
}
it("captures the recipient and prevents duplicate dispatch while pending", async () => {
  let finish!: (response: Response) => void;
  const fetchMock = vi.fn<typeof fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const button = prepare();
  act(() => {
    fireEvent.click(button);
    fireEvent.click(button);
  });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  fireEvent.change(screen.getByLabelText("Test email recipient"), {
    target: { value: "changed@example.test" },
  });
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/email/test");
  expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
    method: "POST",
    body: JSON.stringify({ to: "recipient@example.test" }),
  });
  await act(async () => {
    finish(
      new Response(JSON.stringify({ ok: true, duration_ms: 3 }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  await screen.findByText("Delivered to the mail server in 3ms.");
  expect(toast.success).toHaveBeenCalledOnce();
});
it("does not refresh or replay a 401 response", async () => {
  setRefreshToken("test-refresh");
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        type: "https://silo.example/problems/authentication_required",
        title: "Authentication required",
        status: 401,
      }),
      { status: 401, headers: { "Content-Type": "application/problem+json" } },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  fireEvent.click(prepare());
  await waitFor(() => expect(toast.error).toHaveBeenCalledOnce());
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
});
it("suppresses the result when profile authority changes during body decoding", async () => {
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
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  fireEvent.click(prepare());
  await reading;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ ok: true, duration_ms: 3 }));
  });
  expect(screen.queryByText(/Delivered to the mail server/)).toBeNull();
  expect(toast.success).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
