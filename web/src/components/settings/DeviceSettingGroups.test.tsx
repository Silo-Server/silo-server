import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { DeviceSettingGroups } from "@/components/settings/DeviceSettingGroups";
import type { EffectiveSetting } from "@/hooks/queries/settingValues";
import type { SettingKey } from "@/lib/settingsContract";

// Radix Slider and Select read element sizes via ResizeObserver, which jsdom
// does not provide. A no-op polyfill is enough to render them.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
// Radix Select opens through pointer capture, which jsdom also lacks.
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

function renderGroups(
  settings: Partial<Record<SettingKey, EffectiveSetting>>,
  props: Partial<{ ownerLabel: string }> = {},
) {
  const onChange = vi.fn();
  const onReset = vi.fn();
  render(
    <DeviceSettingGroups
      settings={settings}
      ownerLabel={props.ownerLabel ?? "your"}
      onChange={onChange}
      onReset={onReset}
    />,
  );
  return { onChange, onReset };
}

function effective(overrides: Partial<EffectiveSetting> = {}): EffectiveSetting {
  return { key: "player.hdr_enabled", value: true, source: "default", ...overrides };
}

describe("DeviceSettingGroups", () => {
  it("groups settings under headings a viewer would look under", () => {
    renderGroups({});

    // Scoped to headings: "Subtitles" is also a setting label inside the group.
    const headings = screen.getAllByRole("heading").map((node) => node.textContent);
    expect(headings).toEqual(["Picture", "Sound", "Subtitles", "Episodes"]);
  });

  // The screen is for people who do not know what a manifest key is. Matching
  // on the dotted key shape rather than the prefix alone, because a manifest
  // description may legitimately end a sentence with the word "playback".
  it("never shows a raw setting key", () => {
    const { container } = render(
      <DeviceSettingGroups settings={{}} ownerLabel="your" onChange={vi.fn()} onReset={vi.fn()} />,
    );
    expect(container.textContent).not.toMatch(/\b(?:player|playback|ui)\.[a-z0-9_]+/);
  });

  it("marks a value stored on this device and offers to clear it", async () => {
    const { onReset } = renderGroups({
      "player.hdr_enabled": effective({
        value: false,
        source: "profile_device",
        scope: "profile_device",
      }),
    });

    expect(screen.getByText("Changed here")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Use your setting/ }));
    expect(onReset).toHaveBeenCalledWith("player.hdr_enabled");
  });

  it("names the person when the household parent is acting for someone else", () => {
    renderGroups(
      {
        "player.hdr_enabled": effective({ source: "profile_device", scope: "profile_device" }),
      },
      { ownerLabel: "Robin's" },
    );

    expect(screen.getByRole("button", { name: /Use Robin's setting/ })).toBeInTheDocument();
  });

  it("does not offer a reset when nothing is stored on this device", () => {
    renderGroups({ "player.hdr_enabled": effective({ source: "profile" }) });

    expect(screen.queryByText("Changed here")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Use your setting/ })).not.toBeInTheDocument();
  });

  it("sends typed values rather than strings", async () => {
    const { onChange } = renderGroups({
      "player.hdr_enabled": effective({ value: true, source: "default" }),
    });

    const [firstToggle] = screen.getAllByRole("switch");
    await userEvent.click(firstToggle!);

    expect(onChange).toHaveBeenCalled();
    const [firstCall] = onChange.mock.calls;
    expect(typeof firstCall![1]).toBe("boolean");
  });

  // A capped setting explains the cap. A disabled control with no reason is
  // exactly what the settings contract's UX rules forbid.
  it("explains a household limit instead of silently narrowing", () => {
    renderGroups({
      "playback.preferred_quality": effective({
        key: "playback.preferred_quality",
        value: "1080p",
        stored_value: "2160p",
        source: "profile_device",
        scope: "profile_device",
        constrained: true,
        constraint_kind: "ceiling",
      }),
    });

    expect(screen.getByText("Household limit")).toBeInTheDocument();
    expect(screen.getByText(/limit this to 1080p/)).toBeInTheDocument();
    expect(screen.getByText(/your choice of 2160p isn't available/)).toBeInTheDocument();
  });

  // The bandwidth cap is declared as an integer range with a select control
  // and no members, so the generic path rendered a dropdown with one blank
  // entry — unusable, and silent about the value it was already storing.
  it("offers real bandwidth choices for a numeric range with no members", async () => {
    const { onChange } = renderGroups({
      "playback.max_bitrate_kbps": effective({
        key: "playback.max_bitrate_kbps",
        value: 2000,
        source: "profile_device",
        scope: "profile_device",
      }),
    });

    expect(screen.getByText("2 Mbps")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("combobox", { name: /Maximum bitrate/i }));
    const option = await screen.findByRole("option", { name: "10 Mbps" });
    await userEvent.click(option);

    // Indexed rather than .at(-1): the app tsconfig targets ES2020.
    const calls = onChange.mock.calls;
    const call = calls[calls.length - 1];
    expect(call?.[0]).toBe("playback.max_bitrate_kbps");
    expect(call?.[1]).toBe(10000);
  });

  // The ladder is filtered by the definition's own range, which tops out at
  // 200 Mbps. An earlier hardcoded list stopped at 40 and silently capped
  // people below what their server could already send.
  it("offers the full range the contract allows", async () => {
    renderGroups({
      "playback.max_bitrate_kbps": effective({
        key: "playback.max_bitrate_kbps",
        value: null,
        source: "default",
      }),
    });

    await userEvent.click(screen.getByRole("combobox", { name: /Maximum bitrate/i }));

    expect(await screen.findByRole("option", { name: "200 Mbps" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "100 Mbps" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "1.5 Mbps" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "No limit" })).toBeInTheDocument();
  });

  it("keeps a stored bandwidth value selectable even when it is not a preset", () => {
    renderGroups({
      "playback.max_bitrate_kbps": effective({
        key: "playback.max_bitrate_kbps",
        value: 3500,
        source: "profile_device",
        scope: "profile_device",
      }),
    });

    expect(screen.getByText("3.5 Mbps")).toBeInTheDocument();
  });

  // The sliders shipped controlled with only onValueCommit, so the thumb
  // never moved and no keyboard or pointer gesture could change the value.
  // Keyboard steps are what jsdom can exercise; they commit through the same
  // path a pointer release does.
  it("commits a slider change", async () => {
    const { onChange } = renderGroups({
      "playback.next_up_prompt_seconds": effective({
        key: "playback.next_up_prompt_seconds",
        value: 30,
        source: "default",
      }),
    });

    const slider = screen.getByRole("slider", { name: "Next up prompt" });
    slider.focus();
    await userEvent.keyboard("{ArrowRight}");

    expect(onChange).toHaveBeenCalledWith("playback.next_up_prompt_seconds", 31);
  });

  it("hides settings that cannot apply to the device being edited", () => {
    render(
      <DeviceSettingGroups
        settings={{}}
        ownerLabel="your"
        devicePlatform="macOS Web"
        onChange={vi.fn()}
        onReset={vi.fn()}
      />,
    );

    expect(screen.queryByText("Screen orientation")).not.toBeInTheDocument();
    expect(screen.queryByText("Audio sync offset")).not.toBeInTheDocument();
    expect(screen.getByText("Subtitles", { selector: "h3" })).toBeInTheDocument();
  });

  it("keeps an inapplicable setting visible while this device stores a value", () => {
    render(
      <DeviceSettingGroups
        settings={{
          "player.audio_sync_ms": {
            key: "player.audio_sync_ms",
            value: 250,
            source: "profile_device",
            scope: "profile_device",
          },
        }}
        ownerLabel="your"
        devicePlatform="macOS Web"
        onChange={vi.fn()}
        onReset={vi.fn()}
      />,
    );

    expect(screen.getByText("Audio sync offset")).toBeInTheDocument();
    expect(screen.queryByText("Screen orientation")).not.toBeInTheDocument();
  });

  it("states who set a locked value rather than disabling it silently", () => {
    renderGroups({
      "playback.preferred_quality": effective({
        key: "playback.preferred_quality",
        value: "720p",
        source: "profile",
        constrained: true,
        constraint_kind: "locked",
      }),
    });

    expect(
      screen.getByText(/set for your household and can't be changed here/),
    ).toBeInTheDocument();
  });
});
