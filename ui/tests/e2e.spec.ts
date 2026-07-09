// Foundational E2E suite for the Command Center SPA. Runs against the built
// static bundle (vite preview) with the Go Stem mocked entirely at the
// network layer — HTTP via page.route, /ws via page.routeWebSocket — so
// these run identically in CI and locally with no Docker, no real Stem, and
// no LLM provider. Response shapes mirror ui/src/lib/types.ts, which itself
// mirrors the Go Stem's documented REST + WebSocket surface 1:1.

import { test, expect, type Page } from "@playwright/test";
import type { Session } from "../src/lib/types";

const testApiKey = "e2e-test-key";

/** A session shaped exactly like the Go Stem's `GET /v1/sessions` response. */
function makeSession(overrides: Partial<Session>): Session {
  return {
    sessionId: "tendril-e2e-default",
    origin: "cli",
    createdAt: "2026-01-01T00:00:00Z",
    lastActiveAt: "2026-01-01T00:00:00Z",
    preferences: {},
    ...overrides,
  };
}

/**
 * Mocks the Go Stem's HTTP surface (/health, /v1/sessions and its
 * sub-resources) and the /ws EventBus gateway. Must run before page.goto —
 * page.routeWebSocket only routes sockets created after it is registered.
 *
 * Returns the Authorization header captured off the last /v1/sessions
 * request, so a test can confirm the operator key the UI collected during
 * onboarding is actually the one it sends.
 */
async function mockStemBackend(
  page: Page,
  { sessions = [] as Session[] } = {},
): Promise<{ lastSessionsAuthHeader: () => string | undefined }> {
  let lastSessionsAuthHeader: string | undefined;

  await page.route("**/health", async (route) => {
    await route.fulfill({ status: 200, json: { overall: true } });
  });

  await page.route("**/v1/sessions", async (route) => {
    const request = route.request();
    if (request.method() !== "GET") {
      // Not exercised by this suite (session creation) — let it fall
      // through rather than leaving the route handler unresolved.
      await route.continue();
      return;
    }
    lastSessionsAuthHeader = request.headers()["authorization"];
    await route.fulfill({ status: 200, json: { sessions } });
  });

  // Per-session sub-resources hydrateSessionData() reads on boot. Mocked to
  // keep the run quiet; the store already tolerates these failing.
  await page.route("**/v1/sessions/*/history", async (route) => {
    await route.fulfill({
      status: 200,
      json: { sessionId: sessionIdFromPath(route.request().url()), messages: [] },
    });
  });
  await page.route("**/v1/sessions/*/sprout-runs", async (route) => {
    await route.fulfill({
      status: 200,
      json: { sessionId: sessionIdFromPath(route.request().url()), sproutRuns: [] },
    });
  });
  await page.route("**/v1/sessions/*/events", async (route) => {
    await route.fulfill({
      status: 200,
      json: { sessionId: sessionIdFromPath(route.request().url()), events: [] },
    });
  });

  // The gateway's real first frame is `{"type":"connected"}` — see
  // cmd/stem/internal/gateway/gateway.go. Not calling ws.connectToServer()
  // means Playwright mocks the socket entirely: the page's WebSocket opens
  // (onopen fires) without ever reaching a real server.
  await page.routeWebSocket("**/ws*", (ws) => {
    ws.send(JSON.stringify({ type: "connected" }));
  });

  return { lastSessionsAuthHeader: () => lastSessionsAuthHeader };
}

function sessionIdFromPath(url: string): string {
  const match = /\/v1\/sessions\/([^/]+)\//.exec(url);
  return match ? match[1] : "";
}

/** Drives the real onboarding form (Stem address left blank = same origin). */
async function completeOnboarding(page: Page, apiKey: string): Promise<void> {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: /OpenTendril.*Command Center/ }),
  ).toBeVisible();

  await page.getByLabel("Operator API key").fill(apiKey);
  await page.getByRole("button", { name: "Take root" }).click();

  // Onboarding.tsx flips to the Command Center ~450ms after both /health and
  // /v1/sessions succeed; the "Uproot" button only exists past that point.
  await expect(page.getByRole("button", { name: "Uproot" })).toBeVisible();
}

test.describe("Command Center onboarding", () => {
  test("loads and passes onboarding with a valid API key", async ({ page }) => {
    const backend = await mockStemBackend(page, { sessions: [] });

    await completeOnboarding(page, testApiKey);

    // The onboarding form is gone and the shell is in its place.
    await expect(page.getByRole("button", { name: "Take root" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "+ Sprout" })).toBeVisible();

    // The key collected during onboarding is the one actually sent.
    expect(backend.lastSessionsAuthHeader()).toBe(`Bearer ${testApiKey}`);
  });
});

test.describe("Command Center EventBus connection", () => {
  test("establishes a live WebSocket connection to the EventBus", async ({ page }) => {
    await mockStemBackend(page, { sessions: [] });

    await completeOnboarding(page, testApiKey);

    // wsStatus starts "connecting" and flips to "open" on the mocked
    // socket's onopen — see ui/src/state/store.ts boot() / ui/src/lib/ws.ts.
    await expect(page.getByText("EventBus live")).toBeVisible();
    await expect(page.locator(".conn-dot.open")).toBeVisible();
  });
});

test.describe("Command Center session rail", () => {
  test("renders sessions from the mocked /v1/sessions response", async ({ page }) => {
    const sessions = [
      makeSession({ sessionId: "tendril-e2e-alpha", origin: "cli" }),
      makeSession({ sessionId: "tendril-e2e-beta", origin: "mcp" }),
    ];
    await mockStemBackend(page, { sessions });

    await completeOnboarding(page, testApiKey);

    const cards = page.locator(".session-card");
    await expect(cards).toHaveCount(2);

    // shortId() strips the "tendril-" prefix (ui/src/components/SessionRail.tsx).
    await expect(page.getByText("e2e-alpha", { exact: true })).toBeVisible();
    await expect(page.getByText("e2e-beta", { exact: true })).toBeVisible();
    await expect(cards.first().getByText("cli", { exact: true })).toBeVisible();
    await expect(cards.last().getByText("mcp", { exact: true })).toBeVisible();

    // The empty-state copy must not appear alongside real sessions.
    await expect(page.getByText("No Tendrils yet")).toHaveCount(0);
  });

  test("shows the empty state when the Stem has no sessions", async ({ page }) => {
    await mockStemBackend(page, { sessions: [] });

    await completeOnboarding(page, testApiKey);

    await expect(page.locator(".session-card")).toHaveCount(0);
    await expect(page.getByText(/No Tendrils yet/)).toBeVisible();
  });
});
