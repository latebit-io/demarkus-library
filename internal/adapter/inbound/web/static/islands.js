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
    return tag === "input" || tag === "textarea" || !!e.target.isContentEditable;
  }
  function showOverlay(el, restore) {
    if (!el) return;
    el._restoreFocus = restore || document.activeElement;
    el.hidden = false;
    var panel = el.querySelector(".graph-panel") || el;
    panel.setAttribute("tabindex", "-1");
    panel.focus();
  }
  function hideOverlay(el) {
    if (!el) return;
    el.hidden = true;
    var r = el._restoreFocus;
    if (r && r.focus) r.focus();
  }

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
    if (e.key === "Escape" && g && !g.hidden) { e.preventDefault(); closeGraph(); return; }
    if (e.key !== "g" || e.ctrlKey || e.metaKey || e.altKey || typingIn(e)) return;
    var p = palette();
    if ((p && !p.hidden) || !g) return; // not while the palette is open / no graph here
    e.preventDefault();
    g.hidden ? openGraph() : closeGraph();
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
    if (window.htmx) window.htmx.ajax("GET", m.dataset.mapUrl, "#map-canvas");
  }
  function closeMap() { hideOverlay(mapOverlay()); }
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("a.map-open");
    if (link && mapOverlay()) { e.preventDefault(); openMap(link); return; }
    if (e.target.id === "map-overlay") closeMap(); // click outside the panel
  });
  document.addEventListener("keydown", function (e) {
    var m = mapOverlay();
    if (e.key === "Escape" && m && !m.hidden) { e.preventDefault(); closeMap(); return; }
    if (e.ctrlKey || e.metaKey || e.altKey || typingIn(e)) return;
    // Zoom keys while a map overlay (world or universe) is up: + / - about
    // the centre, 0 resets.
    var o = openOverlay();
    var svg = o && o.querySelector("svg.floor");
    if (svg && (e.key === "+" || e.key === "=")) { e.preventDefault(); zoomBy(svg, wmKeyStep, null); return; }
    if (svg && (e.key === "-" || e.key === "_")) { e.preventDefault(); zoomBy(svg, 1 / wmKeyStep, null); return; }
    if (svg && e.key === "0") { e.preventDefault(); resetBox(svg); return; }
    if (e.key !== "m") return;
    var p = palette();
    if ((p && !p.hidden) || !m) return;
    e.preventDefault();
    m.hidden ? openMap() : closeMap();
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
    if (window.htmx) window.htmx.ajax("GET", u.dataset.universeUrl, "#universe-canvas");
  }
  function closeUniverse() { hideOverlay(universeOverlay()); }
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("a.universe-open");
    if (link && universeOverlay()) { e.preventDefault(); openUniverse(link); return; }
    if (e.target.id === "universe-overlay") closeUniverse(); // click outside the panel
  });
  document.addEventListener("keydown", function (e) {
    var u = universeOverlay();
    if (e.key === "Escape" && u && !u.hidden) { e.preventDefault(); closeUniverse(); return; }
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
      hot: "", query: "", lines: new Map(), nodes: new Map(), search: [],
      stage: null, dim: null, focus: null };
    svg.querySelectorAll("line[data-from]").forEach(function (l) {
      [l.getAttribute("data-from"), l.getAttribute("data-to")].forEach(function (k) {
        if (!st.lines.has(k)) st.lines.set(k, []);
        st.lines.get(k).push(l);
      });
    });
    svg.querySelectorAll("[data-node]").forEach(function (a) {
      var path = a.getAttribute("data-node"), t = a.querySelector("title");
      st.nodes.set(path, a);
      st.search.push({ path: path, text: (t ? t.textContent : path).toLowerCase() });
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
    st.focus.setAttribute("class", "floor wm-focus");
    st.focus.setAttribute("viewBox", svg.getAttribute("viewBox"));
    st.focus.setAttribute("aria-hidden", "true");
    st.stage.appendChild(st.focus);
    st.rect = null; // reparented: re-measure on the next gesture
    return st;
  }
  // One renderer for hover and filter: the lifted set is the hot node's
  // neighbourhood while there is one, else the filter matches, else nothing.
  function wmRender(svg) {
    var st = wmStageOf(svg), lift = null, lines = [];
    if (st.hot) {
      var inc = incident(svg, st.hot);
      lift = inc.nodes; lines = inc.lines;
    } else if (st.query) {
      lift = new Set();
      st.search.forEach(function (n) { if (n.text.indexOf(st.query) !== -1) lift.add(n.path); });
    }
    st.focus.replaceChildren();
    st.stage.classList.toggle("wm-lit", !!lift);
    if (!lift) return;
    var frag = document.createDocumentFragment();
    lines.forEach(function (l) { var c = l.cloneNode(true); c.classList.add("edge-hot"); frag.appendChild(c); });
    lift.forEach(function (path) {
      var a = st.nodes.get(path);
      if (!a) return;
      var c = a.cloneNode(true);
      c.classList.add("node-hot");
      // A clone is paint only: no link, no tab stop, so it never takes focus
      // or re-enters the hover handler. The original underneath stays live.
      c.removeAttribute("href");
      c.setAttribute("tabindex", "-1");
      frag.appendChild(c);
    });
    st.focus.appendChild(frag);
  }
  function setHot(svg, p) {
    var st = wmState(svg);
    if (st.hot === (p || "")) return;
    st.hot = p || "";
    if (svg.classList.contains("floor")) { wmRender(svg); return; }
    svg.querySelectorAll(".edge-hot, .node-hot").forEach(function (n) { n.classList.remove("edge-hot", "node-hot"); });
    if (!p) return;
    var inc = incident(svg, p);
    inc.lines.forEach(function (l) { l.classList.add("edge-hot"); });
    inc.nodes.forEach(function (q) { var a = st.nodes.get(q); if (a) a.classList.add("node-hot"); });
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

  // --- world-map zoom, pan, filter (plan world-map-navigation) ---------
  // Presentational like the hover: the viewBox and a few classes change, the
  // URL and the trail do not. Nodes stay plain <a> links; a press that moves
  // past wmDragSlop pans and swallows the click that would follow. Only a map
  // in the overlay canvas is zoomable; a trail-pane map keeps normal scrolling.
  // Both the world map and the universe overlay carry svg.floor.
  function mapSVG(target) { return target.closest && target.closest(".graph-canvas svg.floor"); }
  function openOverlay() {
    return [mapOverlay(), universeOverlay()].filter(function (o) { return o && !o.hidden; })[0] || null;
  }
  function mapFilter(el) {
    var panel = el.closest(".graph-panel");
    return panel && panel.querySelector(".map-filter");
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
      var vb = st.box.join(" ");
      svg.setAttribute("viewBox", vb);
      if (st.focus) st.focus.setAttribute("viewBox", vb);
      // Hysteresis: labels appear past 1.8x and stay until below 1.5x, so a
      // gesture hovering around one level does not flip them in and out.
      var scale = st.base[2] / st.box[2];
      if (scale >= wmLabelZoomOn) svg.classList.add("zoomed");
      else if (scale < wmLabelZoomOff) svg.classList.remove("zoomed");
    });
  }
  function resetBox(svg) { setBox(svg, wmState(svg).base.slice()); }
  // Screen-to-SVG mapping without a layout flush per event: the element box
  // is measured once (re-measured on resize) and preserveAspectRatio's
  // letterbox is applied by hand.
  function wmView(svg) {
    var st = wmState(svg), v = curBox(svg);
    var r = st.rect || (st.rect = svg.getBoundingClientRect());
    var k = Math.min(r.width / v[2], r.height / v[3]);
    return { box: v, k: k, left: r.left + (r.width - v[2] * k) / 2, top: r.top + (r.height - v[3] * k) / 2 };
  }
  function svgPoint(svg, cx, cy) {
    var w = wmView(svg);
    return { x: w.box[0] + (cx - w.left) / w.k, y: w.box[1] + (cy - w.top) / w.k };
  }
  // The cached rect is viewport-relative: drop it whenever anything scrolls
  // or the window resizes, and it is re-measured on the next gesture.
  function wmDropRects() {
    document.querySelectorAll("svg.floor").forEach(function (svg) {
      var st = wmStates.get(svg);
      if (st) st.rect = null;
    });
  }
  window.addEventListener("resize", wmDropRects);
  window.addEventListener("scroll", wmDropRects, { passive: true, capture: true });
  // Zoom by factor k about an SVG-space point (the centre when null), clamped
  // to [wmZoomMin, wmZoomMax] of the base box.
  function zoomBy(svg, k, p) {
    var v = curBox(svg), b = wmState(svg).base;
    var scale = b[2] / (v[2] * k);
    if (scale < wmZoomMin) k = b[2] / (v[2] * wmZoomMin);
    if (scale > wmZoomMax) k = b[2] / (v[2] * wmZoomMax);
    if (!p) p = { x: v[0] + v[2] / 2, y: v[1] + v[3] / 2 };
    setBox(svg, [p.x - (p.x - v[0]) * k, p.y - (p.y - v[1]) * k, v[2] * k, v[3] * k]);
  }
  // The factor follows the delta, so a trackpad's stream of small deltas
  // zooms smoothly and a mouse wheel's ±100 notch still steps about 18%.
  // Pinch arrives as a ctrl-wheel with small deltas and gets a steeper curve.
  // Listens on the map itself (hydrateMaps), never on the document: a
  // non-passive document wheel listener disables threaded scrolling app-wide.
  function onWheel(e) {
    var svg = e.currentTarget;
    e.preventDefault();
    var d = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
    var rate = e.ctrlKey ? wmPinchRate : wmWheelRate;
    var k = Math.exp(Math.max(-wmWheelClamp, Math.min(wmWheelClamp, d)) * rate);
    zoomBy(svg, k, svgPoint(svg, e.clientX, e.clientY));
  }
  var pan = null, swallowClick = false; // pan: {svg, x, y, box, k, moved}
  document.addEventListener("pointerdown", function (e) {
    swallowClick = false;
    var svg = mapSVG(e.target);
    if (!svg || e.button !== 0) return;
    var w = wmView(svg);
    pan = { svg: svg, x: e.clientX, y: e.clientY, box: w.box, k: w.k, moved: false };
  });
  document.addEventListener("pointermove", function (e) {
    if (!pan) return;
    if (e.buttons === 0) { endPan(); return; } // released off-document: no pointerup came
    var dx = e.clientX - pan.x, dy = e.clientY - pan.y;
    if (!pan.moved && Math.hypot(dx, dy) < wmDragSlop) return;
    pan.moved = true;
    pan.svg.classList.add("panning");
    var b = pan.box;
    setBox(pan.svg, [b[0] - dx / pan.k, b[1] - dy / pan.k, b[2], b[3]]);
  });
  function endPan() {
    if (!pan) return;
    pan.svg.classList.remove("panning");
    swallowClick = pan.moved;
    pan = null;
  }
  document.addEventListener("pointerup", endPan);
  document.addEventListener("pointercancel", endPan);
  document.addEventListener("click", function (e) {
    if (!swallowClick) return;
    swallowClick = false;
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
    var panel = input.closest(".graph-panel"), svg = panel && panel.querySelector("svg.floor");
    if (!svg) return;
    wmState(svg).query = input.value.trim().toLowerCase();
    wmRender(svg);
  }
  document.addEventListener("input", function (e) {
    if (e.target.classList && e.target.classList.contains("map-filter")) applyFilter(e.target);
  });
  // Hydrate (from scan): a freshly swapped-in overlay map gets its wheel
  // listener, its filter input revealed (server-rendered hidden, since it is
  // inert without JS) and a pending filter re-applied. Hover timers and a pan
  // from the previous map are dropped so they cannot pin it in memory.
  function hydrateMaps(root) {
    if (!root.querySelectorAll) return;
    root.querySelectorAll(".graph-canvas svg.floor").forEach(function (svg) {
      if (wmStates.has(svg)) return;
      clearTimeout(hotClear); hotClear = null;
      clearTimeout(hotSwitch); hotSwitch = null;
      pan = null;
      wmState(svg);
      svg.addEventListener("wheel", onWheel, { passive: false });
      var f = mapFilter(svg);
      if (f) { f.hidden = false; if (f.value) applyFilter(f); }
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    scan(document.body);
    showFocusedPane();
  });
  // htmx fragment swaps (hx-boost navigation) land after settle; rescan
  // just the swapped subtree, and re-center the canvas only when the swap
  // actually re-rendered panes. afterSettle fires for EVERY swap — hover
  // preview cards, palette keystrokes, librarian exchanges — and
  // re-centering on those yanked the viewport mid-read.
  document.body.addEventListener("htmx:afterSettle", function (e) {
    var t = e.target;
    scan(t);
    if (t === document.body ||
        (t.matches && t.matches("main.canvas, .pane")) ||
        (t.querySelector && t.querySelector("main.canvas, .pane"))) {
      showFocusedPane();
    }
  });
})();
