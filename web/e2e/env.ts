import { expect, type Page } from "@playwright/test";

export const setupToken = process.env.E2E_SETUP_TOKEN ?? "e2e-setup-token";
export const owner = { email: "owner@acme.test", name: "Olive Owner", password: "owner-password-1" };
export const reviewer = {
  email: "reviewer@acme.test",
  name: "Rae Reviewer",
  password: "reviewer-password-1",
};
export const member = { email: "member@acme.test", name: "Mel Member", password: "member-password-1" };

export async function signIn(page: Page, who: { email: string; password: string }) {
  await page.goto("/sign-in");
  await page.getByLabel("Email").fill(who.email);
  await page.getByLabel("Password").fill(who.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/sign-in/);
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
  await expect(page).toHaveURL(/\/profile|\/settings/);
}
