import { expect, type Page, test } from "@playwright/test";
import { call, fakeAnswerModel, fakeModel, jira, member, owner, signIn } from "./env";

/** The key of the Jira initiative PAY-1. */
async function initiative(page: Page): Promise<string> {
  await jira(page);
  const r = await call(page, "GET", "/api/v1/entities?q=Payment%20retries&type=initiative");
  const key = r.json.entities.find((e: { name: string }) => e.name === "Payment retries")?.key;
  expect(key).toBeTruthy();
  return key;
}

/** Creates a registry record once and returns its key. */
async function record(page: Page, kind: "thing" | "term", body: Record<string, unknown>): Promise<string> {
  const list = await call(page, "GET", "/api/v1/registry/records");
  const found = list.json.records.find((r: { name: string }) => r.name === body.name);
  if (found) return found.key;
  const r = await call(page, "POST", "/api/v1/registry/records", { kind, [kind]: body });
  expect(r.status).toBe(201);
  return r.json.key;
}

const pageURL = (key: string) => `/e/${encodeURIComponent(key)}`;

test("SCN-PAG-001 all page sections render, and the summary replaces the skeleton after the deterministic sections", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await fakeModel(page, "compile");
  const key = await initiative(page);

  // Hold the compile until the deterministic sections are on the screen.
  let release: () => void = () => {};
  const held = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/page/compile", async (route) => {
    await held;
    await route.continue();
  });
  await page.goto(pageURL(key));
  await expect(page.getByRole("heading", { name: "Payment retries", level: 1 })).toBeVisible();
  for (const name of ["Summary", "Key facts", "Conflicts", "Decisions", "Timeline", "Related", "Sources"]) {
    await expect(page.getByRole("region", { name })).toBeVisible();
  }
  for (const label of ["Owner", "Domain", "Newest evidence", "Permissions checked"]) {
    await expect(page.getByText(label, { exact: true })).toBeVisible();
  }
  await expect(page.getByTestId("fact").first()).toBeVisible();
  await expect(page.getByTestId("summary-skeleton")).toBeVisible();
  await expect(page.getByTestId("summary")).toHaveCount(0);

  release();
  await expect(page.getByTestId("summary")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("summary-skeleton")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open claim C1" })).toBeVisible();
  await expect(page.getByTestId("compiled-at")).toBeVisible();

  // A citation chip opens the evidence of the claim.
  await page.getByRole("button", { name: "Open claim C1" }).click();
  await expect(page.getByRole("dialog").getByText("Evidence")).toBeVisible();
  await page.keyboard.press("Escape");

  // Explore links to the page.
  await page.unroute("**/page/compile");
  await page.goto("/explore");
  await page.getByLabel("Search names").fill("Payment retries");
  await page.getByRole("link", { name: "Payment retries" }).click();
  await expect(page).toHaveURL(new RegExp(`/e/${encodeURIComponent(key)}$`));
});

test("SCN-PAG-006 regenerate shows a new compile time", async ({ page }) => {
  await signIn(page, owner);
  await page.goto("/");
  await fakeModel(page, "compile");
  const key = await initiative(page);
  await page.goto(pageURL(key));
  const at = page.getByTestId("compiled-at");
  await expect(at).toBeVisible({ timeout: 15_000 });
  const before = await at.getAttribute("data-at");
  await page.getByRole("button", { name: "Regenerate" }).click();
  await expect(at).not.toHaveAttribute("data-at", before ?? "", { timeout: 15_000 });
  await expect(page.getByTestId("summary")).toBeVisible();
});

test("SCN-VOC-006 pages show term popovers with definition, status and owner, and the member gets only terms from visible sources", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await fakeModel(page, "compile");
  const key = await initiative(page);
  const team = await record(page, "thing", { type: "team", name: "Planning Team" });
  const term = await record(page, "term", {
    name: "Target date",
    definition: "The day that the team plans to finish the work.",
    owner: team,
    status: "approved",
  });

  // The summary of the initiative names its target date.
  await page.goto(pageURL(key));
  const summary = page.getByTestId("summary");
  await expect(summary).toBeVisible({ timeout: 15_000 });
  await summary.getByRole("button", { name: "Term Target date" }).first().click();
  const pop = page.getByRole("dialog").last();
  await expect(pop.getByText("The day that the team plans to finish the work.")).toBeVisible();
  await expect(pop.getByText("Approved")).toBeVisible();
  await expect(pop.getByText("Owner: Planning Team")).toBeVisible();
  await expect(pop.getByRole("link", { name: "Open the term" })).toBeVisible();
  await page.keyboard.press("Escape");

  // The member reads the page of the term. Every linked term is a term
  // that the member can read, and the Jira initiative stays hidden.
  await signIn(page, member);
  await page.goto(pageURL(term));
  await expect(page.getByTestId("summary")).toBeVisible({ timeout: 15_000 });
  const glossary = await call(page, "GET", "/api/v1/terms");
  const visible = new Set(glossary.json.terms.map((x: { name: string }) => x.name));
  const links = page.getByTestId("summary").getByRole("button", { name: /^Term / });
  for (const label of await links.evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") ?? ""))) {
    for (const name of label.replace(/^Term /, "").split(", ")) expect(visible.has(name)).toBe(true);
  }
  await page.goto(pageURL(key));
  await expect(page.getByText("This page does not exist, or you cannot read its sources.")).toBeVisible();
});

test("SCN-UI-005 fixture script, onerror and javascript: content renders inert", async ({ page }) => {
  await signIn(page, owner);
  await page.goto("/");
  await fakeAnswerModel(page);
  await page.getByRole("searchbox", { name: "Question" }).fill("What does the rota tool show as plain text?");
  await page.getByRole("button", { name: "Ask", exact: true }).click();
  await expect(page.getByRole("button", { name: "Open source S1" })).toBeVisible({ timeout: 15_000 });

  const drawer = page.getByRole("dialog");
  let found = false;
  for (const id of ["S1", "S2", "S3", "S4"]) {
    const open = page.getByRole("button", { name: `Open source ${id}` });
    if ((await open.count()) === 0) break;
    await open.click();
    await drawer.locator("[data-cited]").first().waitFor();
    if ((await drawer.getByText("The rota tool shows <script>").count()) > 0) {
      found = true;
      break;
    }
    await drawer.getByRole("button", { name: "Close" }).click();
  }
  expect(found).toBe(true);
  await expect(drawer.getByText("Bad link")).toBeVisible();
  expect(await drawer.locator("script").count()).toBe(0);
  expect(await drawer.locator("[onerror]").count()).toBe(0);
  expect(await page.locator('a[href^="javascript:"]').count()).toBe(0);
  for (const rel of await drawer
    .locator('a[href^="http"]')
    .evaluateAll((els) => els.map((e) => e.getAttribute("rel")))) {
    expect(rel).toBe("noopener noreferrer");
  }
  expect(await page.evaluate(() => (window as unknown as { __unsafe?: number }).__unsafe)).toBeUndefined();
});
