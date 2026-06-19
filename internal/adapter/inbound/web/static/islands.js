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
        theme: window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "default",
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
  }

  // --- trail canvas ------------------------------------------------------
  // The one piece of JS the trail engine needs (ADR 0005): new panes open
  // at the right edge, so bring the focused pane into view after each
  // render. htmx's own `show:` modifier is vertical-biased; this is the
  // pre-agreed snippet. Everything else about the canvas is server state.
  function showFocusedPane() {
    var pane = document.querySelector(".pane.focused");
    if (pane) pane.scrollIntoView({ inline: "nearest", block: "nearest" });
  }

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
    i = (i + delta + rows.length) % rows.length;
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

  document.addEventListener("DOMContentLoaded", function () {
    scan(document.body);
    showFocusedPane();
  });
  // htmx fragment swaps (hx-boost navigation) land after settle; rescan
  // just the swapped subtree and re-center the canvas.
  document.body.addEventListener("htmx:afterSettle", function (e) {
    scan(e.target);
    showFocusedPane();
  });
})();
