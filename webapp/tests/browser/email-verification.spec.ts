import { expect, test } from "@playwright/test";

const canonicalOrigin = "http://127.0.0.1:5173";
const verificationToken = "email-verification-secret";

test("email verification requires deliberate token consumption", { tag: "@cross-browser" }, async ({
  page,
}) => {
  let requests = 0;
  await page.route("**/api/v1/auth/email-verification/complete", async (route) => {
    requests += 1;
    expect(await route.request().postDataJSON()).toEqual({
      token: verificationToken,
    });
    await route.fulfill({ status: 204 });
  });

  await page.goto(
    `/account/verify-email#token=${encodeURIComponent(verificationToken)}`,
  );

  await expect(page).toHaveURL(`${canonicalOrigin}/account/verify-email`);
  await expect(page).toHaveTitle("Confirm email · Proctor");
  await expect(
    page.getByRole("heading", { level: 1, name: "Confirm your email address" }),
  ).toBeVisible();
  expect(requests).toBe(0);
  await expect(page.locator("body")).not.toContainText(verificationToken);
  await expect(
    page.getByText(
      "Confirm the address this link was sent to. The link works once, for 24 hours.",
    ),
  ).toBeVisible();
  await expect(page.getByText("Confirm email", { exact: true })).toHaveCSS(
    "color",
    "rgb(101, 95, 105)",
  );
  await expect(page.locator("[data-proctor-notice]")).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Back to sign in", exact: true }),
  ).toHaveAttribute("href", "/login");

  await page.getByRole("button", { name: "Confirm email address" }).click();

  const verifiedHeading = page.getByRole("heading", {
    level: 1,
    name: "Your email is confirmed",
  });
  await expect(verifiedHeading).toBeVisible();
  await expect(verifiedHeading).toBeFocused();
  expect(await page.evaluate(() => window.scrollY)).toBe(0);
  await expect(page.getByRole("status")).toHaveText("Your email is confirmed");
  await expect(page.getByText("You can now sign in to Proctor.")).toBeVisible();
  await expect(page.getByText("Confirm email", { exact: true })).toHaveCSS(
    "color",
    "rgb(11, 118, 110)",
  );
  await expect(
    page.getByRole("link", { name: "Sign in", exact: true }),
  ).toHaveAttribute("href", "/login");
  await expect(page.getByRole("button")).toHaveCount(0);
  expect(requests).toBe(1);
  await expect(page.locator("body")).not.toContainText(verificationToken);

  await page.reload();
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "This confirmation link can’t be used",
    }),
  ).toBeVisible();
  expect(requests).toBe(1);
});

