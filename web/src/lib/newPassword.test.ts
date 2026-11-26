import { describe, expect, it } from "vitest";

import { newPasswordProblem } from "./newPassword";

const strict = {
  minimum_length: 12,
  maximum_bytes: 72,
  require_uppercase: true,
  require_special: true,
};

describe("newPasswordProblem", () => {
  it("takes a plain password under the default policy", () => {
    expect(newPasswordProblem("plainpassword", "plainpassword")).toBeNull();
  });

  it("names the server's minimum length", () => {
    expect(newPasswordProblem("Short-pass1", "Short-pass1", strict)).toBe(
      "Use at least 12 characters.",
    );
  });

  it("asks for a capital letter when the policy requires one", () => {
    expect(newPasswordProblem("correct-horse1", "correct-horse1", strict)).toBe(
      "Include at least one capital letter.",
    );
  });

  it("asks for a special character, and a space is not one", () => {
    expect(newPasswordProblem("Correct horse1", "Correct horse1", strict)).toBe(
      "Include at least one special character, such as ! or #.",
    );
  });

  it("counts non-Latin capitals and symbols", () => {
    expect(newPasswordProblem("Ωcorrecthorse€", "Ωcorrecthorse€", strict)).toBeNull();
  });
});
