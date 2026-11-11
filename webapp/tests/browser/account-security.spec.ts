import { readFile } from "node:fs/promises";
import { expect, test, type Locator, type Page } from "@playwright/test";
import jsQR from "jsqr";
import { PNG } from "pngjs";

const baseContext = {
  enabled: false, pending: false, recovery_codes_remaining: 0, recovery_codes_total: 10,
  recent_authentication_minutes: 20, password_changed_at: "2026-07-01T09:30:00Z",
  service_enabled: true, mfa_recovery_required: false, mfa_enrollment_required: false,
  authentication_method: "password", authentication_strength: "single_factor",
  recently_authenticated: true,
};
const setupKey = "JBSWY3DPEHPK3PXP";
const provisioningURI = `otpauth://totp/Proctor:preview?secret=${setupKey}&issuer=Proctor`;
const recoveryCodes = ["ABCD-EFGH-IJKL", "MNOP-QRST-UVWX", "2345-6789-ABCD", "EFGH-IJKL-MNOP"];
const groupedSetupKey = "JBSW Y3DP EHPK 3PXP";

const discovery = {
  discovery_version: 1,
  password_minimum_length: 12,
  canonical_origin: "http://127.0.0.1:5173",
  initialized: true,
  capabilities: { local_login: true, public_registration: false, invitation_admission: true, desktop_authorization: true },
  desktop_authorization: { protocol: "proctor-desktop-authorization", minimum_version: 1, maximum_version: 1 },
  institution: { id: "institution-1", name: "northbridge", display_name: "Northbridge Institute" },
  providers: [],
};

async function mockSecurity(page: Page, context = baseContext) {
  const state = {
    context: { ...context }, requests: [] as Array<{ path: string; method: string; body: unknown }>,
    lostActivation: false, signedOut: false,
  };
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();
    state.requests.push({ path, method, body: method === "POST" && request.postData() ? request.postDataJSON() : undefined });
    async function json(value: unknown, status = 200) { await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(value) }); }
    async function problem(code: string, status = 403) { await route.fulfill({ status, contentType: "application/problem+json", body: JSON.stringify({ type: "about:blank", title: "Private server detail", status, code }) }); }
    if (path === "/api/v1/users/me/mfa" && method === "GET") {
      if (state.signedOut) return problem("authentication.required", 401);
      return json(state.context);
    }
    if (path === "/api/v1/users/me/mfa/setup") {
      if (!state.context.recently_authenticated) return problem("authentication.reauthentication_required");
      state.context.pending = true;
      return json({ secret: setupKey, provisioning_uri: provisioningURI, expires_at: new Date(Date.now() + 600_000).toISOString() }, 201);
    }
    if (path === "/api/v1/users/me/mfa/activate") {
      if (request.postDataJSON().code !== "123456") return problem("authentication.mfa.code_invalid");
      state.context.enabled = true; state.context.pending = false;
      state.context.mfa_recovery_required = false;
      state.context.mfa_enrollment_required = false;
      state.context.recovery_codes_remaining = recoveryCodes.length;
      state.context.authentication_strength = "multi_factor";
      return json(state.lostActivation ? {} : { recovery_codes: recoveryCodes });
    }
    if (path === "/api/v1/users/me/mfa/challenge") {
      state.context.authentication_strength = "multi_factor";
      return json({ id: "current-session", client_type: "web" });
    }
    if (path === "/api/v1/users/me/mfa/recovery-codes/regenerate") {
      if (!state.context.recently_authenticated) return problem("authentication.reauthentication_required");
      if (state.context.authentication_strength !== "multi_factor") return problem("authentication.strong_required");
      state.context.recovery_codes_remaining = recoveryCodes.length;
      return json({ recovery_codes: recoveryCodes });
    }
    if (path === "/api/v1/users/me/mfa/disable") {
      state.context.enabled = false; state.context.recovery_codes_remaining = 0;
      await route.fulfill({ status: 204 }); return;
    }
    if (path === "/api/v1/auth/reauthenticate/password") {
      if (request.postDataJSON().password !== "current-password") return problem("authentication.credentials_invalid", 401);
      state.context.recently_authenticated = true;
      (state.context as Record<string, unknown>).recently_authenticated_until = new Date(Date.now() + 12 * 60_000 - 1_000).toISOString();
      return json({ id: "current-session", client_type: "web", reauthenticated_at: new Date().toISOString() });
    }
    if (path === "/api/v1/auth/reauthenticate/external") return json({ redirect_url: "https://sso.example/reauthenticate" });
    if (path === "/api/v1/auth/logout") { state.signedOut = true; await route.fulfill({ status: 204 }); return; }
    if (path === "/api/v1/users/me") return json({ id: "user", username: "person", display_name: "Person" });
    if (path === "/api/v1/discovery" && method === "GET") return json(discovery);
    return problem("unexpected.test.request", 500);
  });
  return state;
}

