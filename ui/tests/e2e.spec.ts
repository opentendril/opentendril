// Foundational E2E suite for the Command Center SPA. Runs against the built
// static bundle (vite preview) with the Go Stem mocked entirely at the
// network layer (HTTP via page.route, /ws via page.routeWebSocket), so
// these run identically in CI and locally with no Docker, no real Stem, and
// no LLM provider. Response shapes mirror ui/src/lib/types.ts, which itself
// mirrors the Go Stem's documented REST + WebSocket surface 1:1.

import { test, expect, type Page, type Request, type WebSocketRoute } from "@playwright/test";
import { readSSEFrames } from "../src/lib/sse";
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

function makeRun(
  sessionId: string,
  runId: string,
  status: SproutRun["status"] = "matured",
): SproutRun {
  return {
    runId,
    sessionId,
    stepId: runId,
    status,
    transcript: `transcript for ${runId}`,
    startedAt: "2026-09-01T12:00:00Z",
    finishedAt: status === "running" ? undefined : "2026-09-01T12:00:08Z",
  };
}

function makeEvent(sessionId: string, runId: string, id: number): EventRecord {
  return {
    id,
    sessionId,
    type: "tool-invoked",
    source: runId,
    data: { tool: "readFile", status: "success" },
    createdAt: "2026-09-01T12:00:02Z",
  };
}

/**
 * Mocks the Go Stem's HTTP surface (/health, /v1/sessions and its
 * sub-resources) and the /ws EventBus gateway. Must run before page.goto.
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
    runsBySession,
    eventsBySession,
    runDelayMs = 0,
  }: {
    sessions?: Session[];
    sproutRuns?: SproutRun[];
    events?: EventRecord[];
    runsBySession?: Record<string, SproutRun[]>;
    eventsBySession?: Record<string, EventRecord[]>;
    runDelayMs?: number;
  } = {},
): Promise<{
  lastSessionsAuthHeader: () => string | undefined;
  lastPreferencePatch: () => Record<string, unknown> | undefined;
  runReads: () => string[];
  eventReads: () => string[];
  completedEventReads: () => string[];
  requestOrder: () => string[];
  maxConcurrentRunReads: () => number;
  sessionListReads: () => number;
  setSessions: (sessions: Session[]) => void;
  setRuns: (sessionId: string, runs: SproutRun[]) => void;
  setEvents: (sessionId: string, events: EventRecord[]) => void;
  failEvents: (sessionId: string, status?: number) => void;
  holdEvents: (sessionId: string) => () => void;
  emit: (event: Record<string, unknown>) => void;
  disconnectSocket: () => Promise<void>;
}> {
  let lastSessionsAuthHeader: string | undefined;
  let lastPreferencePatch: Record<string, unknown> | undefined;
  const liveSessions = sessions.map((session) => ({
    ...session,
    preferences: { ...session.preferences },
  }));
  const liveRunsBySession = { ...runsBySession } as Record<string, SproutRun[]>;
  const liveEventsBySession = { ...eventsBySession } as Record<string, EventRecord[]>;
  let hasRunMap = runsBySession !== undefined;
  let hasEventMap = eventsBySession !== undefined;
  const runReadIds: string[] = [];
  const eventReadIds: string[] = [];
  const completedEventReadIds: string[] = [];
  const requestSequence: string[] = [];
  const eventFailures = new Map<string, number>();
  const eventGates = new Map<string, { promise: Promise<void>; release: () => void }>();
  let activeRunReads = 0;
  let maxRunReads = 0;
  let sessionsReadCount = 0;
  let currentSocket: WebSocketRoute | null = null;

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
      // Not exercised by this suite (session creation). Let it fall
      // through rather than leaving the route handler unresolved.
      await route.continue();
      return;
    }
    sessionsReadCount += 1;
    requestSequence.push("sessions");
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
      const sessionId = sessionIdFromPath(route.request().url());
      runReadIds.push(sessionId);
      requestSequence.push(`runs/${sessionId}`);
      activeRunReads += 1;
      maxRunReads = Math.max(maxRunReads, activeRunReads);
      if (runDelayMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, runDelayMs));
      }
      const sessionRuns = hasRunMap
        ? liveRunsBySession[sessionId] ?? []
        : sproutRuns;
      await route.fulfill({
        status: 200,
        json: { sessionId, sproutRuns: sessionRuns },
      });
      activeRunReads -= 1;
    },
  );
  await page.route(
    (url) => /\/v1\/sessions\/[^/]+\/events$/.test(new URL(url).pathname),
    async (route) => {
      const sessionId = sessionIdFromPath(route.request().url());
      eventReadIds.push(sessionId);
      requestSequence.push(`events/${sessionId}`);
      const gate = eventGates.get(sessionId);
      if (gate) await gate.promise;
      const failureStatus = eventFailures.get(sessionId);
      if (failureStatus) {
        completedEventReadIds.push(sessionId);
        await route.fulfill({ status: failureStatus, body: "event history unavailable" });
        return;
      }
      const sessionEvents = hasEventMap
        ? liveEventsBySession[sessionId] ?? []
        : events;
      completedEventReadIds.push(sessionId);
      await route.fulfill({
        status: 200,
        json: { sessionId, events: sessionEvents },
      });
    },
  );

  // The gateway's real first frame is `{"type":"connected"}`. See
  // cmd/stem/internal/gateway/gateway.go. Not calling ws.connectToServer()
  // means Playwright mocks the socket entirely: the page's WebSocket opens
  // (onopen fires) without ever reaching a real server.
  await page.routeWebSocket("**/ws*", (ws) => {
    currentSocket = ws;
    ws.send(JSON.stringify({ type: "connected" }));
  });

  // Seed-owned watch is a separate channel from /ws. Historical Phytomers 404.
  await page.route(
    (url) => /\/v1\/phytomers\/[^/]+\/watch$/.test(new URL(url).pathname),
    async (route) => {
      await route.fulfill({
        status: 404,
        body: "no seed growth is associated with this phytomer",
      });
    },
  );
  await page.route(
    (url) => /\/v1\/seeds\/runs\/[^/]+$/.test(new URL(url).pathname),
    async (route) => {
      await route.fulfill({ status: 404, body: "no seed run for handle" });
    },
  );
  await page.route("**/v1/fruit", async (route) => {
    await route.fulfill({
      status: 200,
      json: {
        items: [],
        counts: {
          outstanding: 0,
          unknown: 0,
          closedUnmerged: 0,
          merged: 0,
          total: 0,
        },
      },
    });
  });

  return {
    lastSessionsAuthHeader: () => lastSessionsAuthHeader,
    lastPreferencePatch: () => lastPreferencePatch,
    runReads: () => [...runReadIds],
    eventReads: () => [...eventReadIds],
    completedEventReads: () => [...completedEventReadIds],
    requestOrder: () => [...requestSequence],
    maxConcurrentRunReads: () => maxRunReads,
    sessionListReads: () => sessionsReadCount,
    setSessions: (next) => {
      liveSessions.splice(
        0,
        liveSessions.length,
        ...next.map((session) => ({
          ...session,
          preferences: { ...session.preferences },
        })),
      );
    },
    setRuns: (sessionId, next) => {
      hasRunMap = true;
      liveRunsBySession[sessionId] = next;
    },
    setEvents: (sessionId, next) => {
      hasEventMap = true;
      liveEventsBySession[sessionId] = next;
    },
    failEvents: (sessionId, status = 503) => {
      eventFailures.set(sessionId, status);
    },
    holdEvents: (sessionId) => {
      let releaseGate = () => {};
      const promise = new Promise<void>((resolve) => {
        releaseGate = resolve;
      });
      eventGates.set(sessionId, { promise, release: releaseGate });
      return () => {
        eventGates.get(sessionId)?.release();
        eventGates.delete(sessionId);
      };
    },
    emit: (event) => currentSocket?.send(JSON.stringify(event)),
    disconnectSocket: async () => {
      await currentSocket?.close({ code: 1001, reason: "Reconnect hydration case" });
    },
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
    await expect(page.locator("body")).not.toContainText("/var/lib/opentendril-transport");
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
      failureStage: "provider-preflight",
      diagnosticCode: "provider-preflight-rejected",
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
    await expect(drawer.getByText("Withered: provider authentication rejected")).toBeVisible();
    await expect(drawer.getByText("Failure category", { exact: true })).toBeVisible();
    await expect(drawer.getByText("provider authentication rejected", { exact: true })).toBeVisible();
    await expect(drawer.getByText("Failure stage", { exact: true })).toBeVisible();
    await expect(drawer.getByText("provider-preflight", { exact: true })).toBeVisible();
    await expect(drawer.getByText("Diagnostic code", { exact: true })).toBeVisible();
    await expect(drawer.getByText("provider-preflight-rejected", { exact: true })).toBeVisible();
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

  test("does not classify failure stage or diagnostic code from raw error text", async ({
    page,
  }) => {
    const session = makeSession({ sessionId: "tendril-e2e-no-inference" });
    const run: SproutRun = {
      runId: "step-legacy-error",
      sessionId: session.sessionId,
      status: "withered",
      outcome: "failed",
      failureCategory: "execution-failed",
      providerRequestAttempted: false,
      toolInvocations: 0,
      error: "failureStage=substrate-resolution diagnosticCode=substrate-access-denied",
      startedAt: "2026-08-16T12:00:00Z",
      finishedAt: "2026-08-16T12:00:02Z",
    };

    await mockStemBackend(page, { sessions: [session], sproutRuns: [run] });
    await completeOnboarding(page, testApiKey);
    await page.locator(".run-row").click();

    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer).toBeVisible();
    await expect(drawer.getByText("Failure category", { exact: true })).toBeVisible();
    await expect(drawer.getByText("Failure stage", { exact: true })).toHaveCount(0);
    await expect(drawer.getByText("Diagnostic code", { exact: true })).toHaveCount(0);
    await expect(drawer.getByText("substrate-resolution", { exact: true })).toHaveCount(0);
    await expect(drawer.getByText("substrate-access-denied", { exact: true })).toHaveCount(0);
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
    await expect(drawer.getByText("Matured: matured")).toBeVisible();
    await expect(drawer.getByText("complete", { exact: true })).toBeVisible();
    await expect(drawer.getByTestId("run-observation").getByText("docs/GUIDE.md")).toBeVisible();
    await expect(drawer.getByText("add a clarifying sentence to the guide")).toBeVisible();
    await expect(drawer.getByText("readFile", { exact: true })).toBeVisible();
    await expect(drawer.getByText("writeFile", { exact: true })).toBeVisible();
    await expect(drawer.getByTestId("run-tool-activity").getByText("success").first()).toBeVisible();
    await expect(drawer.getByTestId("run-telemetry")).not.toHaveAttribute("open");
    await expect(drawer.getByText("wrote docs/GUIDE.md")).toBeHidden();
  });

  test("models the sanitized backend contract with no whisper presentation", async ({
    page,
  }) => {
    const session = makeSession({ sessionId: "tendril-e2e-private" });
    const run: SproutRun = {
      runId: "step-private",
      sessionId: session.sessionId,
      stepId: "step-private",
      provider: "openrouter",
      model: "anthropic/claude-sonnet-4.6",
      status: "matured",
      outcome: "complete",
      failureCategory: "matured",
      providerRequestAttempted: true,
      toolInvocations: 0,
      transcript: "start thinking",
      startedAt: "2026-08-16T12:00:00Z",
      finishedAt: "2026-08-16T12:00:08Z",
      output: "final sanitized safe evidence",
    };
    const events: EventRecord[] = [
      {
        id: 1,
        sessionId: session.sessionId,
        type: "thought-branch",
        source: "step-private",
        data: { active: true, model: "anthropic/claude-sonnet-4.6" },
        createdAt: "2026-08-16T12:00:02Z",
      },
    ];

    await mockStemBackend(page, { sessions: [session], sproutRuns: [run], events });
    await completeOnboarding(page, testApiKey);

    await page.locator(".run-row").click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    
    // UI proves no `.whisper` reasoning presentation exists
    await expect(drawer.locator(".whisper")).toHaveCount(0);
    // UI proves no `private reasoning` text is visible
    await expect(drawer.getByText("private reasoning")).toHaveCount(0);
    await expect(drawer.getByText("<thought>")).toHaveCount(0);
    
    // useful safe run-review evidence remains visible in telemetry.
    const telemetry = drawer.getByTestId("run-telemetry");
    await telemetry.click();
    await expect(drawer.getByText("final sanitized safe evidence")).toBeVisible();
  });
});

