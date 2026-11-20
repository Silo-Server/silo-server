// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { PluginConfigSchema } from "@/api/types";

import { PluginConfigForm } from "./PluginConfigForm";
import { field } from "./adminFormField.test-support";

const schema: PluginConfigSchema = {
  key: "account",
  title: "Account",
  json_schema: "{}",
  required: true,
  admin_form: {
    fields: [
      field("api_key", "API Key", "PASSWORD", { secret: true }),
      field("region", "Region", "TEXT"),
    ],
  },
};

describe("PluginConfigForm secrets", () => {
  it("derives a form when a plugin only supplies JSON Schema", () => {
    render(
      <PluginConfigForm
        schema={{
          key: "server",
          title: "Server",
          json_schema: JSON.stringify({
            type: "object",
            properties: {
              base_url: { type: "string", title: "Base URL" },
              api_key: { type: "string", format: "password" },
            },
            required: ["base_url", "api_key"],
          }),
          required: true,
        }}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Base URL")).toBeInTheDocument();
    expect(screen.getByLabelText("Api Key")).toHaveAttribute("type", "password");
  });

  it("shows redacted saved state and only clears through an explicit action", async () => {
    const onSave = vi.fn();
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={onSave}
      />,
    );

    expect(screen.getByLabelText("API Key")).toHaveAttribute(
      "placeholder",
      "Saved secret — leave blank to keep",
    );
    expect(screen.getByText("API Key: saved")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      [],
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      ["api_key"],
    );
  });

  it("locks a managed secret and saves edits to the rest of its config entry", async () => {
    const onSave = vi.fn();
    const user = userEvent.setup();
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        managedFields={["api_key"]}
        onSave={onSave}
      />,
    );

    const secret = screen.getByLabelText("API Key");
    expect(secret).toBeDisabled();
    expect(secret).toHaveValue("");
    expect(secret).toHaveAccessibleDescription("Managed by the environment");
    expect(screen.queryByRole("button", { name: "Clear saved secret" })).toBeNull();

    const region = screen.getByLabelText("Region");
    await user.clear(region);
    await user.type(region, "eu");
    await user.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenCalledWith("account", { region: "eu" }, []);
  });

  it("drops drafts and secret clears for newly managed fields and keeps neighboring edits", async () => {
    const onSave = vi.fn();
    const props = { schema, value: { region: "saved" }, configuredSecrets: ["api_key"], onSave };
    const form = (managedFields: string[]) => (
      <PluginConfigForm {...props} managedFields={managedFields} />
    );
    const { rerender } = render(form([]));
    const region = screen.getByLabelText("Region");
    await userEvent.clear(region);
    await userEvent.type(region, "draft");
    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    rerender(form(["api_key"]));
    rerender(form([]));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith("account", { region: "draft" }, []);
    rerender(form(["region"]));
    expect(region).toHaveValue("saved");
    rerender(form([]));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith("account", { region: "saved" }, []);
  });

  it("sends an emptied field as an explicit clear so the stored value goes", async () => {
    const onSave = vi.fn();
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={onSave}
      />,
    );

    await userEvent.clear(screen.getByLabelText("Region"));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    const [, value, clearSecrets] = onSave.mock.lastCall!;
    expect(value).toEqual({ region: "" });
    expect(clearSecrets).toEqual([]);
  });

  it("does not offer to clear a required saved secret into an invalid config", () => {
    const requiredSchema: PluginConfigSchema = {
      ...schema,
      admin_form: {
        ...schema.admin_form!,
        fields: schema.admin_form!.fields.map((field) =>
          field.key === "api_key" ? { ...field, required: true } : field,
        ),
      },
    };
    render(
      <PluginConfigForm
        schema={requiredSchema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByText("API Key: saved (required)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear saved secret" })).not.toBeInTheDocument();
  });

  it("tests the exact draft including staged secret removals", async () => {
    const onTest = vi.fn().mockResolvedValue({
      success: false,
      message: "API key is required",
    });
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
        onTest={onTest}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    await userEvent.click(screen.getByRole("button", { name: "Check Connection" }));

    expect(onTest).toHaveBeenCalledWith("account", expect.objectContaining({ region: "us-east" }), [
      "api_key",
    ]);
  });

  it("reports each edit and staged secret removal as the entry it would save", async () => {
    const onDraftChange = vi.fn();
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
        onDraftChange={onDraftChange}
      />,
    );

    await userEvent.clear(screen.getByLabelText("Region"));
    await userEvent.type(screen.getByLabelText("Region"), "eu");
    expect(onDraftChange).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "eu" }),
      [],
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    expect(onDraftChange).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "eu" }),
      ["api_key"],
    );
  });

  it("keeps numeric-looking string selections as strings when the schema says so", async () => {
    const onSave = vi.fn();
    render(
      <PluginConfigForm
        schema={{
          key: "routing",
          title: "Routing",
          json_schema: JSON.stringify({
            type: "object",
            properties: { tags: { type: "array", items: { type: "string" } } },
          }),
          required: false,
          admin_form: {
            fields: [
              field("tags", "Tags", "MULTI_SELECT", {
                options: [
                  { value: "001", label: "Tag 001" },
                  { value: "123", label: "Tag 123" },
                ],
              }),
            ],
          },
        }}
        onSave={onSave}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Tag 001" }));
    await userEvent.click(screen.getByRole("button", { name: "Tag 123" }));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith("routing", { tags: ["001", "123"] }, []);
  });
});
