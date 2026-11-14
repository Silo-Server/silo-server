// @vitest-environment jsdom
import { beforeEach, expect, it } from "vitest";
import { linkToken, moveLinkTokenOutOfAddress } from "./linkToken";

beforeEach(() => window.history.replaceState(null, "", "/"));

it.each([
  ["a fragment link", "/reset-password#tok-1", "/reset-password"],
  ["a link in the old path form", "/reset-password/tok-1", "/reset-password"],
  ["an invitation", "/invite#tok-1", "/invite"],
  ["an old-form invitation", "/invite/tok-1", "/invite"],
])("takes the token of %s out of the address and keeps it for the entry", (_name, link, page) => {
  window.history.replaceState({ idx: 3 }, "", link);
  expect(moveLinkTokenOutOfAddress()).toBe(true);
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(page);
  expect(linkToken()).toBe("tok-1");
  // The entry's other state, which the router owns, survives.
  expect(window.history.state).toMatchObject({ idx: 3 });
  // A reload keeps the entry's state and finds nothing more to move.
  expect(moveLinkTokenOutOfAddress()).toBe(false);
  expect(linkToken()).toBe("tok-1");
});

it("leaves other pages and tokenless link pages alone", () => {
  for (const address of ["/reset-password", "/invite", "/library#section", "/invite-codes/abc"]) {
    window.history.replaceState(null, "", address);
    expect(moveLinkTokenOutOfAddress()).toBe(false);
    expect(linkToken()).toBe("");
  }
});
