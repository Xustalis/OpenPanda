// Minimal typings for the two node builtins the parser test imports. The
// console follows the same rule as src/types.d.ts: hand-write the handful of
// signatures actually used rather than pull a whole @types package in for a
// single test file. `npm test` runs on node's own runner and type stripping,
// so nothing here reaches the browser bundle.

declare module 'node:test' {
  /** The mock-timer surface the bus tests drive (node ≥ 20.4). */
  interface MockTimers {
    enable(opts: { apis?: Array<'setTimeout' | 'setInterval' | 'setImmediate' | 'Date'> }): void
    tick(ms: number): void
    reset(): void
  }
  export interface TestContext {
    mock: { timers: MockTimers }
    after(fn: () => void | Promise<void>): void
  }
  export function test(name: string, fn: (t: TestContext) => void | Promise<void>): void
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
