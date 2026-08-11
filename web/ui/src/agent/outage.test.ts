import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLocale } from "@/i18n";
import { useAgentStream } from "./useAgentStream";

// The asserted copy is Chinese: pin the locale so the tests do not depend on
// the runner's navigator.language.
setLocale("zh-CN");

// The outage machinery (noteOutage/clearOutage/retryNow) is what the
// cannot-connect page renders. These tests drive open() with a stubbed fetch:
// network failures, terminal HTTP refusals, and a recovery.

function fetchRejecting(message: string) {
  return vi.fn().mockRejectedValue(new Error(message));
}

function fetchWithStatus(status: number) {
  return vi.fn().mockResolvedValue({ ok: false, status, body: null });
}

// A body that opens and stays open: the read loop parks inside it, which is
// what a healthy connection looks like from the store's side.
const okResponse = () => ({
  ok: true,
  status: 200,
  body: new ReadableStream({ start() {} }),
});

describe("connection outage", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("waits out the grace window before declaring the page unreachable", async () => {
    vi.stubGlobal("fetch", fetchRejecting("network down"));
    const s = useAgentStream();

    await s.connect("s1"); // attempt 1 fails at once
    expect(s.unreachable.value).toBe(false); // a blip must not flash the page
    expect(s.outage.value).toMatchObject({ attempts: 1, gaveUp: false, message: "network down" });

    await vi.advanceTimersByTimeAsync(500); // the first backoff retry fires
    expect(s.outage.value?.attempts).toBe(2);
    expect(s.unreachable.value).toBe(false);

    await vi.advanceTimersByTimeAsync(2000); // grace expires (armed at the first failure)
    expect(s.unreachable.value).toBe(true);
  });

  it("a 401 shows the page immediately and never spins the retry loop", async () => {
    const fetchMock = fetchWithStatus(401);
    vi.stubGlobal("fetch", fetchMock);
    const s = useAgentStream();

    await s.connect("s1");
    expect(s.unreachable.value).toBe(true);
    expect(s.outage.value).toMatchObject({ gaveUp: true, attempts: 1 });
    expect(s.outage.value?.message).toContain("token");

    await vi.advanceTimersByTimeAsync(30000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("retryNow reconnects out of the outage and closes the page", async () => {
    const fetchMock = fetchRejecting("down");
    vi.stubGlobal("fetch", fetchMock);
    const s = useAgentStream();

    await s.connect("s1");
    await vi.advanceTimersByTimeAsync(2500);
    expect(s.unreachable.value).toBe(true);

    fetchMock.mockImplementation(() => Promise.resolve(okResponse()));
    void s.retryNow(); // pends on the open stream — that is the healthy state
    await vi.advanceTimersByTimeAsync(1);
    expect(s.connected.value).toBe(true);
    expect(s.unreachable.value).toBe(false);
    expect(s.outage.value).toBeNull();
    s.disconnect();
  });

  it("a server that drops the stream mid-session and stays dead reaches the page", async () => {
    // First the healthy-then-EOF connect (server going away), afterwards
    // refused connections (server gone) — the realistic outage shape.
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        body: new ReadableStream({
          start(c) {
            c.close();
          },
        }),
      })
      .mockRejectedValue(new Error("connection refused"));
    vi.stubGlobal("fetch", fetchMock);
    const s = useAgentStream();

    await s.connect("s1"); // the EOF is an outage, not a clean state
    expect(s.connected.value).toBe(false);
    expect(s.outage.value).toMatchObject({ attempts: 1, gaveUp: false, message: "连接已断开" });

    await vi.advanceTimersByTimeAsync(2500); // retries fail; the grace expires
    expect(s.unreachable.value).toBe(true);
  });
});
