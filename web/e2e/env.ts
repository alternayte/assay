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
  await expect(page).toHaveURL(/\/profile|\/settings/);
}

/** The fixture server as the Assay container reaches it. */
export const fixturesURL = process.env.E2E_FIXTURES_URL ?? "http://127.0.0.1:18090";
export const sourceToken = "acme-token";