test.describe("Command Center EventBus connection", () => {
  test("establishes a live WebSocket connection to the EventBus", async ({ page }) => {
    await mockStemBackend(page, { sessions: [] });

    await completeOnboarding(page, testApiKey);

    // wsStatus starts "connecting" and flips to "open" on the mocked
    // socket's onopen. See ui/src/state/store.ts boot() / ui/src/lib/ws.ts.
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

test.describe("Command Center cross-Phytomer run discovery", () => {
  test("shows runs from supported origins beneath their Phytomers without changing the active ChatPanel", async ({
    page,
  }) => {
    const sessions = [
      makeSession({
        sessionId: "tendril-e2e-cli",
        origin: "cli",
        lastActiveAt: "2026-09-04T00:00:00Z",
      }),
      makeSession({
        sessionId: "tendril-e2e-rest",
        origin: "rest",
        lastActiveAt: "2026-09-03T00:00:00Z",
      }),
      makeSession({
        sessionId: "tendril-e2e-ws",
        origin: "ws",
        lastActiveAt: "2026-09-02T00:00:00Z",
      }),
      makeSession({
        sessionId: "tendril-e2e-mcp",
        origin: "mcp",
        lastActiveAt: "2026-09-01T00:00:00Z",
      }),
    ];
    const runsBySession = Object.fromEntries(
      sessions.map((session) => [
        session.sessionId,
        [makeRun(session.sessionId, `run-${session.origin}`)],
      ]),
    );
    const mcpSession = sessions[3];
    const mcpRun = runsBySession[mcpSession.sessionId][0];
    const backend = await mockStemBackend(page, {
      sessions,
      runsBySession,
      eventsBySession: {
        [sessions[0].sessionId]: [],
        [mcpSession.sessionId]: [makeEvent(mcpSession.sessionId, mcpRun.runId, 41)],
      },
    });

    await completeOnboarding(page, testApiKey);

    await expect(page.locator(".session-run-row")).toHaveCount(4);
    await expect(page.locator(".runs .run-row")).toHaveCount(1);
    await expect(page.locator(".runs .run-row")).toContainText("run-cli");
    for (const session of sessions) {
      const group = page.getByRole("group", {
        name: `Phytomer ${session.sessionId.replace(/^tendril-/, "")}`,
      });
      await expect(group.locator(".session-run-row")).toHaveCount(1);
      await expect(group.locator(".origin-chip")).toHaveText(session.origin);
    }
    await expect(page.locator(".session-group button button")).toHaveCount(0);

    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for run-mcp in Phytomer e2e-mcp",
      })
      .click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.locator("h2")).toHaveText("run-mcp");
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "ready",
    );
    await expect(drawer.locator(".tool-name")).toHaveText("readFile");
    await expect(page.locator(".chat-head .mono")).toHaveText(sessions[0].sessionId);
    expect(backend.eventReads()).toContain(mcpSession.sessionId);
  });

  test("bounds boot hydration to the ten most recent Phytomers selected by lastActiveAt", async ({
    page,
  }) => {
    const allSessions = Array.from({ length: 14 }, (_, index) =>
      makeSession({
        sessionId: `tendril-e2e-set-${index}`,
        origin: ["cli", "rest", "ws", "mcp"][index % 4],
        lastActiveAt: new Date(Date.UTC(2026, 0, index + 1)).toISOString(),
      }),
    );
    const sorted = [...allSessions].sort(
      (a, b) => Date.parse(b.lastActiveAt) - Date.parse(a.lastActiveAt),
    );
    const responseOrder = [sorted[0], sorted[13], ...sorted.slice(1, 10), ...sorted.slice(10, 13)];
    const recentIds = sorted.slice(0, 10).map((session) => session.sessionId);
    const runsBySession = Object.fromEntries(
      allSessions.map((session, index) => [
        session.sessionId,
        [makeRun(session.sessionId, `set-run-${index}`)],
      ]),
    );
    const backend = await mockStemBackend(page, {
      sessions: responseOrder,
      runsBySession,
      runDelayMs: 60,
    });

    await completeOnboarding(page, testApiKey);

    await expect(page.locator(".session-run-row")).toHaveCount(10);
    expect(backend.runReads()).toHaveLength(10);
    expect(new Set(backend.runReads())).toEqual(new Set(recentIds));
    expect(backend.runReads()).not.toContain(sorted[13].sessionId);
    expect(backend.maxConcurrentRunReads()).toBeLessThanOrEqual(3);

    const oldSession = sorted[13];
    const oldRun = runsBySession[oldSession.sessionId][0];
    const oldCard = page
      .locator(".session-card")
      .filter({ hasText: oldSession.sessionId.replace(/^tendril-/, "") });
    await oldCard.click();
    await expect(page.locator(".runs .run-row")).toHaveCount(1);
    await expect(page.locator(".runs .run-row")).toContainText(oldRun.transcript ?? "");
    await expect(page.locator(".chat-head .mono")).toHaveText(oldSession.sessionId);
    expect(backend.runReads().filter((id) => id === oldSession.sessionId)).toHaveLength(1);
  });

  test("refreshes a known Phytomer outside the boot set from canonical run data", async ({
    page,
  }) => {
    const sessions = Array.from({ length: 12 }, (_, index) =>
      makeSession({
        sessionId: `tendril-e2e-live-${index}`,
        lastActiveAt: new Date(Date.UTC(2026, 2, 12 - index)).toISOString(),
      }),
    );
    const outside = sessions[sessions.length - 1];
    const runsBySession = Object.fromEntries(
      sessions.map((session) => [session.sessionId, [] as SproutRun[]]),
    );
    const backend = await mockStemBackend(page, { sessions, runsBySession });
    await completeOnboarding(page, testApiKey);
    await expect(page.locator(".session-run-row")).toHaveCount(0);

    const canonicalRun = makeRun(outside.sessionId, "canonical-live-run", "running");
    backend.setRuns(outside.sessionId, [canonicalRun]);
    const initialSessionReads = backend.sessionListReads();
    backend.emit({
      type: "sprout-matured",
      sessionId: outside.sessionId,
      source: canonicalRun.stepId,
      data: { status: "matured" },
    });

    await expect
      .poll(() => backend.runReads().filter((id) => id === outside.sessionId).length)
      .toBe(1);
    const group = page.getByRole("group", { name: "Phytomer e2e-live-11" });
    await expect(group.locator(".session-run-row")).toHaveCount(1);
    await expect(group.locator(".session-run-dot.running")).toBeVisible();
    expect(backend.sessionListReads()).toBe(initialSessionReads);
  });

  test("refreshes an unknown Phytomer before its targeted run discovery", async ({
    page,
  }) => {
    const active = makeSession({ sessionId: "tendril-e2e-known" });
    const newlyObserved = makeSession({
      sessionId: "tendril-e2e-new-mcp",
      origin: "mcp",
      lastActiveAt: "2026-09-25T00:00:00Z",
    });
    const backend = await mockStemBackend(page, {
      sessions: [active],
      runsBySession: { [active.sessionId]: [] },
    });
    await completeOnboarding(page, testApiKey);

    backend.setSessions([active, newlyObserved]);
    backend.setRuns(newlyObserved.sessionId, [
      makeRun(newlyObserved.sessionId, "newly-observed-run"),
    ]);
    const start = backend.requestOrder().length;
    backend.emit({
      type: "sprout-emerged",
      sessionId: newlyObserved.sessionId,
      source: "newly-observed-run",
    });

    await expect(
      page.getByRole("button", {
        name: "Open Sprout run transcript for newly-observed-run in Phytomer e2e-new-mcp",
      }),
    ).toBeVisible();
    const requests = backend.requestOrder().slice(start);
    const sessionRefreshIndex = requests.indexOf("sessions");
    const runRefreshIndex = requests.indexOf(`runs/${newlyObserved.sessionId}`);
    expect(sessionRefreshIndex).toBeGreaterThanOrEqual(0);
    expect(runRefreshIndex).toBeGreaterThan(sessionRefreshIndex);
  });

  test("fetches current persisted evidence for a run discovered after evidence was cached", async ({
    page,
  }) => {
    const active = makeSession({ sessionId: "tendril-e2e-evidence-refresh" });
    const priorRun = makeRun(active.sessionId, "prior-evidence-run");
    const backend = await mockStemBackend(page, {
      sessions: [active],
      runsBySession: { [active.sessionId]: [] },
      eventsBySession: {
        [active.sessionId]: [makeEvent(active.sessionId, priorRun.runId, 71)],
      },
    });
    await completeOnboarding(page, testApiKey);
    await expect
      .poll(() => backend.completedEventReads().filter((id) => id === active.sessionId).length)
      .toBe(1);
    await expect
      .poll(() => backend.runReads().filter((id) => id === active.sessionId).length)
      .toBe(1);

    const discoveredRun = makeRun(active.sessionId, "later-canonical-run");
    backend.setRuns(active.sessionId, [discoveredRun]);
    backend.setEvents(active.sessionId, [
      makeEvent(active.sessionId, discoveredRun.runId, 72),
    ]);
    backend.emit({
      type: "sprout-matured",
      sessionId: active.sessionId,
      source: discoveredRun.stepId,
      data: { status: "matured" },
    });
    const sessionGroup = page.getByRole("group", { name: "Phytomer e2e-evidence-refresh" });
    const runRow = sessionGroup.locator(".session-run-row");
    await expect(runRow).toHaveCount(1);

    const release = backend.holdEvents(active.sessionId);
    await runRow.click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "loading",
    );
    await expect
      .poll(() => backend.eventReads().filter((id) => id === active.sessionId).length)
      .toBe(2);
    expect(
      backend.completedEventReads().filter((id) => id === active.sessionId),
    ).toHaveLength(1);

    release();
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "ready",
    );
    await expect(drawer.locator(".tool-name")).toHaveText("readFile");
  });

  test("shows loading evidence until persisted observation data arrives", async ({
    page,
  }) => {
    const active = makeSession({ sessionId: "tendril-e2e-evidence-active" });
    const background = makeSession({
      sessionId: "tendril-e2e-evidence-mcp",
      origin: "mcp",
      lastActiveAt: "2026-09-02T00:00:00Z",
    });
    const run = makeRun(background.sessionId, "evidence-run");
    const backend = await mockStemBackend(page, {
      sessions: [active, background],
      runsBySession: {
        [active.sessionId]: [],
        [background.sessionId]: [run],
      },
      eventsBySession: {
        [active.sessionId]: [],
        [background.sessionId]: [makeEvent(background.sessionId, run.runId, 52)],
      },
    });
    const release = backend.holdEvents(background.sessionId);
    await completeOnboarding(page, testApiKey);

    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for evidence-run in Phytomer e2e-evidence-mcp",
      })
      .click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "loading",
    );
    await expect(drawer.getByTestId("run-evidence")).toHaveText(
      "Loading persisted observation evidence...",
    );
    await expect
      .poll(() => backend.eventReads().filter((id) => id === background.sessionId).length)
      .toBe(1);

    release();
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "ready",
    );
    await expect(drawer.locator(".tool-name")).toHaveText("readFile");
  });

  test("makes a persisted evidence failure explicit", async ({ page }) => {
    const active = makeSession({ sessionId: "tendril-e2e-error-active" });
    const background = makeSession({ sessionId: "tendril-e2e-error-run", origin: "mcp" });
    const backend = await mockStemBackend(page, {
      sessions: [active, background],
      runsBySession: {
        [active.sessionId]: [],
        [background.sessionId]: [makeRun(background.sessionId, "error-run")],
      },
    });
    backend.failEvents(background.sessionId);
    await completeOnboarding(page, testApiKey);

    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for error-run in Phytomer e2e-error-run",
      })
      .click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "error",
    );
    await expect(
      drawer.getByText("The Stem could not provide persisted events."),
    ).toBeVisible();
    await drawer.getByText("Raw Event Pulse and telemetry").click();
    await expect(drawer.getByText("Persisted observation evidence is unavailable.")).toBeVisible();
    await expect(drawer.getByText("No persisted events share this run's step id.")).toHaveCount(0);
  });

  test("prevents an older evidence request from replacing a newer selected run", async ({
    page,
  }) => {
    const active = makeSession({ sessionId: "tendril-e2e-stale-active" });
    const first = makeSession({ sessionId: "tendril-e2e-stale-first" });
    const second = makeSession({ sessionId: "tendril-e2e-stale-second" });
    const backend = await mockStemBackend(page, {
      sessions: [active, first, second],
      runsBySession: {
        [active.sessionId]: [],
        [first.sessionId]: [makeRun(first.sessionId, "first-run")],
        [second.sessionId]: [makeRun(second.sessionId, "second-run")],
      },
      eventsBySession: {
        [active.sessionId]: [],
        [first.sessionId]: [makeEvent(first.sessionId, "first-run", 61)],
        [second.sessionId]: [makeEvent(second.sessionId, "second-run", 62)],
      },
    });
    const releaseFirst = backend.holdEvents(first.sessionId);
    const releaseSecond = backend.holdEvents(second.sessionId);
    await completeOnboarding(page, testApiKey);

    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for first-run in Phytomer e2e-stale-first",
      })
      .click();
    await expect(page.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "loading",
    );
    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for second-run in Phytomer e2e-stale-second",
      })
      .click();
    await expect(page.getByRole("dialog").locator("h2")).toHaveText("second-run");
    await expect
      .poll(() => backend.eventReads().filter((id) => id === second.sessionId).length)
      .toBe(1);

    releaseSecond();
    await expect(page.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "ready",
    );
    releaseFirst();
    await expect
      .poll(() => backend.completedEventReads().filter((id) => id === first.sessionId).length)
      .toBe(1);
    await expect(page.getByRole("dialog").locator("h2")).toHaveText("second-run");
    await expect(page.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "ready",
    );
  });

  test("prevents an evidence response from reopening a closed drilldown", async ({
    page,
  }) => {
    const active = makeSession({ sessionId: "tendril-e2e-close-active" });
    const background = makeSession({ sessionId: "tendril-e2e-close-run" });
    const backend = await mockStemBackend(page, {
      sessions: [active, background],
      runsBySession: {
        [active.sessionId]: [],
        [background.sessionId]: [makeRun(background.sessionId, "closing-run")],
      },
    });
    const release = backend.holdEvents(background.sessionId);
    await completeOnboarding(page, testApiKey);

    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for closing-run in Phytomer e2e-close-run",
      })
      .click();
    await expect(page.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "loading",
    );
    await page.getByRole("button", { name: "✕ close" }).click();
    await expect(page.getByRole("dialog", { name: "Sprout run detail" })).toHaveCount(0);

    release();
    await expect
      .poll(() => backend.completedEventReads().filter((id) => id === background.sessionId).length)
      .toBe(1);
    await expect(page.getByRole("dialog", { name: "Sprout run detail" })).toHaveCount(0);
  });

  test("re-hydrates recent run discovery after the EventBus reconnects", async ({ page }) => {
    const active = makeSession({ sessionId: "tendril-e2e-reconnect-active" });
    const background = makeSession({
      sessionId: "tendril-e2e-reconnect-mcp",
      origin: "mcp",
    });
    const backend = await mockStemBackend(page, {
      sessions: [active, background],
      runsBySession: {
        [active.sessionId]: [],
        [background.sessionId]: [makeRun(background.sessionId, "reconnect-run")],
      },
    });
    await completeOnboarding(page, testApiKey);
    await expect(page.locator(".session-run-row")).toHaveCount(1);
    const initialRunReads = backend.runReads().length;

    await backend.disconnectSocket();
    await expect(page.locator(".conn-dot.closed")).toBeVisible();
    await expect(page.getByText("EventBus live")).toBeVisible({ timeout: 8_000 });
    await expect
      .poll(() => backend.runReads().length, { timeout: 8_000 })
      .toBeGreaterThan(initialRunReads);
    await expect(
      page.getByRole("button", {
        name: "Open Sprout run transcript for reconnect-run in Phytomer e2e-reconnect-mcp",
      }),
    ).toBeVisible();
  });

  test("refreshes evidence for a newly re-hydrated run when prior evidence was cached", async ({
    page,
  }) => {
    const cached = makeSession({
      sessionId: "tendril-e2e-reconnect-evidence",
      lastActiveAt: "2026-09-03T00:00:00Z",
    });
    const active = makeSession({
      sessionId: "tendril-e2e-reconnect-active-session",
      lastActiveAt: "2026-09-04T00:00:00Z",
    });
    const priorRun = makeRun(cached.sessionId, "pre-reconnect-run");
    const backend = await mockStemBackend(page, {
      sessions: [cached, active],
      runsBySession: {
        [cached.sessionId]: [],
        [active.sessionId]: [],
      },
      eventsBySession: {
        [cached.sessionId]: [makeEvent(cached.sessionId, priorRun.runId, 81)],
        [active.sessionId]: [],
      },
    });
    await completeOnboarding(page, testApiKey);
    await expect
      .poll(() => backend.completedEventReads().filter((id) => id === cached.sessionId).length)
      .toBe(1);
    await expect
      .poll(() => backend.runReads().filter((id) => id === cached.sessionId).length)
      .toBe(1);

    await page
      .getByRole("group", { name: "Phytomer e2e-reconnect-active-session" })
      .locator(".session-card")
      .click();
    await expect(page.locator(".chat-head .mono")).toHaveText(active.sessionId);
    await expect
      .poll(() => backend.completedEventReads().filter((id) => id === active.sessionId).length)
      .toBe(1);

    const discoveredRun = makeRun(cached.sessionId, "post-reconnect-run");
    backend.setRuns(cached.sessionId, [discoveredRun]);
    backend.setEvents(cached.sessionId, [
      makeEvent(cached.sessionId, discoveredRun.runId, 82),
    ]);
    const priorRunReadCount = backend.runReads().filter((id) => id === cached.sessionId).length;

    await backend.disconnectSocket();
    await expect(page.locator(".conn-dot.closed")).toBeVisible();
    await expect(page.getByText("EventBus live")).toBeVisible({ timeout: 8_000 });
    await expect
      .poll(
        () => backend.runReads().filter((id) => id === cached.sessionId).length,
        { timeout: 8_000 },
      )
      .toBeGreaterThan(priorRunReadCount);

    const cachedGroup = page.getByRole("group", {
      name: "Phytomer e2e-reconnect-evidence",
    });
    const runRow = cachedGroup.locator(".session-run-row");
    await expect(runRow).toHaveCount(1);
    const release = backend.holdEvents(cached.sessionId);
    await runRow.click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "loading",
    );
    await expect
      .poll(() => backend.eventReads().filter((id) => id === cached.sessionId).length)
      .toBe(2);
    release();

    await expect(drawer.getByTestId("run-evidence")).toHaveAttribute(
      "data-evidence-state",
      "ready",
    );
    await expect(drawer.locator(".tool-name")).toHaveText("readFile");
    await expect(page.locator(".chat-head .mono")).toHaveText(active.sessionId);
  });
});

