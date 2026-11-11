// Tests for parse, the collector's only check on what it stores.
// Run with: node --test   (from collector/; needs Node 20 or later)

import { test } from "node:test";
import assert from "node:assert/strict";
import { parse } from "./worker.js";

const good = {
  v: 1,
  meru: "0.7.3",
  os: "darwin",
  arch: "arm64",
  profile: "lite",
  age: "month",
  questions: "1-5",
};

// with_ returns the good report with one field changed; undefined drops it.
// (The underscore is there because "with" is a reserved word in JavaScript.)
function with_(field, value) {
  const r = { ...good, [field]: value };
  if (value === undefined) delete r[field];
  return JSON.stringify(r);
}

test("accepts the report merud sends", () => {
  assert.deepEqual(parse(JSON.stringify(good)), good);
});

test("refuses anything else", () => {
  const cases = {
    "not JSON": "not json",
    "an array": "[1,2,3]",
    "null": "null",
    "a string": '"hello"',
    "an extra field": JSON.stringify({ ...good, id: "x" }),
    "a missing field": with_("questions", undefined),
    "v as a string": with_("v", "1"),
    "a future v": with_("v", 2),
    "a fork version": with_("meru", "0.7.3-acme"),
    "dev": with_("meru", "dev"),
    "a long version": with_("meru", "1234567890.1.1234567"),
    "a version as a number": with_("meru", 7),
    "an unknown os": with_("os", "freebsd"),
    "an unknown chip": with_("arch", "riscv64"),
    "an unknown profile": with_("profile", "custom"),
    "an unknown age": with_("age", "day"),
    "a raw question count": with_("questions", "3"),
    "a prototype key": '{"__proto__":{},"v":1,"meru":"0.7.3","os":"darwin","arch":"arm64","profile":"lite","age":"month","questions":"1-5"}',
  };
  for (const [name, body] of Object.entries(cases)) {
    assert.equal(parse(body), null, name);
  }
});
