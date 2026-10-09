// islands.js — loader for the two rendering islands (ADR 0003 concessions):
// mermaid diagrams and KaTeX math. Both degrade without JS — mermaid source
// stays a readable code block, TeX stays readable TeX — and the heavy
// vendored libraries are fetched only when the current page actually
// contains something to render. Re-scans after htmx swaps (hx-boost
// navigation replaces #main without a full page load).
(function () {
  "use strict";

  var loaded = {}; // src -> Promise
  function loadScript(src) {
    if (!loaded[src]) {
      loaded[src] = new Promise(function (resolve, reject) {
        var s = document.createElement("script");
        s.src = src;
        s.onload = resolve;
        s.onerror = function () {
          s.remove();
          reject(new Error("failed to load " + src));
        };
        document.head.appendChild(s);
      }).catch(function (err) {
        // Drop the rejected promise from the cache so the next scan
        // (htmx swap, reload-less retry) attempts the fetch again
        // instead of being pinned to a transient failure forever.
        delete loaded[src];
        throw err;
      });
    }
    return loaded[src];
  }
  function loadCSS(href) {
    if (!loaded[href]) {
      // Same failure-aware caching as loadScript: resolve only when the
      // stylesheet really loaded, evict on error so a later scan retries
      // (otherwise KaTeX could render unstyled for the whole session
      // after one transient fetch failure).
      loaded[href] = new Promise(function (resolve, reject) {
        var l = document.createElement("link");
        l.rel = "stylesheet";
        l.href = href;
        l.onload = resolve;
        l.onerror = function () {
          l.remove();
          reject(new Error("failed to load " + href));
        };
        document.head.appendChild(l);
      }).catch(function (err) {
        delete loaded[href];
        throw err;
      });
    }
    return loaded[href];
  }

  // --- mermaid -----------------------------------------------------------
  // The markdown adapter renders ```mermaid fences as
  // <pre><code class="language-mermaid">…</code></pre>. Swap each into a
  // <pre class="mermaid"> holding the raw source and let mermaid.run()
  // replace it with the SVG. On render failure mermaid leaves an error
  // bomb — keep the original block instead by restoring it.
  function renderMermaid(root) {
    var blocks = root.querySelectorAll("pre > code.language-mermaid");
    if (!blocks.length) return;
    loadScript("/static/vendor/mermaid.min.js").then(function () {
      window.mermaid.initialize({
        startOnLoad: false,
        securityLevel: "strict",
        // "neutral" is mermaid's greyscale theme: diagrams print in the room's ink.
        theme: window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "neutral",
      });
      var targets = [];
      blocks.forEach(function (code) {
        var holder = document.createElement("pre");
        holder.className = "mermaid";
        holder.textContent = code.textContent;
        var orig = code.parentElement;
        orig.replaceWith(holder);
        targets.push({ holder: holder, orig: orig });
      });
      return window.mermaid
        .run({ nodes: targets.map(function (t) { return t.holder; }) })
        .catch(function () {
          targets.forEach(function (t) {
            // A diagram that failed to parse degrades back to the
            // readable source block (mermaid may have already replaced
            // good ones; only restore holders without an svg).
            if (!t.holder.querySelector("svg")) t.holder.replaceWith(t.orig);
          });
        });
    }).catch(function (err) {
      // Library failed to load — blocks stay readable source (the
      // designed degradation); log for debuggability, never throw.
      console.warn("mermaid island unavailable:", err);
    });
  }

  // --- KaTeX -------------------------------------------------------------
  // The markdown adapter passes \( … \), \[ … \] and $$ … $$ through
  // verbatim; auto-render scans text nodes for those delimiters. Cheap
  // textContent probe gates the (large) library fetch.
  function renderMath(root) {
    var text = root.textContent;
    if (text.indexOf("\\(") === -1 && text.indexOf("$$") === -1 && text.indexOf("\\[") === -1) return;
    Promise.all([
      loadCSS("/static/vendor/katex.min.css"),
      loadScript("/static/vendor/katex.min.js"),
    ])
      .then(function () { return loadScript("/static/vendor/katex-auto-render.min.js"); })
      .then(function () {
        window.renderMathInElement(root, {
          delimiters: [
            { left: "$$", right: "$$", display: true },
            { left: "\\[", right: "\\]", display: true },
            { left: "\\(", right: "\\)", display: false },
          ],
          // Leave unparseable TeX as source text rather than a thrown
          // error aborting the whole scan.
          throwOnError: false,
          // Never evaluate \href and friends from org-authored content.
          trust: false,
        });
      })
      .catch(function (err) {
        // Library failed to load — TeX stays readable source (the
        // designed degradation); log for debuggability, never throw.
        console.warn("katex island unavailable:", err);
      });
  }

  function scan(root) {
    renderMermaid(root);
    renderMath(root);
    hydrateMaps(root);
  }

  // --- trail canvas ------------------------------------------------------
  // The one piece of JS the trail engine needs (ADR 0005): new panes open
  // at the right edge, so bring the focused pane into view after each
  // render. htmx's own `show:` modifier is vertical-biased; this is the
  // pre-agreed snippet. Everything else about the canvas is server state.
  // Horizontal-only, on the canvas scroller itself: scrollIntoView with a
  // pane taller than the viewport also aligned its top edge with the
  // viewport, scrolling the page down past the nav — the canvas's own
  // scrollLeft is the only axis "into view" ever meant here.
  function showFocusedPane() {
    var pane = document.querySelector(".pane.focused");
    var canvas = pane && pane.closest && pane.closest("main.canvas");
    if (!pane || !canvas) return;
    var pr = pane.getBoundingClientRect();
    var cr = canvas.getBoundingClientRect();
    if (pr.right > cr.right) canvas.scrollLeft += pr.right - cr.right;
    if (pr.left < cr.left) canvas.scrollLeft -= cr.left - pr.left;
  }

  // Page navigation cross-fades; camera and graph exploration stay immediate.
  document.addEventListener("htmx:config:request", function (e) {
    var ctx = e.detail && e.detail.ctx, t = ctx && ctx.target;
    if (!ctx || calm.matches) return;
    if (typeof t === "string") t = document.querySelector(t);
    if (t === document.body) ctx.transition = true;
  });

  // htmx boosts only tagName "A", never an SVG <a>, so graph and map nodes
  // reloaded the page: hand the click to a hidden HTML anchor. Aggregates
  // (own hx-get) and modified clicks (new tab) are left alone.
  document.addEventListener("click", function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    var node = e.target.closest && e.target.closest("svg a[href]");
    if (!node || node.hasAttribute("hx-get") || !window.htmx) return;
    if (node.hasAttribute("data-recenter") && node.closest(".graph-panel.exploring")) return;
    var proxy = document.createElement("a");
    proxy.href = node.getAttribute("href");
    proxy.hidden = true;
    document.body.appendChild(proxy);
    window.htmx.process(proxy);
    proxy.addEventListener("htmx:after:request", function () { proxy.remove(); });
    e.preventDefault();
    proxy.click();
  });

  // --- reading position and progress ----------------------------------
  // Every click rebuilds the canvas, so panes would land at their top: keep
  // each document's position (pane head's world+path) for the session.
  // Presentational only; storage failing just means starting at the top.
  var positionsKey = "demarkus:positions", positionsMax = 300;
  var positions = (function () {
    try { return JSON.parse(sessionStorage.getItem(positionsKey)) || {}; } catch (err) { return {}; }
  })();
  function scrollPanes() {
    return document.querySelectorAll("body.pane-scroll .pane:not(.spine)");
  }
  function paneKey(pane) {
    var code = pane.querySelector(".pane-head code");
    return code ? code.textContent : "";
  }
  function savePositions() {
    scrollPanes().forEach(function (p) {
      var k = paneKey(p);
      if (!k) return;
      delete positions[k]; // re-insert so the newest stays last when trimming
      positions[k] = Math.round(p.scrollTop);
    });
    var keys = Object.keys(positions);
    keys.slice(0, Math.max(0, keys.length - positionsMax)).forEach(function (k) { delete positions[k]; });
    try { sessionStorage.setItem(positionsKey, JSON.stringify(positions)); } catch (err) { /* private mode: keep in memory */ }
  }
  function restorePositions() {
    scrollPanes().forEach(function (p) {
      var top = positions[paneKey(p)];
      if (top) p.scrollTop = top;
    });
  }
  // E-reader furniture: a document pane's head reads "38% · 6 min left".
  var wordsPerMinute = 230;
  function progressEl(pane) {
    var body = pane.querySelector(".doc-body"), head = pane.querySelector(".pane-head");
    if (!body || !head || body.querySelector(".listing, #librarian-transcript, .floor-bar")) return null;
    var el = head.querySelector(".pane-progress");
    if (!el) {
      el = head.appendChild(document.createElement("span"));
      el.className = "pane-progress";
      pane._words = (body.textContent.match(/\S+/g) || []).length;
    }
    return el;
  }
  function showProgress(pane, el) {
    el = el || progressEl(pane);
    if (!el) return;
    var max = pane.scrollHeight - pane.clientHeight;
    var total = Math.max(1, Math.round(pane._words / wordsPerMinute)), text = total + " min read";
    if (max > 0) {
      var done = Math.min(1, pane.scrollTop / max), left = Math.round(total * (1 - done));
      text = Math.round(done * 100) + "% \u00b7 " + (left > 0 ? left + " min left" : "end");
    }
    if (el.textContent !== text) el.textContent = text;
  }
  // Every span is added before any pane is measured: one layout, not one each.
  function showAllProgress() {
    var panes = Array.prototype.slice.call(scrollPanes()), els = panes.map(progressEl);
    panes.forEach(function (p, i) { if (els[i]) showProgress(p, els[i]); });
  }
  var progressFrame = null;
  document.addEventListener("scroll", function (e) {
    var pane = e.target.closest && e.target.closest("body.pane-scroll .pane");
    if (!pane || progressFrame) return;
    progressFrame = requestAnimationFrame(function () { progressFrame = null; showProgress(pane); });
  }, { capture: true, passive: true });
  // Saved before a page swap, restored on settle (below): settle runs inside
  // the view transition, before the new state is captured, so no jump.
  document.addEventListener("htmx:before:swap", function (e) {
    if (e.detail && e.detail.ctx && e.detail.ctx.target === document.body) {
      savePositions();
      releaseBackground();
      endPan(false);
      stopMotion();
    }
  });
  window.addEventListener("pagehide", savePositions);

  // ADR 0003 concession: the ask form clears once its answer is swapped in
  // (after:swap, so a failed ask keeps the question). A listener rather than
  // an hx-on attribute: the page CSP forbids the eval'd script hx-on needs.
  document.addEventListener("htmx:after:swap", function (e) {
    var el = e.target;
    if (el instanceof Element && el.matches("form.ask-form")) el.reset();
  });

  // Click engagement: clicking into a pane moves the VISUAL attention cue
  // only — never the URL focus (re-focusing would collapse the panes to its
  // right; the dock is the backtrack mechanism). Purely presentational: a
  // CSS class, no fetch, no URL change, gone on the next server render —
  // the same spirit as the graph hover-highlight above. Interactive targets
  // (links, the ask bar) keep their own behavior; the ask bar lights its
  // pane via :focus-within regardless.
  // ADR 0003 concession: bespoke JS beyond htmx attributes, justified as
  // presentational-only — a class toggle with no fetch, no URL change, no
  // client state; htmx attributes cannot express click-scoped CSS toggling,
  // and the no-JS room keeps the server-truth focus marker.
  document.addEventListener("click", function (e) {
    var pane = e.target.closest && e.target.closest(".pane.body, .pane.focused");
    if (!pane) return;
    if (e.target.closest("a, button, input, textarea, select, label, summary")) return;
    document.querySelectorAll(".pane.engaged").forEach(function (p) { p.classList.remove("engaged"); });
    pane.classList.add("engaged");
  });

  // --- reader overlay (R4) -----------------------------------------------
  // The overlay is pure URL state (?reader=i): the ✕, the backdrop scrim,
  // and browser Back all close it server-side. Esc is the expected reader
  // gesture; rather than pull in _hyperscript for one keybinding, click the
  // close link (hx-boost intercepts the bubbled click, so it stays a swap).
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    var close = document.querySelector(".reader-panel a.reader-close");
    if (close) close.click();
  });

  // --- command palette (⌃K) — ADR 0006 §3 -------------------------------
  // The palette is server-rendered (templates/palette.html) and its results
  // come from htmx (GET /palette → HTML fragment). This is only the keyboard
  // glue ADR 0003 sanctions as the interaction layer: toggle the overlay and
  // move an arrow selection. No fetch, no JSON, no client state — and it
  // degrades to the /search link the nav already points at.
  function palette() { return document.getElementById("palette"); }
  function openPalette() {
    var p = palette();
    if (!p) return;
    hideOverlay(openOverlay());
    p.hidden = false;
    var input = document.getElementById("palette-input");
    if (input) { input.value = ""; input.focus(); }
  }
  function closePalette() {
    var p = palette();
    if (p) p.hidden = true;
  }
  function movePaletteSel(delta) {
    var rows = Array.prototype.slice.call(
      document.querySelectorAll("#palette-results a"));
    if (!rows.length) return;
    var cur = document.querySelector("#palette-results a.sel");
    var i = rows.indexOf(cur);
    if (cur) cur.classList.remove("sel");
    // Nothing selected yet: ArrowDown → first row, ArrowUp → last row.
    if (i === -1) i = delta > 0 ? 0 : rows.length - 1;
    else i = (i + delta + rows.length) % rows.length;
    rows[i].classList.add("sel");
    rows[i].scrollIntoView({ block: "nearest" });
  }
  // The nav "Search" link is a real /search href; with JS it opens the overlay.
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("a.nav-search");
    if (link) { e.preventDefault(); openPalette(); }
  });
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
      e.preventDefault();
      var p = palette();
      if (p && p.hidden) openPalette(); else closePalette();
      return;
    }
    var p = palette();
    if (!p || p.hidden) return;
    if (e.key === "Escape") { e.preventDefault(); closePalette(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); movePaletteSel(1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); movePaletteSel(-1); }
    else if (e.key === "Enter") {
      var sel = document.querySelector("#palette-results a.sel");
      if (sel) { e.preventDefault(); sel.click(); }
    }
  });

  // --- on-demand overlay focus (graph §4, world map §5, universe §6) ----
  // Shared focus handling for the pull-up overlays: move focus into the panel
  // (the role="dialog" element) on open, and restore it on close to whatever
  // had focus — the trigger link for a click, or the active element for a
  // hotkey toggle. Keyboard/screen-reader users land in the dialog and return
  // where they were. The backdrop is the scrim; the panel is the dialog.
  // Hotkeys stay out of text fields.
  function typingIn(e) {
    var tag = (e.target.tagName || "").toLowerCase();
    return tag === "input" || tag === "textarea" || tag === "select" || !!e.target.isContentEditable;
  }
  var calm = window.matchMedia("(prefers-reduced-motion: reduce)");
  var overlayFade = 140; // ms: matches .graph-backdrop.closing in library.css
  // Fading out counts as closed, so a hotkey pressed mid-fade reopens.
  function shown(el) { return !!el && !el.hidden && !el.classList.contains("closing"); }
  var inertBackground = [];
  function releaseBackground() {
    inertBackground.forEach(function (el) { el.inert = false; });
    inertBackground = [];
  }
  function showOverlay(el, restore) {
    if (!el) return;
    var previous = openOverlay();
    var returnTo = previous ? previous._restoreFocus : restore || document.activeElement;
    if (previous && previous !== el) { hideOverlay(previous); previous.hidden = true; }
    releaseBackground();
    Array.from(document.body.children).forEach(function (child) {
      if (child !== el && !child.inert && !child.matches("script, style, link")) {
        child.inert = true;
        inertBackground.push(child);
      }
    });
    clearTimeout(el._closing); // re-summoned mid-fade: keep it open
    el.classList.remove("closing");
    el._restoreFocus = returnTo;
    el.hidden = false;
    var panel = el.querySelector(".graph-panel") || el;
    panel.setAttribute("tabindex", "-1");
    panel.focus();
    wmDropRects();
    panel.querySelectorAll(".graph-canvas svg").forEach(function (svg) {
      if (wmStates.has(svg)) wmSyncView(svg);
    });
  }
  function hideOverlay(el) {
    if (!el || el.hidden || el.classList.contains("closing")) return;
    stopMotion();
    endPan(false);
    releaseBackground();
    var r = el._restoreFocus;
    if (r && r.isConnected && r.focus) r.focus();
    if (calm.matches) { el.hidden = true; return; }
    el.classList.add("closing");
    el._closing = setTimeout(function () {
      el.hidden = true;
      el.classList.remove("closing");
    }, overlayFade);
  }

  // One keyboard scope: Tab stays in the workspace and Escape never also
  // dismisses the reader underneath it. Filter Escape clears its query first.
  document.addEventListener("keydown", function (e) {
    var overlay = openOverlay();
    if (!overlay) return;
    if (e.key === "Escape" && !(e.target.matches(".map-filter") && e.target.value)) {
      e.preventDefault(); e.stopPropagation(); hideOverlay(overlay); return;
    }
    if (e.key !== "Tab") return;
    var stops = Array.from(overlay.querySelectorAll("a[href], button, input, [tabindex]"))
      .filter(function (el) { return el.getAttribute("tabindex") !== "-1" && !el.disabled && el.getClientRects().length; });
    var index = stops.indexOf(document.activeElement);
    if (!stops.length) { e.preventDefault(); return; }
    if (index < 0 || (e.shiftKey ? index === 0 : index === stops.length - 1)) {
      e.preventDefault(); stops[e.shiftKey ? stops.length - 1 : 0].focus();
    }
  }, true);

  document.addEventListener("click", function (e) {
    var button = e.target.closest && e.target.closest("[data-graph-action]");
    if (!button) return;
    var overlay = button.closest(".graph-backdrop"), panel = button.closest(".graph-panel");
    if (!overlay || !panel) return;
    var svg = panel.querySelector(zoomable), action = button.dataset.graphAction;
    if (action === "close") { hideOverlay(overlay); return; }
    if (action === "refresh") { loadMap(overlay, true); return; }
    if (action === "explore") {
      var on = panel.classList.toggle("exploring");
      button.setAttribute("aria-pressed", String(on));
      panel.querySelector(".graph-foot").dataset.idle = on ?
        "Click a neighbour to explore · Use the history above to go back" :
        "Click to read · Shift-click to explore · Scroll to zoom · Drag to pan";
      return;
    }
    if (!svg) return;
    if (action === "fit") resetBox(svg);
    else glideBox(svg, zoomTarget(svg, action === "in" ? wmKeyStep : 1 / wmKeyStep, null));
  });

  // --- graph overlay (g) — ADR 0006 §4 ----------------------------------
  // The overlay is server-rendered (templates/graph-overlay); this is the
  // summon/dismiss glue ADR 0003 sanctions. Node clicks are plain trail links,
  // so navigating dismisses it. Degrades: the margin "graph" link is a real /g/
  // permalink; we only intercept it on the canvas (where the overlay exists).
  function graphOverlay() { return document.getElementById("graph-overlay"); }
  function openGraph(restore) { showOverlay(graphOverlay(), restore); }
  function closeGraph() { hideOverlay(graphOverlay()); }
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("a.graph-open");
    if (link && graphOverlay()) { e.preventDefault(); openGraph(link); return; }
    if (e.target.id === "graph-overlay") closeGraph(); // click outside the panel
  });
  document.addEventListener("keydown", function (e) {
    var g = graphOverlay();
    if (e.key === "Escape" && shown(g)) { e.preventDefault(); closeGraph(); return; }
    if (e.key !== "g" || e.ctrlKey || e.metaKey || e.altKey || typingIn(e)) return;
    var p = palette();
    if ((p && !p.hidden) || !g) return; // not while the palette is open / no graph here
    e.preventDefault();
    shown(g) ? closeGraph() : openGraph();
  });

  // --- graph exploration (shift-click) ---------------------------------
  // Shift-click re-centres the graph on a neighbour (its data-recenter
  // fragment); crumbs or Backspace step back, a plain click opens. The walk
  // lives on the overlay element, so a page swap starts fresh.
  function graphParts() {
    var o = graphOverlay();
    return o && { overlay: o, panel: o.querySelector(".graph-panel"),
      canvas: o.querySelector(".graph-canvas"), title: o.querySelector(".graph-title") };
  }
  function renderCrumbs(g) {
    var walk = g.overlay._walk || [], nav = g.overlay.querySelector(".graph-crumbs");
    nav.hidden = !walk.length;
    nav.replaceChildren();
    walk.forEach(function (step, i) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "graph-crumb";
      b.textContent = step.title;
      b.dataset.step = i;
      nav.appendChild(b);
      nav.appendChild(document.createTextNode(" \u203a "));
    });
    nav.appendChild(document.createTextNode(g.title.textContent));
    wmDropRects();
    var svg = g.canvas.querySelector("svg.graph");
    if (svg && wmStates.has(svg)) wmSyncView(svg);
  }
  function recentre(node) {
    var g = graphParts();
    if (!g || !window.htmx || g.overlay._loading) return;
    // Keep this view to step back to, minus any hover highlight.
    var keep = g.canvas.cloneNode(true);
    clearHot(keep);
    var original = g.canvas.firstElementChild, svg = g.canvas.querySelector("svg.graph");
    var state = svg && wmState(svg);
    var step = { title: g.title.textContent, html: keep.innerHTML,
      base: state && state.base.slice(), box: state && curBox(svg).slice() };
    g.overlay._loading = true;
    g.canvas.setAttribute("aria-busy", "true");
    window.htmx.ajax("GET", node.getAttribute("data-recenter"), g.canvas).then(function () {
      if (g.canvas.firstElementChild === original) throw new Error("graph response was not rendered");
      var walk = g.overlay._walk = g.overlay._walk || [];
      walk.push(step);
      if (walk.length > 40) walk.shift();
      renderCrumbs(g);
    }).catch(function (err) {
      console.warn("graph exploration unavailable:", err);
      g.panel.querySelector(".graph-foot").textContent = "Could not load this neighbourhood. Try again.";
    }).finally(function () {
      g.overlay._loading = false;
      g.canvas.removeAttribute("aria-busy");
    });
  }
  function stepBack(i) {
    var g = graphParts(), walk = g && g.overlay._walk;
    if (!walk || g.overlay._loading || i < 0 || i >= walk.length) return;
    var step = walk[i];
    g.overlay._walk = walk.slice(0, i);
    g.canvas.innerHTML = step.html; // markup this page rendered earlier
    g.title.textContent = step.title;
    var svg = g.canvas.querySelector("svg.graph");
    if (svg && step.base) svg.setAttribute("viewBox", step.base.join(" "));
    renderCrumbs(g);
    hydrateMaps(g.canvas);
    if (svg && step.box) setBox(svg, step.box);
  }
  // A re-centred graph arriving from the server names its centre immediately.
  function graphArrived(svg) {
    var g = graphParts();
    if (!g || !g.canvas.contains(svg)) return;
    var centre = svg.querySelector(".graph-center-label");
    if (centre) g.title.textContent = centre.textContent;
    renderCrumbs(g);
  }
  document.addEventListener("click", function (e) {
    var crumb = e.target.closest && e.target.closest(".graph-crumb");
    if (crumb) { stepBack(+crumb.dataset.step); return; }
    var node = e.target.closest && e.target.closest("#graph-overlay svg a[data-recenter]");
    if (!node || e.metaKey || e.ctrlKey || e.altKey || e.button !== 0) return;
    if (!e.shiftKey && !node.closest(".exploring")) return;
    e.preventDefault(); // shift-click would open a new window
    recentre(node);
  });
  document.addEventListener("keydown", function (e) {
    var g = graphParts();
    if (e.key !== "Backspace" || !g || !shown(g.overlay) || typingIn(e) || !(g.overlay._walk || []).length) return;
    e.preventDefault();
    stepBack(g.overlay._walk.length - 1);
  });

  // --- world-map overlay (m) — ADR 0006 §5 ------------------------------
  // Same overlay chrome as the graph, but lazy: the SVG is htmx-loaded into
  // #map-canvas on summon (the map needs a catalog read, so an unopened map
  // costs nothing). Node clicks are trail links → navigating dismisses it.
  function mapOverlay() { return document.getElementById("map-overlay"); }
  function openMap(restore) {
    var m = mapOverlay();
    if (!m) return;
    showOverlay(m, restore);
    loadMap(m);
  }
  // Reopening keeps the camera, filter and expanded groups. Refresh is explicit;
  // failed loads remain retryable, and rapid summons share the in-flight request.
  function loadMap(overlay, refresh) {
    var canvas = overlay.querySelector(".graph-canvas");
    if (!window.htmx || overlay._loading || (overlay._loaded && !refresh)) return;
    overlay._loading = true;
    var original = canvas.firstElementChild;
    canvas.removeAttribute("data-error");
    canvas.setAttribute("aria-busy", "true");
    var url = overlay.dataset.mapUrl || overlay.dataset.universeUrl;
    window.htmx.ajax("GET", url, canvas).then(function () {
      if (canvas.firstElementChild === original || !canvas.querySelector("svg, .floor-empty")) {
        throw new Error("graph response was not rendered");
      }
      overlay._loaded = true;
    }).catch(function (err) {
      console.warn("map unavailable:", err);
      canvas.setAttribute("data-error", "Graph unavailable. Use Refresh to try again.");
      overlay.querySelector(".graph-foot").textContent = "Could not load the graph. Use Refresh to try again.";
    }).finally(function () {
      overlay._loading = false;
      canvas.removeAttribute("aria-busy");
    });
  }
  function closeMap() { hideOverlay(mapOverlay()); }
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("a.map-open");
    if (link && mapOverlay()) { e.preventDefault(); openMap(link); return; }
    if (e.target.id === "map-overlay") closeMap(); // click outside the panel
  });
  document.addEventListener("keydown", function (e) {
    var m = mapOverlay();
    if (e.key === "Escape" && shown(m)) { e.preventDefault(); closeMap(); return; }
    if (e.ctrlKey || e.metaKey || e.altKey || typingIn(e)) return;
    // Zoom keys while a map overlay (world or universe) is up: + / - about
    // the centre, 0 resets.
    var o = openOverlay();
    var svg = o && o.querySelector(".graph-canvas svg");
    if (svg && (e.key === "+" || e.key === "=")) { e.preventDefault(); glideBox(svg, zoomTarget(svg, wmKeyStep, null)); return; }
    if (svg && (e.key === "-" || e.key === "_")) { e.preventDefault(); glideBox(svg, zoomTarget(svg, 1 / wmKeyStep, null)); return; }
    if (svg && e.key === "0") { e.preventDefault(); resetBox(svg); return; }
    if (e.key !== "m") return;
    var p = palette();
    if ((p && !p.hidden) || !m) return;
    e.preventDefault();
    shown(m) ? closeMap() : openMap();
  });

  // --- universe overlay (§6) --------------------------------------------
  // The floor's full-viewport map pull-up. Same lazy chrome as the world map,
  // summoned by the floor's "view as map" link (a.universe-open) so the universe
  // topology gets real estate as worlds multiply. No summon hotkey — the trigger
  // lives only on the floor pane (the landing). Degrades: with JS off the link
  // is a real ?view=map that renders the map inline on the floor pane.
  function universeOverlay() { return document.getElementById("universe-overlay"); }
  function openUniverse(restore) {
    var u = universeOverlay();
    if (!u) return;
    showOverlay(u, restore);
    loadMap(u);
  }
  function closeUniverse() { hideOverlay(universeOverlay()); }
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("a.universe-open");
    if (link && universeOverlay()) { e.preventDefault(); openUniverse(link); return; }
    if (e.target.id === "universe-overlay") closeUniverse(); // click outside the panel
  });
  document.addEventListener("keydown", function (e) {
    var u = universeOverlay();
    if (e.key === "Escape" && shown(u)) { e.preventDefault(); closeUniverse(); return; }
  });

  // --- librarian (a) ---------------------------------------------------
  // Keyboard glue for the ask box; the form itself is a real POST that works
  // without any of this. `a` puts the cursor in the ask box when a librarian
  // pane is open, else walks through the nav door (which joins the trail).
  document.addEventListener("keydown", function (e) {
    if (e.key !== "a" || e.ctrlKey || e.metaKey || e.altKey || typingIn(e)) return;
    var p = palette();
    if ((p && !p.hidden) || openOverlay()) return;
    var box = document.querySelector(".pane .ask-input");
    var door = document.querySelector("a.nav-librarian");
    if (!box && !door) return;
    e.preventDefault();
    box ? box.focus() : door.click();
  });
  // Enter asks, Shift+Enter breaks the line; nothing is sent while an answer
  // is still streaming (the server would refuse it as busy anyway).
  document.addEventListener("keydown", function (e) {
    var box = e.target;
    if (e.key !== "Enter" || e.shiftKey || e.isComposing || !box.matches || !box.matches("textarea.ask-input")) return;
    e.preventDefault();
    var form = box.form, room = form && form.closest(".librarian");
    if (!box.value.trim() || (room && room.querySelector(".ask-live"))) return;
    form.requestSubmit(form.querySelector(".ask-send"));
  });
  // A question that landed in the transcript clears the box for the next one.
  document.addEventListener("htmx:after:swap", function (e) {
    if (!e.target || e.target.id !== "librarian-exchanges") return;
    var box = document.querySelector(".ask-form .ask-input");
    if (box) { box.value = ""; box.focus(); }
  });

  // --- node-hover highlight (map + graph) ------------------------------
  // ADR 0003 concession (JS island): a hover affordance can't be expressed in
  // SSR/CSS because an edge's two endpoints aren't DOM-adjacent to either node,
  // so relating them needs a script. Purely presentational: no state that
  // outlives the SVG, no fetch, no bearing on the URL-as-state contract, and
  // it degrades to nothing without JS. Hovering a node lifts its incident
  // edges (.edge-hot) and the nodes they connect (.node-hot). The graph pane
  // toggles those classes in place. The world map, hundreds of nodes, must not
  // repaint on hover (a per-node opacity fade re-rasterized the whole overlay
  // every frame and blanked it), so it clones the lifted set into a second SVG
  // sharing its viewBox and fades a paper scrim over the map on the compositor.
  var wmHoverHold = 350, wmHoverSwitch = 90; // ms: leave hold-off, node-switch settle
  var wmDragSlop = 4;                        // px before a press becomes a pan
  var wmCrowd = 40;                          // hot axons past this stop pulsing
  var wmZoomMin = 0.5, wmZoomMax = 8, wmLabelZoomOn = 1.8, wmLabelZoomOff = 1.5;
  var wmWheelRate = 0.0028, wmPinchRate = 0.01, wmWheelClamp = 60, wmKeyStep = 0.7;

  // Per-SVG interaction state, keyed weakly so a swapped-out map takes its
  // state with it. Built once per SVG: lines indexed by endpoint, nodes by
  // path, pre-lowercased search text, the base viewBox, the focus layers.
  var wmStates = new WeakMap();
  function wmState(svg) {
    var st = wmStates.get(svg);
    if (st) return st;
    var v = svg.viewBox.baseVal;
    st = { base: [v.x, v.y, v.width, v.height], box: null, pending: null, rect: null,
      hot: "", query: "", matches: [], matchNodes: [], sel: 0, live: null,
      lines: new Map(), nodes: new Map(), search: [],
      stage: null, dim: null, focus: null };
    svg.querySelectorAll("path[data-from]").forEach(function (l) {
      [l.getAttribute("data-from"), l.getAttribute("data-to")].forEach(function (k) {
        if (!st.lines.has(k)) st.lines.set(k, []);
        st.lines.get(k).push(l);
      });
    });
    svg.querySelectorAll("[data-node]").forEach(function (a) {
      var path = a.getAttribute("data-node"), t = a.querySelector("title");
      st.nodes.set(path, a);
      if (a.matches("a[href]")) st.search.push({ path: path, node: path, text: (t ? t.textContent : path).toLowerCase() });
    });
    // Documents folded into an aggregate: searchable, surfaced through the
    // node that holds them (the server's undrawn .wm-members index).
    svg.querySelectorAll(".wm-members [data-path]").forEach(function (m) {
      var path = m.getAttribute("data-path"), owner = m.parentNode.getAttribute("data-owner");
      if (!st.nodes.has(owner)) return;
      st.search.push({ path: path, node: owner, title: m.textContent,
        text: (m.textContent + " \u2014 " + path).toLowerCase() });
    });
    wmStates.set(svg, st);
    return st;
  }
  function incident(svg, p) {
    var lines = wmState(svg).lines.get(p) || [], lift = new Set([p]);
    lines.forEach(function (l) { lift.add(l.getAttribute("data-from")); lift.add(l.getAttribute("data-to")); });
    return { nodes: lift, lines: lines };
  }
  // The world map's stage: a wrapper holding the map, the scrim and the focus
  // SVG, built once on first use.
  function wmStageOf(svg) {
    var st = wmState(svg);
    if (st.stage) return st;
    st.stage = document.createElement("div");
    st.stage.className = "wm-stage";
    svg.parentNode.insertBefore(st.stage, svg);
    st.stage.appendChild(svg);
    st.dim = st.stage.appendChild(document.createElement("div"));
    st.dim.className = "wm-dim";
    st.focus = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    st.focus.setAttribute("class", svg.getAttribute("class") + " wm-focus");
    st.focus.setAttribute("viewBox", svg.getAttribute("viewBox"));
    st.focus.setAttribute("aria-hidden", "true");
    if (st.k) st.focus.style.setProperty("--wm-k", st.k.toFixed(4));
    st.stage.appendChild(st.focus);
    st.rect = null; // reparented: re-measure on the next gesture
    return st;
  }
  // One renderer for hover and filter: the lifted set is the hot node's
  // neighbourhood while there is one, else the filter matches, else nothing.
  function wmRender(svg) {
    if (svg.classList.contains("graph")) { renderLocalGraph(svg); return; }
    var st = wmStageOf(svg), lift = null, lines = [], inc = null;
    if (st.hot) {
      inc = incident(svg, st.hot);
      lift = inc.nodes; lines = inc.lines;
    } else if (st.query) {
      lift = new Set(st.matchNodes);
    }
    var sel = st.query && st.matches.length ? st.matches[st.sel].node : "";
    wmLive(svg, st, inc);
    st.focus.replaceChildren();
    st.stage.classList.toggle("wm-lit", !!lift);
    st.focus.classList.toggle("wm-crowd", lines.length > wmCrowd);
    if (!lift) return;
    var frag = document.createDocumentFragment();
    lines.forEach(function (l) { var c = l.cloneNode(true); c.classList.add("edge-hot"); frag.appendChild(c); });
    lift.forEach(function (path) {
      var a = st.nodes.get(path);
      if (!a) return;
      var c = a.cloneNode(true);
      c.classList.add("node-hot");
      if (path === sel) c.classList.add("wm-sel");
      // A lifted node always names itself, even where its label was culled.
      c.querySelectorAll(".wm-cull").forEach(function (t) { t.classList.remove("wm-cull"); });
      // A clone is paint only: no link, no tab stop, so it never takes focus
      // or re-enters the hover handler. The original underneath stays live.
      c.removeAttribute("href");
      c.setAttribute("tabindex", "-1");
      frag.appendChild(c);
    });
    st.focus.appendChild(frag);
  }
  // While a filter is set, only what is lifted (matches, plus a hovered
  // match's neighbours) answers the pointer and Tab; the scrim is paint only.
  // A hover touches only the nodes whose state changed.
  function wmLive(svg, st, inc) {
    var live = null, was = st.live;
    if (st.query) {
      live = new Set(st.matchNodes);
      if (inc) inc.nodes.forEach(function (p) { live.add(p); });
    }
    st.live = live;
    if (!live && !was) return;
    svg.classList.toggle("wm-filtering", !!live);
    var set = function (path, on) {
      var a = st.nodes.get(path);
      if (!a) return;
      a.classList.toggle("wm-live", !!live && on);
      if (on) a.removeAttribute("tabindex"); else a.setAttribute("tabindex", "-1");
    };
    if (!was || !live) {
      st.nodes.forEach(function (a, path) { set(path, !live || live.has(path)); });
      return;
    }
    was.forEach(function (p) { if (!live.has(p)) set(p, false); });
    live.forEach(function (p) { if (!was.has(p)) set(p, true); });
  }
  // The overlay's reading line: the hovered node, else the filter's chosen
  // match, with its reach. The full title rides in the node's <title>
  // ("name — where").
  function wmFoot(svg) {
    var panel = svg.closest(".graph-panel"), foot = panel && panel.querySelector(".graph-foot");
    if (!foot) return;
    foot.replaceChildren();
    var st = wmState(svg);
    if (st.query && !st.matches.length) {
      foot.textContent = "Nothing matches \u201c" + st.query + "\u201d.";
      return;
    }
    var m = st.query && !st.hot ? st.matches[st.sel] : null;
    var p = st.hot || (m ? m.node : "");
    if (!p) return;
    var add = function (tag, text) {
      var el = document.createElement(tag);
      el.textContent = text;
      foot.appendChild(el);
    };
    var pick = m ? (st.sel + 1) + " of " + st.matches.length + " \u00b7 \u2191\u2193 choose \u00b7 \u21b5 " : "";
    var parts = nodeName(st, p);
    if (m && m.node !== m.path) {
      // Folded into an aggregate: name the document and the node holding it.
      add("b", m.title);
      add("span", m.path);
      add("span", "in " + parts[0]);
      add("span", pick + "show on map");
      return;
    }
    add("b", parts[0]);
    if (parts.length > 1) add("span", parts.slice(1).join(" \u2014 "));
    var n = (st.lines.get(p) || []).length;
    add("span", n === 1 ? "1 link" : n + " links");
    if (m) add("span", pick + "open");
  }
  // A drawn node's <title> split into its name and where it lives.
  function nodeName(st, p) {
    var node = st.nodes.get(p), t = node && node.querySelector("title");
    return (t ? t.textContent : p).split(" \u2014 ");
  }
  function clearHot(root) {
    root.querySelectorAll(".edge-hot, .node-hot").forEach(function (n) { n.classList.remove("edge-hot", "node-hot"); });
  }
  function renderLocalGraph(svg) {
    var st = wmState(svg), inc = st.hot ? incident(svg, st.hot) : null;
    wmLive(svg, st, inc);
    clearHot(svg);
    svg.querySelectorAll(".wm-sel").forEach(function (a) { a.classList.remove("wm-sel"); });
    svg.classList.toggle("wm-crowd", !!inc && inc.lines.length > wmCrowd);
    if (inc) {
      inc.lines.forEach(function (l) { l.classList.add("edge-hot"); });
      inc.nodes.forEach(function (p) { var a = st.nodes.get(p); if (a) a.classList.add("node-hot"); });
    }
    var match = st.matches[st.sel], selected = match && st.nodes.get(match.node);
    if (selected) selected.classList.add("wm-sel");
  }
  function setHot(svg, p) {
    var st = wmState(svg);
    if (st.hot === (p || "")) return;
    st.hot = p || "";
    wmFoot(svg);
    wmRender(svg);
  }
  // Leaving a node does not clear at once: the cursor crossing a gap between
  // nodes would strobe the highlight. Switching to another node also waits a
  // beat, since zoomed in the labels are wide hit areas and a straight cursor
  // path crosses several. Keyboard focus switches at once.
  var hotClear = null, hotSwitch = null;
  function hotRoot(target) { return target.closest && target.closest("svg.graph, svg.floor"); }
  function scheduleClear(svg) {
    clearTimeout(hotClear);
    hotClear = setTimeout(function () { hotClear = null; setHot(svg, null); }, wmHoverHold);
  }
  function hotFrom(e, immediate) {
    if (pan && pan.moved) return;
    var svg = hotRoot(e.target);
    if (!svg) return;
    var holder = e.target.closest("[data-node]");
    clearTimeout(hotClear); hotClear = null;
    clearTimeout(hotSwitch); hotSwitch = null;
    if (!holder) { scheduleClear(svg); return; }
    var p = holder.getAttribute("data-node");
    if (immediate || !wmState(svg).hot) { setHot(svg, p); return; }
    hotSwitch = setTimeout(function () { hotSwitch = null; setHot(svg, p); }, wmHoverSwitch);
  }
  document.addEventListener("mouseover", function (e) { hotFrom(e, false); });
  document.addEventListener("focusin", function (e) { hotFrom(e, true); });
  document.addEventListener("mouseout", function (e) {
    var svg = hotRoot(e.target);
    if (!svg || (e.relatedTarget && svg.contains(e.relatedTarget))) return;
    clearTimeout(hotSwitch); hotSwitch = null;
    scheduleClear(svg);
  });

  // --- overlay zoom, pan, filter (plan world-map-navigation) -----------
  // Presentational like the hover: the viewBox and a few classes change, the
  // URL and the trail do not. Nodes stay plain <a> links; a press that moves
  // past wmDragSlop pans and swallows the click that would follow. Only a
  // drawing in an overlay canvas is zoomable (world map, universe, graph); a
  // trail-pane map keeps normal scrolling.
  var zoomable = ".graph-canvas svg.floor:not(.wm-focus), .graph-canvas svg.graph";
  function mapSVG(target) { return target.closest && target.closest(zoomable); }
  function openOverlay() {
    return [graphOverlay(), mapOverlay(), universeOverlay()].filter(shown)[0] || null;
  }
  function mapFilter(el) {
    var panel = el.closest(".graph-panel");
    return panel && panel.querySelector(".map-filter");
  }
  function filterSVG(input) {
    var panel = input.closest(".graph-panel");
    return panel && panel.querySelector(zoomable);
  }
  function curBox(svg) { var st = wmState(svg); return st.box || st.base; }
  // viewBox writes coalesce to one per frame: a trackpad emits dozens of
  // events a second and every write repaints the whole SVG.
  function setBox(svg, box) {
    var st = wmState(svg);
    st.box = box;
    if (st.pending) return;
    st.pending = requestAnimationFrame(function () {
      st.pending = null;
      if (!svg.isConnected) return;
      var vb = st.box.join(" ");
      svg.setAttribute("viewBox", vb);
      if (st.focus) st.focus.setAttribute("viewBox", vb);
      // Hysteresis: labels appear past 1.8x and stay until below 1.5x, so a
      // gesture hovering around one level does not flip them in and out.
      var scale = st.base[2] / st.box[2];
      if (scale >= wmLabelZoomOn) svg.classList.add("zoomed");
      else if (scale < wmLabelZoomOff) svg.classList.remove("zoomed");
      wmSyncView(svg);
    });
  }
  function resetBox(svg) { glideBox(svg, wmState(svg).base.slice()); }
  // Labels hold their screen size; the fit control doubles as a zoom readout.
  function wmSyncView(svg) {
    var canvas = svg.closest(".graph-canvas"), st = wmState(svg);
    if (!canvas) return;
    var w = wmView(svg);
    if (!w.k) return; // hidden: measured once shown
    var panel = canvas.closest(".graph-panel"), fit = panel && panel.querySelector(".graph-fit");
    if (fit) {
      var percent = Math.round(st.base[2] / w.box[2] * 100) + "%";
      if (fit.textContent !== percent) fit.textContent = percent;
      fit.setAttribute("aria-label", "Fit graph (current zoom " + percent + ")");
    }
    if (st.k !== w.k) {
      st.k = w.k;
      svg.style.setProperty("--wm-k", w.k.toFixed(4));
      if (st.focus) st.focus.style.setProperty("--wm-k", w.k.toFixed(4));
      clearTimeout(st.cullTimer);
      st.cullTimer = setTimeout(function () { wmCull(svg); }, wmCullSettle);
    }
  }
  // Once a zoom settles, labels that would overlap an earlier one hide:
  // rest-state labels (landmarks, top ranks) claim space first. All rects
  // are read before any class is written, so this is one layout, not n.
  var wmCullSettle = 140; // ms after the last zoom step
  function wmCull(svg) {
    if (!svg.isConnected || !svg.classList.contains("world-map")) return;
    var labels = Array.prototype.slice.call(svg.querySelectorAll("text.floor-doc-label"));
    labels.sort(function (a, b) { return a.classList.contains("label-lod") - b.classList.contains("label-lod"); });
    labels.forEach(function (t) { t.classList.remove("wm-cull"); });
    var rects = labels.map(function (t) { return t.getBoundingClientRect(); });
    var placed = [], cull = [];
    rects.forEach(function (r, i) {
      if (!r.width) return; // not shown at this zoom (label-lod)
      var hit = placed.some(function (p) {
        return r.left < p.right && r.right > p.left && r.top < p.bottom && r.bottom > p.top;
      });
      if (hit) cull.push(labels[i]); else placed.push(r);
    });
    cull.forEach(function (t) { t.classList.add("wm-cull"); });
  }
  // Keyed zoom and reset glide instead of jumping; the pan coasts after a
  // flick. One motion at a time, and any new gesture stops it.
  var motionRaf = 0, wheelGoal = null;
  function stopMotion() { cancelAnimationFrame(motionRaf); motionRaf = 0; wheelGoal = null; }
  function glideBox(svg, to) {
    stopMotion();
    if (calm.matches) { setBox(svg, to); return; }
    var from = curBox(svg).slice(), start = performance.now(), dur = 260;
    (function step(now) {
      if (!svg.isConnected) { stopMotion(); return; }
      var t = Math.min(1, (now - start) / dur), ease = 1 - Math.pow(1 - t, 3);
      setBox(svg, from.map(function (v, i) { return v + (to[i] - v) * ease; }));
      motionRaf = t < 1 ? requestAnimationFrame(step) : 0;
    })(start);
  }
  function coast(svg, vx, vy, k) {
    stopMotion();
    if (calm.matches || Math.hypot(vx, vy) < 0.25) return;
    var last = performance.now();
    (function step(now) {
      if (!svg.isConnected) { stopMotion(); return; }
      var dt = Math.min(48, now - last), b = curBox(svg), decay = Math.pow(0.93, dt / 16);
      last = now;
      setBox(svg, [b[0] - vx * dt / k, b[1] - vy * dt / k, b[2], b[3]]);
      vx *= decay; vy *= decay;
      motionRaf = Math.hypot(vx, vy) > 0.02 ? requestAnimationFrame(step) : 0;
    })(last);
  }
  // Screen-to-SVG mapping without a layout flush per event: the element box
  // is measured once (re-measured on resize) and preserveAspectRatio's
  // letterbox is applied by hand.
  function wmView(svg, box) {
    var st = wmState(svg), v = box || curBox(svg);
    var r = st.rect && st.rect.width ? st.rect : (st.rect = svg.getBoundingClientRect());
    var k = Math.min(r.width / v[2], r.height / v[3]);
    return { box: v, k: k, left: r.left + (r.width - v[2] * k) / 2, top: r.top + (r.height - v[3] * k) / 2 };
  }
  function svgPoint(svg, cx, cy, box) {
    var w = wmView(svg, box);
    return { x: w.box[0] + (cx - w.left) / w.k, y: w.box[1] + (cy - w.top) / w.k };
  }
  // The cached rect is viewport-relative: drop it whenever anything scrolls
  // or the window resizes, and it is re-measured on the next gesture.
  function wmDropRects() {
    document.querySelectorAll(".graph-canvas svg").forEach(function (svg) {
      var st = wmStates.get(svg);
      if (st) st.rect = null;
    });
  }
  window.addEventListener("resize", function () {
    wmDropRects();
    var overlay = openOverlay(), svg = overlay && overlay.querySelector(zoomable);
    if (svg) wmSyncView(svg);
  });
  window.addEventListener("scroll", wmDropRects, { passive: true, capture: true });
  // The box after zooming by factor k about an SVG-space point (the centre
  // when null), clamped to [wmZoomMin, wmZoomMax] of the base box.
  function zoomTarget(svg, k, p, box) {
    var v = box || curBox(svg), b = wmState(svg).base;
    var scale = b[2] / (v[2] * k);
    if (scale < wmZoomMin) k = b[2] / (v[2] * wmZoomMin);
    if (scale > wmZoomMax) k = b[2] / (v[2] * wmZoomMax);
    if (!p) p = { x: v[0] + v[2] / 2, y: v[1] + v[3] / 2 };
    return [p.x - (p.x - v[0]) * k, p.y - (p.y - v[1]) * k, v[2] * k, v[3] * k];
  }
  // The factor follows the delta, so a trackpad's stream of small deltas
  // zooms smoothly and a mouse wheel's ±100 notch still steps about 18%.
  // Pinch arrives as a ctrl-wheel with small deltas and gets a steeper curve.
  // Listens on the map itself (hydrateMaps), never on the document: a
  // non-passive document wheel listener disables threaded scrolling app-wide.
  function onWheel(e) {
    var svg = e.currentTarget;
    e.preventDefault();
    if (pan) return;
    var d = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? svg.clientHeight : 1);
    var rate = e.ctrlKey ? wmPinchRate : wmWheelRate;
    var k = Math.exp(Math.max(-wmWheelClamp, Math.min(wmWheelClamp, d)) * rate);
    var box = wheelGoal && wheelGoal.svg === svg ? wheelGoal.box : curBox(svg);
    var to = zoomTarget(svg, k, svgPoint(svg, e.clientX, e.clientY, box), box);
    if (calm.matches) { stopMotion(); setBox(svg, to); return; }
    if (wheelGoal && wheelGoal.svg === svg) { wheelGoal.box = to; return; }
    stopMotion();
    wheelGoal = { svg: svg, box: to };
    var last = performance.now();
    motionRaf = requestAnimationFrame(function step(now) {
      if (!svg.isConnected) { stopMotion(); return; }
      var goal = wheelGoal.box, current = curBox(svg);
      var ease = 1 - Math.exp(-(now - last) / 45);
      last = now;
      var next = current.map(function (v, i) { return v + (goal[i] - v) * ease; });
      var settled = next.every(function (v, i) { return Math.abs(v - goal[i]) < goal[2] * 0.00005; });
      setBox(svg, settled ? goal : next);
      if (settled) { motionRaf = 0; wheelGoal = null; }
      else motionRaf = requestAnimationFrame(step);
    });
  }
  // pan: {svg, x, y, box, k, moved, t, vx, vy}; v is the release velocity in px/ms.
  var pan = null, swallowClick = false;
  document.addEventListener("pointerdown", function (e) {
    swallowClick = false;
    var svg = mapSVG(e.target);
    if (!svg || e.button !== 0 || pan) return;
    stopMotion();
    var w = wmView(svg);
    pan = { svg: svg, pointer: e.pointerId, x: e.clientX, y: e.clientY, box: w.box, k: w.k, moved: false,
      t: performance.now(), px: e.clientX, py: e.clientY, vx: 0, vy: 0 };
  });
  document.addEventListener("pointermove", function (e) {
    if (!pan || pan.pointer !== e.pointerId) return;
    if (e.buttons === 0) { endPan(false); return; }
    var dx = e.clientX - pan.x, dy = e.clientY - pan.y;
    if (!pan.moved && Math.hypot(dx, dy) < wmDragSlop) return;
    if (!pan.moved) {
      pan.svg.setPointerCapture(e.pointerId);
      clearTimeout(hotClear); clearTimeout(hotSwitch);
      setHot(pan.svg, null);
    }
    pan.moved = true;
    pan.svg.classList.add("panning");
    var b = pan.box, now = performance.now(), dt = Math.max(1, now - pan.t);
    // Smoothed so the last jittery sample does not decide the coast.
    pan.vx = 0.7 * (e.clientX - pan.px) / dt + 0.3 * pan.vx;
    pan.vy = 0.7 * (e.clientY - pan.py) / dt + 0.3 * pan.vy;
    pan.t = now; pan.px = e.clientX; pan.py = e.clientY;
    setBox(pan.svg, [b[0] - dx / pan.k, b[1] - dy / pan.k, b[2], b[3]]);
  });
  function endPan(inertia) {
    if (!pan) return;
    pan.svg.classList.remove("panning");
    if (pan.svg.hasPointerCapture(pan.pointer)) pan.svg.releasePointerCapture(pan.pointer);
    swallowClick = pan.moved;
    // A release after a pause is a placement, not a flick.
    if (inertia && pan.moved && performance.now() - pan.t < 60) coast(pan.svg, pan.vx, pan.vy, pan.k);
    pan = null;
  }
  document.addEventListener("pointerup", function (e) { if (pan && pan.pointer === e.pointerId) endPan(true); });
  document.addEventListener("pointercancel", function (e) { if (pan && pan.pointer === e.pointerId) endPan(false); });
  window.addEventListener("blur", function () { endPan(false); stopMotion(); });
  document.addEventListener("dragstart", function (e) { if (mapSVG(e.target)) e.preventDefault(); });
  document.addEventListener("click", function (e) {
    if (!swallowClick) return;
    swallowClick = false;
    if (!mapSVG(e.target)) return;
    e.preventDefault();
    e.stopPropagation();
  }, true);
  document.addEventListener("dblclick", function (e) {
    var svg = mapSVG(e.target);
    if (svg && !e.target.closest("[data-node]")) resetBox(svg);
  });
  // Filter: lift nodes whose title or path contains the query; the scrim
  // recedes the rest. A hot node takes precedence and the filter view returns
  // when the hover clears (wmRender).
  function applyFilter(input) {
    var svg = filterSVG(input);
    if (!svg) return;
    var st = wmState(svg), q = input.value.trim().toLowerCase();
    st.hot = "";
    st.query = q;
    st.matches = q ? st.search.filter(function (n) { return n.text.indexOf(q) !== -1; }) : [];
    st.matchNodes = st.matches.map(function (m) { return m.node; });
    st.sel = 0;
    wmRender(svg);
    wmFoot(svg);
  }
  // Arrow keys walk the matches with a solid ring, bringing an off-screen
  // one into view; Enter opens a drawn match as a click would, and unfolds a
  // folded one onto the map first (the server redraws with it shown).
  function pickMatch(svg, i) {
    var st = wmState(svg);
    st.sel = i;
    wmRender(svg);
    wmFoot(svg);
    wmReveal(svg, st.matches[i].node);
  }
  function moveMatch(svg, delta) {
    var st = wmState(svg), n = st.matches.length;
    if (n) pickMatch(svg, (st.sel + delta + n) % n);
  }
  var pendingReveal = ""; // a folded match to select once its map arrives
  function wmReveal(svg, path) {
    var a = wmState(svg).nodes.get(path), c = a && a.querySelector("circle");
    if (!c) return;
    var x = +c.getAttribute("cx"), y = +c.getAttribute("cy"), b = curBox(svg);
    var mx = b[2] * 0.15, my = b[3] * 0.15;
    if (x > b[0] + mx && x < b[0] + b[2] - mx && y > b[1] + my && y < b[1] + b[3] - my) return;
    glideBox(svg, [x - b[2] / 2, y - b[3] / 2, b[2], b[3]]);
  }
  function openMatch(svg) {
    var st = wmState(svg), m = st.matches[st.sel];
    if (!m) return;
    if (m.node !== m.path) {
      var url = svg.getAttribute("data-reveal-url");
      if (!url || !window.htmx) return;
      pendingReveal = m.path;
      window.htmx.ajax("GET", url + encodeURIComponent(m.path), "#map-canvas");
      return;
    }
    var a = st.nodes.get(m.node);
    if (a) a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, view: window }));
  }

  // Capture phase, so Esc with text clears the filter before the overlay's
  // own Esc handler would close the map.
  document.addEventListener("keydown", function (e) {
    var input = e.target;
    if (!input.classList || !input.classList.contains("map-filter")) return;
    if (e.key === "Escape" && input.value) {
      e.preventDefault(); e.stopPropagation();
      input.value = "";
      applyFilter(input);
      return;
    }
    var svg = filterSVG(input);
    if (!svg) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); moveMatch(svg, e.key === "ArrowDown" ? 1 : -1); }
    else if (e.key === "Enter") { e.preventDefault(); openMatch(svg); }
  }, true);
  document.addEventListener("input", function (e) {
    if (e.target.classList && e.target.classList.contains("map-filter")) applyFilter(e.target);
  });
  // Hydrate (from scan): a freshly swapped-in overlay map gets its wheel
  // listener, its filter input revealed (server-rendered hidden, since it is
  // inert without JS) and a pending filter re-applied. Hover timers and a pan
  // from the previous map are dropped so they cannot pin it in memory.
  function hydrateMaps(root) {
    if (!root.querySelectorAll) return;
    root.querySelectorAll(zoomable).forEach(function (svg) {
      if (wmStates.has(svg)) return;
      clearTimeout(hotClear); hotClear = null;
      clearTimeout(hotSwitch); hotSwitch = null;
      endPan(false);
      stopMotion();
      wmState(svg);
      wmSyncView(svg);
      svg.addEventListener("wheel", onWheel, { passive: false });
      var f = mapFilter(svg);
      if (f) { f.hidden = false; if (f.value) applyFilter(f); }
      if (svg.classList.contains("graph") && root !== document.body) graphArrived(svg);
      if (pendingReveal) {
        var st = wmState(svg), i = st.matches.findIndex(function (m) { return m.node === pendingReveal; });
        pendingReveal = "";
        if (i >= 0) pickMatch(svg, i);
      }
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    restorePositions();
    showAllProgress();
    scan(document.body);
    showFocusedPane();
  });
  // htmx fragment swaps (hx-boost navigation) land after settle; rescan
  // just the swapped subtree, and re-center the canvas only when the swap
  // actually re-rendered panes. after:settle fires for EVERY swap — hover
  // preview cards, palette keystrokes, librarian exchanges — and
  // re-centering on those yanked the viewport mid-read. Listened on the
  // document: a body-level swap keeps the element, but this survives either way.
  document.addEventListener("htmx:after:settle", function (e) {
    var t = e.target;
    if (t === document.body) { restorePositions(); showAllProgress(); }
    scan(t);
    if (t === document.body ||
        (t.matches && t.matches("main.canvas, .pane")) ||
        (t.querySelector && t.querySelector("main.canvas, .pane"))) {
      showFocusedPane();
    }
  });
})();
