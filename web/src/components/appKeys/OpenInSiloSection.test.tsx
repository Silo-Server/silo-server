// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
import { expectNoLinkCarrying } from "./codeLinkGuard.test-support";
import { OpenInSiloSection } from "./OpenInSiloSection";

const state = vi.hoisted(() => ({
  access: {} as Record<string, unknown>,
  createSessionAppCode: vi.fn(),
  selectProfile: vi.fn(),
  verifyProfilePin: vi.fn(),
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    isImpersonating: false,
    profile: { id: "p-main", name: "Main", has_pin: true },
    selectProfile: state.selectProfile,
    verifyProfilePin: state.verifyProfilePin,
    logoutOfSiloOnly: vi.fn(),
  }),
}));
vi.mock("@/hooks/queries/appKeySession", () => ({
  useAppKeyCapability: () => ({ data: { state: "available", app_codes: true } }),
  useAppKeySessionAccess: () => state.access,
}));
vi.mock("@/api/v2/appCodes", () => ({ createSessionAppCode: state.createSessionAppCode }));
vi.mock("react-qr-code", async () =>
  (await import("@/components/appKeys/codeLinkGuard.test-support")).qrCodeMock(),
);

const code = {
  code: "2t5oNw5THx0tyCc3SnhX0kz2PYJup7MIdr0Mmx9ps0o",
  expires_in: 600,
  server_address: "https://media.example.com",
  link_allowed: false,
};

function problem(type: string, status: number) {
  return new V2ProblemError("createAppCode", {
    type: `https://siloserver.org/docs/api/v2/problems/${type}`,
    title: type,
    status,
    detail: type,
    instance: "urn:silo:request:test",
  });
}

function mount() {
  render(
    <MemoryRouter>
      <OpenInSiloSection />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  state.access = {
    isPending: false,
    data: { list: true, manage: true, approve: false, sign_in_again: false },
    refetch: vi.fn(),
  };
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// A failed read of what this session may do must not look like a server
// without the Silo app: the section says so and offers to ask again.
it("offers a retry when the session answer can't be read", () => {
  state.access = { isPending: false, isError: true, data: undefined, refetch: vi.fn() };
  mount();
  expect(screen.getByRole("heading", { name: "Silo app" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(state.access.refetch).toHaveBeenCalledTimes(1);
});

// A main profile whose PIN this browser hasn't proven is asked for it, and
// the code is requested again with the verified profile.
it("asks for the main profile's PIN and tries again", async () => {
  state.createSessionAppCode
    .mockRejectedValueOnce(problem("profile_verification_required", 403))
    .mockResolvedValueOnce(code);
  state.verifyProfilePin.mockResolvedValue({ valid: true, profile_token: "tok" });
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Get a code for the Silo app" }));
  const pin = await screen.findByLabelText("PIN");
  fireEvent.change(pin, { target: { value: "1234" } });
  fireEvent.submit(pin.closest("form")!);
  await screen.findByText(code.code);
  expect(state.selectProfile).toHaveBeenCalledWith(
    expect.objectContaining({ id: "p-main" }),
    "tok",
  );
  await waitFor(() => expect(state.createSessionAppCode).toHaveBeenCalledTimes(2));
});

// The request isn't acting as the household's main profile: say so, rather
// than the generic failure.
it("says only the main profile can open Silo", async () => {
  state.createSessionAppCode.mockRejectedValueOnce(problem("main_profile_required", 403));
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Get a code for the Silo app" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Only the household’s main profile can get a code",
  );
});

// The server refused this session though the access answer said it may:
// the person gets the notice for this session instead of a silent button.
it("shows the session notice when the server refuses this sign-in", async () => {
  state.createSessionAppCode.mockRejectedValueOnce(problem("permission_denied", 403));
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Get a code for the Silo app" }));
  expect(await screen.findByText("Manage your devices in the Silo app.")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Get a code for the Silo app" })).toBeNull();
  expect(state.access.refetch).toHaveBeenCalledTimes(1);
});

// A session code reaches an existing account, so it is shown to type and is
// never put in anything a device could open: no href, no src, no QR code
// but the scan code, and no text with a URL that carries the code. Any app can claim a link's scheme and
// would redeem the code first.
it("never puts the session code in a link", async () => {
  state.createSessionAppCode.mockResolvedValueOnce(code);
  const { container } = render(
    <MemoryRouter>
      <OpenInSiloSection />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Get a code for the Silo app" }));
  expect(await screen.findByText(code.code)).toBeTruthy();
  expect(screen.getByText(code.server_address)).toBeTruthy();
  expect(screen.getByRole("img", { name: "Scan code for the Silo app" })).toBeTruthy();
  expectNoLinkCarrying(container, code.code);
  expect(container.querySelector("a, img, canvas")).toBeNull();
});
