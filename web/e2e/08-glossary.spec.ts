import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { call, createDomain, fakeAnswerModel, owner, signIn } from "./env";

test("SCN-VOC-004 the glossary searches, filters, creates and edits terms, and a CSV export then import gives the same terms", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Glossary" }).click();
  await expect(page.getByRole("heading", { name: "Glossary", level: 1 })).toBeVisible();

  // Create an approved term with a synonym and a domain.
  await page.getByRole("button", { name: "New term" }).first().click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("Manuscript");
  await dialog.getByLabel("Synonyms").fill("Submission");
  await dialog.getByLabel("Definition").fill("A paper that an author sends to a journal for review.");
  await dialog.getByLabel("Domain").selectOption({ label: "Engineering" });
  await dialog.getByLabel("Status").selectOption({ label: "Approved" });
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: "Manuscript", level: 1 })).toBeVisible();
  await expect(
    page.getByText("A paper that an author sends to a journal for review.", { exact: true }),
  ).toBeVisible();

  // Edit it: a new version.
  await page.getByRole("button", { name: "Edit term" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Definition").fill("A paper that an author sends to a journal for peer review.");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByText("A paper that an author sends to a journal for peer review.", { exact: true }),
  ).toBeVisible();
  const versions = page.getByRole("list", { name: "Versions" }).getByRole("listitem");
  await expect(versions).toHaveCount(2);

  // A deprecated term that prefers it.
  await page.goto("/glossary");
  await page.getByRole("button", { name: "New term" }).first().click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("Paper");
  await dialog.getByLabel("Definition").fill("An old word for a manuscript.");
  await dialog.getByLabel("Preferred term").selectOption("registry:term:manuscript");
  await dialog.getByLabel("Status").selectOption({ label: "Deprecated" });
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: "Paper", level: 1 })).toBeVisible();
  await page.getByRole("link", { name: "Manuscript", exact: true }).click();
  await expect(page.getByText("registry:term:manuscript")).toBeVisible();

  // The term page shows every vocabulary field.
  const fields = page.getByRole("region", { name: "Vocabulary" });
  for (const label of ["Synonyms", "Deprecated forms", "Preferred term", "Owner", "Domain", "Related"])
    await expect(fields.getByText(label, { exact: true })).toBeVisible();
  await expect(fields.getByText("Submission")).toBeVisible();
  await expect(fields.getByText("Paper")).toBeVisible();
  await expect(fields.getByText("Engineering")).toBeVisible();
  await expect(page.getByRole("region", { name: "Usage" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Sources" })).toBeVisible();
  await expect(page.getByRole("region", { name: "History" })).toBeVisible();

  // Search and filters.
  await page.goto("/glossary");
  const rows = page.getByTestId("term-row");
  await page.getByLabel("Search terms").fill("submission");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Manuscript");
  await page.getByLabel("Search terms").fill("");
  await page.getByLabel("Status").selectOption({ label: "Deprecated" });
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Paper");
  await page.getByLabel("Status").selectOption({ label: "All" });
  await page.getByLabel("Domain").selectOption({ label: "Engineering" });
  for (const cell of await rows.locator("td:nth-child(3)").allTextContents())
    expect(cell).toBe("Engineering");
  await page.getByLabel("Domain").selectOption({ label: "All" });

  // CSV export, then import of the same file: the same terms.
  const before = await call(page, "GET", "/api/v1/terms");
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("button", { name: "Export CSV" }).click(),
  ]);
  const path = await download.path();
  const csv = await readFile(path, "utf8");
  expect(csv.split("\n")[0]).toBe("key,name,synonyms,definition,domain,owner,preferred,related,status");
  await page
    .getByTestId("import-file")
    .setInputFiles({ name: "glossary.csv", mimeType: "text/csv", buffer: Buffer.from(csv) });
  await expect(page.getByTestId("import-summary")).toHaveText(
    "Created 0. Updated 0. Unchanged 2. Proposals 0.",
  );
  await page.getByRole("dialog").getByRole("button", { name: "Close" }).click();
  const after = await call(page, "GET", "/api/v1/terms");
  expect(after.json.terms).toEqual(before.json.terms);
});

