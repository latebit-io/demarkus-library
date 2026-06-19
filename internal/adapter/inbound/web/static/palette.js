// palette.js — the command palette (⌃K), ADR 0006 §3: a known-item quick
// switcher, the "front door" of navigation. Name-mode only this round: the
// server supplies the catalog (/palette.json) and the client fuzzy-matches it
// (content-mode/full-text is the separate SEARCH path, ADR 0006 §0.3).
//
// A self-contained island: it builds its own overlay DOM, so neither shell
// (page / canvas) needs palette markup — only the shared <head> loads this and
// each <body> carries data-world. Selecting a result navigates by extending the
// trail URL (the published contract in trail.go), so a jump is just a URL —
// no client-side trail store (ADR 0006: the URL is the state).
(function () {
  "use strict";

  var MAX_PANES = 10; // mirrors trail.go maxPanes
  var TRAIL_SEP = "/~/";

  var state = {
    open: false,
    scope: "world", // "world" (default — almost always what you mean) | "universe"
    sel: 0,
    rows: [], // current result rows: {title, loc, url, badge}
    cache: {}, // scope-key -> Promise<IndexEntry[]>
  };
  var el = {}; // built lazily

  // ── trail helpers (the codec's client side — append/rewind only) ─────────
  function currentChunks() {
    var p = location.pathname;
    if (p.indexOf("/t/") !== 0) return [];
    var rest = p.slice(3);
    if (rest === "") return [];
    return rest.split(TRAIL_SEP);
  }

  function docChunk(world, path) {
    // Mirrors paneChunk for a doc pane: <world>/d<path>, path keeping raw
    // slashes (the codec does not %2F-encode doc paths). path has a leading "/".
    return encodeURIComponent(world) + "/d" + path;
  }

  // jumpURL builds the trail URL for opening (world, path) as a jump (ADR 0006:
  // via='jump' = push). If the doc is already on the trail it rewinds (focuses)
  // it rather than duplicating — matching trailAfterClick's dedup.
  function jumpURL(world, path) {
    var chunks = currentChunks();
    var target = docChunk(world, path);
    var at = chunks.indexOf(target);
    if (at >= 0) {
      var url = "/t/" + chunks.join(TRAIL_SEP);
      if (at !== chunks.length - 1) url += "?focus=" + at;
      return url;
    }
    chunks = chunks.concat([target]);
    if (chunks.length > MAX_PANES) chunks = chunks.slice(chunks.length - MAX_PANES);
    return "/t/" + chunks.join(TRAIL_SEP);
  }

  // recentRows derives the empty-query view: the current trail in reverse
  // (most-recent first) — "get back to the doc I was just in" is the common
  // retrieval. Each row rewinds (focuses) its pane; the active pane is skipped
  // (you're already there).
  function recentRows() {
    var chunks = currentChunks();
    var params = new URLSearchParams(location.search);
    var focus = params.has("focus") ? parseInt(params.get("focus"), 10) : chunks.length - 1;
    var rows = [];
    for (var i = chunks.length - 1; i >= 0; i--) {
      if (i === focus) continue;
      var info = describeChunk(chunks[i]);
      if (!info) continue;
      var url = "/t/" + chunks.join(TRAIL_SEP);
      if (i !== chunks.length - 1) url += "?focus=" + i;
      rows.push({ title: info.title, loc: info.loc, url: url, badge: "" });
    }
    return rows;
  }

  // describeChunk gives a best-effort label for a trail chunk (recent view).
  function describeChunk(chunk) {
    if (chunk === "u") return { title: "universe", loc: "" };
    var slash = chunk.indexOf("/");
    if (slash < 0) return null;
    var world = decode(chunk.slice(0, slash));
    var rest = chunk.slice(slash + 1); // "d/path" | "tags/x" | "g/path" | "u/"
    var kind = rest.charAt(0);
    if (kind === "u") return { title: world + " — map", loc: world };
    var k2 = rest.indexOf("/");
    var value = k2 >= 0 ? rest.slice(k2 + 1) : "";
    if (rest.indexOf("tags/") === 0) return { title: "#" + decode(value), loc: world };
    var path = "/" + value; // doc/graph value carries no leading slash here
    var prefix = rest.indexOf("g/") === 0 ? "graph: " : "";
    return { title: prefix + baseName(path), loc: world + path };
  }

  function baseName(path) {
    var seg = path.slice(path.lastIndexOf("/") + 1);
    return seg.replace(/\.md$/, "") || path;
  }

  function decode(s) {
    try { return decodeURIComponent(s); } catch (e) { return s; }
  }

  // ── index fetch + fuzzy match ───────────────────────────────────────────
  function loadIndex() {
    var world = document.body.dataset.world || "";
    var key = state.scope === "universe" ? "universe" : "world:" + world;
    if (!state.cache[key]) {
      var q = "scope=" + state.scope + "&world=" + encodeURIComponent(world);
      state.cache[key] = fetch("/palette.json?" + q, { headers: { Accept: "application/json" } })
        .then(function (r) { return r.ok ? r.json() : []; })
        .catch(function () {
          delete state.cache[key]; // let a later open retry a transient failure
          return [];
        });
    }
    return state.cache[key];
  }

  // fuzzy: subsequence match on "title path", substring matches ranked first.
  function fuzzy(entries, query) {
    var q = query.toLowerCase();
    var scored = [];
    for (var i = 0; i < entries.length; i++) {
      var e = entries[i];
      var hay = (e.title + " " + e.world + e.path).toLowerCase();
      var sub = hay.indexOf(q);
      var score;
      if (sub >= 0) score = sub;
      else if (isSubsequence(q, hay)) score = 1000 + i;
      else continue;
      scored.push({ score: score, i: i, e: e });
    }
    scored.sort(function (a, b) { return a.score - b.score || a.i - b.i; });
    return scored.slice(0, 50).map(function (s) {
      return {
        title: s.e.title,
        loc: s.e.world + s.e.path,
        url: jumpURL(s.e.world, s.e.path),
        badge: s.e.status || "",
      };
    });
  }

  function isSubsequence(q, hay) {
    var j = 0;
    for (var i = 0; i < hay.length && j < q.length; i++) {
      if (hay[i] === q[j]) j++;
    }
    return j === q.length;
  }

  // ── rendering ───────────────────────────────────────────────────────────
  function build() {
    el.backdrop = document.createElement("div");
    el.backdrop.className = "cmdk-backdrop";
    el.backdrop.innerHTML =
      '<div class="cmdk-panel" role="dialog" aria-modal="true" aria-label="Command palette">' +
      '  <div class="cmdk-head">' +
      '    <input class="cmdk-input" type="text" placeholder="Jump to a document…" ' +
      '           autocomplete="off" autocapitalize="off" spellcheck="false" aria-label="Search documents">' +
      '    <button class="cmdk-scope" type="button" title="Toggle search scope"></button>' +
      "  </div>" +
      '  <ul class="cmdk-list" role="listbox"></ul>' +
      '  <div class="cmdk-hint"><span>↑↓ to move</span><span>↵ to open</span><span>esc to close</span></div>' +
      "</div>";
    el.input = el.backdrop.querySelector(".cmdk-input");
    el.scope = el.backdrop.querySelector(".cmdk-scope");
    el.list = el.backdrop.querySelector(".cmdk-list");

    el.backdrop.addEventListener("mousedown", function (e) {
      if (e.target === el.backdrop) close();
    });
    el.input.addEventListener("input", refresh);
    el.scope.addEventListener("click", function () {
      state.scope = state.scope === "world" ? "universe" : "world";
      el.input.focus();
      refresh();
    });
    el.list.addEventListener("mousemove", function (e) {
      var li = e.target.closest(".cmdk-row");
      if (li) select(parseInt(li.dataset.i, 10));
    });
    el.list.addEventListener("click", function (e) {
      var li = e.target.closest(".cmdk-row");
      if (li) go(parseInt(li.dataset.i, 10));
    });
    document.body.appendChild(el.backdrop);
  }

  function refresh() {
    var q = el.input.value.trim();
    el.scope.textContent = state.scope === "universe" ? "universe" : (document.body.dataset.world || "world");
    el.scope.setAttribute("aria-pressed", state.scope === "universe" ? "true" : "false");
    if (q === "") {
      render(recentRows(), true);
      return;
    }
    loadIndex().then(function (entries) {
      // Guard against a stale resolve after the query changed or palette closed.
      if (!state.open || el.input.value.trim() !== q) return;
      render(fuzzy(entries, q), false);
    });
  }

  function render(rows, isRecent) {
    state.rows = rows;
    state.sel = 0;
    if (!rows.length) {
      el.list.innerHTML =
        '<li class="cmdk-empty">' +
        (isRecent ? "No history yet — start typing to search." : "No matches.") +
        "</li>";
      return;
    }
    var html = "";
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      html +=
        '<li class="cmdk-row" role="option" data-i="' + i + '" aria-selected="' + (i === 0) + '">' +
        '<span class="cmdk-title">' + esc(r.title) + "</span>" +
        '<span class="cmdk-loc">' + esc(r.loc) + "</span>" +
        (r.badge ? '<span class="cmdk-badge">' + esc(r.badge) + "</span>" : "") +
        "</li>";
    }
    el.list.innerHTML = html;
  }

  function esc(s) {
    return String(s).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  function select(i) {
    if (i < 0 || i >= state.rows.length || i === state.sel) return;
    var rows = el.list.querySelectorAll(".cmdk-row");
    if (rows[state.sel]) rows[state.sel].setAttribute("aria-selected", "false");
    state.sel = i;
    if (rows[i]) {
      rows[i].setAttribute("aria-selected", "true");
      rows[i].scrollIntoView({ block: "nearest" });
    }
  }

  function go(i) {
    var r = state.rows[i];
    if (!r) return;
    close();
    window.location.assign(r.url); // a jump is a URL; full nav is fine + robust
  }

  // ── open / close ────────────────────────────────────────────────────────
  function open() {
    if (state.open) return;
    if (!el.backdrop) build();
    state.open = true;
    el.backdrop.style.display = "flex";
    el.input.value = "";
    refresh();
    el.input.focus();
  }

  function close() {
    if (!state.open) return;
    state.open = false;
    el.backdrop.style.display = "none";
  }

  // ── keybindings ─────────────────────────────────────────────────────────
  document.addEventListener("keydown", function (e) {
    // ⌃K / ⌘K toggles (collision-prone; rebinding config is future work).
    if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
      e.preventDefault();
      state.open ? close() : open();
      return;
    }
    if (!state.open) return;
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        close();
        break;
      case "ArrowDown":
        e.preventDefault();
        select(Math.min(state.sel + 1, state.rows.length - 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        select(Math.max(state.sel - 1, 0));
        break;
      case "Enter":
        e.preventDefault();
        go(state.sel);
        break;
    }
  });
})();
