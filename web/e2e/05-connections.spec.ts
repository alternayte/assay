import { expect, type Page, test } from "@playwright/test";
import { fixturesURL, owner, signIn, sourceToken } from "./env";

async function createDomain(page: Page, name: string) {
  await page.goto("/settings/domains");
  await page.getByRole("button", { name: "Create a domain" }).first().click();
  await page.getByRole("dialog").getByLabel("Name").fill(name);
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("cell", { name })).toBeVisible();
}

test("SCN-CON-001 the wizard tests Confluence, lists spaces, sets a domain and saves owner-only", async ({
  page,
}) => {
  await signIn(page, owner);
  await createDomain(page, "Engineering");
  await page.goto("/settings/connections");
  await page.getByRole("link", { name: "Add a connection" }).first().click();

  await page.getByLabel("Source type").selectOption("confluence");
  await page.getByLabel("Name").fill("acme-public");
  await page.getByLabel("Base URL").fill(`${fixturesURL}/confluence`);
  await page.getByLabel("Personal access token").fill(sourceToken);
  await page.getByRole("button", { name: "Test the connection" }).click();
  await expect(page.getByText("Connected as Assay Reader.")).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();

  await expect(page.getByRole("checkbox", { name: "RST Acme Restricted" })).toBeVisible();
  await page.getByRole("checkbox", { name: "ENG Acme Engineering" }).check();
  await page.getByRole("button", { name: "Next" }).click();

  await page.getByLabel("Default domain").selectOption({ label: "Engineering" });
  await expect(page.getByText("Only you can read the content of the new connection.")).toBeVisible();
  await page.getByRole("button", { name: "Save the connection" }).click();

  await expect(page.getByRole("heading", { name: "acme-public", level: 2 })).toBeVisible();
  await expect(page.getByLabel("Default domain")).toHaveValue(/\d+/);
  await page.getByRole("link", { name: "All connections" }).click();
  const row = page.getByRole("row", { name: /acme-public/ });
  await expect(row).toContainText("Owner only");
});

test("SCN-SYN-003 sync now and reconcile now add two runs and update the permissions check", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/settings/connections");
  await page.getByRole("link", { name: "acme-public" }).click();
  const runs = page.getByTestId("run-row");
  const manual = runs.filter({ hasText: "Manual" });
  // The schedule can queue runs of its own. Wait until none is open.
  const idle = async () =>
    expect(runs.filter({ hasText: /Queued|Running/ })).toHaveCount(0, { timeout: 30_000 });
  await idle();
  const before = await manual.count();

  await page.getByRole("button", { name: "Sync now" }).click();
  await expect(page.getByText("The run is in the queue.")).toBeVisible();
  await idle();
  const sync = manual.filter({ hasText: "Sync" }).first();
  await expect(sync).toContainText("Succeeded");
  await expect(sync).toContainText(/\d+ new, \d+ changed/);

  await page.getByRole("button", { name: "Reconcile now" }).click();
  await expect(page.getByText("The run is in the queue.")).toBeVisible();
  await idle();
  await expect(manual).toHaveCount(before + 2);
  const reconcile = manual.filter({ hasText: "Reconciliation" }).first();
  await expect(reconcile).toContainText("Succeeded");
  await expect(reconcile).toContainText(/\d+ deleted/);
  await expect(page.getByTestId("permissions-checked")).toHaveText(/Permissions checked .*(now|second)/, {
    timeout: 10_000,
  });
});

test("SCN-VIS-003 adding a group needs the confirmation and writes an audit entry", async ({ page }) => {
  await signIn(page, owner);
  await page.goto("/settings/connections");
  await expect(page.getByRole("row", { name: /Registry/ })).toContainText("Everyone");
  await page.getByRole("link", { name: "acme-public" }).click();

  await page.getByRole("checkbox", { name: "Everyone" }).check();
  const confirm = page.getByRole("checkbox", {
    name: "All members of these groups can read this scope in the source system.",
  });
  await expect(confirm).toBeVisible();
  await page.getByRole("button", { name: "Save visibility" }).click();
  await expect(page.getByText("Confirm the sentence before you add a group.")).toBeVisible();

  await confirm.check();
  await page.getByRole("button", { name: "Save visibility" }).click();
  await expect(page.getByText("Visibility is saved.")).toBeVisible();
  await page.getByRole("link", { name: "All connections" }).click();
  await expect(page.getByRole("row", { name: /acme-public/ })).toContainText("Everyone");

  await page.goto("/settings/audit");
  await expect(page.getByRole("cell", { name: "connections.visibility" }).first()).toBeVisible();
});
