import { describe, expect, it } from "vitest";

import { emailDir, mixesDirections, startsRightToLeft } from "./direction";

describe("startsRightToLeft", () => {
  it.each([
    ["a Latin title", "The Return of the King", false],
    ["digits before a Latin title", "1883 (2021)", false],
    ["an Arabic title", "عودة الملك", true],
    ["a Hebrew title", "שובו של המלך", true],
    ["a Syriac title", "ܡܠܟܐ", true],
    ["a Thaana title", "ދިވެހި", true],
    ["an N'Ko title", "ߒߞߏ", true],
    ["an Adlam digit before a Latin title", "𞥑 Days", true],
    ["an N'Ko digit before a Latin title", "߁ Days", true],
    ["an Arabic question mark before a Latin title", "؟ Days", true],
    ["only digits", "2049", false],
  ])("%s", (_case, text, want) => {
    expect(startsRightToLeft(text)).toBe(want);
  });
});

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

describe("mixesDirections", () => {
  it.each([
    ["one English paragraph", "Frodo and Sam set out.", false],
    ["English paragraphs", "A collection.\n\nFrodo and Sam set out.", false],
    ["Arabic paragraphs", "مجموعة أفلام.\n\nيواصل فرودو وسام رحلتهما.", false],
    ["Arabic, then English", "مجموعة أفلام.\n\nFrodo and Sam set out.", true],
    ["English, then Hebrew", "A collection.\nשר הטבעות", true],
    ["English, then Thaana", "A collection.\nދިވެހި", true],
    // A line with nothing that sets direction runs left to right.
    ["Arabic with a line of digits and punctuation", "مجموعة أفلام.\n1999–2004\n***", true],
    ["English with a line of digits", "A collection.\n1999–2004", false],
    ["Arabic with a line of only a zero-width space", "مجموعة أفلام.\n\u200b", false],
    ["English with an Arabic word inside a line", "The film was called باب الحارة.", false],
    ["Windows line endings", "A collection.\r\nFrodo and Sam set out.", false],
    ["an RLM before an English line", "A collection.\n\u200fFrodo and Sam set out.", true],
    ["an ALM before an English line", "A collection.\n\u061cFrodo and Sam set out.", true],
    ["an Arabic question mark before an English line", "A collection.\n؟ Frodo asks.", true],
    ["an LRM before an Arabic line", "A collection.\n\u200eيواصل فرودو وسام رحلتهما.", false],
  ])("%s", (_case, text, want) => {
    expect(mixesDirections(text)).toBe(want);
  });
});
