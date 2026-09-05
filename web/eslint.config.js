import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import eslintConfigPrettier from "eslint-config-prettier";

export default tseslint.config(
  { ignores: ["dist"] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.{ts,tsx}"],
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-empty-object-type": [
        "error",
        { allowInterfaces: "with-single-extends" },
      ],
      // New react-hooks v7 rules — warn for now, tighten later.
      "react-hooks/exhaustive-deps": "warn",
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/set-state-in-effect": "warn",
      "react-hooks/refs": "warn",
      "react-hooks/preserve-manual-memoization": "warn",
    },
  },
  {
    // Files on the v2 contract: every request goes through the typed boundary
    // (src/api/v2/request.ts), so the shape is inferred from the generated
    // spec rather than asserted at the call site. Extend the list as call
    // sites migrate; never remove an entry.
    files: [
      "src/api/v2/**/*.{ts,tsx}",
      "src/hooks/useAuth.tsx",
      "src/hooks/queries/libraries.ts",
      "src/hooks/queries/onboarding.ts",
      "src/hooks/queries/progress.ts",
      "src/hooks/queries/profiles.ts",
      "src/hooks/queries/policy.ts",
      "src/hooks/queries/account.ts",
      "src/hooks/queries/admin/users.ts",
      "src/pages/ActivateDevice.tsx",
      "src/pages/Login.tsx",
      "src/pages/OAuthComplete.tsx",
      "src/pages/Signup.tsx",
    ],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.name='api'][typeArguments]",
          message:
            "api<T>(...) asserts a response shape. Migrated files call v2(...) and let the contract infer it.",
        },
        {
          selector: "CallExpression[callee.name='apiWithProfileRequestContext'][typeArguments]",
          message:
            "apiWithProfileRequestContext<T>(...) asserts a response shape. Migrated files call v2(...) and let the contract infer it.",
        },
        {
          selector: "TSAsExpression > TSTypeReference > Identifier[name=/(Response|Request)$/]",
          message:
            "Casting to a *Response/*Request type bypasses the contract. Use the inferred v2 types instead.",
        },
      ],
    },
  },
  eslintConfigPrettier,
);
