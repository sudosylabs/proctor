import { expect, type Locator, type Page } from "@playwright/test";

import { serverURL } from "./environment";

// submitCredentials fills the hosted local-login form and submits it once.
export async function submitCredentials(page: Page, loginID: string, password: string): Promise<void> {
  await page.goto("/login");
  await page.getByLabel("Email or username").fill(loginID);
  await page.locator("#password").fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

// expectCredentialsRejected checks the form-level failure for a wrong email or
// password: either value may be wrong, so neither field is marked invalid.
export async function expectCredentialsRejected(page: Page): Promise<void> {
  await expect(page.locator("[data-proctor-form-feedback]")).toHaveText(
    "That email or password isn’t right. Check both and try again.",
  );
  await expect(page.locator("#password")).not.toHaveAttribute("aria-invalid", "true");
  await expect(page.getByLabel("Email or username")).not.toHaveAttribute("aria-invalid", "true");
}

export async function expectSignedIn(page: Page): Promise<void> {
  await expect(page).toHaveURL(`${serverURL}/authorization/complete`);
  await expect(page.getByRole("heading", { name: "You’re signed in" })).toBeVisible();
}

// currentUser reads the authoritative User for the browser's cookie Session.
export async function currentUser(page: Page): Promise<{ status: number; username?: string }> {
  const response = await page.request.get("/api/v1/users/me");
  if (!response.ok()) return { status: response.status() };
  return { status: response.status(), username: ((await response.json()) as { username: string }).username };
}

export async function signOut(page: Page): Promise<void> {
  await page.goto("/account/security");
  await expect(page.getByRole("heading", { name: "Account security", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(`${serverURL}/login`);
  await expect(page.getByLabel("Email or username")).toBeVisible();
}

// enterCode fills a one-time-code field and retries until the value sticks:
// the OTP control can remount while a page transition settles.
export async function enterCode(field: Locator, code: string): Promise<void> {
  await expect(field).toBeEditable();
  await expect(async () => {
    await field.fill(code);
    await expect(field).toHaveValue(code, { timeout: 1_000 });
  }).toPass();
}
