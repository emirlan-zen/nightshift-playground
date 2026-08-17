import "@testing-library/jest-dom/vitest";
import { afterAll, afterEach, beforeAll } from "vitest";
import { cleanup } from "@testing-library/react";
import { server } from "./msw";

// Node 26 exposes an incomplete experimental localStorage global unless a file
// is configured. Use a deterministic in-memory browser-compatible store in
// tests so the suite behaves the same on Node 22 and newer releases.
class MemoryStorage implements Storage {
  private values = new Map<string, string>();
  get length() {
    return this.values.size;
  }
  clear() {
    this.values.clear();
  }
  getItem(key: string) {
    return this.values.get(key) ?? null;
  }
  key(index: number) {
    return [...this.values.keys()][index] ?? null;
  }
  removeItem(key: string) {
    this.values.delete(key);
  }
  setItem(key: string, value: string) {
    this.values.set(key, String(value));
  }
}
Object.defineProperty(globalThis, "localStorage", {
  value: new MemoryStorage(),
  configurable: true,
});

// jsdom intentionally leaves scrollTo unimplemented; the real app resets
// scroll on full-page route changes so phone navigation never inherits a long
// report's offset.
Object.defineProperty(window, "scrollTo", { value: () => undefined, writable: true });

// @xyflow/react needs ResizeObserver + DOMMatrixReadOnly to measure and show
// nodes; without these the canvas mounts but every node stays hidden. These
// are the mocks from xyflow's testing guide: ResizeObserver reports each
// observed target once so nodes get non-zero dimensions and become visible.
class ResizeObserverMock {
  private cb: ResizeObserverCallback;
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
  }
  observe(target: Element) {
    // Fire async like a real ResizeObserver (after layout): xyflow observes
    // node elements from child effects that run before the parent syncs the
    // node lookup — a synchronous callback would hit an empty store and the
    // measurement would be dropped forever.
    setTimeout(() => {
      this.cb(
        [{ target, contentRect: target.getBoundingClientRect() } as ResizeObserverEntry],
        this as unknown as ResizeObserver,
      );
    }, 0);
  }
  unobserve() {}
  disconnect() {}
}
window.ResizeObserver = ResizeObserverMock as unknown as typeof ResizeObserver;

class DOMMatrixReadOnlyMock {
  m22: number;
  constructor(transform?: string) {
    const scale = transform?.match(/scale\(([1-9.]+)\)/)?.[1];
    this.m22 = scale !== undefined ? +scale : 1;
  }
}
window.DOMMatrixReadOnly = DOMMatrixReadOnlyMock as unknown as typeof DOMMatrixReadOnly;

Object.defineProperties(window.HTMLElement.prototype, {
  offsetHeight: {
    get() {
      return parseFloat((this as HTMLElement).style.height) || 1;
    },
  },
  offsetWidth: {
    get() {
      return parseFloat((this as HTMLElement).style.width) || 1;
    },
  },
});
(window.SVGElement.prototype as unknown as { getBBox: () => DOMRect }).getBBox = () =>
  ({ x: 0, y: 0, width: 0, height: 0 }) as DOMRect;

// One MSW server for the whole run. Unhandled requests error so a view fetching
// an endpoint we forgot to mock fails loudly instead of hanging.
beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  cleanup();
});
afterAll(() => server.close());
