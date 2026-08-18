/*
 * TimeTracker client-side enhancement.
 *
 * The application is server-rendered (docs/adr/0002-server-rendered-htmx.md);
 * every screen works without any of this. What lives here is only what a round
 * trip cannot do well:
 *
 *   1. the ticking clock on running timers - the most latency-sensitive element
 *      on the page, and the one thing that must never wait for the network;
 *   2. instant theme switching, so the picker feels immediate;
 *   3. an ajax layer for the hx-post / hx-confirm attributes in the templates,
 *      so a button press updates the page in place instead of reloading it;
 *   4. keyboard shortcuts.
 *
 * Note on (3): the templates use HTMX's attribute vocabulary, and this file
 * implements the small subset of it the application actually relies on -
 * hx-post, hx-confirm, and the HX-Request / HX-Refresh header protocol. Dropping
 * the upstream htmx library in its place is a straight swap and requires no
 * template changes; until then there is no third-party JavaScript in the tree at
 * all, which is one fewer dependency to audit.
 *
 * Written as plain ES2015+ with no build step, matching the no-toolchain
 * constraint in ASR-003.
 */

(function () {
  "use strict";

  /* ------------------------------------------------------------- clocks --- */

  /*
   * Running timers show a live elapsed time, computed in the browser from the
   * start instant the server rendered into a data attribute. The server's clock
   * is authoritative for what gets stored; this is display only, so a small
   * skew between the two is harmless.
   */
  function updateClocks() {
    var now = Date.now();
    document.querySelectorAll(".live-clock[data-started]").forEach(function (el) {
      var started = Date.parse(el.getAttribute("data-started"));
      if (isNaN(started)) return;

      var seconds = Math.max(0, Math.floor((now - started) / 1000));
      var hours = Math.floor(seconds / 3600);
      var minutes = Math.floor((seconds % 3600) / 60);
      var secs = seconds % 60;
      el.textContent =
        hours + ":" + String(minutes).padStart(2, "0") + ":" + String(secs).padStart(2, "0");
    });
  }

  /* -------------------------------------------------------------- theme --- */

  /*
   * The theme is applied to the document immediately so the change feels
   * instant, and saved to the server afterwards so it follows the user to
   * another device. The server renders data-theme into the initial HTML, which
   * is why there is no flash of the wrong theme on load.
   */
  function initThemePicker() {
    var select = document.getElementById("theme-select");
    if (!select) return;

    select.addEventListener("change", function () {
      var theme = select.value;
      document.documentElement.setAttribute("data-theme", theme);

      var body = new URLSearchParams();
      body.set("theme", theme);
      body.set("csrf_token", currentCSRFToken());

      fetch("/preferences/theme", {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-CSRF-Token": currentCSRFToken(),
        },
        body: body.toString(),
        credentials: "same-origin",
      }).catch(function () {
        /* The visual change has already happened. A failure to persist means
           the choice is lost on the next load, which is not worth interrupting
           the user for. */
      });
    });
  }

  /* ----------------------------------------------------- ajax form posts --- */

  /*
   * Intercepts submissions of forms carrying hx-post, sending them in the
   * background. The server answers a mutation with HX-Refresh, which asks the
   * page to redraw - every mutation affects several regions at once (the timer
   * bar, the entry list and both totals), and a partial update that missed one
   * would leave a stale number on screen, which is worse than a redraw.
   *
   * A form without hx-post, or a page with JavaScript disabled, posts normally
   * and gets a redirect. Both paths are supported by the same handlers.
   */
  function initAjaxForms() {
    document.addEventListener("submit", function (event) {
      var form = event.target;
      if (!(form instanceof HTMLFormElement)) return;

      var url = form.getAttribute("hx-post");
      if (!url) return;

      var confirmText = form.getAttribute("hx-confirm");
      if (confirmText && !window.confirm(confirmText)) {
        event.preventDefault();
        return;
      }

      event.preventDefault();
      submitForm(form, url);
    });
  }

  function submitForm(form, url) {
    var submitButtons = form.querySelectorAll("button[type=submit]");
    submitButtons.forEach(function (b) { b.disabled = true; });

    /* The token also travels as a header. The hidden field in the form body
       already carries it - which is what protects the no-JavaScript path - and
       sending both means the check works even for a submission assembled
       without the field. */
    var headers = { "HX-Request": "true" };
    var tokenField = form.querySelector('input[name="csrf_token"]');
    if (tokenField && tokenField.value) {
      headers["X-CSRF-Token"] = tokenField.value;
    }

    /* URL-encoded, not FormData. FormData sends multipart/form-data, which is
       parsed by a different code path on the server; sending what a plain HTML
       form post sends means the JavaScript and no-JavaScript paths are handled
       by identical code and cannot diverge. A form that uploads a file will need
       multipart and must opt into it explicitly. */
    headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";

    fetch(url, {
      method: "POST",
      /* HX-Request tells the handler this is a background request, so it
         answers with a refresh instruction rather than a redirect. */
      headers: headers,
      body: new URLSearchParams(new FormData(form)).toString(),
      credentials: "same-origin",
    })
      .then(function (response) {
        if (response.headers.get("HX-Refresh") === "true") {
          window.location.reload();
          return null;
        }
        if (!response.ok) {
          return response.text().then(function (text) {
            showError(text || "That did not work.");
          });
        }
        window.location.reload();
        return null;
      })
      .catch(function () {
        showError("Could not reach the server.");
      })
      .finally(function () {
        submitButtons.forEach(function (b) { b.disabled = false; });
      });
  }

  /*
   * Errors are shown in a banner rather than an alert box, so they can be read
   * alongside the form that produced them.
   */
  function showError(message) {
    var existing = document.querySelector(".banner-error");
    if (existing) existing.remove();

    var banner = document.createElement("div");
    banner.className = "banner banner-error";
    banner.setAttribute("role", "alert");
    /* textContent, never innerHTML: the message may quote what the user typed. */
    banner.textContent = message;

    var main = document.querySelector("main.content");
    if (main) main.insertBefore(banner, main.firstChild);
    banner.scrollIntoView({ block: "nearest" });
  }

  /*
   * The optional header clock. Ticking it client-side rather than re-rendering
   * the page is the only sane way to show seconds, and it is decorative - the
   * server's clock is what decides what gets recorded.
   */
  function initHeaderClock() {
    var clock = document.getElementById("header-clock");
    if (!clock) return;

    function tick() {
      var now = new Date();
      clock.textContent =
        String(now.getHours()).padStart(2, "0") + ":" +
        String(now.getMinutes()).padStart(2, "0") + ":" +
        String(now.getSeconds()).padStart(2, "0");
    }
    tick();
    window.setInterval(tick, 1000);
  }

  /* ------------------------------------------------------------ language --- */

  /*
   * Changing language reloads the page rather than translating the DOM in
   * place: doing it client-side would mean shipping the whole catalogue to the
   * browser and re-rendering every string, for no benefit over a round trip
   * that the server already knows how to do.
   */
  function initLanguagePicker() {
    var select = document.getElementById("language-select");
    if (!select) return;

    select.addEventListener("change", function () {
      var body = new URLSearchParams();
      body.set("language", select.value);
      body.set("csrf_token", currentCSRFToken());

      fetch("/preferences/language", {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-CSRF-Token": currentCSRFToken(),
        },
        body: body.toString(),
        credentials: "same-origin",
      })
        .then(function () {
          window.location.reload();
        })
        .catch(function () {
          showError("Could not change the language.");
        });
    });
  }

  /* Any form on the page carries the session's token; they are all the same. */
  function currentCSRFToken() {
    var field = document.querySelector('input[name="csrf_token"]');
    return field ? field.value : "";
  }

  /* ---------------------------------------------------------------- paste --- */

  /*
   * Pasting an image attaches it.
   *
   * A photographed receipt or a screenshot is usually already on the clipboard,
   * and making someone save it to disk first so they can pick it in a file
   * dialogue is the kind of friction that stops people recording expenses at
   * all.
   *
   * It posts to exactly the same upload endpoint a file input does, so there is
   * no second, laxer path into the blob store: the same size limit, the same
   * type sniffing, the same authorisation.
   */
  function initPaste() {
    document.addEventListener("paste", function (event) {
      if (!event.clipboardData) return;

      /* Only act when the paste target is a region that declares somewhere to
         put it. Pasting text into a note field must stay ordinary pasting. */
      var target = event.target.closest("[data-paste-target]");
      if (!target) return;

      var url = target.getAttribute("data-paste-target");
      var items = event.clipboardData.items;

      for (var i = 0; i < items.length; i++) {
        if (items[i].kind !== "file") continue;

        var file = items[i].getAsFile();
        if (!file) continue;

        event.preventDefault();
        uploadPastedFile(url, file, target);
        return;
      }
    });
  }

  function uploadPastedFile(url, file, target) {
    var body = new FormData();
    /* The clipboard rarely supplies a name. The extension matters, because the
       server checks that it agrees with the content, so it is derived from the
       type the browser reported rather than invented. */
    var name = file.name;
    if (!name || name === "image.png") {
      var extension = (file.type.split("/")[1] || "png").replace("jpeg", "jpg");
      name = "pasted-" + Date.now() + "." + extension;
    }
    body.append("file", file, name);
    body.append("csrf_token", currentCSRFToken());

    target.classList.add("is-uploading");

    fetch(url, {
      method: "POST",
      headers: { "HX-Request": "true", "X-CSRF-Token": currentCSRFToken() },
      body: body,
      credentials: "same-origin",
    })
      .then(function (response) {
        if (!response.ok) {
          return response.text().then(function (text) {
            showError(text || "That image could not be attached.");
          });
        }
        window.location.reload();
        return null;
      })
      .catch(function () {
        showError("Could not reach the server.");
      })
      .finally(function () {
        target.classList.remove("is-uploading");
      });
  }

  /* ---------------------------------------------------------------- help --- */

  /*
   * The help control is a real link, so with JavaScript disabled the browser
   * navigates to /help/<screen> and gets a whole page. Here we intercept it and
   * load the same fragment into a panel instead, which keeps the user's place.
   *
   * Focus is moved into the panel when it opens and returned to the control
   * when it closes: a panel that appears without focus is invisible to a screen
   * reader user, and one that closes without returning focus loses their place
   * entirely.
   */
  var helpOpener = null;

  function initHelp() {
    document.addEventListener("click", function (event) {
      var trigger = event.target.closest("[hx-get]");
      if (trigger) {
        event.preventDefault();
        openHelp(trigger.getAttribute("hx-get"), trigger);
        return;
      }
      if (event.target.closest(".help-close")) {
        event.preventDefault();
        closeHelp();
      }
    });
  }

  function openHelp(url, trigger) {
    var panel = document.getElementById("help-panel");
    if (!panel) {
      window.location.href = url;
      return;
    }
    helpOpener = trigger || null;

    fetch(url, { headers: { "HX-Request": "true" }, credentials: "same-origin" })
      .then(function (response) {
        return response.text();
      })
      .then(function (html) {
        /* The fragment comes from our own server and is already escaped by the
           template layer; nothing here concatenates user input. */
        panel.innerHTML = html;
        panel.hidden = false;

        var heading = panel.querySelector("h1");
        if (heading) {
          /* tabindex -1 makes a heading focusable without putting it in the tab
             order, which is the conventional way to move focus to a region. */
          heading.setAttribute("tabindex", "-1");
          heading.focus();
        }
      })
      .catch(function () {
        window.location.href = url;
      });
  }

  function closeHelp() {
    var panel = document.getElementById("help-panel");
    if (!panel || panel.hidden) return;

    panel.hidden = true;
    panel.innerHTML = "";
    if (helpOpener) {
      helpOpener.focus();
      helpOpener = null;
    }
  }

  /* --------------------------------------------------------- shortcuts ---- */

  /*
   * Logging time is a chore; the keyboard is the fast path. Shortcuts are
   * ignored while the user is typing, so they never swallow a keystroke meant
   * for a note field.
   */
  function initShortcuts() {
    document.addEventListener("keydown", function (event) {
      var target = event.target;
      var typing =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLSelectElement;

      if (event.key === "Escape") {
        /* Escape closes the help panel first, then leaves the current field.
           Ordering matters: someone who opened help from a field expects one
           press to close the panel, not to lose their place in the form. */
        var panel = document.getElementById("help-panel");
        if (panel && !panel.hidden) {
          closeHelp();
          return;
        }
        if (typing) {
          target.blur();
        }
        return;
      }
      if (typing || event.metaKey || event.ctrlKey || event.altKey) return;

      switch (event.key) {
        case "n": {
          var quick = document.getElementById("quick-add-input");
          if (quick) {
            event.preventDefault();
            quick.focus();
          }
          break;
        }
        case "t":
          window.location.href = "/today";
          break;
        case "w":
          window.location.href = "/week";
          break;
        case "e":
          window.location.href = "/entries";
          break;
        case "?": {
          /* The shortcut list lives in the help panel, translated, rather than
             in a hard-coded English string here. */
          event.preventDefault();
          var helpLink = document.querySelector(".help-button");
          if (helpLink) {
            openHelp(helpLink.getAttribute("hx-get"), helpLink);
          }
          break;
        }
      }
    });
  }

  /* ------------------------------------------------------------- start ---- */

  function init() {
    updateClocks();
    /* One second is the natural cadence for a clock showing seconds. */
    window.setInterval(updateClocks, 1000);
    initHeaderClock();
    initThemePicker();
    initLanguagePicker();
    initAjaxForms();
    initPaste();
    initHelp();
    initShortcuts();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