function mutations(state: Awaited<ReturnType<typeof mockSecurity>>, suffix: string) {
  return state.requests.filter(({ path, method }) => method === "POST" && path.endsWith(suffix));
}

// enterCode fills a one-time-code field and retries until the value sticks.
// The OTP control can remount while a page transition settles, and a fill that
// lands during that remount is discarded under load.
async function enterCode(field: Locator, code: string) {
  await expect(field).toBeEditable();
  await expect(async () => {
    await field.fill(code);
    await expect(field).toHaveValue(code, { timeout: 1_000 });
  }).toPass();
}

async function activate(page: Page) {
  await page.getByRole("button", { name: "Set up two-step verification", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Scan this code", level: 1 })).toBeFocused();
  await enterCode(page.getByLabel("Authenticator code", { exact: false }), "123456");
  await page.getByRole("button", { name: "Verify and turn on", exact: true }).click();
}

async function expectScannableSetup(page: Page) {
  const qr = page.getByRole("img", { name: "QR code for two-step verification setup" });
  await expect(qr).toBeVisible();
  const png = PNG.sync.read(await qr.screenshot());
  const decoded = jsQR(new Uint8ClampedArray(png.data), png.width, png.height);
  expect(decoded?.data).toBe(provisioningURI);
}

for (const colorScheme of ["light", "dark"] as const) {
  for (const width of [320, 1440]) {
    test(`QR setup and OTP entry remain usable at ${width}px in ${colorScheme}`, { tag: "@cross-browser" }, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 1000 });
      await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
      await mockSecurity(page);
      const external: string[] = [];
      page.on("request", (request) => {
        if (!request.url().startsWith("http://127.0.0.1:5173/")) external.push(request.url());
      });
      await page.goto("/account/security");
      await page.getByRole("button", { name: "Set up two-step verification", exact: true }).click();
      await expectScannableSetup(page);
      const key = page.locator("#authenticator-secret");
      await expect(key).toBeVisible();
      await expect(key).toHaveText(groupedSetupKey);
      await expect(key).toHaveAttribute("translate", "no");
      await expect(page.getByText("Can’t scan it? Enter this setup key in your app instead.")).toBeVisible();
      await expect(page.locator("#authenticator-setup-expiry")).toHaveText("Finish within 10 minutes.");
      await expect(page.getByLabel("Authenticator code")).toHaveAttribute("aria-describedby", /authenticator-setup-expiry/);
      await enterCode(page.getByLabel("Authenticator code"), "012345");
      await expect(page.locator("[data-proctor-otp-slot]")).toHaveText(["0", "1", "2", "3", "4", "5"]);
      await page.screenshot({ path: testInfo.outputPath(`mfa-setup-${width}-${colorScheme}.png`), fullPage: true });
      await page.evaluate(() => { document.body.style.zoom = "2"; document.documentElement.dir = "rtl"; });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
      await expect(page.getByLabel("Authenticator code")).toHaveAttribute("dir", "ltr");
      await expectScannableSetup(page);
      await page.emulateMedia({ forcedColors: "active" });
      await expectScannableSetup(page);
      const focus = await page.locator("[data-proctor-otp-slot][data-active]").evaluate((slot) => ({
        style: getComputedStyle(slot).outlineStyle, width: getComputedStyle(slot).outlineWidth,
      }));
      expect(focus).toEqual({ style: "solid", width: "3px" });
      expect(external).toEqual([]);
    });
  }
}

