import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";

import EmailSettings from "./EmailSettings";

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

function makeForm({
  dirty = [] as string[],
  values = SAVED,
  sensitiveConfigured = ["email.smtp_password"],
} = {}) {
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    resetValue: vi.fn(),
    isDirty: vi.fn((key: string) => dirty.includes(key)),
    isClearStaged: vi.fn((key: string) => dirty.includes(key) && (values[key] ?? "") === ""),
    dirtyCount: dirty.length,
    dirtyKeys: dirty,
    save: vi.fn(() => Promise.resolve()),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured,
    sensitiveManagedByEnv: [],
  };
}

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

  it("holds the mail server that used to sit in the Email channel card", () => {
    useSettingsFormMock.mockReturnValue(makeForm());
    renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "Email" })).toBeInTheDocument();
    expect(new Set(useSettingsFormMock.mock.calls[0]?.[0]?.keys)).toEqual(
      new Set([
        "email.enabled",
        "email.from_address",
        "email.smtp_host",
        "email.smtp_port",
        "email.smtp_security",
        "email.smtp_username",
        "email.smtp_password",
        "email.from_name",
      ]),
    );

    const group = screen.getByRole("group", { name: "Mail server" });
    expect(
      within(group).getByText(
        "Silo sends invitations, password resets, and notification emails through it.",
      ),
    ).toBeInTheDocument();
    expect(
      within(group).getByRole("switch", { name: "Send email from this server" }),
    ).toBeChecked();
    expect(within(group).getByLabelText("From address")).toHaveValue("silo@example.com");
    expect(within(group).getByLabelText("Mail server address")).toHaveValue("smtp.example.com");
    expect(within(group).getByRole("spinbutton", { name: "Port" })).toHaveValue(587);
    expect(within(group).getByRole("combobox", { name: "Encryption" })).toHaveTextContent(
      "STARTTLS",
    );
    expect(within(group).getByLabelText("Username")).toHaveValue("mailer");
    expect(within(group).getByLabelText("Test email recipient")).toBeInTheDocument();
  });

  it("shows the sender name beside the from address with no Advanced disclosure", () => {
    useSettingsFormMock.mockReturnValue(makeForm());
    renderPage();

    expect(screen.getByLabelText("From name")).toHaveValue("Silo");
    expect(screen.queryByRole("button", { name: /^Advanced/ })).not.toBeInTheDocument();
  });

  it("stages the mail server through the page's own save bar", async () => {
    const form = makeForm({ dirty: ["email.smtp_host"] });
    useSettingsFormMock.mockReturnValue(form);
    renderPage();

    await userEvent.type(screen.getByLabelText("Mail server address"), "x");
    expect(form.setValue).toHaveBeenCalledWith("email.smtp_host", "smtp.example.comx");
    await userEvent.click(screen.getByRole("switch", { name: "Send email from this server" }));
    expect(form.setValue).toHaveBeenCalledWith("email.enabled", "false");
    await userEvent.click(screen.getByRole("combobox", { name: "Encryption" }));
    await userEvent.click(screen.getByRole("option", { name: "TLS (implicit)" }));
    expect(form.setValue).toHaveBeenCalledWith("email.smtp_security", "tls");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).toHaveBeenCalledOnce();
  });

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
