"use strict";
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const { spawn } = require("node:child_process");

async function run(t, handler, extra = []) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "silo-profile-test-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const server = http.createServer(handler);
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(() => server.close());
  const output = path.join(dir, "capture.pprof");
  const child = spawn(process.execPath, [path.join(__dirname, "silo-profile"), "--url", `http://127.0.0.1:${server.address().port}`, "--output", output, ...extra]);
  let stderr = "";
  child.stderr.on("data", data => { stderr += data; });
  const code = await new Promise(resolve => child.on("exit", resolve));
  return { output, code, stderr, metadata: JSON.parse(fs.readFileSync(output + ".json")) };
}

function headers(response) {
  response.writeHead(200, { "Content-Type": "application/octet-stream", "Trailer": "X-Silo-Capture-Interrupted", "X-Silo-Revision": "fixture" });
}

test("complete captures are private with metadata and checksum", async t => {
  const result = await run(t, (_request, response) => {
    headers(response);
    response.write(Buffer.from("fixture"));
    response.addTrailers({ "X-Silo-Capture-Interrupted": "false" });
    response.end();
  });
  assert.equal(result.code, 0, result.stderr);
  assert.equal(result.metadata.valid, true);
  assert.equal(result.metadata.bytes, 7);
  assert.equal(result.metadata.headers["x-silo-revision"], "fixture");
  assert.equal(result.metadata.sha256.length, 64);
  assert.equal(fs.statSync(result.output).mode & 0o777, 0o600);
  assert.equal(fs.statSync(result.output + ".json").mode & 0o777, 0o600);
  assert.equal(fs.existsSync(result.output + ".partial"), false);
});

test("byte limit aborts streaming and leaves an invalid bounded partial", async t => {
  const result = await run(t, (_request, response) => {
    headers(response);
    response.write(Buffer.alloc(16384));
    response.addTrailers({ "X-Silo-Capture-Interrupted": "false" });
    response.end();
  }, ["--max-bytes", "1024"]);
  assert.equal(result.code, 1);
  assert.equal(result.metadata.valid, false);
  assert.match(result.metadata.reason, /byte limit/);
  assert.equal(result.metadata.bytes, 1024);
  assert.equal(fs.statSync(result.output + ".partial").size, 1024);
  assert.equal(fs.existsSync(result.output), false);
});

test("server cancellation never publishes a completed artifact", async t => {
  const result = await run(t, (_request, response) => {
    headers(response);
    response.write("fixture");
    response.addTrailers({ "X-Silo-Capture-Interrupted": "true" });
    response.end();
  });
  assert.equal(result.code, 1);
  assert.equal(result.metadata.valid, false);
  assert.equal(fs.existsSync(result.output), false);
});

test("missing trailers and HTTP errors are invalid captures", async t => {
  for (const status of [200, 429]) {
    const result = await run(t, (_request, response) => {
      response.writeHead(status, { "Content-Type": "application/octet-stream" });
      response.end("fixture");
    });
    assert.equal(result.code, 1);
    assert.equal(result.metadata.valid, false);
    assert.equal(fs.existsSync(result.output), false);
  }
});
