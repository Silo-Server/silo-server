// @vitest-environment jsdom
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken } from "@/api/client";
import { expectNoLinkCarrying } from "@/components/appKeys/codeLinkGuard.test-support";
import {
  bodiesSentTo,
  json,
  openLink,
  problemResponse,
  renderRoutes,
} from "./authPage.test-support";
import SignInLink from "./SignInLink";
import policyFixture from "../../../contracts/api/v2/fixtures/get_password_policy_ok.json";
import redeemed from "../../../contracts/api/v2/fixtures/redeem_sign_in_link_ok.json";
import redeemedKeysOff from "../../../contracts/api/v2/fixtures/redeem_sign_in_link_keys_off.json";

vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: null, loading: false }) }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

const fetchMock = vi.fn<typeof fetch>();
let redeem: () => Promise<Response>;
let setPassword: () => Promise<Response>;
const redeems = () => bodiesSentTo(fetchMock, "/api/v2/auth/sign-in-links/redeem");

function mount(link = "/sign-in-link#link-secret") {
  openLink(link);
  return renderRoutes("/sign-in-link", {
    "/sign-in-link": <SignInLink />,
    "/login": <p>Ordinary login</p>,
  });
}

beforeEach(() => {
  setAccessToken(null);
  redeem = async () => json(redeemed);
  setPassword = async () => new Response(null, { status: 204 });
  fetchMock.mockImplementation(async (url) => {
    if (String(url).endsWith("/sign-in-links/redeem")) return redeem();
    if (String(url).endsWith("/sign-in-links/set-password")) return setPassword();
    if (String(url).endsWith("/auth/password-policy")) return json(policyFixture);
    return new Response(null, { status: 404 });
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

// The link works once and its code lives ten minutes, so opening the page
// spends nothing: it explains first, and the token leaves the address bar.
// A double press of Continue redeems once.
it("redeems only when Continue is pressed, once", async () => {
  mount();
  expect(await screen.findByRole("button", { name: "Continue" })).toBeTruthy();
  expect(window.location.hash).toBe("");
  expect(redeems()).toHaveLength(0);
  const button = screen.getByRole("button", { name: "Continue" });
  fireEvent.click(button);
  fireEvent.click(button);
  expect(await screen.findByText(redeemed.code)).toBeTruthy();
  expect(redeems()).toEqual([{ token: "link-secret" }]);
});

// The code is a session code for an existing account: shown to type, never
// in a link, a scannable link or anything else a device could open. An app
// that took such a link would redeem the code with its own keys.
it("never puts the code in a link", async () => {
  const { container } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "Continue" }));
  expect(await screen.findByText(redeemed.code)).toBeTruthy();
  expect(screen.getByText(redeemed.server_address)).toBeTruthy();
  expectNoLinkCarrying(container, redeemed.code);
  expect(container.querySelector("a[href^='silo:'], img, canvas")).toBeNull();
});

// "Set a password instead" sends the redeem's set-password token, not the
// link's, with the new password.
it("sets a password with the redeem's token", async () => {
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Continue" }));
  fireEvent.click(await screen.findByRole("button", { name: "Set a password instead" }));
  fireEvent.change(screen.getByLabelText("New password"), { target: { value: "margin fossil" } });
  fireEvent.change(screen.getByLabelText("Confirm new password"), {
    target: { value: "margin fossil" },
  });
  fireEvent.submit(screen.getByRole("button", { name: "Save password" }).closest("form")!);
  expect(await screen.findByRole("status")).toHaveTextContent("Your password is saved.");
  expect(bodiesSentTo(fetchMock, "/api/v2/auth/sign-in-links/set-password")).toEqual([
    { token: redeemed.set_password_token, new_password: "margin fossil" },
  ]);
});

// With app keys off the answer has only the set-password token: the page
// goes straight to the password form.
it("offers only a password while app keys are off", async () => {
  redeem = async () => json(redeemedKeysOff);
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Continue" }));
  expect(await screen.findByLabelText("New password")).toBeTruthy();
  expect(screen.queryByText(redeemed.code)).toBeNull();
});

// A used or expired link sends the person to the server's owner.
it("says a used link needs a new one from the owner", async () => {
  redeem = async () => problemResponse(404, "not_found");
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Continue" }));
  await waitFor(() =>
    expect(screen.getByText(/Ask the server’s owner for a new one/)).toBeTruthy(),
  );
});
