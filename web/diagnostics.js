"use strict";

// ==========================================================================
// TANILAMA
//
// Uygulamalardan CPU profili ve bellek dump'ı ister, sonucu indirir.
//
// Akış: nabiz, örneğin kaydında gördüğü IP'ye istek atar, uygulama dosyayı
// üretir, nabiz çeker ve saklar. Jeton nabiz'de şifreli durur ve hiçbir zaman
// arayüze dönmez.
// ==========================================================================

const diag = { instances: [], artifacts: [], storage: "", loaded: false, busy: new Set() };

async function refreshDiagnostics() {
  const body = $("#diag-body");
  if (!diag.loaded) showState(body, "empty", t("status.loading"));
  try {
    const [instances, artifacts] = await Promise.all([
      api("/api/v1/diagnostics/instances"),
      api("/api/v1/diagnostics/artifacts"),
    ]);
    diag.instances = instances.instances || [];
    diag.artifacts = artifacts.artifacts || [];
    diag.storage = artifacts.storage || "";
    diag.loaded = true;
    markOk();
    renderDiagnostics();
  } catch (e) {
    if (e.status === 401) return;
    showState(body, "error", friendlyError(e));
    markStale();
  }
}

function renderDiagnostics() {
  const body = $("#diag-body");
  const wrap = el("div");

  wrap.append(el("div", { class: "panel-note danger-note", text: t("diag.warning") }));

  // --- çalışan örnekler ---
  wrap.append(el("div", { class: "detail-section pad", text: t("diag.instances") }));
  if (!diag.instances.length) {
    wrap.append(el("div", { class: "empty", text: t("diag.noInstances") }));
  } else {
    const table = el("table");
    table.append(el("thead", {}, tr([
      th(t("th.service")), th(t("diag.instance")), th(t("diag.address")),
      th(t("diag.state")), th(t("diag.lastSeen"), true), th(""),
    ])));
    const tbody = el("tbody");
    for (const i of diag.instances) {
      const ready = i.verified && i.diagReady;
      const state = !i.verified
        ? el("span", { class: "pill warn", text: t("diag.unverified") })
        : !i.diagReady
          ? el("span", { class: "pill warn", text: t("diag.downloadOff") })
          : el("span", { class: "pill ok", text: t("diag.ready") });

      const actions = el("td", { class: "num actions" });
      if (ready) {
        const key = i.id;
        const busy = diag.busy.has(key);
        actions.append(
          el("button", {
            class: "link-btn", text: busy ? t("diag.working") : t("diag.captureCpu"),
            disabled: busy ? "" : null,
            onclick: () => capture(i, "cpu"),
          }),
          el("button", {
            class: "link-btn danger", text: t("diag.captureMemory"),
            disabled: busy ? "" : null,
            onclick: () => capture(i, "memory"),
          }));
      } else {
        actions.append(el("span", { class: "hint", text: "—" }));
      }

      tbody.append(el("tr", {},
        td(el("b", { text: i.serviceName })),
        td(el("span", {}, el("span", { class: "mono", text: i.pod || i.instanceId }),
          i.namespace ? el("i", { class: "tag", text: i.namespace }) : null)),
        td(el("span", { class: "mono hint", text: i.sourceIp + ":" + i.diagPort })),
        td(state),
        tdNum(el("span", { class: "hint", text: new Date(i.lastSeen).toLocaleTimeString(LOCALE) })),
        actions));
    }
    table.append(tbody);
    wrap.append(table);
  }

  // --- alınan dosyalar ---
  wrap.append(el("div", { class: "detail-section pad", text: t("diag.artifacts") },
    diag.storage ? el("span", { class: "field-hint mono", text: diag.storage }) : null));

  if (!diag.artifacts.length) {
    wrap.append(el("div", { class: "empty", text: t("diag.noArtifacts") }));
  } else {
    const table = el("table");
    table.append(el("thead", {}, tr([
      th(t("th.service")), th(t("diag.kind")), th(t("diag.file")),
      th(t("diag.size"), true), th(t("diag.state")), th(t("diag.by")), th(t("th.time"), true), th(""),
    ])));
    const tbody = el("tbody");
    for (const a of diag.artifacts) {
      const status = a.status === "ready"
        ? el("span", { class: "pill ok", text: t("diag.ready") })
        : a.status === "failed"
          ? el("span", { class: "pill err", text: t("diag.failed"), title: a.error || "" })
          : el("span", { class: "pill warn", text: t("diag.pending") });

      const actions = el("td", { class: "num actions" });
      if (a.status === "ready") {
        actions.append(el("a", {
          class: "link-btn", text: t("diag.download"),
          href: "/api/v1/diagnostics/artifacts/" + a.id + "/download",
        }));
      }
      actions.append(el("button", {
        class: "link-btn danger", text: t("btn.delete"),
        onclick: () => confirmDelete(a.filename || a.kind,
          () => api("/api/v1/diagnostics/artifacts/" + a.id, { method: "DELETE" })
                  .then(refreshDiagnostics)),
      }));

      tbody.append(el("tr", {},
        td(el("b", { text: a.serviceName })),
        td(el("i", { class: "tag", text: t("diag.kind." + a.kind) })),
        td(el("span", { class: "mono hint", text: a.filename || "—" })),
        tdNum(a.bytes ? fmtBytes(a.bytes) : "—"),
        td(status),
        td(el("span", { class: "hint", text: a.createdBy })),
        tdNum(el("span", { class: "hint", text: new Date(a.createdAt).toLocaleString(LOCALE) })),
        actions));
    }
    table.append(tbody);
    wrap.append(table);
  }

  body.replaceChildren(wrap);
}

// capture, dump isteğini başlatır.
//
// Sunucu 202 döner ve işi arka planda yapar: bellek dump'ı dakikalar sürebilir
// ve tarayıcı beklerken zaman aşımına uğrardı. Liste kendiliğinden tazelenip
// sonucu gösterir.
async function capture(instance, kind) {
  if (kind === "memory" && !window.confirm(t("diag.memoryConfirm"))) return;

  diag.busy.add(instance.id);
  renderDiagnostics();
  try {
    const body = kind === "cpu" ? { kind: "cpu", seconds: 20 } : { kind: "memory", type: "heap" };
    await apiJSON("/api/v1/diagnostics/instances/" + instance.id + "/capture", "POST", body);
    await refreshDiagnostics();
  } catch (e) {
    window.alert(friendlyError(e));
  } finally {
    diag.busy.delete(instance.id);
    renderDiagnostics();
  }
}
