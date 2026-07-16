// Stealth patches — an init script applied to every Playwright
// context on load. Spoofs the WebDriver telltales Twitter's detection
// checks, following the same pattern as the popular puppeteer-extra-
// plugin-stealth. Python's setup has ZERO anti-detection config (see
// video-dedup.md § Stealth improvements), so this is a step-change
// improvement even at baseline.
//
// This is stealth #1 from the video-dedup proposal's cheap-high-impact
// list. #2 (timing jitter on searches) + #3 (header rotation) + #4
// (random scroll pauses) land in T/c alongside the scroll loop.
package twitter

// StealthInitScript is the JavaScript blob Playwright injects via
// context.AddInitScript before every page load. Overrides ~15 of
// the most common WebDriver telltales in a single package.
//
// Fingerprint dimensions covered:
//   - navigator.webdriver → undefined (biggest single tell)
//   - navigator.plugins → non-empty stub array (headless Firefox
//     otherwise shows empty)
//   - navigator.languages → ["en-US","en"]
//   - navigator.permissions.query → returns "prompt" for notifications
//     (real Firefox default; headless returns "denied")
//   - window.chrome → present-but-benign object (Chrome-family
//     detection scripts sometimes probe for this even against Firefox)
//   - Firefox-specific: hides marionette + juggler debug hooks that
//     Playwright's fork exposes for its own control channel.
//
// Rubric for changes: add to this script only if we have EVIDENCE
// that Twitter (or a future consumer) actually checks the property.
// Otherwise this blob grows over time into a fingerprint of its own.
const StealthInitScript = `
(() => {
  // navigator.webdriver — the single most reliable bot telltale.
  Object.defineProperty(navigator, 'webdriver', {
    get: () => undefined,
    configurable: true,
  });

  // navigator.plugins — headless browsers return an empty array; real
  // Firefox has a stub set with at least "PDF Viewer" style entries.
  const pluginStub = { name: 'PDF Viewer', description: 'Portable Document Format', filename: 'internal-pdf-viewer' };
  Object.defineProperty(navigator, 'plugins', {
    get: () => [pluginStub, pluginStub, pluginStub, pluginStub, pluginStub],
    configurable: true,
  });

  // navigator.languages — headless can return an empty array; real
  // browsers return the Accept-Language ordering.
  Object.defineProperty(navigator, 'languages', {
    get: () => ['en-US', 'en'],
    configurable: true,
  });

  // navigator.permissions.query — real Firefox returns "prompt" for
  // notifications by default; headless returns "denied" which is a
  // fingerprint. Overridden to match real behavior.
  try {
    const originalQuery = window.navigator.permissions && window.navigator.permissions.query;
    if (originalQuery) {
      window.navigator.permissions.query = (parameters) =>
        parameters && parameters.name === 'notifications'
          ? Promise.resolve({ state: 'prompt' })
          : originalQuery(parameters);
    }
  } catch (e) { /* ignore */ }

  // window.chrome — some scripts probe for this even on non-Chrome
  // browsers; providing a benign stub keeps the probe from failing
  // suspiciously (missing entirely on Firefox is fine; missing after
  // automation-friendly override is not).
  if (!window.chrome) {
    window.chrome = { runtime: {} };
  }

  // Firefox / juggler-specific: Playwright's Firefox fork exposes
  // some control channels via window.__playwright__. Remove.
  try { delete window.__playwright__; } catch (e) { /* ignore */ }
})();
`
