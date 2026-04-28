#!/usr/bin/env node
/**
 * explore.js — SPA-aware page exploration script for playwright-tool
 *
 * Navigates to a URL using Playwright with stealth plugin, detects SPA frameworks,
 * waits for hydration, discovers client-side routes, and extracts all testable elements.
 *
 * Usage: node explore.js --url https://example.com [--depth 1] [--follow-links] [--viewport 1280x720] [--no-spa] [--spa-timeout 15000]
 *
 * Outputs JSON to stdout. Errors go to stderr.
 */

const { chromium } = require('playwright-extra');
const StealthPlugin = require('puppeteer-extra-plugin-stealth');

chromium.use(StealthPlugin());

// Parse CLI arguments
function parseArgs() {
  const args = process.argv.slice(2);
  const opts = {
    url: '',
    depth: 1,
    followLinks: false,
    viewport: '1280x720',
    spa: true,
    spaTimeout: 15000,
  };

  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case '--url': opts.url = args[++i]; break;
      case '--depth': opts.depth = parseInt(args[++i], 10) || 1; break;
      case '--follow-links': opts.followLinks = true; break;
      case '--viewport': opts.viewport = args[++i]; break;
      case '--no-spa': opts.spa = false; break;
      case '--spa-timeout': opts.spaTimeout = parseInt(args[++i], 10) || 15000; break;
    }
  }

  if (!opts.url) {
    console.error('Error: --url is required');
    process.exit(1);
  }

  return opts;
}

// Detect SPA framework
async function detectFramework(page) {
  return await page.evaluate(() => {
    // React
    if (window.__REACT_DEVTOOLS_GLOBAL_HOOK__ ||
        document.querySelector('[data-reactroot]') ||
        document.querySelector('#__next')) {
      const isNext = !!window.__NEXT_DATA__;
      return {
        detected: true,
        framework: isNext ? 'nextjs' : 'react',
        version: window.__REACT_DEVTOOLS_GLOBAL_HOOK__?.renderers?.values?.()?.next?.()?.value?.version || 'unknown',
        router: isNext ? 'next-router' : (window.__REACT_ROUTER__ ? 'react-router' : 'unknown'),
      };
    }

    // Vue
    if (window.__VUE__ || document.querySelector('[data-v-]')) {
      const isNuxt = !!window.__NUXT__;
      return {
        detected: true,
        framework: isNuxt ? 'nuxt' : 'vue',
        version: window.__VUE__?.version || 'unknown',
        router: isNuxt ? 'nuxt-router' : 'vue-router',
      };
    }

    // Angular
    if (window.getAllAngularRootElements || document.querySelector('[ng-version]')) {
      return {
        detected: true,
        framework: 'angular',
        version: document.querySelector('[ng-version]')?.getAttribute('ng-version') || 'unknown',
        router: 'angular-router',
      };
    }

    // Svelte
    if (document.querySelector('[class*="svelte-"]')) {
      return { detected: true, framework: 'svelte', version: 'unknown', router: 'unknown' };
    }

    return { detected: false, framework: 'none', version: null, router: null };
  });
}

// Wait for SPA to be ready (hydration + async content)
async function waitForSPAReady(page, frameworkInfo, timeout) {
  try {
    await page.waitForLoadState('networkidle', { timeout });
  } catch (e) {
    // networkidle may not fire on some SPAs — continue
  }

  if (!frameworkInfo.detected) return;

  const hydrationStart = Date.now();

  try {
    switch (frameworkInfo.framework) {
      case 'react':
      case 'nextjs':
        await page.waitForFunction(() => {
          const root = document.getElementById('root') || document.getElementById('__next') || document.getElementById('app');
          return root && root.children.length > 0;
        }, { timeout: Math.min(timeout, 10000) });
        break;

      case 'vue':
      case 'nuxt':
        await page.waitForFunction(() => {
          return document.querySelectorAll('[data-v-]').length > 0 ||
                 (document.getElementById('__nuxt')?.children?.length || 0) > 0 ||
                 (document.getElementById('app')?.children?.length || 0) > 0;
        }, { timeout: Math.min(timeout, 10000) });
        break;

      case 'angular':
        await page.waitForFunction(() => {
          return !!document.querySelector('[ng-version]') &&
                 (document.querySelector('app-root')?.children?.length || 0) > 0;
        }, { timeout: Math.min(timeout, 10000) });
        break;
    }
  } catch (e) {
    // Hydration wait timed out — continue with what we have
  }

  frameworkInfo.hydration_time_ms = Date.now() - hydrationStart;

  // Wait for loading indicators to disappear
  try {
    await page.waitForFunction(() => {
      const loaders = document.querySelectorAll(
        '[class*="spinner"], [class*="loading"], [class*="skeleton"], [aria-busy="true"]'
      );
      return loaders.length === 0;
    }, { timeout: 5000 });
  } catch (e) {
    // Some loaders may persist — that's OK
  }

  // Wait for DOM to stabilize (no mutations for 500ms)
  try {
    await page.evaluate(() => {
      return new Promise((resolve) => {
        let timeout;
        const observer = new MutationObserver(() => {
          clearTimeout(timeout);
          timeout = setTimeout(() => { observer.disconnect(); resolve(); }, 500);
        });
        observer.observe(document.body, { childList: true, subtree: true });
        timeout = setTimeout(() => { observer.disconnect(); resolve(); }, 500);
      });
    });
  } catch (e) {
    // DOM stabilization failed — continue
  }
}

