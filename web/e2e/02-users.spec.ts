import { expect, test } from "@playwright/test";
import { acceptInvite, invite, owner, reviewer, signIn } from "./env";

test("SCN-USR-003 an invited reviewer sets a password from the link", async ({ page, browser }) => {
  await signIn(page, owner);
  const link = await invite(page, reviewer.email, "Reviewer");

  const ctx = await browser.newContext();
  const guest = await ctx.newPage();
  await acceptInvite(guest, link, reviewer);
  await guest.goto("/profile");
  await expect(guest.getByText("reviewer", { exact: true })).toBeVisible();
  await ctx.close();

  // The link works only once.
  const again = await browser.newContext();
  const second = await again.newPage();
  await second.goto(link);
  await expect(second.getByRole("heading", { name: "This invite link does not work" })).toBeVisible();
  await again.close();

  await page.goto("/settings/users");
  const row = page.getByRole("row", { name: new RegExp(reviewer.email) });
  await expect(row.getByRole("combobox")).toHaveValue("reviewer");
});
