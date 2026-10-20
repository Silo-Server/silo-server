// Typechecks the web app with tsrs, and falls back to tsc on platforms tsrs
// ships no binary for, such as Windows and Intel macOS.
//
// tsrs keeps each binary in a per-platform optional dependency, and the package
// manager installs only the one matching this machine, if any.
//
// Usage: node scripts/typecheck.mjs
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

function hasTsrsBinary() {
  const tsrsManifest = require.resolve("tsrs/package.json");
  const { optionalDependencies = {} } = require(tsrsManifest);
  const requireFromTsrs = createRequire(tsrsManifest);
  return Object.keys(optionalDependencies).some((name) => {
    try {
      requireFromTsrs.resolve(`${name}/package.json`);
      return true;
    } catch {
      return false;
    }
  });
}

let checker = "tsrs";
if (!hasTsrsBinary()) {
  console.warn(
    `tsrs has no binary for ${process.platform}-${process.arch}; typechecking with tsc.`,
  );
  checker = "tsc";
}
const result = spawnSync(checker, ["-b"], {
  stdio: "inherit",
  shell: process.platform === "win32",
});
if (result.error) {
  console.error(`Failed to run ${checker}: ${result.error.message}`);
}
process.exit(result.status ?? 1);