test("email verification names the address from a read-only lookup", { tag: "@cross-browser" }, async ({
  page,
}) => {
  const inspections: unknown[] = [];
  let completions = 0;
  await page.route("**/api/v1/auth/email-verification/inspect", async (route) => {
    inspections.push(await route.request().postDataJSON());
    await route.fulfill({
      contentType: "application/json",
      headers: { "Cache-Control": "no-store" },
      body: JSON.stringify({ email: "ada.okafor@example.edu" }),
    });
  });
  await page.route("**/api/v1/auth/email-verification/complete", async (route) => {
    completions += 1;
    await route.fulfill({ status: 204 });
  });

  await page.goto(`/account/verify-email#token=${encodeURIComponent(verificationToken)}`);
  await expect(page.getByRole("main")).toContainText(
    "Confirm ada.okafor@example.edu as your account’s email address. The link works once, for 24 hours.",
  );
  await expect(page.getByText("ada.okafor@example.edu", { exact: true })).toHaveAttribute("translate", "no");
  expect(inspections).toEqual([{ token: verificationToken }]);
  expect(completions).toBe(0);
  await expect(page.getByRole("heading", { level: 1 })).not.toBeFocused();
  await page.getByRole("button", { name: "Confirm email address" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Your email is confirmed" })).toBeFocused();
  expect(completions).toBe(1);
  await expect(page.locator("body")).not.toContainText(verificationToken);
});

test("email-change undo describes the change and keeps its explicit action", { tag: "@cross-browser" }, async ({
  page,
}) => {
  const undoToken = "email-change-undo-secret";
  const inspections: unknown[] = [];
  const undos: unknown[] = [];
  await page.route("**/api/v1/auth/email-change/undo/inspect", async (route) => {
    inspections.push(await route.request().postDataJSON());
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        restored_email: "ada.okafor@example.edu",
        masked_current_email: "a***@example.org",
        changed_at: "2026-11-06T16:05:00Z",
      }),
    });
  });
  await page.route("**/api/v1/auth/email-change/undo", async (route) => {
    undos.push(await route.request().postDataJSON());
    await route.fulfill({ status: 204 });
  });

  await page.goto(`/account/undo-email-change#token=${undoToken}`);
  await expect(page).toHaveURL(`${canonicalOrigin}/account/undo-email-change`);
  const facts = page.getByRole("main").locator("[data-proctor-fact-list]");
  await expect(facts.getByRole("term")).toHaveText(["Previous address", "New address", "Changed"]);
  await expect(facts.getByRole("definition")).toHaveText([
    "ada.okafor@example.edu",
    "a***@example.org",
    await page.evaluate(() => new Intl.DateTimeFormat(undefined, {
      weekday: "long", day: "numeric", month: "long", year: "numeric",
      hour: "numeric", minute: "2-digit", timeZoneName: "short",
    }).format(new Date("2026-11-06T16:05:00Z"))),
  ]);
  await expect(facts.locator("time")).toHaveAttribute("datetime", "2026-11-06T16:05:00Z");
  expect(inspections).toEqual([{ token: undoToken }]);
  expect(undos).toHaveLength(0);

  await page.getByRole("button", { name: "Undo change" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Your previous email is back" })).toBeFocused();
  await expect(page.locator("[data-proctor-fact-list]")).toHaveCount(0);
  expect(undos).toEqual([{ token: undoToken }]);
  await expect(page.locator("body")).not.toContainText(undoToken);
});

