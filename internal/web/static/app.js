// Behaviour that can't be inline: the Content-Security-Policy forbids
// inline scripts and event handlers. Listeners sit on the document so they
// keep working on content htmx swaps in.

function updateSelection(form) {
  const count = form.querySelectorAll('input[type=checkbox][name="slot_id"]:checked').length;
  form.querySelectorAll("[data-needs-selection]").forEach((b) => { b.disabled = count === 0; });
  const label = form.querySelector("[data-selection-count]");
  if (label) {
    label.textContent = count === 0 ? "No test cases selected"
      : count === 1 ? "1 test case selected" : `${count} test cases selected`;
  }
}

document.addEventListener("change", (e) => {
  const name = e.target.dataset && e.target.dataset.selectAll;
  const form = e.target.closest("form");
  if (name && form) {
    form.querySelectorAll(`input[type=checkbox][name="${name}"]`).forEach((box) => {
      box.checked = e.target.checked;
    });
  }
  if (form && form.hasAttribute("data-selection-form")) updateSelection(form);
});

document.addEventListener("click", (e) => {
  const confirmButton = e.target.closest("[data-confirm]");
  if (confirmButton && !window.confirm(confirmButton.dataset.confirm)) {
    e.preventDefault();
    return;
  }
  // Expand/collapse a test case's detail row; htmx loads its content on the
  // first click.
  const toggle = e.target.closest("[data-toggle-row]");
  if (toggle) {
    const row = document.getElementById(toggle.dataset.toggleRow);
    if (!row) return;
    const open = row.hidden;
    row.hidden = !open;
    toggle.setAttribute("aria-expanded", String(open));
  }
});

// --- loading feedback ---------------------------------------------------------
// Every htmx request shows the top progress bar; the area it will refresh
// (the nearest [data-loading] around its target) also gets a spinner overlay.
// Background polling (hx-trigger="every …") stays silent.

const pending = new Map(); // request → the [data-loading] box it refreshes, or null

function syncLoading() {
  for (const xhr of pending.keys()) {
    if (xhr.readyState === 4) pending.delete(xhr);
  }
  const boxes = new Set([...pending.values()].filter(Boolean));
  document.querySelectorAll("[data-loading]").forEach((el) => {
    const loading = boxes.has(el);
    el.classList.toggle("is-loading", loading);
    if (loading) el.setAttribute("aria-busy", "true"); else el.removeAttribute("aria-busy");
  });
  document.body.classList.toggle("is-busy", pending.size > 0);
}

document.addEventListener("htmx:beforeRequest", (e) => {
  const trigger = e.detail.elt && e.detail.elt.getAttribute && e.detail.elt.getAttribute("hx-trigger");
  if (trigger && trigger.includes("every")) return;
  const target = e.detail.target;
  const box = target && target.closest ? target.closest("[data-loading]") : null;
  pending.set(e.detail.xhr, box);
  syncLoading();
});

for (const evt of ["htmx:afterRequest", "htmx:afterSwap", "htmx:afterSettle",
  "htmx:sendError", "htmx:responseError", "htmx:timeout", "htmx:abort"]) {
  document.addEventListener(evt, syncLoading);
}

// Full page loads (plain links and form posts) show the same progress bar
// until the next page arrives, and a submitted form's buttons disable so a
// slow response can't be submitted twice.
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (e.defaultPrevented || form.hasAttribute("hx-get") || form.hasAttribute("hx-post")) return;
  document.body.classList.add("is-busy");
  // Disable after the browser has captured the form data, so the clicked
  // button's name/value (e.g. action=regenerate) is still submitted.
  setTimeout(() => form.querySelectorAll("button[type=submit], button:not([type])")
    .forEach((b) => { b.disabled = true; }), 0);
});

document.addEventListener("click", (e) => {
  const link = e.target.closest("a[href]");
  if (!link || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (link.hasAttribute("hx-get") || link.target === "_blank" || link.hasAttribute("download")) return;
  if (link.origin !== window.location.origin || link.getAttribute("href").startsWith("#")) return;
  document.body.classList.add("is-busy");
});

// Coming back via the back button restores the page from cache with the
// busy state still on; clear it.
window.addEventListener("pageshow", () => {
  document.body.classList.remove("is-busy");
  document.querySelectorAll("form button[disabled]:not([data-needs-selection])")
    .forEach((b) => { b.disabled = false; });
  document.querySelectorAll("form[data-selection-form]").forEach(updateSelection);
});
