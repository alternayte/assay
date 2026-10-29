import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { owner, signIn } from "./env";

const routes = [
  "/profile",
  "/settings/users",
  "/settings/groups",
  "/settings/general",
  "/settings/email",
  "/settings/audit",
];

async function setTheme(page: Page, theme: "light" | "dark") {
  await page.emulateMedia({ colorScheme: theme });
  await page.evaluate(() => localStorage.removeItem("assay.theme"));
}

test("SCN-UI-002 main routes fit 360 px in both themes, with skeletons and empty states", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.setViewportSize({ width: 360, height: 780 });
  for (const theme of ["light", "dark"] as const) {
    await setTheme(page, theme);
    for (const route of [...routes, "/sign-in"]) {
      await page.goto(route);
      await page.waitForLoadState("networkidle");
      expect(await page.evaluate(() => document.documentElement.dataset.theme)).toBe(theme);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
      );
      expect(overflow, `${route} in ${theme} scrolls sideways`).toBeLessThanOrEqual(0);
    }
  }

  // A slow response shows a skeleton.
  await page.route("**/api/v1/users", async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.continue();
  });
  await page.goto("/settings/users");
  await expect(page.getByRole("status", { name: "Loading." }).first()).toBeVisible();
  await page.unroute("**/api/v1/users");

  // An empty list names the next action.
  await page.goto("/settings/audit");
  await page.getByLabel("Action").fill("no.such.action");
  await page.getByRole("button", { name: "Filter" }).click();
  await expect(page.getByText("No entries match this filter.")).toBeVisible();
  await page.goto("/settings/users");
  for (const r of await page.getByRole("button", { name: "Revoke" }).all()) await r.click();
  await expect(page.getByText("No open invites.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Invite user" })).toHaveCount(2);
});

test("SCN-UI-003 main routes pass axe in both themes; badges have text; reduced motion stops transitions", async ({
  page,
}) => {
  await signIn(page, owner);
  for (const theme of ["light", "dark"] as const) {
    await setTheme(page, theme);
    for (const route of [...routes, "/sign-in"]) {
      await page.goto(route);
      await page.waitForLoadState("networkidle");
      const result = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
        .analyze();
      const bad = result.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
      expect(bad.map((v) => `${route} ${theme}: ${v.id} ${v.nodes.map((n) => n.target).join(", ")}`)).toEqual(
        [],
      );
    }
  }

  await page.goto("/settings/users");
  for (const badge of await page
    .locator("span.font-mono.uppercase, span[class*='uppercase'][class*='border']")
    .all()) {
    expect((await badge.textContent()).trim().length).toBeGreaterThan(0);
  }

  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/settings/users");
  const duration = await page
    .getByRole("button", { name: "Invite user" })
    .first()
    .evaluate((el) => getComputedStyle(el).transitionDuration);
  expect(duration.split(",").every((d) => Number.parseFloat(d) === 0)).toBe(true);
});
