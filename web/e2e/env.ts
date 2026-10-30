import { execFileSync } from "node:child_process";
import { type BrowserContext, expect, type Page } from "@playwright/test";

export const setupToken = process.env.E2E_SETUP_TOKEN ?? "e2e-setup-token";
export const owner = { email: "owner@acme.test", name: "Olive Owner", password: "owner-password-1" };
export const reviewer = {
  email: "reviewer@acme.test",
  name: "Rae Reviewer",
  password: "reviewer-password-1",
};
export const member = { email: "member@acme.test", name: "Mel Member", password: "member-password-1" };

type Cookies = Awaited<ReturnType<BrowserContext["cookies"]>>;
const sessions = new Map<string, Cookies>();

/**
 * Signs a user in. The suite reuses the session of an earlier sign-in, because
 * every sign-in attempt counts against the rate limit of the address.
 */
export async function signIn(page: Page, who: { email: string; password: string }) {
  const saved = sessions.get(who.email);
  if (saved) {
    await page.context().addCookies(saved);
    await page.goto("/");
    await page.waitForLoadState("networkidle");
    if (!page.url().includes("/sign-in")) return;
  }
  await page.goto("/sign-in");
  await page.getByLabel("Email").fill(who.email);
  await page.getByLabel("Password").fill(who.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/sign-in/);
  sessions.set(who.email, await page.context().cookies());
}

