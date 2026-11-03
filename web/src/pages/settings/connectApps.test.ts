// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { AccountIdentity } from "@/hooks/queries/account";
import type { AuthProviderOption } from "@/hooks/useAuth";

import {
  buildJellyfinUsername,
  isLoopbackURL,
  jellyfinUsernameIssue,
  resolveSiloSignIn,
} from "./connectApps";

describe("buildJellyfinUsername", () => {
  it("joins the account and profile with the separator the resolver expects", () => {
    expect(buildJellyfinUsername("johndoe", "Kids")).toBe("johndoe#Kids");
  });

  it("preserves internal spaces, which the resolver matches verbatim", () => {
    expect(buildJellyfinUsername(" johndoe ", " Doe Household ")).toBe("johndoe#Doe Household");
  });

  it("falls back to the bare account when there is no profile to append", () => {
    expect(buildJellyfinUsername("johndoe", "   ")).toBe("johndoe");
  });
});

describe("jellyfinUsernameIssue", () => {
  it("rejects names containing the separator the resolver splits on", () => {
    expect(jellyfinUsernameIssue("Movie #2")).toContain("contains a #");
  });
});

describe("isLoopbackURL", () => {
  it.each([
    "http://127.0.0.1:8096",
    "http://127.1.2.3:8096",
    "http://localhost:8096",
    "https://silo.localhost",
    "http://0.0.0.0:8096",
    "http://[::1]:8096",
  ])("treats %s as unreachable from another device", (url) => {
    expect(isLoopbackURL(url)).toBe(true);
  });

  it.each(["http://192.168.1.10:8096"])("treats %s as a real address", (url) => {
    expect(isLoopbackURL(url)).toBe(false);
  });

  it("does not flag a hostname that merely contains a loopback substring", () => {
    expect(isLoopbackURL("https://localhost.example.com")).toBe(false);
    expect(isLoopbackURL("https://not-127.0.0.1.example.com")).toBe(false);
  });

  it("returns false for unparseable input rather than throwing", () => {
    expect(isLoopbackURL("")).toBe(false);
    expect(isLoopbackURL("not a url")).toBe(false);
  });
});

describe("resolveSiloSignIn", () => {
  const oidc: AuthProviderOption = {
    id: "plugin:3:oidc",
    display_name: "Company SSO",
    mode: "oauth",
    default: false,
    installation_id: "3",
  };
  const ldap: AuthProviderOption = {
    id: "plugin:4:ldap",
    display_name: "Office Directory",
    mode: "credentials",
    default: false,
    installation_id: "4",
  };

  function identity(providerID: string, username = "j.doe"): AccountIdentity {
    return {
      id: "7",
      installation_id: "3",
      provider_id: providerID,
      provider_name: "",
      username,
      email: "",
      display_name: "",
      linked_at: "2026-04-06T00:00:00.000Z",
      last_sign_in_at: null,
      last_checked_at: null,
    };
  }

  it("uses the password while the account has one and the form is shown", () => {
    expect(resolveSiloSignIn(true, true, [oidc], [identity(oidc.id)])).toEqual({
      kind: "password",
    });
  });

  it("uses the linked OAuth provider when the form is hidden", () => {
    expect(resolveSiloSignIn(true, false, [oidc], [identity(oidc.id)])).toEqual({
      kind: "provider",
      provider: oidc,
    });
  });

  it("uses the directory username for a linked directory", () => {
    expect(resolveSiloSignIn(false, true, [ldap], [identity(ldap.id)])).toEqual({
      kind: "directory",
      provider: ldap,
      username: "j.doe",
    });
  });

  it("ignores identities at a disabled provider", () => {
    expect(resolveSiloSignIn(false, false, [oidc], [identity("")])).toEqual({ kind: "none" });
  });

  it("reads an enabled provider missing from discovery as a network provider", () => {
    // Network providers are listed only to requests through their own network.
    const tailnet = { ...identity("plugin:5:tailscale"), provider_name: "Tailscale" };
    expect(resolveSiloSignIn(false, false, [oidc], [tailnet])).toEqual({
      kind: "network",
      providerName: "Tailscale",
    });
  });

  it("prefers a linked OAuth provider over a network one", () => {
    const tailnet = { ...identity("plugin:5:tailscale"), provider_name: "Tailscale" };
    expect(resolveSiloSignIn(false, false, [oidc], [tailnet, identity(oidc.id)])).toEqual({
      kind: "provider",
      provider: oidc,
    });
  });
});