test("status names the signed-in User and signs out from the footer", async ({ page }) => {
  const state = await mockSecurity(page);
  await page.goto("/account/security");
  await expect(page.getByText("Off", { exact: true })).toBeVisible();
  await expect(page.getByText("Signed in with password", { exact: true })).toBeVisible();
  const facts = page.locator("[data-proctor-fact-list]");
  await expect(facts.getByRole("term").first()).toHaveText("Password");
  const changed = facts.getByRole("definition").first();
  await expect(changed.locator("time")).toHaveAttribute("datetime", "2026-07-01T09:30:00Z");
  await expect(changed).toHaveText(`Changed ${await page.evaluate(() => new Intl.DateTimeFormat(undefined, { day: "numeric", month: "long", year: "numeric" }).format(new Date("2026-07-01T09:30:00Z")))}`);
  await expect(page.getByRole("heading", { name: "Turn on two-step verification", level: 2 })).toBeVisible();
  await expect(page.getByRole("button", { name: "Verify this session" })).toHaveCount(0);
  const footer = page.getByRole("contentinfo");
  await expect(footer).toContainText("Signed in as Person");
  await footer.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login$/);
  expect(mutations(state, "/logout")).toHaveLength(1);
});

test("OTP supports leading zeroes, editing, paste, validation and explicit submission", { tag: "@cross-browser" }, async ({ page }) => {
  const state = await mockSecurity(page);
  await page.goto("/account/security");
  await page.getByRole("button", { name: "Set up two-step verification", exact: true }).click();
  const input = page.getByLabel("Authenticator code");
  await expect(input).toHaveAttribute("type", "text");
  await expect(input).toHaveAttribute("inputmode", "numeric");
  await expect(input).toHaveAttribute("autocomplete", "one-time-code");
  await input.pressSequentially("012");
  await page.getByRole("button", { name: "Verify and turn on" }).click();
  await expect(input).toBeFocused();
  await expect(input).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByText("Enter all six digits of your authenticator code.")).toBeVisible();
  expect(mutations(state, "/activate")).toHaveLength(0);
  await input.fill("");
  await input.pressSequentially("012345");
  expect(mutations(state, "/activate")).toHaveLength(0);
  await input.press("Backspace");
  await expect(input).toHaveValue("01234");
  await input.press("ArrowLeft");
  // The OTP control selects the previous slot asynchronously; overwrite only
  // after that selection exists so the keystroke replaces instead of inserts.
  await expect
    .poll(() => input.evaluate((element: HTMLInputElement) => [element.selectionStart, element.selectionEnd]))
    .toEqual([4, 5]);
  await input.press("9");
  await expect(input).toHaveValue("01239");
  await input.press("ControlOrMeta+A");
  await input.press("Backspace");
  await input.pressSequentially("ABC");
  await expect(input).toHaveValue("");
  await input.evaluate((element) => {
    const data = new DataTransfer();
    data.setData("text/plain", "123 456");
    const event = new ClipboardEvent("paste", { bubbles: true, cancelable: true });
    // Firefox drops clipboardData supplied to an untrusted event constructor.
    Object.defineProperty(event, "clipboardData", { value: data });
    element.dispatchEvent(event);
  });
  await expect(input).toHaveValue("123456");
  await expect(page.locator("[data-proctor-otp-slot]")).toHaveText(["1", "2", "3", "4", "5", "6"]);
  expect(mutations(state, "/activate")).toHaveLength(0);
  await input.press("Enter");
  await expect(page.getByRole("heading", { name: "Save your recovery codes" })).toBeVisible();
  expect(mutations(state, "/activate")[0]?.body).toEqual({ code: "123456" });
  await expect(page.getByRole("img", { name: "QR code for two-step verification setup" })).toHaveCount(0);
});

