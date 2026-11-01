// The email alerts plugin's admin page script, mounted inside a shadow root
// on Admin → Notifications. Only the API, the theme, the mount point and
// the few plugin-specific phrases differ from the plugin.
export function mountEmailAlertsPage(root, api) {
  const $ = (s, el = root) => el.querySelector(s);
  const $$ = (s, el = root) => [...el.querySelectorAll(s)];
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  let state = null;
  let draft = null;
  let dirty = false;
  let tpl = "html";
  let previewTimer = 0;

  function toast(msg, bad = false) {
    const t = document.createElement("div");
    t.className = "toast" + (bad ? " bad" : "");
    t.setAttribute("role", "status");
    t.textContent = msg;
    $(".ea-body").appendChild(t);
    setTimeout(() => t.remove(), bad ? 8000 : 3500);
  }

  function confirmBox(title, body, yes = "Confirm") {
    const d = $("#cdlg");
    $("#cdlg-title").textContent = title;
    $("#cdlg-body").textContent = body;
    $("#cdlg-yes").textContent = yes;
    d.returnValue = "";
    d.showModal();
    return new Promise((resolve) => d.addEventListener("close", () => resolve(d.returnValue === "yes"), { once: true }));
  }

  function setDirty(v) {
    dirty = v;
    $("#dirty").hidden = !v;
    $("#save").disabled = !v;
  }

  $$("nav.tabs button").forEach((b) => b.addEventListener("click", () => {
    $$("nav.tabs button").forEach((x) => x.setAttribute("aria-selected", String(x === b)));
    $$("section[data-panel]").forEach((p) => (p.hidden = p.dataset.panel !== b.dataset.tab));
    if (b.dataset.tab === "templates") schedulePreview(0);
  }));

  async function load() {
    state = await api("state");
    draft = structuredClone(state.settings);
    if (!draft.custom_vars) draft.custom_vars = [];
    renderAll();
    setDirty(false);
  }

  function renderAll() {
    const s = state.server;
    $("#server-line").textContent = [s.name, s.smtp_host ? `sending through ${s.smtp_host} as ${s.from}` : "mail server not set", s.now].filter(Boolean).join(" · ");
    const p = state.problems || [];
    $("#problems").innerHTML = p.length
      ? `<div class="banner bad"><b>Not sending yet.</b> Fix these to start sending:<ul>${p.map((x) => `<li>${esc(x)}</li>`).join("")}</ul></div>`
      : "";
    renderOverview();
    renderTemplateEditor();
    renderDesign();
    renderSchedule();
    renderRecipients();
    renderLog();
  }

  function fmtTime(t) {
    if (!t || t.startsWith("0001")) return "–";
    return new Date(t).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
  }

  function renderOverview() {
    $("#stat-active").textContent = state.recipients.filter((r) => r.active).length;
    $("#stat-sent").textContent = (state.log || []).filter((l) => !l.error).length;
    const r = state.report;
    if (r) {
      $("#stat-last").textContent = fmtTime(r.time);
      $("#stat-last-detail").textContent = r.skipped || `${r.reason}: ${r.emails_sent} sent, ${r.recipients_due} due` + (r.errors && r.errors.length ? `. Errors: ${r.errors.join("; ")}` : "");
    } else {
      $("#stat-last").textContent = "None since the server started";
      $("#stat-last-detail").textContent = "";
    }
  }

  // Templates
  function renderTemplateEditor() {
    $("#tpl-subject").value = draft.templates.subject;
    $("#tpl-body").value = draft.templates[tpl];
    $$("[data-tpl]").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.tpl === tpl)));
    $("#pv-frame").hidden = tpl !== "html";
    $("#pv-text").hidden = tpl !== "text";
  }

  $$("[data-tpl]").forEach((b) => b.addEventListener("click", () => {
    tpl = b.dataset.tpl;
    renderTemplateEditor();
    schedulePreview(0);
  }));
  $$("[data-pane]").forEach((b) => b.addEventListener("click", () => {
    $$("[data-pane]").forEach((x) => x.setAttribute("aria-pressed", String(x === b)));
    $("#pane-preview").hidden = b.dataset.pane !== "preview";
    $("#pane-vars").hidden = b.dataset.pane !== "vars";
  }));

  $("#tpl-subject").addEventListener("input", (e) => { draft.templates.subject = e.target.value; setDirty(true); schedulePreview(); });
  $("#tpl-body").addEventListener("input", (e) => { draft.templates[tpl] = e.target.value; setDirty(true); schedulePreview(); });
  $("#tpl-body").addEventListener("keydown", (e) => {
    if (e.key === "Tab" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
      e.preventDefault();
      insertAtCursor($("#tpl-body"), "  ");
    }
  });
  let lastFocus = $("#tpl-body");
  $("#tpl-subject").addEventListener("focus", () => (lastFocus = $("#tpl-subject")));
  $("#tpl-body").addEventListener("focus", () => (lastFocus = $("#tpl-body")));

  function insertAtCursor(el, text) {
    const start = el.selectionStart ?? el.value.length, end = el.selectionEnd ?? start;
    el.value = el.value.slice(0, start) + text + el.value.slice(end);
    el.selectionStart = el.selectionEnd = start + text.length;
    el.focus();
    el.dispatchEvent(new Event("input"));
  }

  $("#reset-tpl").addEventListener("click", async () => {
    const name = tpl === "html" ? "HTML" : "plain-text";
    if (!(await confirmBox(`Reset the ${name} template?`, `Replaces the ${name} template and the subject with the built-in versions. Nothing is saved until you press Save changes.`, "Reset"))) return;
    draft.templates[tpl] = state.defaults.templates[tpl];
    draft.templates.subject = state.defaults.templates.subject;
    renderTemplateEditor();
    setDirty(true);
    schedulePreview(0);
  });

  $("#pv-mode").addEventListener("change", () => schedulePreview(0));
  $("#pv-source").addEventListener("change", () => schedulePreview(0));
  $("#pv-refresh").addEventListener("click", () => schedulePreview(0));

  function schedulePreview(delay = 600) {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(runPreview, delay);
  }

  let previewSeq = 0;
  async function runPreview() {
    if (!draft) return;
    const seq = ++previewSeq;
    $("#pv-note").textContent = "Rendering…";
    try {
      const r = await api("preview", { method: "POST", body: { settings: draft, mode: $("#pv-mode").value, source: $("#pv-source").value } });
      if (seq !== previewSeq) return;
      if (r.error) {
        $("#pv-subject").textContent = "Template error";
        $("#pv-note").innerHTML = `<span style="color:var(--bad)">${esc(r.error)}</span>`;
        return;
      }
      $("#pv-subject").textContent = r.subject;
      $("#pv-frame").srcdoc = r.html;
      $("#pv-text").textContent = r.text;
      $("#pv-note").textContent = r.note || "";
    } catch (e) {
      if (seq === previewSeq) $("#pv-note").textContent = "Preview failed: " + e.message;
    }
  }

  const VARS = [
    ["Anywhere", "Values for the whole email.", ["server_name", "server_url", "date", "short_date", "time", "period", "summary", "total_count", "movie_count", "episode_count", "show_count", "more_count", "recipient_name", "recipient_first_name", "recipient_email", "first_display_title", "first_title", "first_show_name", "first_movie_title", "manage_url", "unsubscribe_url"]],
    ["Theme", "Set on the Design tab.", ["header_title", "intro", "footer_text", "button_text", "logo_url", "accent_color", "background_color", "card_color", "text_color", "muted_color"]],
    ["Conditions", "Use {{#name}}…{{/name}} to show a block only when true, or {{^name}}…{{/name}} when false.", ["has_items", "has_shows", "has_movies", "has_movies_and_shows", "single_item", "has_more", "is_instant", "is_digest", "is_daily", "is_weekly", "is_test"]],
    ["Lists", "Repeat a block for each entry, for example {{#shows}}…{{/shows}}.", ["shows", "movies", "episodes", "items"]],
    ["Inside {{#shows}}", "One entry per show, with its new episodes grouped.", ["show_name", "show_synopsis", "show_synopsis_full", "show_url", "show_web_url", "show_poster", "show_backdrop", "show_backdrop_bg", "show_year", "show_genres", "show_rating", "show_status", "show_content_rating", "show_tagline", "network", "season_count", "cast", "library_name", "episode_count", "episodes_label", "episode_range", "single_episode", "multiple_episodes", "first_episode_name", "first_episode_code", "first_episode_synopsis", "first_episode_image", "first_url", "episodes"]],
    ["Inside {{#episodes}}", "Each episode. Show values are available here too.", ["episode_name", "episode_code", "season_number", "episode_number", "season_label", "episode_synopsis", "episode_synopsis_full", "episode_image", "has_episode_image", "air_date", "runtime", "url", "display_title", "show_name", "show_synopsis", "show_poster", "network", "library_name", "added_date", "is_first", "is_last"]],
    ["Inside {{#movies}}", "Each new movie.", ["title", "year", "display_title", "synopsis", "synopsis_full", "tagline", "genres", "rating", "imdb_rating", "content_rating", "runtime", "runtime_minutes", "release_date", "poster", "backdrop", "backdrop_bg", "has_poster", "cast", "studios", "url", "web_url", "library_name", "added_date"]],
    ["Inside {{#items}}", "Every new item in one list, newest first. Check {{#is_movie}} or {{#is_episode}}.", ["is_movie", "is_episode", "type", "title", "display_title", "synopsis", "poster", "backdrop", "url"]],
  ];
  const LISTS = ["shows", "movies", "episodes", "items", "genre_list"];
  const CONDS = new Set(VARS[2][2].concat(["single_episode", "multiple_episodes", "has_poster", "has_episode_image", "is_first", "is_last", "is_movie", "is_episode"]));

  function renderVars() {
    const custom = (draft.custom_vars || []).map((v) => v.key).filter(Boolean);
    const groups = custom.length ? [...VARS, ["Your custom variables", "Set on the Design tab.", custom]] : VARS;
    $("#pane-vars").innerHTML = groups.map(([title, hint, names], i) => `
      <details ${i === 0 || title.startsWith("Inside {{#shows") ? "open" : ""}>
        <summary>${esc(title)}</summary>
        <p>${esc(hint)}</p>
        ${names.map((n) => `<code data-ins="${esc(n)}" title="Insert">${esc("{{" + n + "}}")}</code>`).join("")}
      </details>`).join("") +
      `<p style="margin-top:10px">Three braces, <span class="mono">{{{name}}}</span>, insert a value without HTML-escaping it. <span class="mono">{{! note }}</span> is a comment.</p>`;
  }
  $("#pane-vars").addEventListener("click", (e) => {
    const c = e.target.closest("[data-ins]");
    if (!c) return;
    const name = c.dataset.ins;
    let text = `{{${name}}}`;
    if (LISTS.includes(name)) text = `{{#${name}}}\n\n{{/${name}}}`;
    else if (CONDS.has(name)) text = `{{#${name}}}{{/${name}}}`;
    insertAtCursor(lastFocus, text);
  });

  // Design
  const COLORS = [["accent_color", "Accent (buttons, highlights)"], ["background_color", "Background"], ["card_color", "Cards"], ["text_color", "Text"], ["muted_color", "Secondary text"]];
  function renderDesign() {
    $("#colors").innerHTML = COLORS.map(([k, label]) => `
      <label class="f"><span>${esc(label)}</span>
        <div class="color-row"><input type="color" data-color="${k}" aria-label="${esc(label)}"><input type="text" data-color-text="${k}" class="mono" style="max-width:140px"></div>
      </label>`).join("");
    COLORS.forEach(([k]) => {
      const v = draft.theme[k] || "";
      $(`[data-color="${k}"]`).value = /^#[0-9a-f]{6}$/i.test(v) ? v : "#000000";
      $(`[data-color-text="${k}"]`).value = v;
    });
    $$("[data-theme-key]").forEach((el) => (el.value = draft.theme[el.dataset.themeKey] || ""));
    $$("[data-theme-bool]").forEach((el) => (el.checked = !!draft.theme[el.dataset.themeBool]));
    $$("[data-theme-num]").forEach((el) => (el.value = draft.theme[el.dataset.themeNum] ?? ""));
    $$("[data-delivery-key]").forEach((el) => {
      const v = draft.delivery[el.dataset.deliveryKey];
      if (el.type === "checkbox") el.checked = !!v; else el.value = v ?? "";
    });
    renderCustomVars();
    renderVars();
  }
  $("#colors").addEventListener("input", (e) => {
    const k = e.target.dataset.color || e.target.dataset.colorText;
    if (!k) return;
    draft.theme[k] = e.target.value;
    if (e.target.dataset.color) $(`[data-color-text="${k}"]`).value = e.target.value;
    else if (/^#[0-9a-f]{6}$/i.test(e.target.value)) $(`[data-color="${k}"]`).value = e.target.value;
    setDirty(true); schedulePreview();
  });
  $$("[data-theme-key]").forEach((el) => el.addEventListener("input", () => { draft.theme[el.dataset.themeKey] = el.value; setDirty(true); schedulePreview(); }));
  $$("[data-theme-bool]").forEach((el) => el.addEventListener("input", () => { draft.theme[el.dataset.themeBool] = el.checked; setDirty(true); schedulePreview(); }));
  $$("[data-theme-num]").forEach((el) => el.addEventListener("input", () => { draft.theme[el.dataset.themeNum] = Number(el.value); setDirty(true); schedulePreview(); }));
  $$("[data-delivery-key]").forEach((el) => el.addEventListener("input", () => {
    draft.delivery[el.dataset.deliveryKey] = el.type === "checkbox" ? el.checked : Number(el.value);
    setDirty(true); schedulePreview();
  }));

  function renderCustomVars() {
    const list = draft.custom_vars;
    $("#custom-vars").innerHTML = list.map((v, i) => `
      <div class="kv">
        <input type="text" class="mono" placeholder="name" value="${esc(v.key)}" data-cv="${i}" data-field="key" aria-label="Variable name">
        <input type="text" placeholder="value" value="${esc(v.value)}" data-cv="${i}" data-field="value" aria-label="Variable value">
        <button class="danger" data-cv-del="${i}" aria-label="Remove variable">✕</button>
      </div>`).join("") || `<p class="muted">None yet.</p>`;
  }
  $("#custom-vars").addEventListener("input", (e) => {
    const i = e.target.dataset.cv;
    if (i === undefined) return;
    let val = e.target.value;
    if (e.target.dataset.field === "key") {
      const cleaned = val.replace(/[^A-Za-z0-9_]/g, "_");
      if (cleaned !== val) { e.target.value = cleaned; val = cleaned; }
    }
    draft.custom_vars[i][e.target.dataset.field] = val;
    setDirty(true); renderVars(); schedulePreview();
  });
  $("#custom-vars").addEventListener("click", (e) => {
    const i = e.target.dataset.cvDel;
    if (i === undefined) return;
    draft.custom_vars.splice(Number(i), 1);
    renderCustomVars(); renderVars(); setDirty(true); schedulePreview();
  });
  $("#add-var").addEventListener("click", () => {
    draft.custom_vars.push({ key: "", value: "" });
    renderCustomVars();
    $$("#custom-vars [data-field=key]").at(-1)?.focus();
  });

  // Schedule
  function renderSchedule() {
    $$("select[data-hours]").forEach((sel) => {
      if (!sel.options.length) sel.innerHTML = Array.from({ length: 24 }, (_, h) => `<option value="${h}">${String(h).padStart(2, "0")}:00</option>`).join("");
    });
    $$("[data-schedule-key]").forEach((el) => (el.value = draft.schedule[el.dataset.scheduleKey] ?? ""));
    $$("[data-self-key]").forEach((el) => (el.checked = !!draft.self_service[el.dataset.selfKey]));
    $("#tz-now").textContent = `Currently ${state.server.timezone} (${state.server.now}). Leave empty to use the container's TZ setting.`;
    const tzl = $("#tz-list");
    if (!tzl.options.length && Intl.supportedValuesOf) tzl.innerHTML = Intl.supportedValuesOf("timeZone").map((z) => `<option value="${z}">`).join("");
  }
  $$("[data-schedule-key]").forEach((el) => el.addEventListener("input", () => {
    const k = el.dataset.scheduleKey;
    draft.schedule[k] = k === "timezone" ? el.value.trim() : Number(el.value);
    setDirty(true);
  }));
  $$("[data-self-key]").forEach((el) => el.addEventListener("input", () => { draft.self_service[el.dataset.selfKey] = el.checked; setDirty(true); }));

  // Recipients
  const MODE = { instant: "Instant", instant_daily: "Instant + daily digest", instant_weekly: "Instant + weekly digest", daily: "Daily digest", weekly: "Weekly digest", test: "Test" };
  const libName = (id) => (state.libraries.find((l) => l.id === id) || {}).name || id;
  function renderRecipients() {
    const rows = state.recipients.map((r) => {
      const types = !r.types || !r.types.length ? "TV and movies" : r.types.map((t) => (t === "movie" ? "Movies" : "TV")).join(", ");
      const libs = !r.libraries || !r.libraries.length ? "All libraries" : r.libraries.map(libName).join(", ");
      let status = r.active ? `<span class="pill ok">Active</span>` : r.enabled ? `<span class="pill warn">Not confirmed</span>` : `<span class="pill">Off</span>`;
      if (r.failures) status += ` <span class="pill bad" title="Retrying ${esc(fmtTime(r.next_attempt))}">${r.failures} failed</span>`;
      if (r.pending_email) status += `<div class="hint">Waiting for ${esc(r.pending_email)} to confirm</div>`;
      if (r.held_until) status += `<div class="hint">Rate limited: next email ${esc(fmtTime(r.held_until))}</div>`;
      return `<tr>
        <td><b>${esc(r.name || r.email)}</b>${r.name ? `<div class="hint">${esc(r.email)}</div>` : ""}<div class="hint">${r.source === "user" ? "Signed up themselves" : "Added by an admin"}</div></td>
        <td>${esc(MODE[r.mode] || r.mode)}${r.next_due ? `<div class="hint">Next ${esc(fmtTime(r.next_due))}</div>` : ""}</td>
        <td>${esc(types)}<div class="hint">${esc(libs)}</div>${r.max_age_hours ? `<div class="hint">New = within ${esc(((state.max_age_options || []).find((o) => o.hours === r.max_age_hours) || {}).label || r.max_age_hours + " hours")}</div>` : ""}</td>
        <td>${status}</td>
        <td>${esc(fmtTime(r.last_sent))}</td>
        <td><div class="row" style="justify-content:flex-end">
          <button data-edit="${esc(r.id)}">Edit</button>
          <button data-rewind="${esc(r.id)}" title="Send the last 24 hours again on the next run">Resend 24h</button>
          <button class="danger" data-del="${esc(r.id)}">Remove</button>
        </div></td></tr>`;
    }).join("");
    $("#recipients").innerHTML = rows || `<tr><td colspan="6" class="muted">No recipients yet. Add one, or let people turn email on in their notification settings.</td></tr>`;
  }

  function openRecipient(r) {
    const f = $("#rform");
    $("#rdlg-title").textContent = r ? "Edit recipient" : "Add recipient";
    f.rid.value = r?.id || "";
    f.rname.value = r?.name || "";
    f.remail.value = r?.email || "";
    f.rmode.value = r?.mode || "instant";
    f.rmaxage.innerHTML = (state.max_age_options || []).map((o) => `<option value="${o.hours}">${esc(o.label)}</option>`).join("");
    f.rmaxage.value = String(r?.max_age_hours || 0);
    const types = r?.types?.length ? r.types : ["episode", "movie"];
    f["type-episode"].checked = types.includes("episode");
    f["type-movie"].checked = types.includes("movie");
    f.renabled.checked = r ? r.enabled : true;
    const chosen = new Set(r?.libraries || []);
    $("#rdlg-libs").innerHTML = state.libraries.map((l) => `<label class="check"><input type="checkbox" value="${esc(l.id)}" ${chosen.has(l.id) ? "checked" : ""}> ${esc(l.name)}</label>`).join("") || `<span class="muted">No libraries found.</span>`;
    $("#rdlg").returnValue = "";
    $("#rdlg").showModal();
  }
  $("#add-recipient").addEventListener("click", () => openRecipient(null));
  $("#rdlg").addEventListener("close", async () => {
    if ($("#rdlg").returnValue !== "save") return;
    const f = $("#rform");
    const types = [];
    if (f["type-episode"].checked) types.push("episode");
    if (f["type-movie"].checked) types.push("movie");
    if (!types.length) return toast("Pick at least one of TV episodes or Movies.", true);
    const body = {
      id: f.rid.value, name: f.rname.value, email: f.remail.value, mode: f.rmode.value, enabled: f.renabled.checked,
      max_age_hours: Number(f.rmaxage.value),
      types: types.length === 2 ? [] : types,
      libraries: $$("#rdlg-libs input:checked").map((i) => i.value),
    };
    try {
      await api("recipients", { method: "POST", body });
      toast("Recipient saved.");
      await refreshKeepDraft();
    } catch (e) { toast(e.message, true); }
  });
  $("#recipients").addEventListener("click", async (e) => {
    const b = e.target.closest("button");
    if (!b) return;
    const id = b.dataset.edit || b.dataset.del || b.dataset.rewind;
    const r = state.recipients.find((x) => x.id === id);
    if (!r) return;
    if (b.dataset.edit) return openRecipient(r);
    if (b.dataset.del) {
      if (!(await confirmBox("Remove recipient?", `${r.email} will stop getting alerts. This can't be undone.`, "Remove"))) return;
      try { await api("recipients/" + encodeURIComponent(id), { method: "DELETE" }); toast("Removed."); await refreshKeepDraft(); } catch (err) { toast(err.message, true); }
    }
    if (b.dataset.rewind) {
      if (!(await confirmBox("Resend the last 24 hours?", `Everything added in the last 24 hours goes to ${r.email} again on the next run, which is within a few minutes.`, "Resend"))) return;
      try { await api(`recipients/${encodeURIComponent(id)}/rewind`, { method: "POST", body: { hours: 24 } }); toast("Queued. Use Check and send now on the Overview tab to send it straight away."); } catch (err) { toast(err.message, true); }
    }
  });

  async function refreshKeepDraft() {
    const keep = draft, wasDirty = dirty;
    state = await api("state");
    draft = wasDirty ? keep : structuredClone(state.settings);
    if (!draft.custom_vars) draft.custom_vars = [];
    renderAll();
    setDirty(wasDirty);
  }

  // Log
  function renderLog() {
    $("#log").innerHTML = (state.log || []).map((l) => `<tr>
      <td>${esc(fmtTime(l.time))}</td><td>${esc(l.recipient)}</td><td>${esc(MODE[l.mode] || l.mode)}</td><td>${l.items}</td>
      <td>${l.error ? `<span style="color:var(--bad)">${esc(l.error)}</span>` : esc(l.subject)}</td></tr>`).join("") || `<tr><td colspan="5" class="muted">Nothing sent yet.</td></tr>`;
  }

  // Actions
  $("#save").addEventListener("click", async () => {
    $("#save").disabled = true;
    try {
      state = await api("settings", { method: "PUT", body: { settings: draft } });
      draft = structuredClone(state.settings);
      if (!draft.custom_vars) draft.custom_vars = [];
      renderAll();
      setDirty(false);
      toast("Saved.");
    } catch (e) { toast(e.message, true); setDirty(true); }
  });

  $("#run-now").addEventListener("click", async () => {
    const b = $("#run-now");
    b.disabled = true;
    $("#run-result").textContent = "Checking…";
    try {
      const r = await api("run", { method: "POST" });
      $("#run-result").textContent = r.skipped || `${r.emails_sent} email(s) sent. ${r.items_found} recent item(s) found, ${r.recipients_due} recipient(s) due.` + (r.errors?.length ? " Errors: " + r.errors.join("; ") : "");
      await refreshKeepDraft();
    } catch (e) { $("#run-result").textContent = e.message; }
    b.disabled = false;
  });

  async function sendTest(email, mode, btn, pick = "") {
    if (!email) return toast("Enter an address to send the test to.", true);
    btn.disabled = true;
    try {
      const r = await api("test", { method: "POST", body: { email, mode, pick, settings: draft } });
      toast(`Sent “${r.subject}” to ${email}.`);
      refreshKeepDraft();
    } catch (e) { toast("Test failed: " + e.message, true); }
    btn.disabled = false;
  }
  $("#test-send").addEventListener("click", (e) => sendTest($("#test-email").value.trim(), $("#pv-mode").value, e.currentTarget));
  $("#test-send-ov").addEventListener("click", (e) => sendTest($("#test-email-ov").value.trim(), $("#test-mode-ov").value, e.currentTarget, $("#test-pick-ov").value));

  const onBeforeUnload = (e) => { if (dirty) { e.preventDefault(); e.returnValue = ""; } };
  window.addEventListener("beforeunload", onBeforeUnload);

  load().catch((e) => {
    $("#server-line").textContent = "";
    $("#problems").innerHTML = `<div class="banner bad">Couldn't load email alerts: ${esc(e.message)}</div>`;
  });

  return () => {
    window.removeEventListener("beforeunload", onBeforeUnload);
    clearTimeout(previewTimer);
  };
}
