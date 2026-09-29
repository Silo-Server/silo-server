// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
import ActivateDevice from "./ActivateDevice";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { username: "laura" }, loading: false, setupLoading: false }),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
vi.mock("@/hooks/useServerBranding", () => ({ useServerBranding: () => ({ serverName: "Silo" }) }));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const details = (status: string) => ({
  status,
  user_code: "ABCD-1234",
  match_code: "42",
  device_name: "Living room TV",
  device_platform: "tvos",
  ip_address_hint: "192.168.1.x",
  client_purpose: "device_login",
  temporary: false,
});

function mount() {
  return render(
    <MemoryRouter initialEntries={["/activate?code=ABCD-1234"]}>
      <Routes>
        <Route path="/activate" element={<ActivateDevice />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => request.mockReset());
afterEach(cleanup);

it("tells the approver the device cancelled the request", async () => {
  request.mockResolvedValue(details("cancelled"));
  mount();
  expect(await screen.findByText(/The device canceled this sign-in request/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Approve sign-in" })).toBeNull();
});

it("shows the cancellation when the device cancels while the page is open", async () => {
  let lookups = 0;
  request.mockImplementation(async (operation: string) => {
    if (operation === "GET /api/v2/auth/device")
      return details(lookups++ === 0 ? "pending" : "cancelled");
    if (operation !== "POST /api/v2/auth/device/approve") return undefined;
    throw new V2ProblemError("approveDeviceLogin", {
      type: "https://example.invalid/problems/conflict",
      title: "Conflict",
      status: 409,
      detail: "The device canceled this sign-in request.",
      instance: "request-test",
    });
  });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Approve sign-in" }));
  expect(await screen.findByText(/The device canceled this sign-in request/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Approve sign-in" })).toBeNull();
});

it("keeps the request and its buttons when a decision fails for another reason", async () => {
  request.mockImplementation(async (operation: string) => {
    if (operation === "GET /api/v2/auth/device") return details("pending");
    if (operation !== "POST /api/v2/auth/device/approve") return undefined;
    throw new TypeError("Failed to fetch");
  });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Approve sign-in" }));
  expect(await screen.findByRole("button", { name: "Approve sign-in" })).toBeTruthy();
  expect(request.mock.calls.filter(([op]) => op === "GET /api/v2/auth/device")).toHaveLength(1);
});