/** Creates an invite as the signed-in admin and returns the link. */
export async function invite(
  page: Page,
  email: string,
  role: "Member" | "Reviewer" | "Admin",
): Promise<string> {
  await page.goto("/settings/users");
  await page.getByRole("button", { name: "Invite user" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Email").fill(email);
  await dialog.getByLabel("Role").selectOption({ label: role });
  await dialog.getByRole("button", { name: "Create invite" }).click();
  const link = await dialog.getByRole("textbox", { name: "Invite link" }).inputValue();
  await dialog.getByRole("button", { name: "Done" }).click();
  return link;
}

export async function acceptInvite(page: Page, link: string, who: { name: string; password: string }) {
  await page.goto(link);
  await page.getByLabel("Your name").fill(who.name);
  await page.getByLabel("Password").fill(who.password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("heading", { name: "Ask", level: 1 })).toBeVisible();
}

/** The fixture server as the Assay container reaches it. */
export const fixturesURL = process.env.E2E_FIXTURES_URL ?? "http://127.0.0.1:18090";
export const sourceToken = "acme-token";

/** Calls the API from the app page, with the session cookie and the origin of the app. */
export async function call(page: Page, method: string, path: string, body?: unknown) {
  return page.evaluate(
    async ({ method, path, body }) => {
      const r = await fetch(path, {
        method,
        headers: { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await r.text();
      return { status: r.status, json: text ? JSON.parse(text) : undefined };
    },
    { method, path, body },
  );
}

/** Turns the answer function on with the fake model API of the fixture server. */
export async function fakeAnswerModel(page: Page) {
  await fakeModel(page, "answer");
}

/** Turns a model function on with the fake model API of the fixture server. */
export async function fakeModel(page: Page, fn: "answer" | "compile") {
  const list = await call(page, "GET", "/api/v1/models/endpoints");
  let id = list.json.endpoints.find((e: { name: string }) => e.name === "fake")?.id;
  if (!id) {
    const r = await call(page, "POST", "/api/v1/models/endpoints", {
      name: "fake",
      kind: "openai",
      baseUrl: `${fixturesURL}/v1`,
      apiKey: "fake-key",
      extra: {},
    });
    expect(r.status).toBe(201);
    id = r.json.id;
  }
  const f = await call(page, "PUT", `/api/v1/models/functions/${fn}`, {
    endpointId: id,
    model: "fake-chat",
    params: {},
    priceInPerMTok: 0,
    priceOutPerMTok: 0,
    monthlyBudgetUsd: 5,
    enabled: true,
  });
  expect(f.status).toBe(204);
}

/** Creates a domain as the signed-in admin. */
export async function createDomain(page: Page, name: string) {
  await page.goto("/settings/domains");
  await page.getByRole("button", { name: "Create a domain" }).first().click();
  await page.getByRole("dialog").getByLabel("Name").fill(name);
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("cell", { name })).toBeVisible();
}

type ConnectionSpec = {
  name: string;
  kind: string;
  path: string;
  scope: Record<string, unknown>;
  share?: boolean;
};

/** The ID of a connection by name, or undefined. */
export async function connectionId(page: Page, name: string): Promise<number | undefined> {
  const list = await call(page, "GET", "/api/v1/connections");
  return list.json.connections.find((c: { name: string }) => c.name === name)?.id;
}

/** Starts a sync and waits until one more run has succeeded. */
export async function syncNow(page: Page, id: number) {
  const done = async () => {
    const r = await call(page, "GET", `/api/v1/connections/${id}/runs`);
    return r.json.runs.filter((run: { status: string }) => run.status === "succeeded").length;
  };
  const before = await done();
  await call(page, "POST", `/api/v1/connections/${id}/sync`);
  await expect.poll(done, { timeout: 30_000 }).toBeGreaterThan(before);
}

/** Creates a connection once, as the signed-in admin, and waits for its first sync. */
async function connection(page: Page, c: ConnectionSpec): Promise<number> {
  let id = await connectionId(page, c.name);
  if (!id) {
    const r = await call(page, "POST", "/api/v1/connections", {
      name: c.name,
      kind: c.kind,
      baseUrl: `${fixturesURL}${c.path}`,
      token: sourceToken,
      scope: c.scope,
      syncMinutes: 30,
      reconcileHours: 24,
      rateConcurrency: 4,
      ratePerSecond: 50,
    });
    expect(r.status).toBe(201);
    id = r.json.id as number;
    if (c.share) {
      const groups = await call(page, "GET", "/api/v1/groups");
      const everyone = groups.json.groups.find((g: { builtin: boolean }) => g.builtin).id;
      const v = await call(page, "PUT", `/api/v1/connections/${id}/visibility`, {
        groupIds: [everyone],
        confirm: true,
      });
      expect(v.status).toBe(204);
    }
    await syncNow(page, id);
  }
  return id;
}

/** Creates the Jira connection once and waits for its first sync. */
export async function jira(page: Page) {
  return connection(page, {
    name: "acme-jira",
    kind: "jira",
    path: "/jira",
    scope: { projects: ["PAY", "REM"] },
  });
}

/** Creates the GitHub connection with the catalog files once, shared with Everyone. */
export async function github(page: Page) {
  return connection(page, {
    name: "acme-github",
    kind: "github",
    path: "/github",
    scope: { repositories: ["acme/payments-api", "acme/reminder-service"] },
    share: true,
  });
}

/** Creates the restricted Confluence space once, readable by the owner only. */
export async function restricted(page: Page) {
  return connection(page, {
    name: "acme-restricted",
    kind: "confluence",
    path: "/confluence",
    scope: { spaces: ["RST"] },
  });
}

/** The fixture server as the test runner reaches it, for the control routes. */
const fixturesControl = process.env.E2E_FIXTURES_CONTROL ?? "http://127.0.0.1:18090";

/** Appends storage-format text to a fixture Confluence page. */
export async function appendToPage(id: string, html: string) {
  const r = await fetch(`${fixturesControl}/control/pages/${id}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ append: html }),
  });
  expect(r.status).toBe(204);
}

/** The HTTP methods that the fixture sources received. */
export async function fixtureMethods(): Promise<string[]> {
  const r = await fetch(`${fixturesControl}/control/hits`);
  return ((await r.json()) as { method: string }[]).map((h) => h.method);
}

/**
 * Runs SQL in the database of the e2e project. Tests use it to seed review
 * cards whose real source is a model decision.
 */
export function sql(text: string): string {
  const container = process.env.E2E_DB_CONTAINER ?? "assay-e2e-db-1";
  return execFileSync(
    "docker",
    ["exec", "-i", container, "psql", "-U", "assay", "-d", "assay", "-v", "ON_ERROR_STOP=1", "-qAt"],
    {
      input: text,
      encoding: "utf8",
    },
  ).trim();
}
