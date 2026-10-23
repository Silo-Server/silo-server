import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";

import EmailSettings from "./EmailSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

// JSDOM omits the browser methods used by Radix Select.
if (!HTMLElement.prototype.hasPointerCapture) HTMLElement.prototype.hasPointerCapture = () => false;
if (!HTMLElement.prototype.scrollIntoView) HTMLElement.prototype.scrollIntoView = () => {};

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

const SAVED: Record<string, string> = {
  "email.enabled": "true",
  "email.from_address": "silo@example.com",
  "email.from_name": "Silo",
  "email.smtp_host": "smtp.example.com",
  "email.smtp_port": "587",
  "email.smtp_security": "starttls",
  "email.smtp_username": "mailer",
};

const makeForm = ({
  dirty = [] as string[],
  values = SAVED,
  sensitiveConfigured = ["email.smtp_password"],
} = {}) =>
  fakeSettingsForm(values, { dirty, sensitiveConfigured, save: vi.fn(() => Promise.resolve()) });

function renderPage() {
  return render(
    <MemoryRouter>
      <EmailSettings />
    </MemoryRouter>,
  );
}

describe("EmailSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    useSettingsFormMock.mockReset();
    toast.success.mockReset();
    toast.error.mockReset();
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows the saved SMTP password as a masked, editable input", async () => {
    const form = makeForm();
    useSettingsFormMock.mockReturnValue(form);
    renderPage();

    // No Replace step: typing stages a replacement, blank keeps the saved one.
    const input = screen.getByLabelText("Password");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    expect(screen.queryByRole("button", { name: /Replace/ })).not.toBeInTheDocument();

    // A relay that needs no sign-in has to be able to drop the password too.
    await userEvent.click(screen.getByRole("button", { name: "Clear saved value" }));
    expect(form.setValue).toHaveBeenCalledWith("email.smtp_password", "");
  });

  it("sends a test email through the saved settings", async () => {
    setAccessToken("test-admin");
    setRefreshToken(null);
    setProfileId("profile-a");
    setProfileToken("pin-a");
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ ok: true, duration_ms: 7 }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    useSettingsFormMock.mockReturnValue(makeForm());
    renderPage();

    await userEvent.type(screen.getByLabelText("Test email recipient"), "admin@example.test");
    await userEvent.click(screen.getByRole("button", { name: "Send test" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/email/test");
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({ to: "admin@example.test" }),
    });
    expect(await screen.findByText("Delivered to the mail server in 7ms.")).toBeInTheDocument();
  });
});