test("SCN-REG-004 a record shows versions and a diff, and revert creates a new version", async ({ page }) => {
  await signIn(page, owner);
  await page.goto("/explore");
  await page.getByRole("button", { name: "New record" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("Type").selectOption({ label: "Service" });
  await dialog.getByLabel("Name").fill("Card Gateway");
  await dialog.getByLabel("Description").fill("The Card Gateway sends card payments.");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("heading", { name: "Card Gateway", level: 1 })).toBeVisible();

  await page.getByRole("button", { name: "Edit record" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Description").fill("The Card Gateway sends card payments to the card network.");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByText("The Card Gateway sends card payments to the card network.").first(),
  ).toBeVisible();

  const versions = page.getByRole("list", { name: "Versions" }).getByRole("listitem");
  await expect(versions).toHaveCount(2);
  const diff = page.getByRole("list", { name: "Change from the version before" });
  await expect(diff.locator('[data-op="-"]')).toContainText("The Card Gateway sends card payments.");
  await expect(diff.locator('[data-op="+"]')).toContainText("to the card network.");

  await versions.nth(1).getByRole("button").click();
  await page.getByRole("button", { name: "Revert to this version" }).click();
  await expect(versions).toHaveCount(3);
  await expect(
    page.getByRole("region", { name: "Fields" }).getByText("The Card Gateway sends card payments."),
  ).toBeVisible();
});

test("SCN-VOC-006 answers and passages show term popovers with definition, status and owner", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await fakeAnswerModel(page);
  await page.getByRole("searchbox", { name: "Question" }).fill("What is a manuscript in the glossary?");
  await page.getByRole("button", { name: "Ask", exact: true }).click();
  await expect(page.getByRole("button", { name: "Open source S1" })).toBeVisible({ timeout: 15_000 });

  // The answer links the approved term.
  await page
    .getByRole("region", { name: "Answer" })
    .getByRole("button", { name: "Term Manuscript" })
    .first()
    .click();
  let pop = page.getByRole("dialog").last();
  await expect(pop.getByText("A paper that an author sends to a journal for peer review.")).toBeVisible();
  await expect(pop.getByText("Approved")).toBeVisible();
  await expect(pop.getByRole("link", { name: "Open the term" })).toBeVisible();
  await page.keyboard.press("Escape");

  // A passage in the evidence drawer links it too.
  const drawer = page.getByRole("dialog");
  let found = false;
  for (const id of ["S1", "S2", "S3"]) {
    const open = page.getByRole("button", { name: `Open source ${id}` });
    if ((await open.count()) === 0) break;
    await open.click();
    await drawer.locator("[data-cited]").first().waitFor();
    const term = drawer.getByRole("button", { name: "Term Manuscript" });
    if ((await term.count()) > 0) {
      await term.first().click();
      found = true;
      break;
    }
    await drawer.getByRole("button", { name: "Close" }).click();
  }
  expect(found).toBe(true);
  pop = page.getByRole("dialog").last();
  await expect(pop.getByText("A paper that an author sends to a journal for peer review.")).toBeVisible();
});

test("SCN-VOC-010 an admin creates a domain and sets it as a connection default", async ({ page }) => {
  await signIn(page, owner);
  await createDomain(page, "Journals");
  await page.goto("/settings/connections");
  await page.getByRole("link", { name: "acme-public" }).click();
  await page.getByLabel("Default domain").selectOption({ label: "Journals" });
  await page.getByRole("button", { name: "Save configuration" }).click();
  await expect(page.getByText("Configuration is saved.")).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("Default domain")).toHaveValue(/\d+/);
  const conns = await call(page, "GET", "/api/v1/connections");
  const dom = await call(page, "GET", "/api/v1/domains");
  const journals = dom.json.domains.find((d: { name: string }) => d.name === "Journals").id;
  expect(conns.json.connections.find((c: { name: string }) => c.name === "acme-public").defaultDomainId).toBe(
    journals,
  );
});