test("switching proof methods clears values, restores focus and preserves recovery codes", { tag: "@cross-browser" }, async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, enabled: true });
  await page.goto("/account/security");
  await page.getByRole("button", { name: "Verify this session", exact: true }).click();
  await enterCode(page.getByLabel("Authenticator code"), "012345");
  await page.getByRole("button", { name: "Use a recovery code instead" }).click();
  const recovery = page.getByRole("textbox", { name: "Recovery code", exact: true });
  await expect(recovery).toBeFocused();
  await expect(recovery).toHaveValue("");
  await recovery.fill("ABCD-EFGH-IJKL");
  await page.getByRole("button", { name: "Use an authenticator code" }).click();
  await expect(page.getByLabel("Authenticator code")).toBeFocused();
  await expect(page.getByLabel("Authenticator code")).toHaveValue("");
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByLabel("Authenticator code")).toBeFocused();
  expect(mutations(state, "/challenge")).toHaveLength(0);
  await page.getByRole("button", { name: "Use a recovery code instead" }).click();
  await recovery.fill("ABCD-EFGH-IJKL");
  await recovery.press("Enter");
  await expect(page.getByRole("heading", { name: "Account security", exact: true })).toBeVisible();
  expect(mutations(state, "/challenge")[0]?.body).toEqual({ code: "ABCD-EFGH-IJKL" });
});

test("production QR and OTP work under the server Content Security Policy", { tag: "@cross-browser" }, async ({ page }) => {
  const serverSource = await readFile("../server/webui/webui.go", "utf8");
  const policy = serverSource.match(/header\.Set\(\s*"Content-Security-Policy",\s*"([^"]+)",?\s*\)/)?.[1];
  expect(policy).toBeDefined();
  await mockSecurity(page);
  await page.route("**/account/security", async (route) => route.fulfill({
    contentType: "text/html", body: await readFile("dist/index.html"),
    headers: { "Content-Security-Policy": policy! },
  }));
  await page.route("**/assets/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    expect(path).toMatch(/^\/assets\/[A-Za-z0-9_.-]+$/);
    const type = path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css"
      : path.endsWith(".woff2") ? "font/woff2" : path.endsWith(".svg") ? "image/svg+xml" : "image/png";
    await route.fulfill({ contentType: type, body: await readFile(`dist${path}`) });
  });
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => { if (entry.type() === "error") errors.push(entry.text()); });
  await page.goto("/account/security");
  await page.getByRole("button", { name: "Set up two-step verification", exact: true }).click();
  // QR decoding is covered separately: Playwright's WebKit screenshots inject an inline style.
  await expect(page.getByRole("img", { name: "QR code for two-step verification setup" })).toBeVisible();
  await page.getByLabel("Authenticator code").pressSequentially("123456");
  await expect(page.locator("[data-proctor-otp-slot]")).toHaveText(["1", "2", "3", "4", "5", "6"]);
  await page.getByRole("button", { name: "Verify and turn on", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Save your recovery codes" })).toBeVisible();
  expect(errors).toEqual([]);
});

test("enrollment shows codes once and requires acknowledgement before hiding them", { tag: "@cross-browser" }, async ({ page }) => {
  const state = await mockSecurity(page);
  const logs: string[] = [];
  page.on("console", (entry) => logs.push(entry.text()));
  await page.goto("/account/security#unexpected=secret");
  await expect(page.getByRole("heading", { name: "Account security", level: 1 })).toBeVisible();
  expect(state.requests.filter(({ method }) => method === "POST")).toHaveLength(0);
  await activate(page);
  await expect(page.getByRole("heading", { name: "Save your recovery codes", level: 1 })).toBeFocused();
  await expect(page.getByText("Two-step verification", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("list", { name: "Recovery codes" }).getByRole("listitem")).toHaveText(recoveryCodes);
  const finish = page.getByRole("button", { name: "Done", exact: true });
  await expect(finish).toBeDisabled();
  await expect(page.getByRole("button", { name: "Cancel" })).toHaveCount(0);
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download codes" }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("proctor-recovery-codes.txt");
  expect(await readFile((await download.path())!, "utf8")).toBe(`${recoveryCodes.join("\n")}\n`);
  await expect(finish).toBeDisabled();
  const stored = await page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } }));
  for (const secret of [setupKey, ...recoveryCodes]) {
    expect(JSON.stringify(stored)).not.toContain(secret);
    expect(page.url()).not.toContain(secret);
    expect(logs.join("\n")).not.toContain(secret);
  }
  await page.getByRole("checkbox", { name: "I’ve saved these codes somewhere safe" }).check();
  await finish.click();
  await expect(page.getByRole("heading", { name: "Account security", level: 1 })).toBeVisible();
  await expect(page.getByText(recoveryCodes[0], { exact: true })).toHaveCount(0);
  await expect(page.getByText(`${recoveryCodes.length} of 10 left`, { exact: true })).toBeVisible();
  expect(mutations(state, "/activate")).toHaveLength(1);
  await page.reload();
  await expect(page.getByRole("heading", { name: "Account security", level: 1 })).toBeVisible();
  await expect(page.getByText(recoveryCodes[0], { exact: true })).toHaveCount(0);
});

