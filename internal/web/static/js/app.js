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
   * The optional header clock.
   *
   * The server renders the current time into the element, so this function only
   * keeps it moving. If it never runs, the header shows the time the page was
   * loaded - stale, but true and obviously a clock. The previous version left a
   * "--:--:--" placeholder for the script to replace, which meant any failure
   * here showed the user a row of dashes with nothing to explain it.
   *
   * The time is shown in the user's configured zone rather than the browser's.
   * Those differ often enough to matter - a laptop still on the previous
   * country's zone, a server-side profile set deliberately - and a header
   * disagreeing with the entries beneath it is worse than no header at all.
   */
  function initHeaderClock() {
    var clock = document.getElementById("header-clock");
    if (!clock) return;

    var zone = clock.getAttribute("data-zone");
    /* The clock format is the administrator's choice, and the server has
       already rendered the first tick in it. Reading it back off the element
       rather than assuming 24-hour is what stops the clock from flipping
       format the moment this script runs. */
    var hour12 = clock.getAttribute("data-hour12") === "1";
    var formatter = null;
    try {
      formatter = new Intl.DateTimeFormat(hour12 ? "en-US" : "en-GB", {
        hour: hour12 ? "numeric" : "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hour12: hour12,
        timeZone: zone || undefined,
      });
    } catch (err) {
      /* An unknown zone name, or an engine without full ICU data. Falling back
         to the browser's own zone is better than stopping the clock; it is the
         same answer for most people and a visibly running clock either way. */
      formatter = null;
    }

    function tick() {
      var now = new Date();
      if (formatter) {
        var text = formatter.format(now);
        /* Some engines render midnight as "24:00:00" under hour12:false. */
        if (!hour12 && text.indexOf("24:") === 0) text = "00:" + text.slice(3);
        clock.textContent = text;
        return;
      }
      var hours = now.getHours();
      var suffix = "";
      if (hour12) {
        suffix = hours >= 12 ? " PM" : " AM";
        hours = hours % 12;
        if (hours === 0) hours = 12;
      }
      clock.textContent =
        (hour12 ? String(hours) : String(hours).padStart(2, "0")) + ":" +
        String(now.getMinutes()).padStart(2, "0") + ":" +
        String(now.getSeconds()).padStart(2, "0") + suffix;
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

  /* ---------------------------------------------------------- timeline ---- */

  /*
   * Dragging a block to move it, and its bottom edge to resize it.
   *
   * Strictly an enhancement. The server draws the timeline and every block
   * already carries a form with a start time and a length that posts to the
   * same endpoint this does. Script hides that form and drives the endpoint
   * from pointer events instead; with no script, or if this throws, the forms
   * come back and nothing is lost but the dragging.
   *
   * Pointer events rather than mouse events, so a touch screen and a stylus
   * work without a second code path.
   */
  function initTimeline() {
    var timeline = document.querySelector("[data-timeline]");
    if (!timeline) return;

    /* Marking the document lets the stylesheet hide the fallback forms only
       when there is script to replace them. Doing it here rather than at load
       means a failure earlier in this file leaves the forms visible. */
    document.documentElement.classList.add("js-enabled");

    var startHour = parseInt(timeline.getAttribute("data-start-hour"), 10);
    var endHour = parseInt(timeline.getAttribute("data-end-hour"), 10);
    var date = timeline.getAttribute("data-date");
    if (isNaN(startHour) || isNaN(endHour) || endHour <= startHour) return;

    var spanMinutes = (endHour - startHour) * 60;
    /* Fifteen minutes is the granularity people actually record in, and it is
       what makes dragging land on a round number instead of 09:07. */
    var snapMinutes = 15;

    timeline.querySelectorAll(".timeline-block").forEach(function (block) {
      var grip = block.querySelector("[data-grip]");
      if (grip) makeDraggable(block, grip, true);
      makeDraggable(block, block, false);
    });


    /* pixelsToMinutes converts a drag distance into time, snapped.
     *
     * Against scrollHeight rather than clientHeight: the timeline scrolls when
     * the day is long, and the visible height would then be less than the grid
     * it is measuring, so every drag would move less than the pointer did. */
    function pixelsToMinutes(pixels) {
      var height = timeline.scrollHeight || timeline.clientHeight;
      if (!height) return 0;
      var minutes = (pixels / height) * spanMinutes;
      return Math.round(minutes / snapMinutes) * snapMinutes;
    }

    /* dragThreshold is how far the pointer must travel before this counts as a
       drag rather than a click.
       
       The whole block is a link to the correction screen, so both gestures
       start in the same place and something has to tell them apart. Distance
       is the honest test: a click that wandered three pixels is still a click,
       and anything past that was somebody moving the block. */
    var dragThreshold = 4;

    function makeDraggable(block, handle, resizing) {
      handle.addEventListener("pointerdown", function (event) {
        /* A form control keeps its own behaviour: typing a time into the
           fallback field must not drag the block it sits in. */
        if (event.target.closest("input, button, select")) return;
        if (event.button !== 0 && event.pointerType === "mouse") return;

        /* The grip sits inside the block, so a pointerdown on it would reach
           the block's own handler as well and both would run - one resizing,
           one moving, and both posting. The grip claims the gesture. */
        if (resizing) event.stopPropagation();

        /* Deliberately no preventDefault here. Doing it would kill the click
           that opens the correction screen, which is the commoner action. The
           click is suppressed further down, and only once a real drag has
           happened. */
        var startY = event.clientY;
        var originalTop = block.offsetTop;
        var originalHeight = block.offsetHeight;
        var startMinutes = minutesOf(block.getAttribute("data-start"));
        var lengthMinutes = Math.round(
          parseInt(block.getAttribute("data-duration"), 10) / 60);
        if (startMinutes === null || isNaN(lengthMinutes)) return;

        var deltaMinutes = 0;
        var dragged = false;

        /* The moves are listened for on the window rather than on the handle,
         * and the pointer is deliberately not captured.
         *
         * The handle can be an eight-pixel grip, so a drag leaves it
         * immediately and listeners attached to it would stop hearing anything
         * after the first few pixels. Pointer capture would fix that and
         * introduce a worse problem: while an element holds the pointer the
         * browser retargets the following click to it, so a plain click on a
         * block would never reach the link inside it and the correction screen
         * would silently stop opening. The window hears everything and
         * retargets nothing. */
        function onMove(moveEvent) {
          var travelled = moveEvent.clientY - startY;
          if (!dragged) {
            if (Math.abs(travelled) < dragThreshold) return;
            dragged = true;
            block.classList.add("is-dragging");
          }
          deltaMinutes = pixelsToMinutes(travelled);
          var pixels = (deltaMinutes / spanMinutes) *
            (timeline.scrollHeight || timeline.clientHeight);
          if (resizing) {
            /* A block cannot be shorter than one snap: a zero-length entry is
               not a thing anybody means to create by dragging. */
            block.style.height =
              Math.max(originalHeight + pixels, 4) + "px";
          } else {
            block.style.top = originalTop + pixels + "px";
          }
        }

        function onUp() {
          window.removeEventListener("pointermove", onMove);
          window.removeEventListener("pointerup", onUp);
          window.removeEventListener("pointercancel", onUp);
          block.classList.remove("is-dragging");

          if (!dragged || deltaMinutes === 0) {
            /* A click, or a drag that snapped back to where it started.
               Leave the block where the server put it and let the click
               through to the correction screen. */
            block.style.top = "";
            block.style.height = "";
            return;
          }

          /* This was a drag, so the click that follows it is not a request to
             open anything. Swallowed once, on the way out.
             
             The listener also clears itself on a timer: a drag that ends
             without a click - released outside the block, or cancelled by the
             browser - would otherwise leave it attached to eat somebody's next
             real click, which is the kind of fault that looks like the page
             randomly ignoring you. */
          var suppress = function (clickEvent) {
            clickEvent.preventDefault();
            clickEvent.stopPropagation();
            release();
          };
          var release = function () {
            block.removeEventListener("click", suppress, true);
          };
          block.addEventListener("click", suppress, true);
          window.setTimeout(release, 400);

          var newStart = startMinutes;
          var newLength = lengthMinutes;
          if (resizing) {
            newLength = Math.max(lengthMinutes + deltaMinutes, snapMinutes);
          } else {
            newStart = Math.max(startMinutes + deltaMinutes, 0);
          }
          submitMove(block, newStart, newLength);
        }

        window.addEventListener("pointermove", onMove);
        window.addEventListener("pointerup", onUp);
        window.addEventListener("pointercancel", onUp);
      });
    }

    /* submitMove posts the block's new position through its own form, so the
       CSRF token and the endpoint are the ones the server already rendered
       rather than a second set assembled here. */
    function submitMove(block, startMinutes, lengthMinutes) {
      var form = block.querySelector(".timeline-form");
      if (!form) return;

      var startField = form.querySelector('input[name="start"]');
      var durationField = form.querySelector('input[name="duration"]');
      if (!startField || !durationField) return;

      startField.value = clockOf(startMinutes);
      durationField.value = lengthMinutes + "m";

      var dateField = document.createElement("input");
      dateField.type = "hidden";
      dateField.name = "date";
      dateField.value = date;
      form.appendChild(dateField);

      form.requestSubmit ? form.requestSubmit() : form.submit();
    }
  }

  /* minutesOf reads "09:30" as minutes past midnight. */
  function minutesOf(clock) {
    if (!clock) return null;
    var parts = clock.split(":");
    if (parts.length !== 2) return null;
    var hours = parseInt(parts[0], 10);
    var minutes = parseInt(parts[1], 10);
    if (isNaN(hours) || isNaN(minutes)) return null;
    return hours * 60 + minutes;
  }

  /* clockOf is the inverse, clamped to the day: dragging a block off the top
     must not produce a negative hour. */
  function clockOf(minutes) {
    if (minutes < 0) minutes = 0;
    if (minutes > 23 * 60 + 59) minutes = 23 * 60 + 59;
    var hours = Math.floor(minutes / 60);
    var rest = minutes % 60;
    return (
      String(hours).padStart(2, "0") + ":" + String(rest).padStart(2, "0")
    );
  }

  /* ------------------------------------------------------------- start ---- */

  function startLiveClocks() {
    updateClocks();
    /* One second is the natural cadence for a clock showing seconds. */
    window.setInterval(updateClocks, 1000);
  }

  /* ---------------------------------------------------------- idle watch --- */

  /*
   * While a timer runs, the page watches for stretches during which it saw
   * nothing, and reports them. It never decides anything: a report becomes a
   * question on the Today screen, and only a person answers it
   * (docs/adr/0033-idle-time-is-observed.md).
   *
   * Two things are observable from inside a browser tab, and they are reported
   * as two different sources because one is far better evidence than the other:
   *
   *   asleep    - the tick that should have arrived a second ago arrived much
   *               later, so wall-clock time passed with nothing in this tab
   *               running. Either the machine slept or the browser suspended
   *               the tab; from in here those are the same event, which is why
   *               the message the server stores says the page was not running
   *               rather than claiming the machine was asleep.
   *
   *   untouched - the page ran and was visible throughout, and saw no pointer,
   *               key or scroll event. Much weaker: a visible tab on a second
   *               monitor is untouched all day by somebody working hard. It is
   *               still worth reporting, because "keep" is one click and the
   *               alternative is billing a lunch.
   *
   * A hidden tab is deliberately not watched for the second case. The page
   * knows nothing about a person who has switched to another window, and
   * reporting them as untouched would be inventing an observation rather than
   * making one.
   */

  /* The tick cadence, and how late a tick has to be before it counts as the
     page having stopped. Two seconds of lateness is ordinary scheduling jitter
     under load; a real suspension is orders of magnitude longer than that, and
     the threshold is the configured one anyway. */
  var IDLE_TICK_MS = 1000;
  /* How often an ongoing untouched stretch is re-reported so the row on the
     server keeps up with it. The server widens an overlapping observation
     rather than adding one, so this cannot turn one absence into many. */
  var IDLE_REPORT_EVERY_MS = 60000;

  function idleThresholdMs() {
    var raw = document.body.getAttribute("data-idle-seconds");
    var seconds = parseInt(raw, 10);
    if (isNaN(seconds) || seconds <= 0) return 0;
    return seconds * 1000;
  }

  function runningEntryIds() {
    var ids = [];
    document.querySelectorAll(".running-item[data-entry-id]").forEach(function (el) {
      var id = el.getAttribute("data-entry-id");
      if (id) ids.push(id);
    });
    return ids;
  }

  /*
   * Reporting is fire-and-forget. A failed report is not something a person can
   * act on, and the observation it describes is one the server is free to
   * decline anyway - too short, outside the entry, already recorded.
   */
  function reportIdle(from, to, source) {
    var ids = runningEntryIds();
    if (!ids.length) return;

    ids.forEach(function (id) {
      var body = new URLSearchParams();
      body.set("entry_id", id);
      body.set("from", new Date(from).toISOString());
      body.set("to", new Date(to).toISOString());
      body.set("source", source);
      body.set("csrf_token", currentCSRFToken());

      fetch("/idle", {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-CSRF-Token": currentCSRFToken(),
        },
        body: body.toString(),
        credentials: "same-origin",
      }).catch(function () {
        /* Nothing to do and nothing to say: the timesheet is unaffected. */
      });
    });
  }

  function initIdleWatch() {
    var threshold = idleThresholdMs();
    if (!threshold) return;

    var lastTick = Date.now();
    var lastInteraction = Date.now();
    var untouchedSince = 0;
    var untouchedReportedAt = 0;

    function interacted() {
      var now = Date.now();
      if (untouchedSince) {
        /* The stretch has ended, so report it whole - including the part since
           the last periodic report. */
        reportIdle(untouchedSince, now, "untouched");
        untouchedSince = 0;
        untouchedReportedAt = 0;
      }
      lastInteraction = now;
    }

    ["pointerdown", "keydown", "scroll", "wheel", "touchstart"].forEach(function (event) {
      window.addEventListener(event, interacted, { passive: true });
    });
    document.addEventListener("visibilitychange", function () {
      /* Becoming visible is a sign of life; becoming hidden ends the page's
         claim to be observing anything. */
      if (!document.hidden) interacted();
      else {
        untouchedSince = 0;
        untouchedReportedAt = 0;
        lastInteraction = Date.now();
      }
    });

    window.setInterval(function () {
      var now = Date.now();
      var slept = now - lastTick;
      lastTick = now;

      if (slept >= threshold) {
        /* The page was not running for that stretch. The tick before this one
           is where it stopped. */
        reportIdle(now - slept, now, "asleep");
        lastInteraction = now;
        untouchedSince = 0;
        untouchedReportedAt = 0;
        return;
      }

      if (document.hidden) return;

      if (now - lastInteraction >= threshold) {
        if (!untouchedSince) untouchedSince = lastInteraction;
        if (now - untouchedReportedAt >= IDLE_REPORT_EVERY_MS) {
          reportIdle(untouchedSince, now, "untouched");
          untouchedReportedAt = now;
        }
      }
    }, IDLE_TICK_MS);
  }

  /*
   * Each feature is started separately, and a failure in one does not stop the
   * rest.
   *
   * These are independent features sharing a file, not a pipeline. Running them
   * as a straight sequence meant the first one to throw silently disabled every
   * one after it - so a fault anywhere near the top of the list could leave the
   * page looking fine while the clock, the theme picker and the paste handler
   * had all quietly never started. Nothing was logged, because nothing had gone
   * wrong as far as the page was concerned.
   */
  function init() {
    var features = [
      ["live timers", startLiveClocks],
      ["header clock", initHeaderClock],
      ["theme picker", initThemePicker],
      ["language picker", initLanguagePicker],
      ["forms", initAjaxForms],
      ["paste", initPaste],
      ["help", initHelp],
      ["shortcuts", initShortcuts],
      ["timeline", initTimeline],
      ["idle watch", initIdleWatch],
    ];

    features.forEach(function (feature) {
      try {
        feature[1]();
      } catch (err) {
        /* Loud in the console, silent on the page: a user cannot act on this,
           and a broken enhancement must not obscure the timesheet itself. */
        if (window.console && window.console.error) {
          window.console.error("timetracker: " + feature[0] + " failed to start", err);
        }
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
