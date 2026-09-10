/**
 * OLPC canonical JSON (http://wiki.laptop.org/go/Canonical_JSON) — the
 * canonicalization used by TUF metadata (securesystemslib `cjson`).
 *
 * The Keryx app consumes TUF root/timestamp/snapshot/targets metadata and
 * must reproduce the exact bytes the publisher signed (spec/core.md §1:
 * "TUF metadata canonicalization: securesystemslib canonical JSON (OLPC)
 * — delegated to the TUF library; never hand-rolled" — that applies to the
 * publisher; the app implements the same rules to verify).
 *
 * Rules (mirrors go-securesystemslib cjson.EncodeCanonical):
 * - strings: escape only `\` and `"`; everything else stays raw UTF-8
 * - numbers: integers only (floats are rejected by TUF tooling)
 * - object keys: sorted by UTF-8 byte value (Go string sort order)
 * - no whitespace anywhere
 */

const encoder = new TextEncoder();

function byteLess(a: string, b: string): boolean {
  const ba = encoder.encode(a);
  const bb = encoder.encode(b);
  const n = Math.min(ba.length, bb.length);
  for (let i = 0; i < n; i++) {
    if (ba[i] !== bb[i]) return ba[i] < bb[i];
  }
  return ba.length < bb.length;
}

function byteCompare(a: string, b: string): number {
  return byteLess(a, b) ? -1 : byteLess(b, a) ? 1 : 0;
}

function encodeString(s: string): string {
  return '"' + s.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';
}

export function olpcCanonical(value: unknown): string {
  if (value === null) return 'null';
  switch (typeof value) {
    case 'boolean':
      return value ? 'true' : 'false';
    case 'number':
      if (!Number.isInteger(value)) {
        throw new Error(`OLPC canonical JSON: non-integer number ${value}`);
      }
      return String(value);
    case 'string':
      return encodeString(value);
    case 'object': {
      if (Array.isArray(value)) {
        return '[' + value.map(olpcCanonical).join(',') + ']';
      }
      const obj = value as Record<string, unknown>;
      const keys = Object.keys(obj).sort(byteCompare);
      return (
        '{' +
        keys.map((k) => encodeString(k) + ':' + olpcCanonical(obj[k])).join(',') +
        '}'
      );
    }
    default:
      throw new Error(`OLPC canonical JSON: cannot encode ${typeof value}`);
  }
}
