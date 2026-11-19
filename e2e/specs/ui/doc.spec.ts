/**
 * The documentation pages under `/doc`.
 *
 * The bundle carries the pages: `npm run build` runs `fetch-docs` first, and
 * `ui/.documentation-config.json` points it at `help/docs` in this repository, which it copies with
 * no network access. Each case reads the heading of a page under `help/docs` rather than counting
 * pages, so an edit to the documentation breaks a case only when it renames that page.
 *
 * What the API cannot see: the bundle's own routing of `/doc`, the markdown rendered in place, the
 * contents tree and the search moving between pages with no reload, and the not-found page a path
 * outside the contents gets.
 *
 * Shown failing: the stale-success assertion fails on the existing bundle when the delayed Chains
 * request is released after Services loads.
 *
 * The cases open `/doc/`, not `/doc`. nginx answers `/doc` with a redirect to `/doc/` that names
 * its own port, 8080: Compose publishes nginx on host port 8080, so the redirect reaches it, but the
 * Kubernetes target serves nginx on host port 30080 and the browser follows the redirect to a port
 * nothing listens on. `absolute_redirect off` in the chart's `routes.conf` would keep the port; that
 * is the owner's call.
 */
import type { Page } from "@playwright/test";
import { test, expect } from "../../support/page-guard.js";

/** Marks the loaded document, so a full page load is told apart from a route change in place. */
async function markLoad(page: Page): Promise<() => Promise<boolean>> {
  await page.evaluate(() => Object.assign(window, { e2eDocLoad: true }));
  return () => page.evaluate(() => "e2eDocLoad" in window);
}

async function markMarkdownResponse(page: Page, path: string): Promise<void> {
  await page.addInitScript((targetPath: string) => {
    const originalFetch = window.fetch.bind(window);
    window.fetch = (input, init) =>
      originalFetch(input, init).then((response) => {
      const requestUrl =
        input instanceof Request
          ? input.url
          : input instanceof URL
            ? input.href
            : input;
      if (new URL(requestUrl, window.location.href).pathname === targetPath) {
        const originalText = response.text.bind(response);
        response.text = () => {
          Object.assign(window, { e2eMarkdownTextStarted: targetPath });
          return originalText();
        };
        queueMicrotask(() =>
          queueMicrotask(() =>
            Object.assign(window, { e2eMarkdownFetchPath: targetPath }),
          ),
        );
      }
      return response;
    });
  }, path);
}

interface DelayedMarkdownRequest {
  started: Promise<void>;
  release: () => void;
}

async function delayMarkdownRequest(
  page: Page,
  route: string,
): Promise<DelayedMarkdownRequest> {
  let release!: () => void;
  let markStarted!: () => void;
  const started = new Promise<void>((resolve) => {
    markStarted = resolve;
  });

  await page.route(route, async (requestRoute) => {
    await new Promise<void>((resolve) => {
      release = resolve;
      markStarted();
    });
    await requestRoute.continue();
  });

  return { started, release: () => release() };
}

async function releaseMarkdownRequest(
  page: Page,
  request: DelayedMarkdownRequest,
  path: string,
): Promise<boolean> {
  const responsePromise = page.waitForResponse(
    (response) => new URL(response.url()).pathname === path,
  );
  request.release();
  const response = await responsePromise;
  expect(response.status()).toBe(200);
  await page.waitForFunction(
    (responsePath) =>
      (window as Window & { e2eMarkdownFetchPath?: string })
        .e2eMarkdownFetchPath === responsePath,
    path,
  );
  return page.evaluate(
    (responsePath) =>
      (window as Window & { e2eMarkdownTextStarted?: string })
        .e2eMarkdownTextStarted === responsePath,
    path,
  );
}

test.describe("documentation", () => {
  test("opens the first page and moves through contents in place", async ({ page }) => {
    await page.goto("/doc/");
    await expect(page).toHaveURL(/\/doc\/00__Overview\/readme$/);
    await expect(page.getByRole("heading", { level: 1, name: "Overview" })).toBeVisible();
    const sameLoad = await markLoad(page);

    await page.getByRole("tree").getByText("Chains", { exact: true }).click();
    await expect(page).toHaveURL(/\/doc\/01__Chains\/readme$/);
    await expect(page.getByRole("heading", { level: 1, name: "Chains" })).toBeVisible();
    expect(await sameLoad()).toBe(true);
  });

  test("a search result opens its page in place", async ({ page }) => {
    await page.goto("/doc/");
    await expect(page.getByRole("heading", { level: 1, name: "Overview" })).toBeVisible();
    const sameLoad = await markLoad(page);

    await page.getByRole("textbox", { name: "Search documentation..." }).fill("Circuit Breaker");
    // Each result shows its page's title, and its breadcrumb and snippets below it.
    await page.getByRole("listitem").filter({ has: page.getByTitle("Circuit Breaker", { exact: true }) }).click();
    await expect(page).toHaveURL(/\/1__Circuit_Breaker\/readme$/);
    await expect(page.getByRole("heading", { level: 1, name: "Circuit Breaker" })).toBeVisible();
    expect(await sameLoad()).toBe(true);
  });

  test("a path outside the contents shows the not-found page", async ({ page }) => {
    await page.goto("/doc/e2e-no-such-page");
    await expect(page.getByText("Document Not Found", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Go to Documentation Home" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Overview" })).toBeVisible();
  });

  test("keeps Services visible after a late Chains response", async ({ page }) => {
    const chainsPath = "/doc/01__Chains/readme.md";
    await markMarkdownResponse(page, chainsPath);
    const chainsRequest = await delayMarkdownRequest(page, `**${chainsPath}`);

    await page.goto("/doc/");
    await expect(page.getByRole("heading", { level: 1, name: "Overview" })).toBeVisible();
    await page.getByRole("tree").getByText("Chains", { exact: true }).click();
    await chainsRequest.started;
    await page.getByRole("tree").getByText("Services", { exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Services" })).toBeVisible();

    const readResponseBody = await releaseMarkdownRequest(
      page,
      chainsRequest,
      chainsPath,
    );
    if (readResponseBody) {
      await expect(
        page.getByRole("heading", { level: 1, name: "Chains" }),
      ).toBeVisible();
    }

    await expect(page.getByRole("heading", { level: 1, name: "Services" })).toBeVisible();
    await expect(page.getByRole("heading", { level: 1, name: "Chains" })).toHaveCount(0);
  });
});
