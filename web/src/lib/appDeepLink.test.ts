import { describe, expect, it } from "vitest";
import { buildInviteDeepLink, detectMobilePlatform } from "./appDeepLink";

describe("detectMobilePlatform", () => {
  it("detects Android", () => {
    expect(
      detectMobilePlatform("Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36"),
    ).toBe("android");
  });

  it("detects iPhone and iPad", () => {
    expect(detectMobilePlatform("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")).toBe(
      "ios",
    );
    expect(detectMobilePlatform("Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X)")).toBe("ios");
  });

  it("returns null for desktop browsers", () => {
    expect(detectMobilePlatform("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBeNull();
    expect(detectMobilePlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")).toBeNull();
    expect(detectMobilePlatform("Mozilla/5.0 (X11; Linux x86_64)")).toBeNull();
  });
});

describe("buildInviteDeepLink", () => {
  it("emits the silo://invite contract the Android app registers", () => {
    expect(buildInviteDeepLink("https://silo.arkyncdn.net", "wIAUTS99-abc")).toBe(
      "silo://invite?server=https%3A%2F%2Fsilo.arkyncdn.net&token=wIAUTS99-abc",
    );
  });

  it("keeps a non-default port inside the server origin", () => {
    expect(buildInviteDeepLink("https://silo.example.net:8443", "t")).toBe(
      "silo://invite?server=https%3A%2F%2Fsilo.example.net%3A8443&token=t",
    );
  });

  it("carries plain-http LAN origins verbatim", () => {
    expect(buildInviteDeepLink("http://192.168.1.10:8090", "t")).toBe(
      "silo://invite?server=http%3A%2F%2F192.168.1.10%3A8090&token=t",
    );
  });

  it("rejects unrepresentable origins", () => {
    expect(buildInviteDeepLink("not a url", "t")).toBeNull();
    expect(buildInviteDeepLink("ftp://silo.example.net", "t")).toBeNull();
    expect(buildInviteDeepLink("https://user:pw@silo.example.net", "t")).toBeNull();
  });
});
