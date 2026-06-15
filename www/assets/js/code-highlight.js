/**
 * Tiny, dependency-free JSON syntax highlighter shared across TruvaG3 pages.
 * Pair with /assets/css/code-highlight.css.
 *
 * Usage — two ways to opt a block in:
 *   1. Page-level:  put data-highlight="json" on <body> (or any ancestor).
 *                   Every plain-text <pre><code> under it is highlighted.
 *   2. Per-block:   add class="lang-json" to a <code> element.
 *
 * Safety: blocks that already contain markup (e.g. a manual
 * <span class="hi"> highlight) are left untouched, so this can be loaded on
 * multi-language pages without clobbering hand-authored code samples.
 *
 * Idempotent: re-running does nothing to already-processed blocks.
 */
(function () {
  function esc(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // Order matters: key (string + colon) → string → literal → number → punctuation.
  var RE = /("(?:\\.|[^"\\])*")(\s*:)|("(?:\\.|[^"\\])*")|\b(true|false|null)\b|(-?\d+(?:\.\d+)?(?:[eE][+\-]?\d+)?)|([{}\[\],:])/g;

  function highlight(code) {
    return esc(code).replace(RE, function (m, keyStr, keyColon, str, lit, num, punct) {
      if (keyStr !== undefined) return '<span class="t-key">' + keyStr + '</span><span class="t-punct">' + keyColon + '</span>';
      if (str    !== undefined) return '<span class="t-str">' + str + '</span>';
      if (lit    !== undefined) return '<span class="t-lit">' + lit + '</span>';
      if (num    !== undefined) return '<span class="t-num">' + num + '</span>';
      if (punct  !== undefined) return '<span class="t-punct">' + punct + '</span>';
      return m;
    });
  }

  function apply(code) {
    if (code.dataset.hl) return;          // already done
    if (code.children.length > 0) return; // has manual markup — leave it alone
    code.innerHTML = highlight(code.textContent);
    code.dataset.hl = '1';
  }

  function run() {
    // Per-block opt-in
    document.querySelectorAll('code.lang-json, code.language-json').forEach(apply);
    // Page/section-level opt-in
    document.querySelectorAll('[data-highlight~="json"]').forEach(function (scope) {
      scope.querySelectorAll('pre > code').forEach(apply);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', run);
  } else {
    run();
  }
})();
