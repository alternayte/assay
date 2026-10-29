import { expect, test } from "@playwright/test";
import { owner, setupToken } from "./env";

test("SCN-USR-001 setup creates the first admin once", async ({ page, request }) => {
  await page.goto("/setup");
  await page.getByLabel("Setup token").fill("wrong-token");
  await page.getByLabel("Your name").fill(owner.name);
  await page.getByLabel("Email").fill(owner.email);
  await page.getByLabel("Password").fill(owner.password);
  await page.getByRole("button", { name: "Create admin account" }).click();
  await expect(page.getByRole("alert")).toContainText("The setup token is not correct.");

  await page.getByLabel("Setup token").fill(setupToken);
  await page.getByRole("button", { name: "Create admin account" }).click();
  await expect(page.getByRole("heading", { name: "Ask", level: 1 })).toBeVisible();
  await page.goto("/settings/users");
  await expect(page.getByRole("cell", { name: /Olive Owner/ })).toBeVisible();

  const res = await request.get("/setup");
  expect(res.status()).toBe(404);
  const api = await request.get("/api/v1/setup");
  expect(api.status()).toBe(404);
});