test("lost activation confirmation reads status and requires explicit challenge and regeneration", async ({ page }) => {
  const state = await mockSecurity(page);
  state.lostActivation = true;
  await page.goto("/account/security");
  await activate(page);
  await expect(page.getByRole("heading", { name: "Check whether the change worked" })).toBeVisible();
  await page.getByRole("button", { name: "Check current status" }).click();
  await expect(page.getByText("On", { exact: true })).toBeVisible();
  expect(mutations(state, "/activate")).toHaveLength(1);
  expect(mutations(state, "/regenerate")).toHaveLength(0);
  await page.getByRole("button", { name: "Make new codes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Make new recovery codes?" })).toBeVisible();
  expect(mutations(state, "/regenerate")).toHaveLength(0);
  await page.getByRole("button", { name: "Replace my codes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Save your recovery codes" })).toBeVisible();
  expect(mutations(state, "/regenerate")).toHaveLength(1);
});

test("disable and replacement require an explicit destructive confirmation", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, enabled: true, authentication_strength: "multi_factor" });
  await page.goto("/account/security");
  await expect(page.getByRole("heading", { name: "Recovery codes", level: 2 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Turn off two-step verification", level: 2 })).toBeVisible();
  // A verified Session is not offered verification as its main action.
  await expect(page.getByRole("button", { name: "Verify this session" })).toHaveCount(0);
  await expect(page.getByText("Verified with two-step", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Turn off", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Turn off two-step verification?", level: 1 })).toBeFocused();
  await expect(page.getByText("Moving to a new phone?", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Keep it on", exact: true })).toBeVisible();
  const confirm = page.getByRole("button", { name: "Turn off", exact: true });
  const colors = await confirm.evaluate((button) => ({
    background: getComputedStyle(button).backgroundColor,
    danger: (() => {
      const probe = document.createElement("span");
      probe.style.color = "var(--proctor-color-action-danger)";
      document.body.append(probe);
      const value = getComputedStyle(probe).color;
      probe.remove();
      return value;
    })(),
  }));
  expect(colors.background).toBe(colors.danger);
  expect(mutations(state, "/disable")).toHaveLength(0);
  await confirm.click();
  await expect(page.getByRole("button", { name: "Set up two-step verification", exact: true })).toBeVisible();
  expect(mutations(state, "/disable")).toHaveLength(1);
  expect(mutations(state, "/setup")).toHaveLength(0);
});

test("fresh password proof and MFA challenge return without replaying the security action", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, enabled: true, recently_authenticated: false });
  await page.goto("/account/security");
  await page.getByRole("button", { name: "Make new codes", exact: true }).click();
  await page.getByRole("button", { name: "Replace my codes", exact: true }).click();
  await page.getByRole("link", { name: "Confirm it’s you", exact: true }).click();
  await expect(page).toHaveURL(/\/account\/reauthenticate\?task=security$/);
  // The window comes from the server's configured value, not a client default.
  await expect(page.getByText("You won’t be asked again for 20 minutes.", { exact: false })).toBeVisible();
  await expect(page.getByRole("contentinfo")).toContainText("Signed in as Person");
  await page.getByRole("textbox", { name: "Password" }).fill("wrong-password");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByText("That password isn’t right. Try again.")).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Password" })).toHaveValue("");
  await page.getByRole("textbox", { name: "Password" }).fill("current-password");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.getByRole("button", { name: "Use a recovery code instead" }).click();
  await enterCode(page.getByRole("textbox", { name: "Recovery code", exact: true }), "ABCD-EFGH");
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByRole("heading", { name: "You’re confirmed" })).toBeVisible();
  // Once confirmed the page states what is left of this Session's window.
  await expect(page.getByText("You won’t be asked to confirm again for 12 minutes.", { exact: false })).toBeVisible();
  await page.getByRole("link", { name: "Back to account security" }).click();
  await expect(page.getByRole("heading", { name: "Account security", level: 1 })).toBeVisible();
  expect(mutations(state, "/regenerate")).toHaveLength(1);
  expect(mutations(state, "/login")).toHaveLength(0);
  expect(mutations(state, "/challenge")[0]?.body).toEqual({ code: "ABCD-EFGH" });
  await expect(page.getByText("Private server detail")).toHaveCount(0);
});

