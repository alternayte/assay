import { expect, type Page, test } from "@playwright/test";
import { call, fakeAnswerModel, github, member, owner, restricted, signIn, sql } from "./env";

const base = process.env.E2E_BASE_URL ?? "http://localhost:18080";
const paymentsAPI = "backstage:component:default/payments-api";
const canary = /canary-7q4z/i;

/** Calls MCP tools/list with a bearer token from the test runner. */
async function mcpStatus(token: string): Promise<number> {
  const r = await fetch(`${base}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
    },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }),
  });
  return r.status;
}

test("SCN-AGT-003 a token shows once, last used updates, and revoke makes MCP return 401", async ({
  page,
}) => {
  await signIn(page, member);
  await page.goto("/tokens");
  await page.getByRole("button", { name: "Create a token" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("Laptop agent");
  await dialog.getByRole("button", { name: "Create" }).click();
  const shown = page.getByTestId("token-value");
  await expect(shown).toContainText("Assay does not show it again.");
  const value = (await shown.locator("code").textContent()) ?? "";
  expect(value).toMatch(/^assay_/);

  const row = page.getByTestId("token").filter({ hasText: "Laptop agent" });
  await expect(row.getByTestId("last-used")).toHaveText("Never");
  expect(await mcpStatus(value)).toBe(200);

  // After a reload the value is gone and the list shows the last use.
  await page.reload();
  await expect(page.getByTestId("token-value")).toHaveCount(0);
  await expect(page.getByText(value)).toHaveCount(0);
  await expect(row.getByTestId("last-used")).not.toHaveText("Never");

  await row.getByRole("button", { name: "Revoke Laptop agent" }).click();
  await expect(page.getByText("The token is revoked.")).toBeVisible();
  await expect(row).toContainText("Revoked");
  expect(await mcpStatus(value)).toBe(401);
});

test("SCN-AGT-005 the snippet has the instance MCP URL and Copy works; glossary.md lists only visible approved terms", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await signIn(page, member);
  await page.goto("/connect");
  const config = page.getByTestId("mcp-config");
  await expect(config).toContainText(`"url": "${base}/mcp"`);
  await page.getByRole("button", { name: "Copy" }).click();
  await expect(page.getByRole("button", { name: "Copied." })).toBeVisible();
  const copied = await page.evaluate(() => navigator.clipboard.readText());
  expect(JSON.parse(copied).mcpServers.assay.url).toBe(`${base}/mcp`);

  const md = await page.evaluate(async () => (await fetch("/glossary.md")).text());
  const terms = await call(page, "GET", "/api/v1/terms");
  const approved = (terms.json.terms as { name: string; status: string }[])
    .filter((x) => x.status === "approved")
    .map((x) => x.name)
    .sort();
  const listed = [...md.matchAll(/^## \[([^\]]+)\]/gm)].map((m) => m[1]).sort();
  expect(listed.length).toBeGreaterThan(0);
  expect(listed).toEqual(approved);
  expect(md).not.toMatch(canary);
});

/** Fails when the visible text or the HTML of the page holds the canary. */
async function clean(page: Page, where: string) {
  const html = await page.content();
  expect(html, where).not.toMatch(canary);
}

test("SCN-VIS-012 the member visits every route, opens every drawer and popover, and sees no canary", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await signIn(page, owner);
  await page.goto("/");
  await github(page);
  await restricted(page);
  await fakeAnswerModel(page);
  // A restricted claim and alias on a public entity.
  sql(`
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name)
SELECT e.id, k.id, 'CANARY-7Q4Z gateway', 'canary 7q4z gateway', true FROM entities e, connections k
WHERE e.key = '${paymentsAPI}' AND k.name = 'acme-restricted' ON CONFLICT DO NOTHING;
WITH c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
           SELECT id, 'stated', 'CANARY-7Q4Z exposed test card numbers', 'canary 7q4z exposed test card numbers'
           FROM entities WHERE key = '${paymentsAPI}' ON CONFLICT DO NOTHING RETURNING id),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
           JOIN connections k ON k.id = i.connection_id
           WHERE k.name = 'acme-restricted' AND p.norm LIKE '%CANARY%' ORDER BY p.id LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM c, p;`);
  // The owner sees it, so the walk reads real data.
  const ownerView = await call(page, "GET", `/api/v1/entities/${encodeURIComponent(paymentsAPI)}`);
  expect(JSON.stringify(ownerView.json)).toMatch(canary);

  await signIn(page, member);
  const routes = [
    "/",
    "/explore",
    "/glossary",
    "/updates",
    "/profile",
    "/tokens",
    "/connect",
    `/e/${encodeURIComponent(paymentsAPI)}`,
  ];
  for (const r of routes) {
    await page.goto(r);
    await page.waitForLoadState("networkidle");
    await clean(page, r);
  }

  // Every source drawer and term popover of the page.
  await page.goto(`/e/${encodeURIComponent(paymentsAPI)}`);
  await page.waitForLoadState("networkidle");
  const sources = page.getByRole("button", { name: /^Sources: / });
  for (let i = 0; i < (await sources.count()); i++) {
    await sources.nth(i).click();
    await page.getByRole("dialog").waitFor();
    await clean(page, `page drawer ${i}`);
    await page.keyboard.press("Escape");
  }
  const terms = page.getByRole("button", { name: /^Term / });
  for (let i = 0; i < (await terms.count()); i++) {
    await terms.nth(i).click();
    await clean(page, `term popover ${i}`);
    await page.keyboard.press("Escape");
  }

  // Search, the palette and Explore with the canary words.
  await page.goto("/explore");
  await page.getByLabel("Search names").fill("canary");
  await page.waitForLoadState("networkidle");
  await clean(page, "explore search");
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByRole("dialog").getByRole("combobox").fill("7Q4Z gateway");
  await page.waitForLoadState("networkidle");
  await clean(page, "palette");
  await page.keyboard.press("Escape");

  // An answer with every citation drawer.
  await page.goto("/");
  await page
    .getByRole("searchbox", { name: "Question" })
    .fill("What did the debug log of the Payments API expose?");
  await page.getByRole("button", { name: "Ask", exact: true }).click();
  await expect(page.getByRole("button", { name: /^Open (source|claim) / }).first()).toBeVisible({
    timeout: 15_000,
  });
  await clean(page, "answer");
  const cites = page.getByRole("button", { name: /^Open (source|claim) / });
  for (let i = 0; i < (await cites.count()); i++) {
    await cites.nth(i).click();
    await page.getByRole("dialog").waitFor();
    await page.waitForLoadState("networkidle");
    await clean(page, `answer drawer ${i}`);
    await page.keyboard.press("Escape");
  }
});
