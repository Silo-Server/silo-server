// Typechecks the web app with tsrs, and falls back to tsc on platforms tsrs
// ships no binary for, such as Windows and Intel macOS.
//
// tsrs keeps each binary in a per-platform optional dependency named
// @tsrs/<os>-<cpu>, with a -gnu or -musl suffix on Linux. A node_modules shared
// across machines can hold other platforms' packages, so only this host's
// package counts. A wrong libc guess falls back to tsc rather than failing.
//
// Usage: node scripts/typecheck.mjs
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

function tsrsPlatformPackage() {
  let libc = "";
  if (process.platform === "linux") {
    libc = process.report?.getReport().header.glibcVersionRuntime ? "-gnu" : "-musl";
  }
  return `@tsrs/${process.platform}-${process.arch}${libc}`;
}

function hasTsrsBinary() {
  const requireFromTsrs = createRequire(require.resolve("tsrs/package.json"));
  try {
    requireFromTsrs.resolve(`${tsrsPlatformPackage()}/package.json`);
    return true;
  } catch {
    return false;
  }
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
