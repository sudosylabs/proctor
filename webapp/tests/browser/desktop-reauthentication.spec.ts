import { expect, test, type Page } from "@playwright/test";

const canonicalOrigin = "http://127.0.0.1:5173";
const entry = "/reauthenticate/desktop?request=desktop-handle&state=desktop-state#proof=private-browser-proof";

async function mockDesktopReauthentication(page: Page) {
  await page.route("**/api/v1/discovery", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        discovery_version: 1, password_minimum_length: 14, canonical_origin: canonicalOrigin, initialized: true,
        capabilities: { local_login: true, public_registration: false, invitation_admission: false, desktop_authorization: true },
        desktop_authorization: { protocol: "proctor-desktop-authorization", minimum_version: 1, maximum_version: 1 },
        institution: { id: "institution-1", name: "northbridge", display_name: "Northbridge University" },
        providers: [],
      }),
    });
  });
  await page.route("**/api/v1/auth/desktop/reauthentications/bind", async (route) => {
    expect(await route.request().postDataJSON()).toEqual({
      handle: "desktop-handle", browser_proof: "private-browser-proof", state: "desktop-state",
    });
    await route.fulfill({ status: 204 });
  });
  await page.route("**/api/v1/auth/desktop/reauthentications/context", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        state: "bound", display_name: "Ada Candidate", authentication_method: "password",
        expires_at: new Date(Date.now() + 300_000).toISOString(),
      }),
    });
  });
}

test("Desktop reauthentication confirms the password and returns to Desktop", async ({ page }) => {
  const redirect = `http://127.0.0.1:55000/${"A".repeat(43)}?code=${"B".repeat(43)}&state=desktop-state`;
  let handoffs = 0;
  // A 204 keeps this document so the confirmed state stays observable.
  await page.route(redirect, async (route) => {
    handoffs += 1;
    await route.fulfill({ status: 204 });
  });
  await mockDesktopReauthentication(page);
  const passwords: unknown[] = [];
  await page.route("**/api/v1/auth/desktop/reauthentications/authenticate/password", async (route) => {
    passwords.push(await route.request().postDataJSON());
    if (passwords.length === 1) {
      await route.fulfill({
        status: 401, contentType: "application/problem+json",
        body: JSON.stringify({ type: "about:blank", title: "Unauthorized", status: 401, code: "authentication.credentials_invalid" }),
      });
      return;
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        state: "authenticated", display_name: "Ada Candidate", authentication_method: "password",
        expires_at: new Date(Date.now() + 300_000).toISOString(),
      }),
    });
  });
  await page.route("**/api/v1/auth/desktop/reauthentications/approve", async (route) => {
    expect(await route.request().postDataJSON()).toEqual({ state: "desktop-state" });
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ redirect_url: redirect, expires_at: new Date(Date.now() + 60_000).toISOString() }),
    });
  });

  await page.goto(entry);

  await expect(page.getByRole("heading", { level: 1, name: "Confirm it’s you" })).toBeVisible();
  await expect(page.getByRole("main")).toContainText("Enter the password for Ada Candidate to continue in Proctor Desktop.");
  // The one-use handle and fragment proof leave browser history at once.
  expect(page.url()).toBe(`${canonicalOrigin}/reauthenticate/desktop?state=desktop-state`);
  // No other way to prove the account is offered for a password Session.
  await expect(page.getByRole("button", { name: /Continue with/ })).toHaveCount(0);

  await page.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(page.getByText("Enter your password.")).toBeVisible();

  const password = page.getByRole("textbox", { name: "Password" });
  await password.fill("wrong-synthetic-password");
  await page.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(page.getByRole("main")).toContainText("That password isn’t right. Try again.");
  expect(handoffs).toBe(0);

  await password.fill("synthetic-local-password");
  await page.getByRole("button", { name: "Confirm", exact: true }).click();

  const heading = page.getByRole("heading", { level: 1, name: "You’re confirmed" });
  await expect(heading).toBeFocused();
  await expect(page.getByRole("main")).toContainText("Go back to Proctor Desktop to continue. You can close this tab.");
  expect(passwords).toEqual([{ password: "wrong-synthetic-password" }, { password: "synthetic-local-password" }]);
  expect(handoffs).toBe(1);
});