test("external fresh proof uses the original provider and an explicit closed task", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, authentication_method: "oidc", authentication_provider_id: "university-oidc", recently_authenticated: false } as typeof baseContext);
  await page.route("**/api/v1/discovery", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({
    ...discovery, providers: [{ id: "university-oidc", display_name: "University SSO", type: "oidc" }],
  }) }));
  await page.route("https://sso.example/reauthenticate", (route) => route.fulfill({ contentType: "text/html", body: "<h1>Provider proof</h1>" }));
  await page.goto("/account/reauthenticate?task=connect-provider&return_to=https://attacker.example#external_login=failed");
  await expect(page.getByText("University SSO couldn’t confirm it’s you. Nothing was changed.", { exact: false })).toBeVisible();
  await expect(page).toHaveURL(/\/account\/reauthenticate\?task=connect-provider$/);
  expect(mutations(state, "/external")).toHaveLength(0);
  await expect(page.getByRole("textbox", { name: "Password" })).toHaveCount(0);
  await page.getByRole("button", { name: "Continue with University SSO" }).click();
  await expect(page.getByRole("heading", { name: "Provider proof" })).toBeVisible();
  expect(mutations(state, "/external")[0]?.body).toEqual({ task: "connect-provider" });
});

test("recovery restriction remains visible with management disabled and offers only its real actions", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, service_enabled: false, mfa_recovery_required: true });
  await page.goto("/account/security");
  await expect(page.getByText("Your institution needs you to set up two-step verification again", { exact: false })).toBeVisible();
  await expect(page.getByText("Setup is turned off right now, so you can’t finish this yet.", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Set up two-step verification", exact: true })).toHaveCount(0);
  const main = page.getByRole("main");
  await expect(main.getByRole("button")).toHaveText(["Check current status", "Sign out"]);
  await expect(page.getByRole("contentinfo")).not.toContainText("Signed in as");
  expect(state.requests.some(({ path }) => path === "/api/v1/users/me")).toBe(false);
  await page.goto("/account/reauthenticate?task=connect-provider");
  await expect(page.getByRole("link", { name: "Restore account access" })).toHaveAttribute("href", "/account/security");
  expect(state.requests.filter(({ method }) => method === "POST")).toHaveLength(0);
});

test("pending enrollment restarts explicitly and invalid codes keep the setup key available", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, pending: true });
  await page.goto("/account/security");
  await expect(page.getByText("Setup not finished", { exact: true })).toBeVisible();
  await expect(page.getByText("Starting again gives you a new code", { exact: false })).toBeVisible();
  expect(mutations(state, "/setup")).toHaveLength(0);
  await page.getByRole("button", { name: "Start setup again" }).click();
  // Restarting re-renders the form with the new setup key; type the code only
  // after that render so it cannot clear the field under load.
  await expect.poll(() => mutations(state, "/setup").length).toBe(1);
  await expect(page.locator("#authenticator-secret")).toHaveText(groupedSetupKey);
  await enterCode(page.getByLabel("Authenticator code", { exact: false }), "000000");
  await page.getByRole("button", { name: "Verify and turn on" }).click();
  await expect(page.getByText("That code isn’t right.", { exact: false })).toBeVisible();
  await expect(page.locator("#authenticator-secret")).toHaveText(groupedSetupKey);
  await expect(page.getByLabel("Authenticator code", { exact: false })).toHaveValue("");
  expect(mutations(state, "/setup")).toHaveLength(1);
});

