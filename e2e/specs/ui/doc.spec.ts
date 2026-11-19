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
 * On the original bundle, these cases fail when a delayed Chains response replaces Services
 * content, displays a not-found page, or removes the loading state before Services loads.
 *
 * The cases open `/doc/`, not `/doc`. nginx answers `/doc` with a redirect to `/doc/` that names
 * its own port, 8080: Compose publishes nginx on host port 8080, so the redirect reaches it, but the
 * Kubernetes target serves nginx on host port 30080 and the browser follows the redirect to a port
 * nothing listens on. `absolute_redirect off` in the chart's `routes.conf` would keep the port; that
 * is the owner's call.
 */
import type { Page, Route } from "@playwright/test";
import { test, expect } from "../../support/page-guard.js";

/** Marks the loaded document, so a full page load is told apart from a route change in place. */
async function markLoad(page: Page): Promise<() => Promise<boolean>> {
  await page.evaluate(() => Object.assign(window, { e2eDocLoad: true }));
  return () => page.evaluate(() => "e2eDocLoad" in window);
}

const CHAINS_PATH = "/doc/01__Chains/readme.md";
const SERVICES_PATH = "/doc/02__Services/readme.md";
const NOT_FOUND_HTML = "<!doctype html><title>Document Not Found</title>";

async function delayMarkdown(page: Page, pathname: string, htmlFallback?: string) {
  let intercept!: (route: Route) => void;
  const intercepted = new Promise<Route>((resolve) => {
    intercept = resolve;
  });
  await page.route((url) => url.pathname === pathname, intercept);

  return {
    intercepted,
    async finish() {
      const route = await intercepted;
      const request = route.request();
      if (!request.failure()) {
        if (htmlFallback) {
          await route.fulfill({ contentType: "text/html", body: htmlFallback });
        } else {
          await route.continue();
        }
      }
      const response = await request.response();
      expect([200, "net::ERR_ABORTED"]).toContain(response?.status() ?? request.failure()?.errorText);
      if (response) {
        await response.finished();
        await page.waitForFunction((url) => performance.getEntriesByName(url, "resource").length > 0, request.url());
      }
      // Let the page render after the browser records the completed resource.
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    },
  };
}

async function selectChainsThenServices(page: Page, chainsRequest: Promise<Route>) {
  await page.goto("/doc/");
  await expect(page.getByRole("heading", { level: 1, name: "Overview" })).toBeVisible();
  const tree = page.getByRole("tree");
  await tree.getByText("Chains", { exact: true }).click();
  await chainsRequest;
  await tree.getByText("Services", { exact: true }).click();
  await expect(page).toHaveURL(/\/doc\/02__Services\/readme$/);
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

  for (const { outcome, htmlFallback } of [
    { outcome: "successful", htmlFallback: undefined },
    { outcome: "not-found", htmlFallback: NOT_FOUND_HTML },
  ]) {
    test(`keeps Services visible after a late ${outcome} Chains response`, async ({ page }) => {
      const chains = await delayMarkdown(page, CHAINS_PATH, htmlFallback);
      await selectChainsThenServices(page, chains.intercepted);
      const services = page.getByRole("heading", { level: 1, name: "Services" });
      await expect(services).toBeVisible();

      await chains.finish();

      await expect(services).toBeVisible();
    });
  }

  test("keeps Services loading when Chains finishes first", async ({ page }) => {
    const chains = await delayMarkdown(page, CHAINS_PATH);
    const services = await delayMarkdown(page, SERVICES_PATH);
    await selectChainsThenServices(page, chains.intercepted);
    await services.intercepted;
    const topicHeadings = page.getByRole("heading", { level: 1, name: /^(Overview|Chains|Services)/ });
    await expect(topicHeadings).toHaveCount(0);

    await chains.finish();

    await expect(topicHeadings).toHaveCount(0);
    await services.finish();
    await expect(page.getByRole("heading", { level: 1, name: "Services" })).toBeVisible();
  });
});
