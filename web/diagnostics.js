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

const diag = { instances: [], artifacts: [], storage: null, loaded: false, busy: new Set() };

// Heartbeat 60 saniyede bir; bir tur kaçırmış örnek muhtemelen kapanmıştır.
// Ölü bir örneğe dump düğmesi göstermek, kullanıcıyı anlamsız bir hataya
// sürükler.
const STALE_AFTER_MS = 95_000;
const isStale = (instance) => Date.now() - new Date(instance.lastSeen).getTime() > STALE_AFTER_MS;

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
    diag.storage = artifacts.storage || null;
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
      const stale = isStale(i);
      const ready = i.verified && i.diagReady && !stale;
      const state = stale
        ? el("span", { class: "pill err", text: t("diag.stale") })
        : !i.verified
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
  wrap.append(el("div", { class: "detail-section pad", text: t("diag.artifacts") }));
  if (diag.storage) wrap.append(renderStorage(diag.storage));

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

// capture, dump isteğini başlatır ve ilerleme penceresini açar.
//
// Sunucu 202 döner ve işi arka planda yapar: bellek dump'ı dakikalar sürebilir
// ve tarayıcı beklerken zaman aşımına uğrardı. Pencere kaydın durumunu
// yoklayarak ilerlemeyi gösterir.
async function capture(instance, kind) {
  if (kind === "memory" && !window.confirm(t("diag.memoryConfirm"))) return;

  diag.busy.add(instance.id);
  renderDiagnostics();
  try {
    const body = kind === "cpu" ? { kind: "cpu", seconds: 20 } : { kind: "memory", type: "heap" };
    const started = await apiJSON(
      "/api/v1/diagnostics/instances/" + instance.id + "/capture", "POST", body);
    openCaptureProgress(started.artifactId, kind, instance);
  } catch (e) {
    window.alert(friendlyError(e));
  } finally {
    diag.busy.delete(instance.id);
    renderDiagnostics();
  }
}

// --- ilerleme penceresi ---

let captureTimer = null;