test("a failed undo lookup leaves the explicit action available", async ({ page }) => {
  await page.route("**/api/v1/auth/email-change/undo/inspect", async (route) => {
    await route.fulfill({
      status: 400,
      contentType: "application/problem+json",
      body: JSON.stringify({ type: "about:blank", title: "Invalid", status: 400, code: "authentication.account_token.invalid" }),
    });
  });
  await page.goto("/account/undo-email-change#token=email-change-undo-secret");
  await expect(page.getByRole("heading", { level: 1, name: "Undo the email change?" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Undo change" })).toBeEnabled();
  await expect(page.locator("[data-proctor-fact-list]")).toHaveCount(0);
});

test("missing and malformed fragment credentials make no request", async ({
  page,
}) => {
  let requests = 0;
  await page.route("**/api/v1/auth/email-verification/complete", async (route) => {
    requests += 1;
    await route.fulfill({ status: 204 });
  });
  await page.route("**/api/v1/auth/email-verification/inspect", async (route) => {
    requests += 1;
    await route.fulfill({ status: 500 });
  });

  for (const destination of [
    "/account/verify-email#token=secret&other=value",
    "/account/verify-email",
  ]) {
    await page.goto(destination);
    await expect(page).toHaveURL(`${canonicalOrigin}/account/verify-email`);
    await expect(
      page.getByRole("heading", {
        level: 1,
        name: "This confirmation link can’t be used",
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Confirm email address" }),
    ).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText("secret");
    await expect(page.getByText("Confirm email", { exact: true })).toHaveCSS(
      "color",
      "rgb(180, 35, 60)",
    );
    await expect(
      page.getByRole("link", { name: "Sign in", exact: true }),
    ).toHaveAttribute("href", "/login");
  }
  expect(requests).toBe(0);
});

test("terminal token failures remain deliberately indistinguishable", { tag: "@cross-browser" }, async ({
  page,
}) => {
  await page.route("**/api/v1/auth/email-verification/complete", async (route) => {
    await route.fulfill({
      status: 400,
      contentType: "application/problem+json",
      body: JSON.stringify({
        type: "/problems/account-token-invalid",
        title: "Private server title",
        detail: "Private server detail",
        status: 400,
        code: "authentication.account_token.invalid",
      }),
    });
  });

  await page.goto(`/account/verify-email#token=${verificationToken}`);
  await page.getByRole("button", { name: "Confirm email address" }).click();

  const invalidHeading = page.getByRole("heading", {
    level: 1,
    name: "This confirmation link can’t be used",
  });
  await expect(invalidHeading).toBeFocused();
  expect(await page.evaluate(() => window.scrollY)).toBe(0);
  await expect(
    page.getByText(
      "Confirmation links work once, for 24 hours. Sign in to request a new one.",
    ),
  ).toBeVisible();
  await expect(page.locator("body")).not.toContainText("Private server");
  await expect(page.locator("body")).not.toContainText(verificationToken);
});

test("a retryable failure retains one in-memory token without duplicate submission", { tag: "@cross-browser" }, async ({
  page,
}) => {
  let requests = 0;
  let releaseFirstRequest: (() => void) | undefined;
  const firstRequestGate = new Promise<void>((resolve) => {
    releaseFirstRequest = resolve;
  });
  await page.route("**/api/v1/auth/email-verification/complete", async (route) => {
    requests += 1;
    expect(await route.request().postDataJSON()).toEqual({
      token: verificationToken,
    });
    if (requests === 1) {
      await firstRequestGate;
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          type: "/problems/unavailable",
          title: "Unavailable",
          status: 503,
          code: "authentication.account_recovery.unavailable",
        }),
      });
      return;
    }
    await route.fulfill({ status: 204 });
  });

  await page.goto(`/account/verify-email#token=${verificationToken}`);
  const verifyButton = page.getByRole("button", { name: "Confirm email address" });
  await verifyButton.click();
  await expect(page.getByRole("button", { name: "Confirming…" })).toBeDisabled();
  await expect(page.getByRole("status")).toHaveText(
    "Confirming your email address…",
  );
  expect(requests).toBe(1);
  releaseFirstRequest?.();

  const unavailableHeading = page.getByRole("heading", {
    level: 1,
    name: "We couldn’t confirm your email",
  });
  await expect(unavailableHeading).toBeFocused();
  await expect(
    page.getByText(
      "Proctor couldn’t finish the check. Try again, or sign in if your email is already confirmed.",
    ),
  ).toBeVisible();
  await expect(page.getByText("Confirm email", { exact: true })).toHaveCSS(
    "color",
    "rgb(149, 82, 13)",
  );
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(
    page.getByRole("heading", { level: 1, name: "Your email is confirmed" }),
  ).toBeFocused();
  expect(requests).toBe(2);
});

for (const colorScheme of ["light", "dark"] as const) {
  test(`email verification follows the ${colorScheme} theme without narrow overflow`, { tag: "@cross-browser" }, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
    await page.goto(`/account/verify-email#token=${verificationToken}`);

    await expect(
      page.getByRole("heading", { level: 1, name: "Confirm your email address" }),
    ).toBeVisible();
    const theme = await page.evaluate(() => ({
      canvas: getComputedStyle(document.documentElement).backgroundColor,
      body: getComputedStyle(document.body).backgroundColor,
      colorScheme: getComputedStyle(document.documentElement).colorScheme,
    }));
    expect(theme.body).toBe(theme.canvas);
    expect(theme.colorScheme).toBe(colorScheme);

    await page.evaluate(() => {
      document.body.style.zoom = "2";
    });
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
}
