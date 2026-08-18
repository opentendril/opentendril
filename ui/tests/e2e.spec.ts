// Foundational E2E suite for the Command Center SPA. Runs against the built
// static bundle (vite preview) with the Go Stem mocked entirely at the
// network layer — HTTP via page.route, /ws via page.routeWebSocket — so
// these run identically in CI and locally with no Docker, no real Stem, and
// no LLM provider. Response shapes mirror ui/src/lib/types.ts, which itself
// mirrors the Go Stem's documented REST + WebSocket surface 1:1.

import { test, expect, type Page } from "@playwright/test";
import type { EventRecord, Session, SproutRun } from "../src/lib/types";

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
  {
    sessions = [] as Session[],
    sproutRuns = [] as SproutRun[],
    events = [] as EventRecord[],
  } = {},
): Promise<{
  lastSessionsAuthHeader: () => string | undefined;
  lastPreferencePatch: () => Record<string, unknown> | undefined;
}> {
  let lastSessionsAuthHeader: string | undefined;
  let lastPreferencePatch: Record<string, unknown> | undefined;
  const liveSessions = sessions.map((session) => ({
    ...session,
    preferences: { ...session.preferences },
  }));

  await page.route("**/health", async (route) => {
    await route.fulfill({ status: 200, json: { overall: true } });
  });

  await page.route("**/v1/config/substrates", async (route) => {
    await route.fulfill({
      status: 200,
      json: { substrates: ["opentendril", "docs"] },
    });
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
    await route.fulfill({ status: 200, json: { sessions: liveSessions } });
  });

  await page.route(
    (url) => /\/v1\/sessions\/[^/]+$/.test(new URL(url).pathname),
    async (route) => {
      if (route.request().method() !== "PATCH") {
        await route.continue();
        return;
      }
      const body = route.request().postDataJSON() as {
        preferences?: Record<string, unknown>;
      };
      lastPreferencePatch = body;
      const id = /\/v1\/sessions\/([^/]+)$/.exec(
        new URL(route.request().url()).pathname,
      )?.[1];
      const session = liveSessions.find((item) => item.sessionId === id);
      if (!session) {
        await route.fulfill({ status: 404, body: "session not found" });
        return;
      }
      session.preferences = {
        ...session.preferences,
        ...(body.preferences ?? {}),
      };
      await route.fulfill({ status: 200, json: session });
    },
  );

  // Per-session sub-resources hydrateSessionData() reads on boot. Mocked to
  // keep the run quiet; the store already tolerates these failing.
  await page.route("**/v1/sessions/*/history", async (route) => {
    await route.fulfill({
      status: 200,
      json: { sessionId: sessionIdFromPath(route.request().url()), messages: [] },
    });
  });
  await page.route(
    (url) => url.pathname.includes("/sprout-runs"),
    async (route) => {
      await route.fulfill({
        status: 200,
        json: { sessionId: sessionIdFromPath(route.request().url()), sproutRuns },
      });
    },
  );
  await page.route(
    (url) => /\/v1\/sessions\/[^/]+\/events$/.test(new URL(url).pathname),
    async (route) => {
      await route.fulfill({
        status: 200,
        json: { sessionId: sessionIdFromPath(route.request().url()), events },
      });
    },
  );

  // The gateway's real first frame is `{"type":"connected"}` — see
  // cmd/stem/internal/gateway/gateway.go. Not calling ws.connectToServer()
  // means Playwright mocks the socket entirely: the page's WebSocket opens
  // (onopen fires) without ever reaching a real server.
  await page.routeWebSocket("**/ws*", (ws) => {
    ws.send(JSON.stringify({ type: "connected" }));
  });

  return {
    lastSessionsAuthHeader: () => lastSessionsAuthHeader,
    lastPreferencePatch: () => lastPreferencePatch,
  };
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

  await page.getByLabel("Botanist key").fill(apiKey);
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

  test("same-origin onboarding does not ask for a Stem socket path", async ({ page }) => {
    await mockStemBackend(page, { sessions: [] });
    await page.goto("/");
    await expect(page.getByLabel("Stem address")).toHaveValue("");
    await expect(page.getByText(/normal Greenhouse path/)).toBeVisible();
    await expect(page.locator("body")).not.toContainText("stem.sock");
    await expect(page.locator("body")).not.toContainText("/run/opentendril");
  });

  test("distinguishes nginx 502 from Botanist 401", async ({ page }) => {
    await page.route("**/health", async (route) => {
      await route.fulfill({ status: 502, body: "Bad Gateway" });
    });
    await page.goto("/");
    await page.getByLabel("Botanist key").fill(testApiKey);
    await page.getByRole("button", { name: "Take root" }).click();
    await expect(
      page.getByText("Greenhouse cannot reach the configured Stem transport"),
    ).toBeVisible();
    await expect(page.getByText("Botanist key rejected")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Uproot" })).toHaveCount(0);
  });

  test("distinguishes nginx 504 from Botanist 401", async ({ page }) => {
    await page.route("**/health", async (route) => {
      await route.fulfill({ status: 504, body: "Gateway Timeout" });
    });
    await page.goto("/");
    await page.getByLabel("Botanist key").fill(testApiKey);
    await page.getByRole("button", { name: "Take root" }).click();
    await expect(
      page.getByText("Greenhouse cannot reach the configured Stem transport"),
    ).toBeVisible();
    await expect(page.getByText("Botanist key rejected")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Uproot" })).toHaveCount(0);
  });

  test("keeps degraded Stem 503 distinct from transport failure", async ({ page }) => {
    await page.route("**/health", async (route) => {
      await route.fulfill({ status: 503, json: { overall: false } });
    });
    await page.goto("/");
    await page.getByLabel("Botanist key").fill(testApiKey);
    await page.getByRole("button", { name: "Take root" }).click();
    await expect(page.getByText("Stem answered but reports degraded health")).toBeVisible();
    await expect(
      page.getByText("Greenhouse cannot reach the configured Stem transport"),
    ).toHaveCount(0);
    await expect(page.getByText("Botanist key rejected")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Uproot" })).toHaveCount(0);
  });

  test("reports Botanist 401 after a reachable Stem health check", async ({ page }) => {
    await page.route("**/health", async (route) => {
      await route.fulfill({ status: 200, json: { overall: true } });
    });
    await page.route("**/v1/sessions", async (route) => {
      await route.fulfill({ status: 401, body: "unauthorized" });
    });
    await page.goto("/");
    await page.getByLabel("Botanist key").fill("wrong-key");
    await page.getByRole("button", { name: "Take root" }).click();
    await expect(page.getByText("Botanist key rejected")).toBeVisible();
    await expect(
      page.getByText("Greenhouse cannot reach the configured Stem transport"),
    ).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Uproot" })).toHaveCount(0);
  });
});

test.describe("Command Center sprout-run observation", () => {
  test("renders structured auth-failure facts without reading the raw error", async ({
    page,
  }) => {
    const session = makeSession({ sessionId: "tendril-e2e-observe" });
    const run: SproutRun = {
      runId: "step-auth",
      sessionId: session.sessionId,
      stepId: "step-auth",
      provider: "openrouter",
      model: "anthropic/claude-sonnet-4.6",
      status: "withered",
      outcome: "failed",
      failureCategory: "provider-auth-rejected",
      providerRequestAttempted: true,
      toolInvocations: 0,
      providerDiagnostic: {
        statusCode: 401,
        message: "User not found",
        provider: "openrouter",
      },
      transcript: "investigate the withered run",
      error: "llm returned 401: User not found (provider=openrouter model=anthropic/claude-sonnet-4.6)",
      startedAt: "2026-08-16T12:00:00Z",
      finishedAt: "2026-08-16T12:00:02Z",
    };

    await mockStemBackend(page, { sessions: [session], sproutRuns: [run] });
    await completeOnboarding(page, testApiKey);

    await page.locator(".run-row").click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer).toBeVisible();
    await expect(drawer.getByTestId("run-observation")).toBeVisible();
    await expect(drawer.getByText("Withered — provider authentication rejected")).toBeVisible();
    await expect(drawer.getByText("openrouter", { exact: true })).toBeVisible();
    await expect(drawer.getByText("attempted", { exact: true })).toBeVisible();
    await expect(drawer.getByText("HTTP 401 / User not found")).toBeVisible();
    await expect(drawer.getByText("Fruit", { exact: true })).toBeVisible();
    await expect(drawer.getByText("none", { exact: true })).toBeVisible();
    await expect(drawer.getByText("investigate the withered run")).toBeVisible();
    await expect(drawer.getByTestId("run-tool-activity")).toBeVisible();

    // Raw telemetry stays collapsed; facts are readable without expanding it.
    const telemetry = drawer.getByTestId("run-telemetry");
    await expect(telemetry).toBeVisible();
    await expect(telemetry).not.toHaveAttribute("open");
    await expect(drawer.getByText("Raw error (secondary)")).toBeHidden();
    await expect(
      drawer.getByText("llm returned 401: User not found (provider=openrouter model=anthropic/claude-sonnet-4.6)"),
    ).toBeHidden();
    await expect(page.getByLabel("Living orchestration garden")).toHaveCount(0);
    await expect(page.locator(".ticker-secondary")).toBeVisible();
    await expect(page.locator(".ticker-secondary")).not.toHaveAttribute("open");
  });

  test("shows matured-run facts and tool names without expanding telemetry", async ({
    page,
  }) => {
    const session = makeSession({ sessionId: "tendril-e2e-matured" });
    const run: SproutRun = {
      runId: "step-ok",
      sessionId: session.sessionId,
      stepId: "step-ok",
      provider: "openrouter",
      model: "anthropic/claude-sonnet-4.6",
      status: "matured",
      outcome: "complete",
      failureCategory: "matured",
      providerRequestAttempted: true,
      toolInvocations: 2,
      transcript: "add a clarifying sentence to the guide",
      startedAt: "2026-08-16T12:00:00Z",
      finishedAt: "2026-08-16T12:00:08Z",
      output: "wrote docs/GUIDE.md",
    };
    const events: EventRecord[] = [
      {
        id: 1,
        sessionId: session.sessionId,
        type: "tool-invoked",
        source: "step-ok",
        data: { tool: "readFile", status: "success" },
        createdAt: "2026-08-16T12:00:02Z",
      },
      {
        id: 2,
        sessionId: session.sessionId,
        type: "tool-invoked",
        source: "step-ok",
        data: { tool: "writeFile", status: "success" },
        createdAt: "2026-08-16T12:00:05Z",
      },
      {
        id: 3,
        sessionId: session.sessionId,
        type: "sprout-matured",
        source: "step-ok",
        data: {
          outcome: "complete",
          filesModified: ["docs/GUIDE.md"],
          toolInvocations: 2,
        },
        createdAt: "2026-08-16T12:00:08Z",
      },
    ];

    await mockStemBackend(page, { sessions: [session], sproutRuns: [run], events });
    await completeOnboarding(page, testApiKey);

    await page.locator(".run-row").click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.getByText("Matured — matured")).toBeVisible();
    await expect(drawer.getByText("complete", { exact: true })).toBeVisible();
    await expect(drawer.getByTestId("run-observation").getByText("docs/GUIDE.md")).toBeVisible();
    await expect(drawer.getByText("add a clarifying sentence to the guide")).toBeVisible();
    await expect(drawer.getByText("readFile", { exact: true })).toBeVisible();
    await expect(drawer.getByText("writeFile", { exact: true })).toBeVisible();
    await expect(drawer.getByTestId("run-tool-activity").getByText("success").first()).toBeVisible();
    await expect(drawer.getByTestId("run-telemetry")).not.toHaveAttribute("open");
    await expect(drawer.getByText("wrote docs/GUIDE.md")).toBeHidden();
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

test.describe("Command Center Substrate binding", () => {
  test("shows a bound Substrate and persists a named one via PATCH", async ({
    page,
  }) => {
    const session = makeSession({
      sessionId: "tendril-e2e-soil",
      preferences: {},
    });
    const backend = await mockStemBackend(page, { sessions: [session] });
    await completeOnboarding(page, testApiKey);

    await expect(page.getByText("no substrate", { exact: true })).toBeVisible();
    await expect(page.getByText("unbound", { exact: true })).toBeVisible();

    const input = page.getByLabel("Substrate");
    await input.fill("opentendril");
    await input.blur();

    await expect(page.getByText("bound: opentendril")).toBeVisible();
    await expect(page.getByText("opentendril", { exact: true }).first()).toBeVisible();

    const patch = backend.lastPreferencePatch();
    expect(patch).toEqual({ preferences: { substrate: "opentendril" } });
  });
});