// openCaptureProgress, dump sürerken ilerlemeyi gösteren pencereyi açar.
//
// Süre tahmini vermiyoruz: bellek dump'ının ne kadar süreceği sürecin
// belleğine bağlı ve uydurma bir yüzde çubuğu, bekleyeni yanıltmaktan başka
// işe yaramaz. Geçen süre gerçek bilgidir, o gösteriliyor.
function openCaptureProgress(artifactId, kind, instance) {
  const startedAt = Date.now();
  const dlg = $("#capture-dialog");
  const body = $("#capture-body");
  const stopBtn = $("#capture-stop");
  const closeBtn = $("#capture-close");

  $("#capture-title").textContent = t("diag.kind." + kind);
  dlg.hidden = false;
  stopBtn.hidden = false;
  stopBtn.disabled = false;
  stopBtn.textContent = t("diag.stop");
  closeBtn.textContent = t("diag.runInBackground");

  const elapsed = () => {
    const s = Math.floor((Date.now() - startedAt) / 1000);
    return s < 60 ? `${s} sn` : `${Math.floor(s / 60)} dk ${s % 60} sn`;
  };

  // running=false iken durdurma ipucu gösterilmiyor: iş bittikten sonra
  // "durdurursanız…" demek okuyanı bir daha yapamayacağı şeye yönlendirir.
  const paint = (state, running = true) => {
    const rows = [
      el("div", { class: "cap-target" },
        el("b", { text: instance.serviceName }),
        el("span", { class: "hint", text: " · " + (instance.pod || instance.instanceId) })),
      el("div", { class: "cap-state" }, state),
    ];
    if (running) {
      rows.push(el("div", { class: "cap-note", text: kind === "memory"
        ? t("diag.memoryNotInterruptible") : t("diag.cpuInterruptible") }));
    }
    body.replaceChildren(...rows);
  };

  paint(el("span", {},
    el("span", { class: "spinner" }),
    el("span", { text: t("diag.capturing") + " · " + elapsed() })));

  const finish = (node, keepStop) => {
    clearInterval(captureTimer);
    captureTimer = null;
    stopBtn.hidden = !keepStop;
    closeBtn.textContent = t("btn.close");
    paint(node, false);
    refreshDiagnostics();
  };

  captureTimer = setInterval(async () => {
    paint(el("span", {},
      el("span", { class: "spinner" }),
      el("span", { text: t("diag.capturing") + " · " + elapsed() })));
    try {
      const a = await api("/api/v1/diagnostics/artifacts/" + artifactId);
      if (a.status === "ready") {
        finish(el("span", {},
          el("span", { class: "pill ok", text: t("diag.ready") }),
          el("span", { text: "  " + fmtBytes(a.bytes) + " · " + elapsed() }),
          el("a", {
            class: "btn primary cap-download", text: t("diag.download"),
            href: "/api/v1/diagnostics/artifacts/" + a.id + "/download",
          })), false);
      } else if (a.status === "failed") {
        finish(el("span", {},
          el("span", { class: "pill err", text: t("diag.failed") }),
          el("div", { class: "cap-error", text: a.error || "" })), false);
      } else if (a.status === "cancelled") {
        finish(el("span", { class: "pill warn", text: t("diag.cancelled") }), false);
      }
    } catch (e) {
      if (e.status === 401) finish(el("span", { class: "pill err", text: t("err.forbidden") }), false);
    }
  }, 2000);

  stopBtn.onclick = async () => {
    stopBtn.disabled = true;
    stopBtn.textContent = t("diag.stopping");
    try {
      const r = await apiJSON("/api/v1/diagnostics/artifacts/" + artifactId + "/cancel", "POST", {});
      if (r.agentStopped) {
        // Kesilebildi: uygulamanın elinde o ana kadarki geçerli profil var,
        // nabiz onu indiriyor. Yoklama birazdan "hazır" görecek.
        $("#capture-note").textContent = t("diag.stoppedPartial");
        $("#capture-note").hidden = false;
        stopBtn.hidden = true;
      } else {
        // Kesilemedi. Durdur düğmesini bırakmak, tekrar denendiğinde yine
        // olmayacak bir şeyi vaat etmek olurdu.
        $("#capture-note").textContent = r.reason || t("diag.memoryNotInterruptible");
        $("#capture-note").hidden = false;
        stopBtn.hidden = true;
      }
    } catch (e) {
      window.alert(friendlyError(e));
      stopBtn.disabled = false;
      stopBtn.textContent = t("diag.stop");
    }
  };

  closeBtn.onclick = () => {
    // Pencereyi kapatmak işi durdurmaz: kullanıcı arka planda devam etmesini
    // isteyebilir. Durdurmak için ayrı düğme var.
    clearInterval(captureTimer);
    captureTimer = null;
    $("#capture-note").hidden = true;
    dlg.hidden = true;
    refreshDiagnostics();
  };
}

// renderStorage, disk kullanımını ve saklama kuralını gösterir.
//
// Bellek dump'ları yüzlerce MB; kotanın ne kadarının dolduğunu görmeden
// çalışmak, nabiz'in diskinin izlediği sistemden önce dolmasıyla biter.
function renderStorage(storage) {
  const used = storage.usedBytes || 0;
  const quota = storage.quotaBytes || 0;
  const ratio = quota > 0 ? Math.min(used / quota, 1) : 0;
  const level = ratio > 0.9 ? "err" : ratio > 0.7 ? "warn" : "ok";

  return el("div", { class: "storage-bar" },
    el("div", { class: "storage-head" },
      el("span", { class: "mono hint", text: storage.directory }),
      el("div", { class: "spacer" }),
      el("span", { class: "hint", text:
        `${storage.files} ${t("diag.fileCount")} · ${fmtBytes(used)} / ${fmtBytes(quota)} · ` +
        `${t("diag.retention")} ${storage.retentionDays} ${t("diag.days")}` })),
    el("div", { class: "bd-bar" },
      el("div", { class: "bd-seg storage-" + level, style: "width:" + (ratio * 100) + "%" })));
}
