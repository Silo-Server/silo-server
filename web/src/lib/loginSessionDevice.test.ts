import { describe, expect, it } from "vitest";
import { loginSessionDevice } from "./loginSessionDevice";

describe("loginSessionDevice", () => {
  it("summarizes a browser User-Agent", () => {
    expect(
      loginSessionDevice({
        device_name:
          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36",
      }),
    ).toEqual({ name: "Chrome on Windows", kind: "desktop" });
    expect(loginSessionDevice({ device_name: "Silo/1.0 (tvOS)" })).toEqual({
      name: "Silo app on Apple TV",
      kind: "tv",
    });
  });

  it("keeps a name the client reported", () => {
    expect(
      loginSessionDevice({ device_name: "Living Room Apple TV", device_platform: "tvOS" }),
    ).toEqual({ name: "Living Room Apple TV", kind: "tv" });
    expect(
      loginSessionDevice({ device_name: "Firefox on Linux", device_platform: "Linux Web" }),
    ).toEqual({ name: "Firefox on Linux", kind: "desktop" });
  });

  it("keeps a device-code sign-in's name", () => {
    expect(loginSessionDevice({ device_name: "Bedroom TV" })).toEqual({
      name: "Bedroom TV",
      kind: "unknown",
    });
  });

  it("falls back when nothing was recorded", () => {
    expect(loginSessionDevice({ device_name: "" })).toEqual({
      name: "Unknown device",
      kind: "unknown",
    });
  });
});
