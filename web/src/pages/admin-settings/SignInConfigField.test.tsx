// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { field } from "@/components/admin/plugins/adminFormField.test-support";

import { SignInConfigField } from "./SignInConfigField";

describe("SignInConfigField", () => {
  it("masks a secret its plugin marks multiline", () => {
    render(
      <SignInConfigField
        field={field("client_secret", "Client secret", "TEXTAREA", {
          secret: true,
          multiline: true,
        })}
        values={{}}
        idPrefix="oidc"
        dirty={false}
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Client secret")).toHaveAttribute("type", "password");
  });
});
