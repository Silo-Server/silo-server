import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AdminDeviceSetting } from "@/hooks/queries/admin/users";
import { AdminSubtitleAppearanceDialog } from "./AdminSubtitleAppearanceDialog";

afterEach(cleanup);

const setting: AdminDeviceSetting = {
  user_id: 1,
  profile_id: "profile-a",
  device_id: "device-a",
  device_name: "Chrome on Linux",
  device_platform: "web",
  key: "playback.subtitle_appearance",
  value: JSON.stringify({ fontSize: "medium", fontFamily: "sans-serif" }),
  updated_at: "2026-11-18T00:00:00Z",
};

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  // The dialog owns the failure; keep an unfixed dialog that ignores the
  // promise from surfacing an unhandled rejection instead of a failed assertion.
  promise.catch(() => undefined);
  return { promise, resolve, reject };
}

function renderDialog(
  onSave: (s: AdminDeviceSetting, value: string) => Promise<unknown>,
  onReset = vi.fn(),
) {
  return render(
    <AdminSubtitleAppearanceDialog
      setting={setting}
      isOverride
      onClose={vi.fn()}
      onSave={onSave}
      onReset={onReset}
    />,
  );
}

const radio = (name: string) => screen.getByRole("radio", { name: new RegExp(`^${name}$`) });
const savedValue = (onSave: ReturnType<typeof vi.fn>, call: number) =>
  JSON.parse(String(onSave.mock.calls[call]?.[1])) as Record<string, unknown>;

it("drops a rejected change so the next save doesn't store it", async () => {
  const failed = deferred();
  const onSave = vi.fn().mockReturnValueOnce(failed.promise).mockResolvedValue(undefined);
  renderDialog(onSave);

  fireEvent.click(radio("Large"));
  expect(radio("Large")).toHaveAttribute("aria-checked", "true");
  await act(async () => failed.reject(new Error("offline")));

  expect(radio("Medium")).toHaveAttribute("aria-checked", "true");
  expect(screen.getByText("Changes saved automatically")).toBeInTheDocument();

  fireEvent.click(radio("Monospace"));
  expect(savedValue(onSave, 1)).toMatchObject({ fontSize: "medium", fontFamily: "monospace" });
});

it("keeps a newer change when an earlier save fails", async () => {
  const first = deferred();
  const onSave = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
  renderDialog(onSave);

  fireEvent.click(radio("Large"));
  fireEvent.click(radio("Monospace"));
  await act(async () => first.reject(new Error("offline")));

  expect(radio("Medium")).toHaveAttribute("aria-checked", "true");
  expect(radio("Monospace")).toHaveAttribute("aria-checked", "true");
  expect(onSave).toHaveBeenCalledTimes(2);
  expect(savedValue(onSave, 1)).toMatchObject({ fontSize: "medium", fontFamily: "monospace" });
});

it("doesn't send a queued change after the override is reset", async () => {
  const first = deferred();
  const onSave = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
  const onReset = vi.fn();
  renderDialog(onSave, onReset);

  fireEvent.click(radio("Large"));
  fireEvent.click(radio("Monospace"));
  fireEvent.click(screen.getByRole("button", { name: "Reset override" }));
  await act(async () => first.resolve());

  expect(onReset).toHaveBeenCalledTimes(1);
  expect(onSave).toHaveBeenCalledTimes(1);
});

it("doesn't send a queued change after the editor closes", async () => {
  const first = deferred();
  const onSave = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined);
  const { unmount } = renderDialog(onSave);

  fireEvent.click(radio("Large"));
  fireEvent.click(radio("Monospace"));
  unmount();
  await act(async () => first.resolve());

  expect(onSave).toHaveBeenCalledTimes(1);
});
