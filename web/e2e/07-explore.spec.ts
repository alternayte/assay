import { expect, test } from "@playwright/test";
import { acceptInvite, invite, owner, signIn } from "./env";

test("SCN-ENT-004 explore filters by type, domain and status, searches, sorts and multi-selects", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/explore");
  const rows = page.getByTestId("entity-row");
  await expect(rows.first()).toBeVisible();

  // Newest change first.
  const changed = await rows.evaluateAll((els) =>
    els.map((e) => Date.parse(e.getAttribute("data-changed") ?? "")),
  );
  expect(changed.length).toBeGreaterThan(2);
  expect([...changed].sort((a, b) => b - a)).toEqual(changed);

  await page.getByLabel("Type").selectOption({ label: "Decision" });
  await expect(rows.first()).toBeVisible();
  for (const cell of await rows.locator("td:nth-child(3)").allTextContents()) expect(cell).toBe("Decision");

  await page.getByLabel("Type").selectOption({ label: "All" });
  await page.getByLabel("Domain").selectOption({ label: "Engineering" });
  await expect(rows.first()).toBeVisible();
  for (const cell of await rows.locator("td:nth-child(4)").allTextContents())
    expect(cell).toBe("Engineering");

  await page.getByLabel("Domain").selectOption({ label: "All" });
  await page.getByLabel("Status").selectOption({ label: "Proposed" });
  await expect(page.getByText("No entity matches.")).toBeVisible();
  await page.getByLabel("Status").selectOption({ label: "All" });

  await page.getByLabel("Search names").fill("PAY-1");
  await expect(rows.filter({ hasText: /:PAY-1$/ })).toHaveCount(1);
  await page.getByLabel("Search names").fill("");

  const boxes = rows.getByRole("checkbox");
  await boxes.nth(0).check();
  await boxes.nth(1).check();
  const bar = page.getByRole("toolbar", { name: "2 selected" });
  await expect(bar).toBeVisible();
  await bar.getByRole("button", { name: "Merge selected" }).click();
  await expect(page.getByRole("dialog", { name: "Merge 2 entities" })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await bar.getByRole("button", { name: "Clear the selection" }).click();
  await expect(bar).toBeHidden();
});

test("SCN-USR-002 after sign-out a visit to Explore goes to sign-in", async ({ page }) => {
  await signIn(page, owner);
  const link = await invite(page, "explorer@acme.test", "Member");
  await page.context().clearCookies();
  await acceptInvite(page, link, { name: "Eve Explorer", password: "explorer-password-1" });
  await page.goto("/explore");
  await expect(page.getByRole("heading", { name: "Explore", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/sign-in/);
  await page.goto("/explore");
  await expect(page).toHaveURL(/\/sign-in/);
  await expect(page.getByRole("heading", { name: "Sign in to Assay" })).toBeVisible();
});
