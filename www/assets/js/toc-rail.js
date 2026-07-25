/* ==========================================================================
   Fixed left scroll-spy rail (#toc-rail) — shared behaviour.
   Pair with /assets/css/toc-rail.css. Load at the end of <body>; safe to
   include on pages that have no rail. See www/AGENTS.md for the contract.

   MARKUP — one attribute configures the page:

     <aside id="toc-rail" data-rail="section" aria-label="Section navigation">
       ...dots derived from .pillrow if you leave it empty...
     </aside>

     data-rail="section"  whitepapers + homepage — section scale,
                          measures "section .container"
     data-rail="blog"     blog posts — compact scale, measures "article"

   DOTS come from one of two places:
     1. Authored — any .toc-dot children you write are used as-is. Use this
        when the rail is editorial (a curated subset, custom labels/colours),
        as on the homepage and the blog posts.
     2. Derived — leave the <aside> empty on a whitepaper and the dots are
        built from that page's own .pillrow, reusing its targets, labels and
        colours. The rail then cannot drift from the pill row.

     data-rail-extra="id:Label:colour;..."   appends rail-only dots after the
        derived ones (the whitepapers list their glossary this way, since the
        pill rows deliberately omit it).

     data-measure="<selector>"   escape hatch to override the measured
        content column when a page is laid out unusually.

   Also drives, when present: a.pill / details.toc a / .nav-links a
   smooth-scrolling, and the #to-top button.
   ========================================================================== */