test("a response arriving after page exit cannot restore one-time codes on history restoration", { tag: "@cross-browser" }, async ({ page }) => {
  const state = await mockSecurity(page);
  let release!: () => void;
  let observed!: () => void;
  const waiting = new Promise<void>((resolve) => { release = resolve; });
  const requested = new Promise<void>((resolve) => { observed = resolve; });
  await page.route("**/api/v1/users/me/mfa/activate", async (route) => {
    observed();
    await waiting;
    state.context.enabled = true; state.context.pending = false;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ recovery_codes: recoveryCodes }) });
  });
  await page.goto("/account/security");
  await page.getByRole("button", { name: "Set up two-step verification", exact: true }).click();
  await enterCode(page.getByLabel("Authenticator code", { exact: false }), "123456");
  await page.getByRole("button", { name: "Verify and turn on" }).click();
  await requested;
  await page.evaluate(() => { window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true })); });
  const response = page.waitForResponse("**/api/v1/users/me/mfa/activate");
  release();
  await response;
  await page.evaluate(() => { window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true })); });
  await expect(page.getByText("On", { exact: true })).toBeVisible();
  await expect(page.getByText(recoveryCodes[0], { exact: true })).toHaveCount(0);
  await expect(page.locator("#authenticator-secret")).toHaveCount(0);
});

