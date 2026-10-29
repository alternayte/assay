import { expect, test } from "@playwright/test";
import { acceptInvite, invite, member, owner, reviewer, signIn } from "./env";

test("SCN-USR-008 an admin creates a group, adds two members and renames it", async ({ page, browser }) => {
  await signIn(page, owner);
  const link = await invite(page, member.email, "Member");
  const ctx = await browser.newContext();
  await acceptInvite(await ctx.newPage(), link, member);
  await ctx.close();

  await page.goto("/settings/groups");
  await page.getByRole("button", { name: "Create group" }).first().click();
  await page.getByRole("dialog").getByLabel("Name").fill("Platform");
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await page.getByRole("button", { name: /^Platform/ }).click();

  for (const who of [member, reviewer]) {
    await page.getByLabel("Add member").selectOption({ label: `${who.name} (${who.email})` });
    await page.getByRole("button", { name: "Add member" }).click();
    await expect(page.getByRole("cell", { name: new RegExp(who.email) })).toBeVisible();
  }
  await expect(page.getByRole("button", { name: /^Platform/ })).toContainText("2 members");

  await page.getByRole("button", { name: "Rename" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill("Platform team");
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("button", { name: /^Platform team/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Members: Platform team" })).toBeVisible();
});