// Extract all testable elements from the current page
async function extractPageElements(page) {
  return await page.evaluate(() => {
    // Helper: generate a unique CSS selector for an element
    function getSelector(el) {
      if (el.id) return `#${el.id}`;
      if (el.getAttribute('data-testid')) return `[data-testid="${el.getAttribute('data-testid')}"]`;
      if (el.getAttribute('name')) return `${el.tagName.toLowerCase()}[name="${el.getAttribute('name')}"]`;

      // Use nth-child as fallback
      const parent = el.parentElement;
      if (!parent) return el.tagName.toLowerCase();
      const siblings = Array.from(parent.children).filter(c => c.tagName === el.tagName);
      if (siblings.length === 1) return `${parent.tagName.toLowerCase()} > ${el.tagName.toLowerCase()}`;
      const idx = siblings.indexOf(el) + 1;
      return `${parent.tagName.toLowerCase()} > ${el.tagName.toLowerCase()}:nth-child(${idx})`;
    }

    // Navigation links
    const navigation = Array.from(document.querySelectorAll('a[href]'))
      .filter(a => a.offsetParent !== null) // visible only
      .slice(0, 50) // limit
      .map(a => ({
        text: a.textContent.trim().substring(0, 100),
        href: a.href,
        selector: getSelector(a),
      }));

    // Forms
    const forms = Array.from(document.querySelectorAll('form'))
      .slice(0, 20)
      .map(form => {
        const fields = Array.from(form.querySelectorAll('input, select, textarea'))
          .map(field => ({
            name: field.name || field.id || '',
            type: field.type || field.tagName.toLowerCase(),
            required: field.required || field.getAttribute('aria-required') === 'true',
            placeholder: field.placeholder || '',
            selector: getSelector(field),
          }));

        const submitBtn = form.querySelector('button[type="submit"], input[type="submit"]');

        return {
          action: form.action || '',
          method: (form.method || 'GET').toUpperCase(),
          selector: getSelector(form),
          fields,
          submit_button: submitBtn ? {
            text: submitBtn.textContent?.trim() || submitBtn.value || 'Submit',
            selector: getSelector(submitBtn),
          } : null,
        };
      });

    // Interactive elements (buttons not in forms, dropdowns)
    const interactive = [];
    document.querySelectorAll('button:not(form button), [role="button"], select:not(form select)')
      .forEach(el => {
        if (el.offsetParent === null) return; // skip hidden
        if (interactive.length >= 30) return;

        const item = {
          type: el.tagName.toLowerCase() === 'select' ? 'dropdown' : 'button',
          text: el.textContent?.trim().substring(0, 100) || '',
          selector: getSelector(el),
        };

        if (el.tagName.toLowerCase() === 'select') {
          item.options = Array.from(el.options).map(o => o.text).slice(0, 20);
        }

        interactive.push(item);
      });

    // Images
    const images = Array.from(document.querySelectorAll('img'))
      .slice(0, 30)
      .map(img => ({
        src: img.src || img.getAttribute('data-src') || '',
        alt: img.alt || '',
        loaded: img.complete && img.naturalHeight > 0,
      }));

    // Performance
    const perf = window.performance?.timing;
    const performance = {
      load_time_ms: perf ? (perf.loadEventEnd - perf.navigationStart) : 0,
      dom_elements: document.querySelectorAll('*').length,
    };

    // Meta tags
    const meta = {};
    document.querySelectorAll('meta[name], meta[property]').forEach(m => {
      const key = m.getAttribute('name') || m.getAttribute('property');
      if (key) meta[key] = m.getAttribute('content') || '';
    });

    return { navigation, forms, interactive_elements: interactive, images, performance, meta };
  });
}

// Main exploration function
async function explore(opts) {
  const [vw, vh] = opts.viewport.split('x').map(Number);

  const browser = await chromium.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage'],
  });

  const context = await browser.newContext({
    viewport: { width: vw || 1280, height: vh || 720 },
    userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
  });

  const page = await context.newPage();

  // Capture console errors
  const consoleErrors = [];
  page.on('console', msg => {
    if (msg.type() === 'error') {
      consoleErrors.push(msg.text());
    }
  });

  // Capture API calls during load
  const apiCalls = [];
  page.on('response', response => {
    const url = response.url();
    if (url.includes('/api/') || url.includes('/graphql')) {
      apiCalls.push({
        url: url,
        method: response.request().method(),
        status: response.status(),
      });
    }
  });

  try {
    // Navigate to the page
    await page.goto(opts.url, { waitUntil: 'domcontentloaded', timeout: 30000 });

    // Detect SPA framework
    let spaInfo = null;
    if (opts.spa) {
      spaInfo = await detectFramework(page);
      await waitForSPAReady(page, spaInfo, opts.spaTimeout);
      spaInfo.api_calls_during_load = apiCalls;
    }

    // Extract page elements
    const elements = await extractPageElements(page);

    // Get page title
    const title = await page.title();

    // Discover same-origin links for pages_found
    const pagesFound = [...new Set(
      elements.navigation
        .map(n => {
          try { return new URL(n.href).pathname; } catch { return null; }
        })
        .filter(p => p !== null)
    )].slice(0, 30);

    const result = {
      url: page.url(),
      title,
      pages_found: pagesFound,
      navigation: elements.navigation,
      forms: elements.forms,
      interactive_elements: elements.interactive_elements,
      images: elements.images,
      console_errors: consoleErrors,
      performance: elements.performance,
      meta: elements.meta,
      spa_info: spaInfo,
    };

    // Output JSON to stdout
    console.log(JSON.stringify(result));

  } finally {
    await browser.close();
  }
}

// Run
const opts = parseArgs();
explore(opts).catch(err => {
  console.error(`Exploration failed: ${err.message}`);
  process.exit(1);
});
