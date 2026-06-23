#!/usr/bin/env node
"use strict";

// Tests the production Andorra Review post-review script
// (.github/workflows/andorra-ocr-review.yml) — specifically the "Scanner
// breakdown" section and its nested per-scanner "full output" dropdowns. The
// script is embedded inline in the workflow YAML, so we extract it and run it
// in a vm sandbox with mocked fs / github / process, then assert on the
// finalized status-comment body.

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const repoRoot = path.join(__dirname, "..", "..");
const workflowPath = ".github/workflows/andorra-ocr-review.yml";

// GitHub rejects an issue-comment body over this many characters with a 422.
const GH_COMMENT_LIMIT = 65536;

// Extract the inline github-script body that contains `marker`. Mirrors the
// indentation handling in post-review-comments.test.js: a `script: |` line
// opens a block whose content is indented two past the key.
function extractScript(marker) {
  const text = fs.readFileSync(path.join(repoRoot, workflowPath), "utf8");
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(/^(\s*)script:\s*\|\s*$/);
    if (!m) continue;
    const blockIndent = m[1].length + 2;
    const block = [];
    for (let j = i + 1; j < lines.length; j++) {
      const cur = lines[j];
      if (cur.trim() === "") {
        block.push("");
        continue;
      }
      if (cur.match(/^ */)[0].length < blockIndent) break;
      block.push(cur.slice(blockIndent));
    }
    const script = block.join("\n");
    if (script.includes(marker)) return script;
  }
  throw new Error(`script containing ${JSON.stringify(marker)} not found in ${workflowPath}`);
}

function mockGithub() {
  const updateComment = [];
  const createComment = [];
  const createReview = [];
  return {
    updateComment,
    createComment,
    createReview,
    rest: {
      pulls: { createReview: async (p) => { createReview.push(p); return { data: {} }; } },
      issues: {
        updateComment: async (p) => { updateComment.push(p); return { data: {} }; },
        createComment: async (p) => { createComment.push(p); return { data: {} }; },
      },
      reactions: { deleteForIssueComment: async () => ({ data: {} }) },
    },
  };
}

async function runPostReview(reviewJson) {
  const script = extractScript("Scanner breakdown");
  const github = mockGithub();
  const sandbox = {
    github,
    context: { repo: { owner: "owner", repo: "repo" }, payload: {} },
    core: { warning() {}, setOutput() {} },
    console: { log() {}, error() {} },
    process: { env: { PR_NUMBER: "123", PR_HEAD_SHA: "head-sha", STATUS_COMMENT_ID: "999" } },
    require(name) {
      if (name === "fs") return { readFileSync: () => JSON.stringify(reviewJson) };
      throw new Error(`unexpected require: ${name}`);
    },
  };
  await vm.runInNewContext(`(async () => {\n${script}\n})()`, sandbox, { timeout: 2000 });
  assert.strictEqual(github.updateComment.length, 1, "expected the status comment to be finalized once");
  return github.updateComment[0].body;
}

// 1. The nested per-scanner dropdown renders the scanner's raw output, even
//    when the arbiter failed and there are no accepted findings/inline comments.
async function testNestedRawDropdownRenders() {
  const body = await runPostReview({
    comments: [],
    warnings: [{ type: "arbiter_failed", message: "arbiter call failed" }],
    ensemble: {
      duration_ms: 704000, // 11m 44s total
      scanners: [
        {
          name: "spark", status: "partial", findings: 2, provider: "Spark",
          model: "DeepSeek-V4-Flash", err: "all 2 file review(s) failed",
          duration: 90000000000, // 90s in nanoseconds (Go time.Duration)
          raw: [
            {
              path: "main.go", start_line: 10, end_line: 12, title: "nil map write",
              explicit_title: true, severity: "P1", confidence: 0.9,
              detail: "writes to a nil map and panics at runtime",
              existing_code: "m[k] = v", suggestion_code: "if m == nil { m = map[string]int{} }\nm[k] = v",
            },
            { path: "util.go", start_line: 5, end_line: 5, title: "off-by-one", detail: "loop overruns the slice" },
          ],
        },
      ],
      groups: [],
      token_summary: [],
    },
  });

  assert.match(body, /⏱️ \*\*Elapsed:\*\* 11m 44s/, "total elapsed line missing");
  assert.match(body, /\| Scanner \| Status \| Findings \| Elapsed \| Provider \|/, "breakdown table missing Elapsed column");
  assert.match(body, /1m 30s/, "per-scanner elapsed missing from breakdown row");
  assert.match(body, /<summary>Scanner breakdown<\/summary>/, "outer breakdown dropdown missing");
  assert.match(body, /<summary>spark — full output \(2 finding\(s\)\)<\/summary>/, "nested per-scanner dropdown missing");
  assert.match(body, /nil map write/, "raw finding title missing");
  assert.match(body, /writes to a nil map/, "raw finding detail missing");
  assert.match(body, /main\.go:10-12/, "raw finding location missing");
  assert.match(body, /P1 · confidence 0\.90/, "raw finding severity/confidence missing");
  // The second finding has no explicit_title, so its (synthesized) title is
  // suppressed to avoid duplicating the detail's first line — only its detail
  // should render.
  assert.match(body, /loop overruns the slice/, "second raw finding (detail) missing");
  assert.ok(!body.includes("off-by-one"), "a synthesized (non-explicit) title should not be rendered");
  // The nested dropdown must sit INSIDE the outer breakdown dropdown.
  const outerOpen = body.indexOf("<summary>Scanner breakdown</summary>");
  const nestedOpen = body.indexOf("<summary>spark — full output");
  const outerClose = body.lastIndexOf("</details>");
  assert.ok(outerOpen < nestedOpen && nestedOpen < outerClose, "nested dropdown is not inside the breakdown");
}

// 2. Oversized scanner output is truncated so the comment stays under GitHub's
//    65536-char limit, with a pointer to the full artifact.
async function testOversizedOutputIsTruncated() {
  const huge = "x".repeat(200000);
  const many = [];
  for (let i = 0; i < 50; i++) {
    many.push({ path: `f${i}.go`, start_line: i, end_line: i, title: `finding ${i}`, detail: huge });
  }
  const body = await runPostReview({
    comments: [],
    warnings: [],
    ensemble: {
      scanners: [{ name: "spark", status: "ok", findings: many.length, provider: "Spark", raw: many }],
      groups: [],
      token_summary: [],
    },
  });

  assert.ok(body.length < GH_COMMENT_LIMIT, `body is ${body.length} chars, must be < ${GH_COMMENT_LIMIT}`);
  assert.match(body, /truncated|more char\(s\)|more finding\(s\) omitted/, "expected a truncation notice");
  assert.match(body, /debug-trace\.json/, "truncation notice should point at the artifact");
}

// 3. A scanner with no raw output renders the table row but no empty dropdown.
async function testNoRawNoDropdown() {
  const body = await runPostReview({
    comments: [],
    warnings: [],
    ensemble: {
      scanners: [{ name: "spark", status: "ok", findings: 0, provider: "Spark" }],
      groups: [],
      token_summary: [],
    },
  });
  assert.match(body, /<summary>Scanner breakdown<\/summary>/, "breakdown table should still render");
  assert.ok(!body.includes("full output"), "no nested dropdown expected when a scanner has no raw output");
}

async function main() {
  await testNestedRawDropdownRenders();
  await testOversizedOutputIsTruncated();
  await testNoRawNoDropdown();
  console.log("scanner-breakdown.test.js: all assertions passed");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