test.describe("Command Center Substrate binding", () => {
  test("offers configured Substrates and does not bind one through session preferences", async ({
    page,
  }) => {
    const session = makeSession({
      sessionId: "tendril-e2e-soil",
      preferences: {},
    });
    const backend = await mockStemBackend(page, { sessions: [session] });
    await completeOnboarding(page, testApiKey);

    await expect(page.getByText("no substrate", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Substrate")).toBeVisible();
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Substrate").blur();

    expect(backend.lastPreferencePatch()).toBeUndefined();
    await expect(page.getByText("unbound", { exact: true })).toHaveCount(0);
    await expect(page.getByText("bound: opentendril")).toHaveCount(0);
  });
});

const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function sseFrame(observation: Record<string, unknown>): string {
  return `event: observation\ndata: ${JSON.stringify(observation)}\n\n`;
}

function phytomerIdFromPath(url: string): string {
  const match = /\/v1\/phytomers\/([^/]+)\/watch$/.exec(new URL(url).pathname);
  return match ? decodeURIComponent(match[1]) : "";
}

async function fillVerifier(page: Page, executable: string, args: string[]) {
  await page.getByLabel("Verifier executable").fill(executable);
  for (let index = 0; index < args.length; index += 1) {
    await page.getByRole("button", { name: "Add argument", exact: true }).click();
    await page.locator(`#work-arg-${index}`).fill(args[index]);
  }
}

interface ServedSeed {
  phytomerId: string;
  handle: string;
  goal: string;
  status: string;
  substrate?: string;
  iterations?: number;
  branch?: string;
  commit?: string;
  fruitRepository?: string;
  fruitPublicationState?: string;
  diff?: string;
  logs?: string;
  terrariumProvider?: string;
  omitTerrarium?: boolean;
  provider?: string;
  model?: string;
  verificationDiagnostics?: Array<Record<string, unknown>>;
  publicationDiagnostic?: Record<string, unknown>;
  ssePrelude?: string;
}

async function serveSeed(page: Page, seed: ServedSeed): Promise<{ watches: () => Request[] }> {
  const watches: Request[] = [];
  await page.route(
    (url) => /\/v1\/phytomers\/[^/]+\/watch$/.test(new URL(url).pathname),
    async (route) => {
      const request = route.request();
      watches.push(request);
      if (phytomerIdFromPath(request.url()) !== seed.phytomerId) {
        await route.fulfill({
          status: 404,
          body: "no seed growth is associated with this phytomer",
        });
        return;
      }
      const sprout: Record<string, unknown> = {
        runId: "run-observed",
        status: seed.status === "running" ? "running" : "matured",
        provider: seed.provider ?? "grok",
        model: seed.model ?? "grok-4",
      };
      if (!seed.omitTerrarium) sprout.terrariumProvider = seed.terrariumProvider ?? "docker";
      const observation: Record<string, unknown> = {
        handle: seed.handle,
        phytomerId: seed.phytomerId,
        substrate: seed.substrate ?? "opentendril",
        status: seed.status,
        iterations: seed.iterations ?? 1,
        sprouts: [sprout],
      };
      if (seed.branch) observation.branch = seed.branch;
      if (seed.commit) observation.commit = seed.commit;
      if (seed.verificationDiagnostics) {
        observation.verificationDiagnostics = seed.verificationDiagnostics;
      }
      if (seed.publicationDiagnostic) observation.publicationDiagnostic = seed.publicationDiagnostic;
      await route.fulfill({
        status: 200,
        headers: {
          "content-type": "text/event-stream",
          "cache-control": "no-cache",
        },
        body: `${seed.ssePrelude ?? ""}${sseFrame(observation)}`,
      });
    },
  );
  await page.route(
    (url) => /\/v1\/seeds\/runs\/[^/]+$/.test(new URL(url).pathname),
    async (route) => {
      await route.fulfill({
        status: 200,
        json: {
          handle: seed.handle,
          phytomerId: seed.phytomerId,
          substrate: seed.substrate ?? "opentendril",
          goal: seed.goal,
          status: seed.status,
          iterations: seed.iterations ?? 1,
          branch: seed.branch,
          commit: seed.commit,
          fruitRepository: seed.fruitRepository,
          fruitPublicationState: seed.fruitPublicationState,
          diff: seed.diff,
          logs: seed.logs,
          verificationDiagnostics: seed.verificationDiagnostics,
          publicationDiagnostic: seed.publicationDiagnostic,
          startedAt: "2026-09-26T00:00:00Z",
        },
      });
    },
  );
  return { watches: () => watches };
}

async function stableWatchCount(read: () => number, expected: number) {
  const started = Date.now();
  await expect
    .poll(() => {
      const count = read();
      if (count !== expected) return count;
      return Date.now() - started >= 400 ? expected : -1;
    })
    .toBe(expected);
}

const unresolvedRetryStorageKey = "opentendril.unresolvedSeedRetry";

async function unresolvedRetryEnvelope(
  page: Page,
): Promise<{ request?: Record<string, unknown>; message?: string } | null> {
  return page.evaluate((key) => {
    const raw = sessionStorage.getItem(key);
    if (!raw) return null;
    return JSON.parse(raw) as { request?: Record<string, unknown>; message?: string };
  }, unresolvedRetryStorageKey);
}

test.describe("Canonical Seed workbench", () => {
  test("parses Phytomer observation frames split across byte chunks", async () => {
    const encoder = new TextEncoder();
    const parts = [
      "event: observa",
      'tion\r\ndata: {"handle":"seed-1","status":"run',
      'ning","iterations":2}\n',
      "\n: comment\nid: 7\nretry: 1000\nfoo: ignored\nevent: heartbeat\ndata: ignore-me\n\n",
      'event: error\ndata: {"error":"watch closed"}',
    ];
    const events: string[] = [];
    const observations: Array<Record<string, unknown>> = [];
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        for (const part of parts) controller.enqueue(encoder.encode(part));
        controller.close();
      },
    });

    await expect(
      readSSEFrames(stream, (frame) => {
        events.push(frame.event);
        if (frame.event === "observation") observations.push(JSON.parse(frame.data) as Record<string, unknown>);
        if (frame.event === "error") throw new Error(frame.data);
      }),
    ).rejects.toThrow(/watch closed/);

    expect(events).toEqual(["observation", "heartbeat", "error"]);
    expect(observations).toEqual([
      { handle: "seed-1", status: "running", iterations: 2 },
    ]);
  });

  test("dispatches a detached Seed with argv verification and does not call chat completions", async ({
    page,
  }) => {
    const backend = await mockStemBackend(page, { sessions: [] });
    const grows: Array<Record<string, unknown>> = [];
    const chatCalls: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/v1/chat/completions")) chatCalls.push(request.url());
    });
    const surface = await serveSeed(page, {
      phytomerId: "tendril-canonical-1",
      handle: "seed-canonical-1",
      goal: "make the tests pass",
      status: "running",
      substrate: "opentendril",
      iterations: 1,
      terrariumProvider: "docker",
      provider: "grok",
      model: "grok-4",
      ssePrelude:
        ": comment\nid: 9\nretry: 1000\nfoo: ignored\nevent: heartbeat\ndata: ignore-me\n\n",
    });
    await page.route("**/v1/seeds/grow", async (route) => {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      grows.push(body);
      backend.setSessions([
        makeSession({ sessionId: "tendril-canonical-1", origin: "rest" }),
      ]);
      await route.fulfill({
        status: 202,
        json: {
          handle: "seed-canonical-1",
          phytomerId: "tendril-canonical-1",
          status: "running",
        },
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "opentendril" })).toBeAttached();
    await expect(page.getByTestId("seed-bounds")).not.toHaveAttribute("open");
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Task").fill("make the tests pass");
    await fillVerifier(page, "go", ["test", "./...", "hello world", "foo;bar"]);
    await page.getByTestId("seed-bounds").locator("summary").click();
    await page.getByLabel("Max iterations").fill("99");
    await page.getByLabel("Timeout seconds").fill("30");
    await page.getByRole("button", { name: "Start work" }).click();

    await expect(page.getByTestId("seed-goal")).toHaveText("make the tests pass");
    await expect(page.locator(".chat-head .mono")).toHaveText("tendril-canonical-1");
    await expect(page.getByTestId("terrarium-provider")).toContainText("Docker");
    await expect(page.getByTestId("terrarium-provider-note")).toHaveCount(0);
    await expect(page.getByTestId("seed-model")).toContainText("grok / grok-4");
    expect(chatCalls).toEqual([]);
    expect(grows).toHaveLength(1);
    expect(grows[0]).toMatchObject({
      substrate: "opentendril",
      goal: "make the tests pass",
      verify: ["go", "test", "./...", "hello world", "foo;bar"],
      detached: true,
      origin: "rest",
      maxIterations: 99,
      timeoutSeconds: 30,
    });
    expect(grows[0].verify).toEqual(["go", "test", "./...", "hello world", "foo;bar"]);
    expect(grows[0].idempotencyKey).toEqual(expect.stringMatching(uuidPattern));
    expect(grows[0].idempotencyKey).not.toContain("make the tests pass");
    expect(grows[0].idempotencyKey).not.toContain("opentendril");
    const watch = surface.watches()[0];
    expect(watch.headers().authorization).toBe(`Bearer ${testApiKey}`);
    expect(watch.url()).not.toContain(testApiKey);
    expect(watch.url()).not.toContain("key=");
    await stableWatchCount(() => surface.watches().length, 1);
  });

  test("aborts the prior watch when active work changes and does not double-watch it", async ({
    page,
  }) => {
    const backend = await mockStemBackend(page, { sessions: [] });
    const watches: Array<{ id: string; request: Request }> = [];
    let growCount = 0;
    await page.route("**/v1/seeds/grow", async (route) => {
      growCount += 1;
      const id = growCount === 1 ? "tendril-watch-a" : "tendril-watch-b";
      backend.setSessions([makeSession({ sessionId: id, origin: "rest" })]);
      await route.fulfill({
        status: 202,
        json: { handle: `seed-${growCount}`, phytomerId: id, status: "running" },
      });
    });
    await page.route(
      (url) => /\/v1\/seeds\/runs\/[^/]+$/.test(new URL(url).pathname),
      async (route) => {
        const handle = decodeURIComponent(
          new URL(route.request().url()).pathname.split("/").pop() ?? "",
        );
        const phytomerId = handle === "seed-1" ? "tendril-watch-a" : "tendril-watch-b";
        await route.fulfill({
          status: 200,
          json: {
            handle,
            phytomerId,
            substrate: "opentendril",
            goal: handle === "seed-1" ? "first task" : "second task",
            status: "running",
            iterations: 0,
            startedAt: "2026-09-26T00:00:00Z",
          },
        });
      },
    );
    await page.route(
      (url) => /\/v1\/phytomers\/[^/]+\/watch$/.test(new URL(url).pathname),
      async (route) => {
        const request = route.request();
        const id = phytomerIdFromPath(request.url());
        watches.push({ id, request });
        if (id === "tendril-watch-a") {
          await new Promise<void>((resolve) => {
            const timer = setInterval(() => {
              if (request.failure()) {
                clearInterval(timer);
                resolve();
              }
            }, 20);
            setTimeout(() => {
              clearInterval(timer);
              resolve();
            }, 8000);
          });
          try {
            await route.fulfill({ status: 204, body: "" });
          } catch {
            // The page already aborted this watch.
          }
          return;
        }
        await route.fulfill({
          status: 200,
          headers: { "content-type": "text/event-stream" },
          body: sseFrame({
            handle: "seed-2",
            phytomerId: id,
            substrate: "opentendril",
            status: "running",
            iterations: 0,
            sprouts: [],
          }),
        });
      },
    );

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "opentendril" })).toBeAttached();
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Task").fill("first task");
    await fillVerifier(page, "go", ["test"]);
    await page.getByRole("button", { name: "Start work" }).click();
    await expect(page.getByTestId("seed-goal")).toHaveText("first task");
    await expect.poll(() => watches.filter((watch) => watch.id === "tendril-watch-a").length).toBe(1);
    await page.locator(".session-card").click();
    await stableWatchCount(
      () => watches.filter((watch) => watch.id === "tendril-watch-a").length,
      1,
    );

    await page.getByRole("button", { name: "New work" }).click();
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Task").fill("second task");
    await fillVerifier(page, "go", ["test"]);
    await page.getByRole("button", { name: "Start work" }).click();
    await expect
      .poll(() => watches.find((watch) => watch.id === "tendril-watch-a")?.request.failure())
      .toBeTruthy();
    expect(watches.filter((watch) => watch.id === "tendril-watch-a")).toHaveLength(1);
    await expect.poll(() => watches.some((watch) => watch.id === "tendril-watch-b")).toBe(true);
    expect(growCount).toBe(2);
  });

  test("a terminal observation closes the watch", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-terminal", origin: "rest" });
    await mockStemBackend(page, { sessions: [session] });
    const surface = await serveSeed(page, {
      phytomerId: "tendril-terminal",
      handle: "seed-terminal",
      goal: "finish the bounded task",
      status: "satisfied",
      iterations: 2,
      verificationDiagnostics: [{ iteration: 2, outcome: "passed", timedOut: false }],
    });
    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-goal")).toHaveText("finish the bounded task");
    await expect(page.getByTestId("seed-status")).toHaveText("satisfied");
    await expect(page.getByTestId("continuation-form")).toHaveCount(0);
    await expect(page.getByTestId("fruit-absent")).toBeVisible();
    await stableWatchCount(() => surface.watches().length, 1);
  });

  test("shows a recorded host boundary and a missing provider as unknown", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-boundary", origin: "rest" });
    const dockerRun = {
      ...makeRun(session.sessionId, "docker-run"),
      terrariumProvider: "docker",
    };
    await mockStemBackend(page, {
      sessions: [session],
      runsBySession: { [session.sessionId]: [dockerRun] },
      eventsBySession: { [session.sessionId]: [makeEvent(session.sessionId, "docker-run", 7)] },
    });
    await serveSeed(page, {
      phytomerId: session.sessionId,
      handle: "seed-boundary",
      goal: "inspect the boundary",
      status: "running",
      terrariumProvider: "host",
    });
    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("terrarium-provider")).toContainText("Host execution");
    await expect(page.getByTestId("terrarium-provider-note")).toHaveText(
      "Host execution bypasses Terrarium isolation.",
    );
    await page.locator(".runs .run-row").click();
    const drawer = page.getByRole("dialog", { name: "Sprout run detail" });
    await expect(drawer.getByTestId("run-terrarium-provider")).toContainText("Docker");
    await expect(drawer.getByTestId("run-terrarium-provider")).not.toContainText(
      "bypasses Terrarium isolation",
    );
    await expect(page.locator(".chat-head .mono")).toHaveText(session.sessionId);
  });

  test("leaves an absent Terrarium provider unknown on the workbench and the run", async ({
    page,
  }) => {
    const session = makeSession({ sessionId: "tendril-unknown-boundary", origin: "cli" });
    const run = makeRun(session.sessionId, "unknown-provider-run");
    await mockStemBackend(page, {
      sessions: [session],
      runsBySession: { [session.sessionId]: [run] },
    });
    await serveSeed(page, {
      phytomerId: session.sessionId,
      handle: "seed-unknown-boundary",
      goal: "historical provider",
      status: "running",
      omitTerrarium: true,
    });
    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("terrarium-provider")).toContainText("Unknown");
    await expect(page.getByTestId("terrarium-provider-note")).toHaveCount(0);
    await expect(page.getByTestId("terrarium-provider")).not.toContainText("Docker");
    await page.locator(".runs .run-row").click();
    await expect(
      page.getByRole("dialog", { name: "Sprout run detail" }).getByTestId("run-terrarium-provider"),
    ).toContainText("Unknown");
  });

  test("continues the running Seed and shows a terminal-race rejection without starting another Seed", async ({
    page,
  }) => {
    const seedSession = makeSession({
      sessionId: "tendril-seed-live",
      origin: "rest",
      lastActiveAt: "2026-09-04T00:00:00Z",
    });
    const historical = makeSession({
      sessionId: "tendril-historical",
      origin: "mcp",
      lastActiveAt: "2026-09-01T00:00:00Z",
    });
    const historicalRun = makeRun(historical.sessionId, "run-hist");
    await mockStemBackend(page, {
      sessions: [seedSession, historical],
      runsBySession: {
        [seedSession.sessionId]: [],
        [historical.sessionId]: [historicalRun],
      },
      eventsBySession: {
        [historical.sessionId]: [makeEvent(historical.sessionId, historicalRun.runId, 11)],
      },
    });
    await serveSeed(page, {
      phytomerId: seedSession.sessionId,
      handle: "seed-live",
      goal: "keep the public API stable",
      status: "running",
    });
    const continues: Array<{ url: string; body: Record<string, unknown> }> = [];
    const grows: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url().includes("/v1/seeds/grow")) {
        grows.push(request.url());
      }
    });
    await page.route(
      (url) => /\/v1\/phytomers\/[^/]+\/continue$/.test(new URL(url).pathname),
      async (route) => {
        continues.push({
          url: route.request().url(),
          body: route.request().postDataJSON() as Record<string, unknown>,
        });
        await route.fulfill({
          status: 409,
          body: "phytomer is not continuation-eligible",
        });
      },
    );

    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-goal")).toHaveText("keep the public API stable");
    await page
      .getByRole("button", {
        name: "Open Sprout run transcript for run-hist in Phytomer historical",
      })
      .click();
    await expect(page.getByTestId("seed-work-context")).toBeVisible();
    await expect(page.locator(".chat-head .mono")).toHaveText(seedSession.sessionId);
    await page.getByLabel("Continue this Seed").fill("preserve the argv verifier");
    await page.getByRole("button", { name: "Continue work" }).click();

    await expect(page.getByTestId("continuation-error")).toContainText(
      "phytomer is not continuation-eligible",
    );
    expect(continues).toHaveLength(1);
    expect(continues[0].url).toContain("/v1/phytomers/tendril-seed-live/continue");
    expect(continues[0].body).toMatchObject({
      intent: "preserve the argv verifier",
      sessionId: "tendril-seed-live",
    });
    expect(continues[0].body.idempotencyKey).toEqual(expect.stringMatching(uuidPattern));
    expect(continues[0].body.idempotencyKey).not.toBe("tendril-seed-live");
    expect(grows).toEqual([]);
    await expect(page.getByTestId("continuation-form")).toBeVisible();
  });

  test("reconstructs the durable goal and Fruit review state after refresh", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-refresh", origin: "rest" });
    await mockStemBackend(page, { sessions: [session] });
    await serveSeed(page, {
      phytomerId: session.sessionId,
      handle: "seed-refresh",
      goal: "refresh the docs",
      status: "satisfied",
      substrate: "docs",
      iterations: 2,
      branch: "tendril/seed-refresh",
      commit: "abc123def",
      fruitRepository: "opentendril/opentendril",
      fruitPublicationState: "published",
      diff: "diff --git a/README.md b/README.md\n+reviewed line",
      logs: "verify ok",
      verificationDiagnostics: [{ iteration: 2, outcome: "passed", timedOut: false }],
    });
    await page.route("**/v1/fruit", async (route) => {
      await route.fulfill({
        status: 200,
        json: {
          items: [
            {
              producerKind: "sprout",
              producerIdentity: "tendril/seed-refresh",
              reviewState: "merged",
              repository: "decoy/repo",
              branch: "tendril/seed-refresh",
              commit: "abc123def",
              pullRequest: 99,
            },
            {
              producerKind: "seed",
              producerIdentity: "seed-refresh",
              phytomerId: session.sessionId,
              reviewState: "outstanding",
              repository: "opentendril/opentendril",
              branch: "tendril/seed-refresh",
              commit: "abc123def",
              pullRequest: 12,
            },
          ],
          counts: {
            outstanding: 1,
            unknown: 0,
            closedUnmerged: 0,
            merged: 1,
            total: 2,
          },
        },
      });
    });
    const grows: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url().includes("/v1/seeds/grow")) {
        grows.push(request.url());
      }
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-goal")).toHaveText("refresh the docs");
    await expect(page.getByTestId("phytomer-label")).toHaveText("refresh the docs");
    await expect(page.getByTestId("fruit-branch")).toHaveText("tendril/seed-refresh");
    await expect(page.getByTestId("fruit-commit")).toHaveText("abc123def");
    await expect(page.getByTestId("fruit-repository")).toHaveText("opentendril/opentendril");
    await expect(page.getByTestId("fruit-publication-state")).toHaveText("published");
    await expect(page.getByTestId("fruit-review-state")).toHaveText("outstanding");
    await expect(page.getByTestId("fruit-pull-request")).toHaveText("Pull request 12");
    await expect(page.getByTestId("fruit-result")).not.toContainText("merged");
    await expect(page.getByText("diff --git a/README.md")).toBeHidden();
    await page.getByTestId("seed-diff").locator("summary").click();
    await expect(page.getByText("diff --git a/README.md")).toBeVisible();
    const stored = await page.evaluate(() => JSON.stringify(window.localStorage));
    expect(stored).not.toContain("refresh the docs");

    await page.reload();
    await expect(page.getByTestId("seed-goal")).toHaveText("refresh the docs", { timeout: 10000 });
    await expect(page.getByTestId("fruit-review-state")).toHaveText("outstanding");
    expect(grows).toEqual([]);
  });

  test("does not invent Fruit when branch and commit provenance are absent", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-exhausted", origin: "rest" });
    await mockStemBackend(page, { sessions: [session] });
    await serveSeed(page, {
      phytomerId: session.sessionId,
      handle: "seed-exhausted",
      goal: "try until the bounds end",
      status: "exhausted",
      iterations: 3,
      verificationDiagnostics: [
        {
          iteration: 3,
          outcome: "predicate-failed",
          exitCode: 1,
          timedOut: false,
          message: "tests failed",
        },
      ],
    });
    await page.route("**/v1/fruit", async (route) => {
      await route.fulfill({
        status: 200,
        json: {
          items: [
            {
              producerKind: "seed",
              producerIdentity: "seed-exhausted",
              phytomerId: session.sessionId,
              reviewState: "outstanding",
              repository: "opentendril/opentendril",
              branch: "tendril/invented",
              commit: "deadbeef",
              pullRequest: 4,
            },
          ],
          counts: {
            outstanding: 1,
            unknown: 0,
            closedUnmerged: 0,
            merged: 0,
            total: 1,
          },
        },
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-status")).toHaveText("exhausted");
    await expect(page.getByTestId("verification-diagnostics")).toContainText("predicate-failed");
    await expect(page.getByTestId("verification-diagnostics")).toContainText("tests failed");
    await expect(page.getByTestId("fruit-absent")).toBeVisible();
    await expect(page.getByTestId("fruit-branch")).toHaveCount(0);
    await expect(page.getByTestId("fruit-review-state")).toHaveCount(0);
    await expect(page.getByTestId("fruit-result")).not.toContainText("outstanding");
    await expect(page.getByTestId("fruit-result")).not.toContainText("tendril/invented");
  });

  test("shows a publication diagnostic without claiming Fruit was published", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-publish-failed", origin: "rest" });
    await mockStemBackend(page, { sessions: [session] });
    await serveSeed(page, {
      phytomerId: session.sessionId,
      handle: "seed-publish-failed",
      goal: "publish the result",
      status: "fruit-publication-failed",
      publicationDiagnostic: {
        failureCategory: "fruit-publication",
        executionStatus: "satisfied",
        phase: "publication",
        outcome: "publication-failed",
        retrySafe: false,
        message: "remote rejected the update",
      },
    });
    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("fruit-status")).toHaveText("fruit-publication-failed");
    await expect(page.getByTestId("publication-diagnostic")).toContainText(
      "remote rejected the update",
    );
    await expect(page.getByTestId("fruit-absent")).toBeVisible();
    await expect(page.getByTestId("fruit-branch")).toHaveCount(0);
    await expect(page.getByTestId("fruit-result")).not.toContainText("published");
  });

  test("keeps the original idempotency key after an ambiguous transport failure", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      const original = crypto.randomUUID.bind(crypto);
      let count = 0;
      crypto.randomUUID = () => {
        count += 1;
        (window as Window & { __uuidCount?: number }).__uuidCount = count;
        return original();
      };
    });
    const backend = await mockStemBackend(page, { sessions: [] });
    const posts: Array<Record<string, unknown>> = [];
    await serveSeed(page, {
      phytomerId: "tendril-retry",
      handle: "seed-retry",
      goal: "retry the same seed",
      status: "running",
    });
    await page.route("**/v1/seeds/grow", async (route) => {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      posts.push(body);
      if (posts.length === 1) {
        await route.abort("failed");
        return;
      }
      backend.setSessions([makeSession({ sessionId: "tendril-retry", origin: "rest" })]);
      await route.fulfill({
        status: 202,
        json: { handle: "seed-retry", phytomerId: "tendril-retry", status: "running" },
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "opentendril" })).toBeAttached();
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Task").fill("retry the same seed");
    await fillVerifier(page, "go", ["test", "./..."]);
    const readsBefore = backend.sessionListReads();
    await page.getByRole("button", { name: "Start work" }).click();

    await expect(page.getByTestId("dispatch-ambiguous")).toContainText(
      "The dispatch outcome is uncertain",
    );
    await expect.poll(() => backend.sessionListReads()).toBeGreaterThan(readsBefore);
    expect(posts).toHaveLength(1);
    const retained = await page.getByTestId("dispatch-idempotency-key").textContent();
    expect(retained).toBe(posts[0].idempotencyKey);
    expect(retained).toEqual(expect.stringMatching(uuidPattern));
    const keysBeforeRetry = await page.evaluate(
      () => (window as Window & { __uuidCount?: number }).__uuidCount,
    );
    expect(keysBeforeRetry).toBe(1);

    await expect(page.getByRole("button", { name: "Discard uncertain dispatch" })).toHaveCount(0);

    await page.getByRole("button", { name: "Retry dispatch" }).click();
    await expect(page.getByTestId("seed-goal")).toHaveText("retry the same seed");
    expect(posts).toHaveLength(2);
    expect(posts[1].idempotencyKey).toBe(posts[0].idempotencyKey);
    expect(posts[1].goal).toBe(posts[0].goal);
    expect(posts[1].verify).toEqual(posts[0].verify);
    expect(posts[1].substrate).toBe(posts[0].substrate);
    const keysAfterRetry = await page.evaluate(
      () => (window as Window & { __uuidCount?: number }).__uuidCount,
    );
    expect(keysAfterRetry).toBe(keysBeforeRetry);
    expect(await unresolvedRetryEnvelope(page)).toBeNull();
  });

  test("presents an explicit Seed rejection without dispatching again", async ({ page }) => {
    await page.addInitScript(() => {
      const original = crypto.randomUUID.bind(crypto);
      let count = 0;
      crypto.randomUUID = () => {
        count += 1;
        (window as Window & { __uuidCount?: number }).__uuidCount = count;
        return original();
      };
    });
    await mockStemBackend(page, { sessions: [] });
    const posts: Array<Record<string, unknown>> = [];
    await page.route("**/v1/seeds/grow", async (route) => {
      posts.push(route.request().postDataJSON() as Record<string, unknown>);
      await route.fulfill({
        status: 403,
        body: "delegation denied: substrate is outside the grant",
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "opentendril" })).toBeAttached();
    await page.getByLabel("Substrate").selectOption("docs");
    await page.getByLabel("Task").fill("a rejected task");
    await fillVerifier(page, "go", ["test"]);
    await page.getByRole("button", { name: "Start work" }).click();

    await expect(page.getByTestId("dispatch-rejected")).toContainText(
      "delegation denied: substrate is outside the grant",
    );
    await expect(page.getByTestId("dispatch-ambiguous")).toHaveCount(0);
    await expect(page.getByTestId("new-work-form")).toBeVisible();
    await stableWatchCount(() => posts.length, 1);
    expect(
      await page.evaluate(() => (window as Window & { __uuidCount?: number }).__uuidCount),
    ).toBe(1);
    expect(await unresolvedRetryEnvelope(page)).toBeNull();
  });

  test("does not attach a concurrent Seed that shares the goal and Substrate", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      const original = crypto.randomUUID.bind(crypto);
      let count = 0;
      crypto.randomUUID = () => {
        count += 1;
        (window as Window & { __uuidCount?: number }).__uuidCount = count;
        return original();
      };
    });
    const backend = await mockStemBackend(page, { sessions: [] });
    const posts: Array<Record<string, unknown>> = [];
    const impostorWatches: string[] = [];
    const impostorCollects: string[] = [];
    const sharedGoal = "shared goal text";
    await serveSeed(page, {
      phytomerId: "tendril-authoritative",
      handle: "seed-authoritative",
      goal: sharedGoal,
      status: "running",
      substrate: "opentendril",
    });
    // Registered after serveSeed so this Phytomer can answer the watch. Greenhouse
    // must still not treat the shared goal and Substrate as the failed write.
    await page.route(
      (url) => /\/v1\/phytomers\/[^/]+\/watch$/.test(new URL(url).pathname),
      async (route) => {
        const id = phytomerIdFromPath(route.request().url());
        if (id !== "tendril-impostor") {
          await route.fallback();
          return;
        }
        impostorWatches.push(route.request().url());
        await route.fulfill({
          status: 200,
          headers: { "content-type": "text/event-stream" },
          body: sseFrame({
            handle: "seed-impostor",
            phytomerId: "tendril-impostor",
            substrate: "opentendril",
            status: "running",
            iterations: 1,
            sprouts: [],
          }),
        });
      },
    );
    await page.route(
      (url) => /\/v1\/seeds\/runs\/[^/]+$/.test(new URL(url).pathname),
      async (route) => {
        if (!route.request().url().includes("/seed-impostor")) {
          await route.fallback();
          return;
        }
        impostorCollects.push(route.request().url());
        await route.fulfill({
          status: 200,
          json: {
            handle: "seed-impostor",
            phytomerId: "tendril-impostor",
            substrate: "opentendril",
            goal: sharedGoal,
            status: "running",
            iterations: 1,
            startedAt: "2026-09-26T00:00:00Z",
          },
        });
      },
    );
    await page.route("**/v1/seeds/grow", async (route) => {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      posts.push(body);
      if (posts.length === 1) {
        backend.setSessions([makeSession({ sessionId: "tendril-impostor", origin: "rest" })]);
        await route.abort("failed");
        return;
      }
      backend.setSessions([
        makeSession({ sessionId: "tendril-impostor", origin: "rest" }),
        makeSession({ sessionId: "tendril-authoritative", origin: "rest" }),
      ]);
      await route.fulfill({
        status: 202,
        json: {
          handle: "seed-authoritative",
          phytomerId: "tendril-authoritative",
          status: "running",
        },
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "opentendril" })).toBeAttached();
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Task").fill(sharedGoal);
    await fillVerifier(page, "go", ["test", "./..."]);
    await page.getByRole("button", { name: "Start work" }).click();

    await expect(page.getByTestId("dispatch-ambiguous")).toContainText(
      "The dispatch outcome is uncertain",
    );
    await expect(page.getByRole("group", { name: "Phytomer impostor" })).toBeVisible();
    const started = Date.now();
    await expect
      .poll(() => {
        if (impostorWatches.length !== 0 || impostorCollects.length !== 0) return "attached";
        return Date.now() - started >= 1000 ? "stable" : "waiting";
      })
      .toBe("stable");
    await expect(page.locator(".chat-head .mono")).toHaveText("none");
    await expect(page.getByTestId("active-seed-work")).toHaveCount(0);
    await expect(page.getByTestId("seed-goal")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Discard uncertain dispatch" })).toHaveCount(0);
    expect(posts).toHaveLength(1);
    const retained = await page.getByTestId("dispatch-idempotency-key").textContent();
    expect(retained).toBe(posts[0].idempotencyKey);

    await page.getByRole("button", { name: "Retry dispatch" }).click();
    await expect(page.locator(".chat-head .mono")).toHaveText("tendril-authoritative");
    await expect(page.getByTestId("seed-goal")).toHaveText(sharedGoal);
    await expect(page.getByTestId("dispatch-ambiguous")).toHaveCount(0);
    expect(posts).toHaveLength(2);
    expect(posts[1].idempotencyKey).toBe(posts[0].idempotencyKey);
    expect(posts[1].goal).toBe(posts[0].goal);
    expect(posts[1].substrate).toBe(posts[0].substrate);
    expect(posts[1].verify).toEqual(posts[0].verify);
    expect(impostorWatches).toEqual([]);
    expect(impostorCollects).toEqual([]);
    expect(
      await page.evaluate(() => (window as Window & { __uuidCount?: number }).__uuidCount),
    ).toBe(1);
  });

  test("restores the unresolved Seed retry after reload and does not dispatch it", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      const original = crypto.randomUUID.bind(crypto);
      let count = 0;
      crypto.randomUUID = () => {
        count += 1;
        (window as Window & { __uuidCount?: number }).__uuidCount = count;
        return original();
      };
    });
    const backend = await mockStemBackend(page, { sessions: [] });
    const posts: Array<Record<string, unknown>> = [];
    await serveSeed(page, {
      phytomerId: "tendril-reloaded",
      handle: "seed-reloaded",
      goal: "survive the reload",
      status: "running",
      substrate: "docs",
    });
    await page.route("**/v1/seeds/grow", async (route) => {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      posts.push(body);
      if (posts.length === 1) {
        await route.abort("failed");
        return;
      }
      backend.setSessions([makeSession({ sessionId: "tendril-reloaded", origin: "rest" })]);
      await route.fulfill({
        status: 202,
        json: { handle: "seed-reloaded", phytomerId: "tendril-reloaded", status: "running" },
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "docs" })).toBeAttached();
    await page.getByLabel("Substrate").selectOption("docs");
    await page.getByLabel("Task").fill("survive the reload");
    await fillVerifier(page, "go", ["test", "./..."]);
    await page.getByRole("button", { name: "Start work" }).click();
    await expect(page.getByTestId("dispatch-ambiguous")).toContainText(
      "The dispatch outcome is uncertain",
    );
    const retained = await page.getByTestId("dispatch-idempotency-key").textContent();
    expect(retained).toBe(posts[0].idempotencyKey);
    const beforeReload = await unresolvedRetryEnvelope(page);
    expect(beforeReload?.request).toMatchObject({
      substrate: "docs",
      goal: "survive the reload",
      verify: ["go", "test", "./..."],
      detached: true,
      origin: "rest",
      idempotencyKey: posts[0].idempotencyKey,
    });

    await page.reload();
    await expect(page.getByTestId("dispatch-idempotency-key")).toHaveText(retained ?? "", {
      timeout: 10000,
    });
    await stableWatchCount(() => posts.length, 1);
    const afterReload = await unresolvedRetryEnvelope(page);
    expect(afterReload?.request).toMatchObject(beforeReload?.request ?? {});
    await expect(page.getByTestId("dispatch-ambiguous")).toContainText("survive the reload");
    await expect(page.getByTestId("dispatch-ambiguous")).toContainText("docs");
    await expect(page.getByTestId("dispatch-ambiguous")).toContainText("./...");
    await expect(page.getByTestId("new-work-form")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Discard uncertain dispatch" })).toHaveCount(0);
    expect(
      await page.evaluate(() => (window as Window & { __uuidCount?: number }).__uuidCount ?? 0),
    ).toBe(0);

    await page.getByRole("button", { name: "Retry dispatch" }).click();
    await expect(page.getByTestId("seed-goal")).toHaveText("survive the reload");
    await expect(page.locator(".chat-head .mono")).toHaveText("tendril-reloaded");
    expect(posts).toHaveLength(2);
    expect(posts[1]).toMatchObject({
      substrate: posts[0].substrate,
      goal: posts[0].goal,
      verify: posts[0].verify,
      idempotencyKey: posts[0].idempotencyKey,
      detached: true,
      origin: "rest",
    });
    expect(
      await page.evaluate(() => (window as Window & { __uuidCount?: number }).__uuidCount ?? 0),
    ).toBe(0);
    expect(await unresolvedRetryEnvelope(page)).toBeNull();
  });

  test("stays ambiguous when a successful Seed response has no canonical identity", async ({
    page,
  }) => {
    const backend = await mockStemBackend(page, { sessions: [] });
    const posts: Array<Record<string, unknown>> = [];
    await serveSeed(page, {
      phytomerId: "tendril-repaired",
      handle: "seed-repaired",
      goal: "repair the response",
      status: "running",
      substrate: "opentendril",
    });
    await page.route("**/v1/seeds/grow", async (route) => {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      posts.push(body);
      if (posts.length === 1) {
        await route.fulfill({ status: 202, json: { status: "running" } });
        return;
      }
      backend.setSessions([makeSession({ sessionId: "tendril-repaired", origin: "rest" })]);
      await route.fulfill({
        status: 202,
        json: { handle: "seed-repaired", phytomerId: "tendril-repaired", status: "running" },
      });
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByRole("option", { name: "opentendril" })).toBeAttached();
    await page.getByLabel("Substrate").selectOption("opentendril");
    await page.getByLabel("Task").fill("repair the response");
    await fillVerifier(page, "go", ["test"]);
    await page.getByRole("button", { name: "Start work" }).click();

    await expect(page.getByTestId("dispatch-ambiguous")).toContainText(
      "did not include a Seed handle and Phytomer",
    );
    await expect(page.locator(".chat-head .mono")).toHaveText("none");
    await expect(page.getByTestId("active-seed-work")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Discard uncertain dispatch" })).toHaveCount(0);
    expect(posts).toHaveLength(1);
    const envelope = await unresolvedRetryEnvelope(page);
    expect(envelope?.request?.idempotencyKey).toBe(posts[0].idempotencyKey);

    await page.getByRole("button", { name: "Retry dispatch" }).click();
    await expect(page.getByTestId("seed-goal")).toHaveText("repair the response");
    await expect(page.locator(".chat-head .mono")).toHaveText("tendril-repaired");
    expect(posts).toHaveLength(2);
    expect(posts[1].idempotencyKey).toBe(posts[0].idempotencyKey);
    expect(posts[1].goal).toBe(posts[0].goal);
    expect(posts[1].verify).toEqual(posts[0].verify);
    expect(await unresolvedRetryEnvelope(page)).toBeNull();
  });

  test("classifies only a 404 watch as historical non-Seed work", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-historical-watch", origin: "cli" });
    const run = makeRun(session.sessionId, "run-historical-watch");
    await mockStemBackend(page, {
      sessions: [session],
      runsBySession: { [session.sessionId]: [run] },
    });

    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-workbench")).toHaveAttribute("data-watch-phase", "not-seed");
    await expect(page.getByTestId("new-work-form")).toBeVisible();
    await expect(page.getByTestId("watch-error")).toHaveCount(0);
    await expect(page.getByTestId("active-seed-work")).toHaveCount(0);
    await expect(page.locator(".runs .run-row")).toHaveCount(1);
    await expect(page.locator(".chat-head .mono")).toHaveText(session.sessionId);
  });

  test("shows a non-event-stream watch response as a watch error", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-bad-watch", origin: "cli" });
    const run = makeRun(session.sessionId, "run-bad-watch");
    await mockStemBackend(page, {
      sessions: [session],
      runsBySession: { [session.sessionId]: [run] },
    });
    await page.route(
      (url) => /\/v1\/phytomers\/[^/]+\/watch$/.test(new URL(url).pathname),
      async (route) => {
        await route.fulfill({
          status: 200,
          headers: { "content-type": "application/json" },
          body: JSON.stringify({
            handle: "seed-should-not-attach",
            phytomerId: session.sessionId,
            status: "running",
            iterations: 1,
            substrate: "opentendril",
          }),
        });
      },
    );

    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-workbench")).toHaveAttribute("data-watch-phase", "error");
    await expect(page.getByTestId("watch-error")).toContainText("not an event stream");
    await expect(page.getByTestId("new-work-form")).toHaveCount(0);
    await expect(page.getByTestId("active-seed-work")).toHaveCount(0);
    await expect(page.getByTestId("seed-goal")).toHaveCount(0);
    await expect(page.locator(".runs .run-row")).toHaveCount(1);
    await expect(page.locator(".chat-head .mono")).toHaveText(session.sessionId);
  });

  test("keeps malformed Phytomer observation data a watch error", async ({ page }) => {
    const session = makeSession({ sessionId: "tendril-malformed-watch", origin: "cli" });
    await mockStemBackend(page, { sessions: [session] });
    await page.route(
      (url) => /\/v1\/phytomers\/[^/]+\/watch$/.test(new URL(url).pathname),
      async (route) => {
        await route.fulfill({
          status: 200,
          headers: { "content-type": "text/event-stream" },
          body: "event: observation\ndata: not-json\n\n",
        });
      },
    );

    await completeOnboarding(page, testApiKey);
    await expect(page.getByTestId("seed-workbench")).toHaveAttribute("data-watch-phase", "error");
    await expect(page.getByTestId("watch-error")).toContainText(
      "Phytomer watch observation was not valid JSON",
    );
    await expect(page.getByTestId("new-work-form")).toHaveCount(0);
    await expect(page.getByTestId("active-seed-work")).toHaveCount(0);
  });
});
