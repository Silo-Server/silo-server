import { describe, expect, it } from "vitest";

import { emailDir } from "./direction";

describe("emailDir", () => {
  // undefined keeps the page's direction; "ltr" forces left to right.
  it.each([
    ["a Latin name", "alexandra@example.com", "ltr"],
    ["an Arabic name", "وسام.عبدالله@النطاق.مثال.شبكة", undefined],
    ["an Arabic name with a Latin domain", "عبدالله.الحربي@example.com", undefined],
    ["a Latin name with an Arabic domain", "admin@مثال.شبكة", "ltr"],
    ["ASCII digits before an Arabic domain", "1234567890@مثال.شبكة", "ltr"],
    ["Adlam digits, which set direction", "𞥑𞥒𞥓𞥔@example.test", undefined],
    ["a Hebrew maqaf, which sets direction", "־@דוגמה.רשת", undefined],
    ["Persian digits, which don't", "۱۲۳۴۵۶@example.test", "ltr"],
    ["a fullwidth digit before an Arabic name", "１وسام@example.com", undefined],
    ["Arabic-Indic digits before a Latin name", "١٢٣.alice@example.test", "ltr"],
    ["only Arabic-Indic digits, before an Arabic domain", "١٢٣٤٥@مثال.شبكة", undefined],
    ["only Arabic-Indic digits, before a Latin domain", "١٢٣٤٥@example.test", "ltr"],
  ])("%s", (_case, email, want) => {
    expect(emailDir(email)).toBe(want);
  });
});