(function () {
  'use strict';

  var FAMILY = {
    section: { measure: 'section .container' },
    blog: { measure: 'article' }
  };

  function warn(msg) {
    if (window.console && console.warn) console.warn('[toc-rail] ' + msg);
  }

  // Bind every in-page anchor style this site uses. Each selector is a
  // harmless no-op on pages that lack it. Called after any derivation so
  // generated dots are included.
  function bindAnchors() {
    document.querySelectorAll(
      '#toc-rail .toc-dot, details.toc a[href^="#"], a.pill, .nav-links a[href^="#"]'
    ).forEach(function (a) {
      if (a.dataset.railBound) return;
      a.dataset.railBound = '1';
      a.addEventListener('click', function (e) {
        var id = a.getAttribute('href');
        if (id && id.charAt(0) === '#' && id.length > 1) {
          var el = document.querySelector(id);
          if (el) {
            e.preventDefault();
            el.scrollIntoView({ behavior: 'smooth', block: 'start' });
            history.replaceState(null, '', id);
          }
        }
      });
    });
  }

  // Back-to-top button (whitepapers). Independent of the rail.
  var toTop = document.getElementById('to-top');
  function syncTop() {
    if (toTop) toTop.classList.toggle('show', window.scrollY > 600);
  }
  if (toTop) {
    toTop.addEventListener('click', function () {
      window.scrollTo({ top: 0, behavior: 'smooth' });
    });
  }

  var railEl = document.getElementById('toc-rail');
  if (!railEl) {
    bindAnchors();
    syncTop();
    window.addEventListener('scroll', syncTop, { passive: true });
    return;
  }

  if (!railEl.hasAttribute('data-rail')) {
    railEl.setAttribute('data-rail', 'section');
  }
  var family = FAMILY[railEl.getAttribute('data-rail')] || FAMILY.section;

  function makeDot(id, label, colour, cls) {
    var a = document.createElement('a');
    a.className = 'toc-dot' + (cls ? ' ' + cls : '');
    a.href = '#' + id;
    a.setAttribute('data-target', id);
    var d = document.createElement('span');
    d.className = 'd';
    if (colour) d.style.background = colour;
    var l = document.createElement('span');
    l.className = 'lbl';
    l.textContent = label;
    a.appendChild(d);
    a.appendChild(l);
    return a;
  }

  // "id:Label:colour" — split from the outside in so a label may contain ':'
  function parseExtra(spec) {
    var parts = spec.split(':');
    if (parts.length < 2) return null;
    return {
      id: parts[0].trim(),
      colour: parts.length > 2 ? parts[parts.length - 1].trim() : '',
      label: (parts.length > 2 ? parts.slice(1, -1) : parts.slice(1)).join(':').trim()
    };
  }

  // Derive the rail from the page's own pill row, so the two cannot disagree.
  function deriveFromPillrow() {
    var pills = document.querySelectorAll('.pillrow a.pill');
    if (!pills.length) pills = document.querySelectorAll('a.pill');
    var frag = document.createDocumentFragment();
    var made = 0;

    Array.prototype.forEach.call(pills, function (p) {
      var href = p.getAttribute('href') || '';
      if (href.charAt(0) !== '#' || href.length < 2) return;
      var id = href.slice(1);
      if (!document.getElementById(id)) {
        warn('pill "' + href + '" has no matching element — skipped');
        return;
      }
      var swatch = p.querySelector('.dot');
      var colour = swatch ? (swatch.style.background || swatch.style.backgroundColor) : '';
      frag.appendChild(makeDot(id, p.textContent.trim(), colour));
      made++;
    });

    (railEl.getAttribute('data-rail-extra') || '').split(';').forEach(function (spec) {
      if (!spec.trim()) return;
      var x = parseExtra(spec);
      if (!x) { warn('malformed data-rail-extra entry: ' + spec); return; }
      if (!document.getElementById(x.id)) {
        warn('data-rail-extra "' + x.id + '" has no matching element — skipped');
        return;
      }
      frag.appendChild(makeDot(x.id, x.label, x.colour));
      made++;
    });

    if (made) railEl.appendChild(frag);
    return made;
  }

  if (!railEl.querySelector('.toc-dot')) {
    if (!deriveFromPillrow()) {
      warn('rail is empty and no .pillrow was found to derive it from');
      return;
    }
  }

  bindAnchors();

  var railLinks = Array.prototype.slice.call(railEl.querySelectorAll('.toc-dot'));

  // A dot whose target no longer exists is dead weight: it can never activate,
  // and clicking it does nothing. Derived dots are skipped at build time; hide
  // authored ones here so both routes behave the same way.
  var dangling = railLinks.filter(function (a) { return !document.getElementById(a.dataset.target); });
  if (dangling.length) {
    warn('dot(s) point at an id that does not exist and were hidden: ' +
         dangling.map(function (a) { return '#' + a.dataset.target; }).join(', '));
    dangling.forEach(function (a) { a.hidden = true; });
    railLinks = railLinks.filter(function (a) { return !a.hidden; });
  }

  var railSections = railLinks
    .map(function (a) { return document.getElementById(a.dataset.target); })
    .filter(Boolean);

  // Nested sub-items: revealed only while their parent group is current.
  // ONE group per page — every .sub is associated with the first .has-subs.
  if (railEl.querySelectorAll('.toc-dot.has-subs').length > 1) {
    warn('more than one .has-subs group found; only the first is used and every ' +
         '.sub dot is associated with it');
  }
  var groupParent = railEl.querySelector('.toc-dot.has-subs');
  var groupIds = groupParent
    ? [groupParent.dataset.target].concat(
        Array.prototype.map.call(railEl.querySelectorAll('.toc-dot.sub'), function (a) {
          return a.dataset.target;
        })
      )
    : [];
  if (railEl.querySelector('.toc-dot.sub')) railEl.classList.add('rail-subs');

  // getBoundingClientRect()+scrollY rather than offsetTop: offsetTop is relative
  // to the offset parent, so it only agrees for targets that are direct children
  // of an unpositioned ancestor. This form is correct for every page layout.
  function docTop(el) { return el.getBoundingClientRect().top + window.scrollY; }

  function syncRail() {
    if (!railSections.length) return;
    var probe = window.scrollY + window.innerHeight * 0.30;
    var current = railSections[0];
    railSections.forEach(function (s) { if (docTop(s) <= probe) current = s; });
    railLinks.forEach(function (a) {
      a.classList.toggle('active', a.dataset.target === current.id);
    });
    if (groupIds.length) {
      railEl.classList.toggle('show-subs', groupIds.indexOf(current.id) !== -1);
    }
  }

  // Keep the rail from overlapping content when the page is zoomed/magnified
  // or the window is narrow. Measures the real left margin at the current zoom.
  var measureSel = railEl.getAttribute('data-measure') || family.measure;
  var railMeasure = document.querySelector(measureSel);
  if (!railMeasure) {
    warn('no element matches "' + measureSel + '" — the rail cannot tell when it ' +
         'overlaps content, so it will never auto-hide. Check data-rail/data-measure.');
  }
  function fitRail() {
    if (!railMeasure) return;
    if (railEl.matches(':hover')) return; // don't reflow while the user is using it
    var contentEdge = railMeasure.getBoundingClientRect().left + 24; // text start (24px container padding)
    railEl.classList.remove('rail-hidden', 'no-active-label');
    // 0) Does the rail fit VERTICALLY? Pills deliberately do not flex-shrink
    //    (squashed pills look broken), so a rail taller than the viewport would
    //    spill off both ends with its end dots unreachable. The stylesheet
    //    tightens spacing on short viewports first; if that is still not enough
    //    — a very long rail, or a very short window — hide it rather than clip.
    //    Measured after the class removal above so it reads the untightened height.
    if (railEl.scrollHeight > window.innerHeight * 0.92) {
      railEl.classList.add('rail-hidden');
      return;
    }
    // 1) Does a collapsed dot pill fit in the left margin at all? (non-active
    //    items stay collapsed, so they measure the bare dot footprint.)
    var collapsed = railEl.querySelector('.toc-dot:not(.active)') || railEl;
    if (collapsed.getBoundingClientRect().right + 10 > contentEdge) {
      railEl.classList.add('rail-hidden');
      return;
    }
    // 2) Does the expanded active pill (dot + label) stay out of the content?
    var active = railEl.querySelector('.toc-dot.active');
    if (active && active.getBoundingClientRect().right + 10 > contentEdge) {
      railEl.classList.add('no-active-label');
    }
  }

  var ticking = false;
  window.addEventListener('scroll', function () {
    if (!ticking) {
      window.requestAnimationFrame(function () {
        syncRail(); syncTop(); fitRail(); ticking = false;
      });
      ticking = true;
    }
  }, { passive: true });
  window.addEventListener('resize', function () { syncRail(); fitRail(); }, { passive: true });
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', function () { syncRail(); fitRail(); });
  }
  syncRail(); syncTop(); fitRail();
})();