test("a missing Session exposes sign in and a read failure can be retried", async ({ page }) => {
  const state = await mockSecurity(page);
  state.signedOut = true;
  await page.goto("/account/security");
  await expect(page.getByRole("heading", { name: "Sign in to manage your security" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Sign in", exact: true })).toHaveAttribute("href", "/login");
  state.signedOut = false;
  await page.route("**/api/v1/users/me/mfa", async (route) => {
    await page.unroute("**/api/v1/users/me/mfa");
    await route.fulfill({ status: 503 });
  });
  await page.reload();
  await expect(page.getByRole("heading", { name: "We couldn’t check your security" })).toBeVisible();
  await page.getByRole("button", { name: "Check current status" }).click();
  await expect(page.getByRole("heading", { name: "Account security", exact: true })).toBeFocused();
});

test("restricted password login goes directly to reenrollment", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, mfa_recovery_required: true });
  await page.route("**/api/v1/discovery", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({
    discovery_version: 1, password_minimum_length: 12, canonical_origin: "http://127.0.0.1:5173", initialized: true,
    capabilities: { local_login: true, public_registration: false, invitation_admission: false, desktop_authorization: true },
    desktop_authorization: { protocol: "proctor-desktop-authorization", minimum_version: 1, maximum_version: 1 },
    institution: { id: "institution", name: "institution", display_name: "Institution" }, providers: [],
  }) }));
  await page.route("**/api/v1/auth/login", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ session: { id: "restricted", client_type: "web", mfa_recovery_required: true } }) }));
  await page.goto("/login");
  await page.getByLabel("Email or username").fill("person");
  await page.locator("#password").fill("current-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/account\/security$/);
  await expect(page.getByText("Your institution needs you to set up two-step verification again", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Set up two-step verification", exact: true })).toBeVisible();
  await expect(page.getByRole("main").getByRole("button", { name: "Sign out" })).toBeVisible();
  expect(state.requests.some(({ path }) => path === "/api/v1/users/me")).toBe(false);
});

test("password login restricted by the institution goes directly to two-step setup", async ({ page }) => {
  const state = await mockSecurity(page, { ...baseContext, mfa_enrollment_required: true });
  await page.route("**/api/v1/discovery", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify(discovery) }));
  await page.route("**/api/v1/auth/login", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ session: { id: "restricted", client_type: "web", mfa_enrollment_required: true } }) }));
  await page.goto("/login");
  await page.getByLabel("Email or username").fill("person");
  await page.locator("#password").fill("current-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/account\/security$/);
  await expect(page.getByText("Your institution requires two-step verification for accounts that hold a role.", { exact: false })).toBeVisible();
  await expect(page.getByText("Your institution needs you to set up two-step verification again", { exact: false })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Set up two-step verification", exact: true })).toBeVisible();
  await expect(page.getByRole("main").getByRole("button", { name: "Sign out" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Turn off two-step verification" })).toHaveCount(0);
  expect(state.requests.some(({ path }) => path === "/api/v1/users/me")).toBe(false);
});

test("an institution-restricted Session with a second factor confirms it instead of setting up", async ({ page }) => {
  await mockSecurity(page, { ...baseContext, enabled: true, recovery_codes_remaining: 4, mfa_enrollment_required: true, authentication_method: "oidc" });
  await page.goto("/account/security");
  await expect(page.getByText("Confirm this sign-in with your authenticator app", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Set up two-step verification", exact: true })).toHaveCount(0);
  await expect(page.getByRole("main").getByRole("button", { name: "Sign out" })).toBeVisible();
});

test("sign-in status offers the bounded security destination for ordinary and restricted Sessions", async ({ page }) => {
  const state = await mockSecurity(page);
  await page.goto("/authorization/complete");
  await expect(page.getByText("Sign-in confirmed", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Go to account security" })).toHaveAttribute("href", "/account/security");
  await expect(page.getByRole("link", { name: "Add a sign-in method" })).toHaveAttribute("href", "/account/connect-provider");
  state.context.mfa_recovery_required = true;
  await page.route("**/api/v1/users/me", (route) => route.fulfill({ status: 401, contentType: "application/problem+json", body: JSON.stringify({
    type: "about:blank", title: "Private recovery detail", status: 401, code: "authentication.token_invalid",
  }) }));
  await page.reload();
  await expect(page.getByRole("heading", { name: "Restore account access" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Restore account access" })).toHaveAttribute("href", "/account/security");
  await expect(page.getByText("Private recovery detail")).toHaveCount(0);
  state.context.mfa_recovery_required = false;
  state.context.mfa_enrollment_required = true;
  await page.reload();
  await expect(page.getByRole("heading", { name: "Two-step verification required" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Set up two-step verification" })).toHaveAttribute("href", "/account/security");
});

test("fresh proof remains usable with expanded copy, dark mode, and 200 percent zoom", { tag: "@cross-browser" }, async ({ browserName, page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await mockSecurity(page, { ...baseContext, recently_authenticated: false });
  await page.goto("/account/reauthenticate?task=security");
  await expect(page.getByRole("heading", { name: "Confirm it’s you" })).toBeVisible();
  await page.keyboard.press(browserName === "webkit" ? "Alt+Tab" : "Tab");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("main")).toBeFocused();
  await page.getByRole("heading", { level: 1 }).evaluate((heading) => {
    heading.textContent = `${heading.textContent} — ${heading.textContent} — ${heading.textContent}`;
  });
  await page.evaluate(() => { document.body.style.zoom = "2"; });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("reauthentication-expanded-dark.png"), fullPage: true });
  await page.locator("#current-password").fill("current-password");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("heading", { name: "You’re confirmed" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to account security" })).toHaveAttribute("href", "/account/security");
});

for (const viewport of [{ width: 390, height: 844 }, { width: 768, height: 1024 }, { width: 1440, height: 1024 }]) {
  for (const colorScheme of ["light", "dark"] as const) {
    test(`security lifecycle fits ${viewport.width}px ${colorScheme} with keyboard and zoom`, { tag: "@cross-browser" }, async ({ browserName, page }, testInfo) => {
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
      await mockSecurity(page);
      await page.goto("/account/security");
      await expect(page.getByRole("heading", { name: "Account security", level: 1 })).not.toBeFocused();
      await page.keyboard.press(browserName === "webkit" ? "Alt+Tab" : "Tab");
      await expect(page.getByRole("link", { name: "Skip to account security" })).toBeFocused();
      await page.keyboard.press("Enter");
      await expect(page.getByRole("main")).toBeFocused();
      await activate(page);
      await expect(page.getByRole("heading", { level: 1 })).toHaveCount(1);
      await page.screenshot({ path: testInfo.outputPath(`security-codes-${viewport.width}-${colorScheme}.png`), fullPage: true });
      await page.evaluate(() => { document.body.style.zoom = "2"; });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
      await page.getByRole("checkbox").focus();
      await page.keyboard.press("Space");
      await page.keyboard.press(browserName === "webkit" ? "Alt+Tab" : "Tab");
      await expect(page.getByRole("button", { name: "Done", exact: true })).toBeFocused();
      await page.emulateMedia({ forcedColors: "active" });
      await expect(page.getByRole("button", { name: "Done", exact: true })).toBeVisible();
    });
  }
}
