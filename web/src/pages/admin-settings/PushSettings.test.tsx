import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import PushSettings from "./PushSettings";

const updateSettingMutateAsync = vi.fn(() => Promise.resolve());
const sendTestMutate = vi.fn();

vi.mock("@/hooks/queries/admin/settings", () => ({
  useUpdateServerSetting: () => ({
    mutateAsync: updateSettingMutateAsync,
    isPending: false,
  }),
  useAdminSensitiveStatus: () => ({
    data: { configured: ["push.webpush.vapid_public", "push.apns.key_id"] },
  }),
}));

vi.mock("@/hooks/queries/push", () => ({
  usePushStatus: () => ({ data: { apns: false, fcm: false, webpush: true } }),
  useGenerateVapidKeys: () => ({
    mutateAsync: async () => ({ vapid_public: "PUB", vapid_private: "PRIV" }),
    isPending: false,
  }),
  useSendTestPush: () => ({ mutate: sendTestMutate, isPending: false }),
}));

describe("PushSettings", () => {
  beforeEach(() => {
    updateSettingMutateAsync.mockClear();
    sendTestMutate.mockClear();
  });

  it("renders the three provider cards", () => {
    render(<PushSettings />);
    expect(screen.getByRole("heading", { name: "Web Push" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "APNs" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "FCM" })).toBeInTheDocument();
  });

  it("shows the web push provider as configured", () => {
    render(<PushSettings />);
    // CredentialStatus renders "Configured" when configured is true.
    expect(screen.getAllByText("Configured").length).toBeGreaterThanOrEqual(1);
  });

  it("reflects configured status on non-password fields", () => {
    render(<PushSettings />);
    // SettingField renders placeholder "configured" for a configured field.
    expect(screen.getByLabelText("VAPID Public Key")).toHaveAttribute(
      "placeholder",
      "configured",
    );
    expect(screen.getByLabelText("Key ID")).toHaveAttribute("placeholder", "configured");
    // A field that is not in the configured list does not get the indicator.
    expect(screen.getByLabelText("Team ID")).not.toHaveAttribute("placeholder", "configured");
  });

  it("saves the vapid public key with the typed value", async () => {
    const user = userEvent.setup();
    render(<PushSettings />);

    const publicField = screen.getByLabelText("VAPID Public Key");
    await user.type(publicField, "my-public-key");

    // The Save button immediately following the public key field.
    const saveButtons = screen.getAllByRole("button", { name: "Save" });
    await user.click(saveButtons[0]!);

    expect(updateSettingMutateAsync).toHaveBeenCalledWith({
      key: "push.webpush.vapid_public",
      value: "my-public-key",
    });
  });

  it("populates the key fields when generating keys", async () => {
    const user = userEvent.setup();
    render(<PushSettings />);

    await user.click(screen.getByRole("button", { name: "Generate keys" }));

    expect(await screen.findByDisplayValue("PUB")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("PRIV")).toBeInTheDocument();
  });

  it("sends a test push", async () => {
    const user = userEvent.setup();
    render(<PushSettings />);

    await user.click(screen.getByRole("button", { name: "Send test push" }));

    expect(sendTestMutate).toHaveBeenCalledTimes(1);
  });
});
