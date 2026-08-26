// Minimal typings for the two node builtins the parser test imports. The
// console follows the same rule as src/types.d.ts: hand-write the handful of
// signatures actually used rather than pull a whole @types package in for a
// single test file. `npm test` runs on node's own runner and type stripping,
// so nothing here reaches the browser bundle.

declare module 'node:test' {
  export function test(name: string, fn: () => void | Promise<void>): void
}

declare module 'node:assert/strict' {
  interface StrictAssert {
    /** Strict deep equality (node:assert/strict aliases deepStrictEqual). */
    deepEqual(actual: unknown, expected: unknown, message?: string): void
    /** Strict equality (=== with NaN handling). */
    equal(actual: unknown, expected: unknown, message?: string): void
    ok(value: unknown, message?: string): void
  }
  const assert: StrictAssert
  export default assert
}
