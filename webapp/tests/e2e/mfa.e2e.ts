import { expect, test } from "@playwright/test";

import { cliSession, invitedAccount } from "./support/api";
import { currentUser, enterCode, expectSignedIn, signOut, submitCredentials } from "./support/browser";
import { currentTimeStep, nextTimeStep, totp } from "./support/totp";

test("MFA enrollment protects the next login with an authenticator challenge", async ({ page }) => {
  const account = await invitedAccount();
  await submitCredentials(page, account.username, account.password);
  await expectSignedIn(page);

  await page.goto("/account/security");
  await expect(page.getByText("Off", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Set up two-step verification", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Scan this code", level: 1 })).toBeFocused();
  await expect(page.getByRole("img", { name: "QR code for two-step verification setup" })).toBeVisible();
  const secret = ((await page.locator("#authenticator-secret").textContent()) ?? "").replace(/\s+/g, "");
  expect(secret).toMatch(/^[A-Z2-7]+=*$/);

  const enrolledStep = currentTimeStep();
  await enterCode(page.getByLabel("Authenticator code"), totp(secret, enrolledStep));
  await page.getByRole("button", { name: "Verify and turn on", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Save your recovery codes", level: 1 })).toBeFocused();
  await page.getByRole("checkbox", { name: "I’ve saved these codes somewhere safe" }).check();
  await page.getByRole("button", { name: "Done", exact: true }).click();
  await expect(page.getByText("On", { exact: true })).toBeVisible();

  // Authoritative state: the server reports an enabled factor for this User.
  const status = await (await page.request.get("/api/v1/users/me/mfa")).json();
  expect(status).toMatchObject({ enabled: true, pending: false });
  expect(status.recovery_codes_remaining).toBeGreaterThan(0);

  await signOut(page);
  await submitCredentials(page, account.username, account.password);
  await expect(page.getByRole("heading", { name: "Enter your code" })).toBeVisible();
  const code = page.getByLabel("Authenticator code");
  await expect(code).toBeFocused();
  expect(await currentUser(page)).toEqual({ status: 401 });

  // A wrong code keeps the challenge and creates no Session.
  const wrong = String((Number(totp(secret, nextTimeStep(enrolledStep))) + 1) % 1_000_000).padStart(6, "0");
  await enterCode(code, wrong);
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(code).toHaveAttribute("aria-invalid", "true");
  expect(await currentUser(page)).toEqual({ status: 401 });

  await enterCode(code, totp(secret, nextTimeStep(enrolledStep)));
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expectSignedIn(page);
  expect(await currentUser(page)).toEqual({ status: 200, username: account.username });

  // Password alone no longer authenticates any client.
  await expect(cliSession(account.username, account.password)).rejects.toThrow(/authentication\.mfa\.required/);
});
