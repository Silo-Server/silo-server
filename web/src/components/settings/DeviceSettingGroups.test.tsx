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
  it("shows a profile-wide title art choice without offering a device edit", () => {
    renderGroups({
      "ui.title_art": effective({
        key: "ui.title_art",
        value: false,
        source: "profile",
        scope: "profile",
      }),
    });

    expect(screen.getByRole("switch", { name: "Show title art" })).toBeDisabled();
    expect(screen.getByText(/Set for all devices on this profile/)).toBeInTheDocument();
  });

  it("keeps a device's own title art choice resettable while the profile value wins", async () => {
    const onReset = vi.fn();
    render(
      <DeviceSettingGroups
        settings={{
          "ui.title_art": effective({
            key: "ui.title_art",
            value: true,
            source: "profile",
            scope: "profile",
          }),
        }}
        storedOnDevice={{ "ui.title_art": false }}
        ownerLabel="your"
        onChange={vi.fn()}
        onReset={onReset}
      />,
    );

    expect(screen.getByRole("switch", { name: "Show title art" })).toBeDisabled();
    expect(screen.getByText("Changed here")).toBeInTheDocument();
    expect(
      screen.getByText(/this device's own choice \(Disabled\) isn't used/),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Use your setting/ }));
    expect(onReset).toHaveBeenCalledWith("ui.title_art");
  });

  it("lets a device keep its own title art choice", () => {
    renderGroups({
      "ui.title_art": effective({
        key: "ui.title_art",
        value: false,
        source: "profile_device",
        scope: "profile_device",
      }),
    });

    expect(screen.getByRole("switch", { name: "Show title art" })).toBeEnabled();
    expect(screen.getByText("Changed here")).toBeInTheDocument();
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

  // Beside the control, the reset took width from the description: in the
  // narrow pane next to the device list the text wrapped a word per line and
  // the button overlapped it.
  it("puts the reset under the description, apart from the control", () => {
    renderGroups({
      "playback.intro_skip_mode": effective({
        key: "playback.intro_skip_mode",
        value: "skip",
        source: "profile_device",
        scope: "profile_device",
      }),
    });

    const description = screen.getByText(/What Silo does when an intro starts/);
    const labelColumn = description.parentElement as HTMLElement;
    const reset = screen.getByRole("button", { name: /Use your setting/ });

    expect(labelColumn).toContainElement(reset);
    expect(labelColumn).not.toContainElement(screen.getByRole("combobox", { name: "Skip intros" }));
    expect(
      description.compareDocumentPosition(reset) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("does not offer a reset when nothing is stored on this device", () => {
    renderGroups({ "player.hdr_enabled": effective({ source: "profile" }) });

    expect(screen.queryByText("Changed here")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Use your setting/ })).not.toBeInTheDocument();
  });

  // After "Use your setting" the row must still say where its value now comes
  // from, or a reset looks like it did nothing.
  it("names the profile as the source of a value not changed on this device", () => {
    renderGroups({
      "playback.audio_language": effective({
        key: "playback.audio_language",
        value: "fr",
        source: "profile",
        scope: "profile",
      }),
    });

    expect(screen.getByText("From your profile")).toBeInTheDocument();
    expect(screen.queryByText("App default")).not.toBeInTheDocument();
    expect(screen.queryByText("Changed here")).not.toBeInTheDocument();
  });

  it("names the other profile when acting for someone in the household", () => {
    renderGroups(
      {
        "playback.audio_language": effective({
          key: "playback.audio_language",
          value: "fr",
          source: "profile",
          scope: "profile",
        }),
      },
      { ownerLabel: "Sam's" },
    );

    expect(screen.getByText("From Sam's profile")).toBeInTheDocument();
  });

  it("names the app default when neither the device nor the profile sets a value", () => {
    renderGroups({ "player.hdr_enabled": effective({ value: true, source: "default" }) });

    expect(screen.getByText("App default")).toBeInTheDocument();
    expect(screen.queryByText(/From your profile/)).not.toBeInTheDocument();
  });

  it("shows only the device badge for a value changed on this device", () => {
    renderGroups({
      "playback.audio_language": effective({
        key: "playback.audio_language",
        value: "de",
        source: "profile_device",
        scope: "profile_device",
      }),
    });

    expect(screen.getByText("Changed here")).toBeInTheDocument();
    expect(screen.queryByText("From your profile")).not.toBeInTheDocument();
    expect(screen.queryByText("App default")).not.toBeInTheDocument();
  });

  it("does not repeat the source under a profile-wide value", () => {
    renderGroups({
      "ui.title_art": effective({
        key: "ui.title_art",
        value: false,
        source: "profile",
        scope: "profile",
      }),
    });

    expect(screen.getByText(/Set for all devices on this profile/)).toBeInTheDocument();
    expect(screen.queryByText("From your profile")).not.toBeInTheDocument();
  });

  // A household limit replaces the value but keeps the source, so the source
  // would credit the profile or the default with a value neither set.
  it("leaves the source to the household limit when the limit narrowed the value", () => {
    renderGroups({
      "playback.preferred_quality": effective({
        key: "playback.preferred_quality",
        value: "1080p",
        stored_value: "2160p",
        source: "profile",
        scope: "profile",
        constrained: true,
        constraint_kind: "ceiling",
      }),
    });

    expect(screen.getByText("Household limit")).toBeInTheDocument();
    expect(screen.queryByText("From your profile")).not.toBeInTheDocument();
    expect(screen.queryByText("App default")).not.toBeInTheDocument();
  });

  it("offers the three intro modes and writes the selected device override", async () => {
    const { onChange } = renderGroups({
      "playback.intro_skip_mode": effective({
        key: "playback.intro_skip_mode",
        value: "ask",
        source: "profile",
      }),
    });

    await userEvent.click(screen.getByRole("combobox", { name: "Skip intros" }));
    await userEvent.click(screen.getByRole("option", { name: "Never" }));

    expect(onChange).toHaveBeenCalledWith("playback.intro_skip_mode", "never");
    expect(screen.queryByText("Auto-skip intros")).not.toBeInTheDocument();
  });

  // Against a server that predates the enum the deprecated switch is the only
  // intro control there is; hiding it unconditionally removed the setting from
  // this screen and stranded any override already stored on the device.
  it("falls back to the legacy intro switch on a server without the three-way key", () => {
    render(
      <DeviceSettingGroups
        settings={{
          "playback.auto_skip_intro": effective({
            key: "playback.auto_skip_intro",
            value: true,
            source: "profile_device",
            scope: "profile_device",
          }),
        }}
        keys={["playback.auto_skip_intro", "playback.auto_play_next"]}
        ownerLabel="your"
        onChange={vi.fn()}
        onReset={vi.fn()}
      />,
    );

    expect(screen.getByText("Auto-skip intros")).toBeInTheDocument();
    expect(screen.getByText("Changed here")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Skip intros" })).not.toBeInTheDocument();
  });

  it("shows only the three-way control on a revision-7 server", () => {
    render(
      <DeviceSettingGroups
        settings={{}}
        keys={["playback.auto_skip_intro", "playback.intro_skip_mode"]}
        ownerLabel="your"
        onChange={vi.fn()}
        onReset={vi.fn()}
      />,
    );

    expect(screen.getByRole("combobox", { name: "Skip intros" })).toBeInTheDocument();
    expect(screen.queryByText("Auto-skip intros")).not.toBeInTheDocument();
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
    for (const name of ["200 Mbps", "100 Mbps", "1.5 Mbps", "No limit"]) {
      expect(screen.getByRole("option", { name })).toBeInTheDocument();
    }
    await userEvent.click(option);

    // Indexed rather than .at(-1): the app tsconfig targets ES2020.
    const calls = onChange.mock.calls;
    const call = calls[calls.length - 1];
    expect(call?.[0]).toBe("playback.max_bitrate_kbps");
    expect(call?.[1]).toBe(10000);
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

  it("sends typed values rather than strings", async () => {
    const { onChange } = renderGroups({
      "player.hdr_enabled": effective({ value: true, source: "default" }),
    });

    await userEvent.click(screen.getByRole("switch", { name: "HDR" }));

    expect(onChange).toHaveBeenCalledExactlyOnceWith("player.hdr_enabled", false);
  });
});
