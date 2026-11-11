// Tests for parse, the collector's only check on what it stores.
// Run with: node --test   (from collector/; needs Node 20 or later)

import { test } from "node:test";
import assert from "node:assert/strict";
import { parse } from "./worker.js";

const v1 = {
  v: 1, meru: "0.7.6", os: "darwin", arch: "arm64", profile: "lite", age: "month", questions: "1-5",
};
const v2 = {
  v: 2, meru: "0.7.7", os: "darwin", arch: "arm64", profile: "lite", age: "month", questions: "1-5",
  active_days: "3-5", streak: "2-4", previous: "0.7.6", week_first: true, month_first: false, first: false,
};

// with_ returns base with one field changed; undefined drops it.
// (The underscore is there because "with" is a reserved word in JavaScript.)
function with_(base, field, value) {
  const r = { ...base, [field]: value };
  if (value === undefined) delete r[field];
  return JSON.stringify(r);
}

test("accepts the reports merud sends, both versions", () => {
  assert.deepEqual(parse(JSON.stringify(v1)), v1);
  assert.deepEqual(parse(JSON.stringify(v2)), v2);
  assert.ok(parse(with_(v2, "previous", "none")));
});

test("refuses anything else", () => {
  const cases = {
    "not JSON": "not json",
    "an array": "[1,2,3]",
    "null": "null",
    "a string": '"hello"',
    "v1 with an extra field": JSON.stringify({ ...v1, id: "x" }),
    "v1 with v2 fields": JSON.stringify({ ...v2, v: 1 }),
    "v2 with v1 keys": JSON.stringify({ ...v1, v: 2 }),
    "v2 with an ID": JSON.stringify({ ...v2, id: "x" }),
    "v2 missing a flag": with_(v2, "first", undefined),
    "a flag as a string": with_(v2, "first", "true"),
    "a flag as a number": with_(v2, "week_first", 1),
    "an unknown streak": with_(v2, "streak", "3"),
    "an unknown active_days": with_(v2, "active_days", "4"),
    "previous not a version": with_(v2, "previous", "rm -rf /"),
    "previous empty": with_(v2, "previous", ""),
    "v as a string": with_(v2, "v", "2"),
    "a future v": with_(v2, "v", 3),
    "a fork version": with_(v2, "meru", "0.7.7-acme"),
    "dev": with_(v1, "meru", "dev"),
    "a long version": with_(v1, "meru", "1234567890.1.1234567"),
    "an unknown os": with_(v1, "os", "freebsd"),
    "a raw question count": with_(v1, "questions", "3"),
    "a prototype key": '{"__proto__":{},"v":1,"meru":"0.7.6","os":"darwin","arch":"arm64","profile":"lite","age":"month","questions":"1-5"}',
  };
  for (const [name, body] of Object.entries(cases)) {
    assert.equal(parse(body), null, name);
  }
});
