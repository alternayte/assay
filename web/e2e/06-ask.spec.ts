import { expect, test } from "@playwright/test";
import { fakeAnswerModel, jira, owner, signIn } from "./env";

test("SCN-ASK-006 a citation opens the drawer, how do we know lists claims and sources, history shows the question", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await fakeAnswerModel(page);
  await jira(page);

  const question = "What is the status of PAY-1?";
  await page.getByRole("searchbox", { name: "Question" }).fill(question);
  await page.getByRole("button", { name: "Ask", exact: true }).click();

  const source = page.getByRole("button", { name: "Open source S1" });
  await expect(source).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("button", { name: "Open claim C1" })).toBeVisible();

  await source.click();
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByRole("link", { name: "Open in source" })).toHaveAttribute(
    "rel",
    "noopener noreferrer",
  );
  await expect(drawer.locator("[data-cited]").first()).toBeVisible();
  await drawer.getByRole("button", { name: "Close" }).click();

  await page.getByText("How do we know?").click();
  const how = page.locator("details");
  await expect(how.getByRole("heading", { name: "Claims" })).toBeVisible();
  await expect(how.getByRole("heading", { name: "Sources" })).toBeVisible();
  await expect(how.getByRole("button", { name: "C1" })).toBeVisible();
  await expect(how.getByRole("button", { name: "S1" })).toBeVisible();

  const history = page.getByRole("region", { name: "Your questions" });
  await expect(history.getByRole("link", { name: question }).first()).toBeVisible();
  await expect(page).toHaveURL(/\/questions\/\d+/);
});
