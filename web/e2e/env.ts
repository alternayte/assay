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

/** Creates the Jira connection once and waits for its first sync. */
export async function jira(page: Page) {
  const list = await call(page, "GET", "/api/v1/connections");
  let id = list.json.connections.find((c: { name: string }) => c.name === "acme-jira")?.id;
  if (!id) {
    const r = await call(page, "POST", "/api/v1/connections", {
      name: "acme-jira",
      kind: "jira",
      baseUrl: `${fixturesURL}/jira`,
      token: sourceToken,
      scope: { projects: ["PAY", "REM"] },
      syncMinutes: 30,
      reconcileHours: 24,
      rateConcurrency: 4,
      ratePerSecond: 50,
    });
    expect(r.status).toBe(201);
    id = r.json.id;
    await call(page, "POST", `/api/v1/connections/${id}/sync`);
  }
  await expect
    .poll(
      async () => {
        const r = await call(page, "GET", `/api/v1/connections/${id}/runs`);
        return r.json.runs.some((run: { status: string }) => run.status === "succeeded");
      },
      { timeout: 30_000 },
    )
    .toBe(true);
}
