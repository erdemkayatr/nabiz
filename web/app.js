"use strict";

// ==========================================================================
// DİL
//
// Sözlük tek yerde. Statik metinler data-i18n ile işaretli, dinamikler t()
// üzerinden geçer. Sayı ve saat biçimi de dile bağlıdır.
// ==========================================================================

const STRINGS = {
  tr: {
    "nav.topology": "Topoloji", "nav.services": "Servisler", "nav.traces": "Trace'ler", "nav.admin": "Yönetim",
    "range.15m": "Son 15 dakika", "range.1h": "Son 1 saat", "range.6h": "Son 6 saat", "range.24h": "Son 24 saat",
    "btn.refresh": "Yenile", "btn.search": "Ara", "btn.backToList": "← listeye dön", "btn.close": "Kapat",
    "btn.save": "Kaydet", "btn.cancel": "Vazgeç", "btn.create": "Oluştur", "btn.delete": "Sil",
    "btn.edit": "Düzenle", "btn.new": "Yeni",
    "live.auto": "otomatik", "live.offline": "bağlantı yok",
    "topo.title": "Servis topolojisi", "topo.hint": "isteklerden çıkarıldı — elle tanımlanmadı",
    "level.service": "Servis seviyesi", "level.workload": "Kubernetes workload", "level.namespace": "Kubernetes namespace",
    "zoom.in": "Yakınlaştır", "zoom.out": "Uzaklaştır", "zoom.fit": "Ekrana sığdır", "zoom.fitShort": "sığdır",
    "legend.errorRate": "hata oranı > %1",
    "legend.size": "daire boyutu = trafik · akan noktalar = çağrı hızı",
    "legend.drag": "düğümü sürükleyin, tekerlekle yakınlaştırın",
    "services.title": "Servisler", "services.hint": "giriş span'lerinden hesaplanan RED metrikleri",
    "traces.title": "Trace'ler",
    "filter.service": "servis adı", "filter.minMs": "min ms", "filter.onlyErrors": "sadece hatalılar",
    "a11y.range": "Zaman aralığı", "a11y.level": "Topoloji seviyesi",
    "type.service": "servis", "type.database": "veritabanı", "type.messaging": "kuyruk",
    "type.external": "dış bağımlılık", "type.user": "giriş noktası", "type.entry": "giriş trafiği",
    "th.service": "Servis", "th.kubernetes": "Kubernetes", "th.rate": "Hız", "th.errors": "Hata",
    "th.calls": "Çağrı", "th.rootOp": "Kök işlem", "th.services": "Servisler", "th.duration": "Süre",
    "th.spans": "Span", "th.time": "Zaman",
    "detail.namespace": "namespace", "detail.workload": "workload", "detail.calls": "çağrı",
    "detail.errorRate": "hata oranı", "detail.errors": "hata", "detail.latency": "gecikme",
    "detail.avg": "ortalama", "detail.max": "maksimum",
    "detail.inbound": "gelen", "detail.outbound": "giden", "detail.goTraces": "Bu servisin trace'leri →",
    "tooltip.type": "tür", "tooltip.calls": "çağrı", "tooltip.error": "hata", "tooltip.avg": "ort",
    "tooltip.start": "başlangıç", "tooltip.duration": "süre", "tooltip.failed": "HATA",
    "label.query": "sorgu", "label.target": "hedef", "label.unknown": "bilinmiyor",
    "status.loading": "Yükleniyor…",
    "empty.traffic": "Bu aralıkta trafik yok.", "empty.services": "Bu aralıkta servis verisi yok.",
    "empty.traces": "Bu filtrelerle trace bulunamadı.", "empty.spans": "Bu trace'te span yok.",
    "err.topology": "Topoloji alınamadı", "err.services": "Servisler alınamadı",
    "err.traces": "Trace'ler alınamadı", "err.trace": "Trace açılamadı",
    "unit.perSec": "/sn", "unit.perMin": "/dk", "unit.million": " Mn",

    // --- giriş ---
    "login.title": "nabiz'e giriş",
    "login.subtitle": "Devam etmek için hesabınızla giriş yapın",
    "login.email": "E-posta", "login.password": "Parola", "login.submit": "Giriş yap",
    "login.working": "Giriş yapılıyor…",
    "login.badCredentials": "E-posta ya da parola hatalı.",
    "login.inactive": "Hesabınız pasif durumda. Yöneticinizle görüşün.",
    "login.rateLimited": "Çok fazla başarısız deneme. Birkaç dakika sonra tekrar deneyin.",
    "login.failed": "Giriş yapılamadı.",
    "login.noAccess": "Hiçbir projeye erişiminiz yok. Yöneticinizin sizi bir rol grubuna eklemesi gerekiyor.",

    // --- kullanıcı menüsü ---
    "user.logout": "Çıkış yap", "user.changePassword": "Parolamı değiştir",
    "user.superAdmin": "Sistem yöneticisi",
    "pw.title": "Parolayı değiştir", "pw.current": "Mevcut parola", "pw.new": "Yeni parola",
    "pw.confirm": "Yeni parola (tekrar)", "pw.mismatch": "Parolalar eşleşmiyor.",
    "pw.tooShort": "Parola en az 8 karakter olmalı.",
    "pw.changed": "Parola değişti. Yeniden giriş yapmanız gerekiyor.",

    // --- proje seçici ---
    "project.all": "Tüm projeler", "project.label": "Proje",

    // --- yönetim ---
    "admin.users": "Kullanıcılar", "admin.roleGroups": "Rol grupları", "admin.projects": "Projeler",
    "admin.newUser": "Yeni kullanıcı", "admin.newRoleGroup": "Yeni rol grubu", "admin.newProject": "Yeni proje",
    "admin.editUser": "Kullanıcıyı düzenle", "admin.editRoleGroup": "Rol grubunu düzenle",
    "admin.editProject": "Projeyi düzenle",
    "admin.name": "Ad", "admin.email": "E-posta", "admin.password": "Parola",
    "admin.description": "Açıklama", "admin.key": "Anahtar", "admin.permissions": "Yetkiler",
    "admin.roleGroupsOf": "Rol grupları", "admin.applications": "Uygulamalar",
    "admin.members": "Üye", "admin.projectCount": "Proje", "admin.status": "Durum",
    "admin.active": "Aktif", "admin.inactive": "Pasif", "admin.superAdmin": "Sistem yöneticisi",
    "admin.lastLogin": "Son giriş", "admin.never": "hiç",
    "admin.resetPassword": "Parola ata", "admin.newPassword": "Yeni parola",
    "admin.noUsers": "Henüz kullanıcı yok.", "admin.noRoleGroups": "Henüz rol grubu yok.",
    "admin.noProjects": "Henüz proje yok.",
    "admin.appsHint": "Telemetri gönderen servisler. Seçilenler bu projeye atanır.",
    "admin.noDiscovered": "Henüz telemetri gönderen bir servis görülmedi.",
    "admin.groupsHint": "Bu projeye erişebilecek rol grupları.",
    "admin.permsHint": "Bu gruptaki kullanıcılar, gruba bağlı projelerde bu işlemleri yapabilir.",
    "admin.confirmDelete": "silinsin mi?",
    "admin.keyHint": "kısa, benzersiz, değişmez",
    "admin.lastSeen": "son görülme", "admin.assignedTo": "atandığı projeler",
    "admin.unassigned": "atanmamış",
    "admin.projectsHint": "Uygulamalar projelere atanır, rol grupları projelere bağlanır. Kullanıcı bir uygulamanın verisini, ancak ait olduğu rol grubu o uygulamanın projesine bağlıysa görebilir.",

    "perm.topology.read": "Topolojiyi görüntüle",
    "perm.services.read": "Servis metriklerini görüntüle",
    "perm.traces.read": "Trace'leri görüntüle",
    "perm.project.manage": "Proje uygulamalarını yönet",
    "perm.admin.manage": "Tüm sistemi yönet",

    "err.forbidden": "Bu işlem için yetkiniz yok.",
    "err.duplicate": "Bu ad ya da e-posta zaten kullanımda.",
    "err.lastAdmin": "Sistemdeki son yöneticiyi kaldıramazsınız.",
    "err.generic": "İşlem tamamlanamadı",
    "empty.noPermission": "Bu bölüm için yetkiniz yok.",
  },
  en: {
    "nav.topology": "Topology", "nav.services": "Services", "nav.traces": "Traces", "nav.admin": "Administration",
    "range.15m": "Last 15 minutes", "range.1h": "Last 1 hour", "range.6h": "Last 6 hours", "range.24h": "Last 24 hours",
    "btn.refresh": "Refresh", "btn.search": "Search", "btn.backToList": "← back to list", "btn.close": "Close",
    "btn.save": "Save", "btn.cancel": "Cancel", "btn.create": "Create", "btn.delete": "Delete",
    "btn.edit": "Edit", "btn.new": "New",
    "live.auto": "live", "live.offline": "disconnected",
    "topo.title": "Service topology", "topo.hint": "derived from requests — never hand-defined",
    "level.service": "Service level", "level.workload": "Kubernetes workload", "level.namespace": "Kubernetes namespace",
    "zoom.in": "Zoom in", "zoom.out": "Zoom out", "zoom.fit": "Fit to screen", "zoom.fitShort": "fit",
    "legend.errorRate": "error rate > 1%",
    "legend.size": "circle size = traffic · moving dots = call rate",
    "legend.drag": "drag nodes, scroll to zoom",
    "services.title": "Services", "services.hint": "RED metrics computed from entry spans",
    "traces.title": "Traces",
    "filter.service": "service name", "filter.minMs": "min ms", "filter.onlyErrors": "errors only",
    "a11y.range": "Time range", "a11y.level": "Topology level",
    "type.service": "service", "type.database": "database", "type.messaging": "queue",
    "type.external": "external dependency", "type.user": "entry point", "type.entry": "entry traffic",
    "th.service": "Service", "th.kubernetes": "Kubernetes", "th.rate": "Rate", "th.errors": "Errors",
    "th.calls": "Calls", "th.rootOp": "Root operation", "th.services": "Services", "th.duration": "Duration",
    "th.spans": "Spans", "th.time": "Time",
    "detail.namespace": "namespace", "detail.workload": "workload", "detail.calls": "calls",
    "detail.errorRate": "error rate", "detail.errors": "errors", "detail.latency": "latency",
    "detail.avg": "average", "detail.max": "maximum",
    "detail.inbound": "inbound", "detail.outbound": "outbound", "detail.goTraces": "Traces for this service →",
    "tooltip.type": "type", "tooltip.calls": "calls", "tooltip.error": "errors", "tooltip.avg": "avg",
    "tooltip.start": "start", "tooltip.duration": "duration", "tooltip.failed": "ERROR",
    "label.query": "query", "label.target": "target", "label.unknown": "unknown",
    "status.loading": "Loading…",
    "empty.traffic": "No traffic in this range.", "empty.services": "No service data in this range.",
    "empty.traces": "No traces match these filters.", "empty.spans": "This trace has no spans.",
    "err.topology": "Could not load topology", "err.services": "Could not load services",
    "err.traces": "Could not load traces", "err.trace": "Could not open trace",
    "unit.perSec": "/s", "unit.perMin": "/min", "unit.million": "M",

    "login.title": "Sign in to nabiz",
    "login.subtitle": "Sign in with your account to continue",
    "login.email": "Email", "login.password": "Password", "login.submit": "Sign in",
    "login.working": "Signing in…",
    "login.badCredentials": "Incorrect email or password.",
    "login.inactive": "Your account is inactive. Contact your administrator.",
    "login.rateLimited": "Too many failed attempts. Try again in a few minutes.",
    "login.failed": "Could not sign in.",
    "login.noAccess": "You have access to no projects. An administrator needs to add you to a role group.",

    "user.logout": "Sign out", "user.changePassword": "Change my password",
    "user.superAdmin": "System administrator",
    "pw.title": "Change password", "pw.current": "Current password", "pw.new": "New password",
    "pw.confirm": "New password (again)", "pw.mismatch": "Passwords do not match.",
    "pw.tooShort": "Password must be at least 8 characters.",
    "pw.changed": "Password changed. Please sign in again.",

    "project.all": "All projects", "project.label": "Project",

    "admin.users": "Users", "admin.roleGroups": "Role groups", "admin.projects": "Projects",
    "admin.newUser": "New user", "admin.newRoleGroup": "New role group", "admin.newProject": "New project",
    "admin.editUser": "Edit user", "admin.editRoleGroup": "Edit role group",
    "admin.editProject": "Edit project",
    "admin.name": "Name", "admin.email": "Email", "admin.password": "Password",
    "admin.description": "Description", "admin.key": "Key", "admin.permissions": "Permissions",
    "admin.roleGroupsOf": "Role groups", "admin.applications": "Applications",
    "admin.members": "Members", "admin.projectCount": "Projects", "admin.status": "Status",
    "admin.active": "Active", "admin.inactive": "Inactive", "admin.superAdmin": "System administrator",
    "admin.lastLogin": "Last sign-in", "admin.never": "never",
    "admin.resetPassword": "Set password", "admin.newPassword": "New password",
    "admin.noUsers": "No users yet.", "admin.noRoleGroups": "No role groups yet.",
    "admin.noProjects": "No projects yet.",
    "admin.appsHint": "Services sending telemetry. Selected ones are assigned to this project.",
    "admin.noDiscovered": "No service has sent telemetry yet.",
    "admin.groupsHint": "Role groups that can access this project.",
    "admin.permsHint": "Users in this group can do these things in the projects the group is bound to.",
    "admin.confirmDelete": "— delete it?",
    "admin.keyHint": "short, unique, permanent",
    "admin.lastSeen": "last seen", "admin.assignedTo": "assigned to",
    "admin.unassigned": "unassigned",
    "admin.projectsHint": "Applications are assigned to projects, role groups are bound to projects. A user sees an application's data only when one of their role groups is bound to that application's project.",

    "perm.topology.read": "View topology",
    "perm.services.read": "View service metrics",
    "perm.traces.read": "View traces",
    "perm.project.manage": "Manage project applications",
    "perm.admin.manage": "Administer the whole system",

    "err.forbidden": "You do not have permission for this action.",
    "err.duplicate": "That name or email is already in use.",
    "err.lastAdmin": "You cannot remove the last administrator.",
    "err.generic": "The operation could not be completed",
    "empty.noPermission": "You do not have permission for this section.",
  },
};

const LOCALES = { tr: "tr-TR", en: "en-US" };

function pickLang() {
  try {
    const saved = localStorage.getItem("nabiz.lang");
    if (saved && STRINGS[saved]) return saved;
  } catch (_) {}
  return (navigator.language || "").toLowerCase().startsWith("tr") ? "tr" : "en";
}

let LANG = pickLang();
let LOCALE = LOCALES[LANG];
const t = (key) => (STRINGS[LANG][key] !== undefined ? STRINGS[LANG][key] : key);

function setLang(lang) {
  if (!STRINGS[lang]) return;
  LANG = lang; LOCALE = LOCALES[lang];
  try { localStorage.setItem("nabiz.lang", lang); } catch (_) {}
  document.getElementById("html-root").setAttribute("lang", lang);
  applyStaticStrings();
  renderUserChip();
  renderProjectSelector();
  if (topo.nodes.length) renderGraph();
  refresh();
}

function applyStaticStrings() {
  for (const n of document.querySelectorAll("[data-i18n]")) n.textContent = t(n.dataset.i18n);
  for (const n of document.querySelectorAll("[data-i18n-ph]")) n.setAttribute("placeholder", t(n.dataset.i18nPh));
  for (const n of document.querySelectorAll("[data-i18n-aria]")) n.setAttribute("aria-label", t(n.dataset.i18nAria));
  for (const n of document.querySelectorAll("[data-i18n-title]")) n.setAttribute("title", t(n.dataset.i18nTitle));
  markLive();
}

// ==========================================================================
// TEMEL YARDIMCILAR
// ==========================================================================

const SVGNS = "http://www.w3.org/2000/svg";

const state = { view: "topology", range: "1h", level: "service", traceId: null, lastOk: 0, offline: false };

// session, sunucunun /auth/me yanıtını tutar: kim, ne yapabilir, hangi
// projelere erişir. Arayüz yetki kararlarını buradan okur — ama bu yalnızca
// görünürlük içindir; asıl kontrol sunucuda.
const session = {
  user: null, permissions: new Set(), projects: [], isSuperAdmin: false,
  project: "",
};

const $ = (s) => document.querySelector(s);
const el = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (v != null) n.setAttribute(k, v);
  }
  for (const kid of kids) if (kid != null) n.append(kid);
  return n;
};
const svg = (tag, attrs = {}) => {
  const n = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v != null) n.setAttribute(k, v);
  return n;
};

// api, tüm istekleri tek noktadan geçirir. 401 alındığında oturum düşmüş
// demektir: kullanıcıyı giriş ekranına döndürürüz, aksi halde her panel ayrı
// ayrı "yükleniyor"da asılı kalır.
async function api(path, options = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { accept: "application/json", ...(options.body ? { "content-type": "application/json" } : {}) },
    ...options,
  });
  if (res.status === 401 && session.user) { handleSessionLost(); }
  if (!res.ok) {
    let payload = {};
    try { payload = await res.json(); } catch (_) {}
    const err = new Error(payload.error || "HTTP " + res.status);
    err.status = res.status;
    err.code = payload.code;
    throw err;
  }
  if (res.status === 204) return null;
  return res.json();
}

const apiJSON = (path, method, body) => api(path, { method, body: JSON.stringify(body) });

const fmtMs = (ms) => ms >= 1000 ? (ms / 1000).toFixed(2) + " s" : ms >= 10 ? ms.toFixed(0) + " ms" : ms.toFixed(2) + " ms";
const fmtRate = (r) => r >= 1 ? r.toFixed(1) + t("unit.perSec") : (r * 60).toFixed(1) + t("unit.perMin");
const fmtPct = (p) => (p * 100).toFixed(p > 0 && p < 0.001 ? 3 : 1) + "%";
const fmtCount = (n) => n >= 1e6 ? (n / 1e6).toLocaleString(LOCALE, { maximumFractionDigits: 1 }) + t("unit.million") : n.toLocaleString(LOCALE);
const fmtDate = (iso) => iso ? new Date(iso).toLocaleString(LOCALE) : t("admin.never");
const errClass = (r) => r > 0.05 ? "err" : r > 0.01 ? "warn" : "ok";
const truncate = (s, n) => s.length > n ? s.slice(0, n - 1) + "…" : s;
const showState = (c, kind, msg) => c.replaceChildren(el("div", { class: kind, text: msg }));
const tr_unknown = () => STRINGS[LANG]["label.unknown"];

// scopeQuery, seçili projeyi sorgu dizesine ekler.
const scopeQuery = () => session.project ? "&project=" + encodeURIComponent(session.project) : "";

const th = (txt, num) => el("th", { class: num ? "num" : null, text: txt });
const td = (kid) => el("td", {}, typeof kid === "string" ? document.createTextNode(kid) : kid);
const tdNum = (kid) => { const c = el("td", { class: "num" }); c.append(typeof kid === "string" ? document.createTextNode(kid) : kid); return c; };
const tr = (cells) => { const r = el("tr"); for (const c of cells) r.append(c); return r; };

// friendlyError, sunucunun makine okunur kodunu kullanıcı diline çevirir.
function friendlyError(e) {
  switch (e.code) {
    case "forbidden": return t("err.forbidden");
    case "duplicate": return t("err.duplicate");
    case "last_admin": return t("err.lastAdmin");
    default: return e.message || t("err.generic");
  }
}

// ==========================================================================
// KİMLİK
// ==========================================================================

function applySession(me) {
  session.user = me.user;
  session.isSuperAdmin = me.isSuperAdmin;
  session.permissions = new Set(me.permissions || []);
  session.projects = me.projects || [];
  if (!session.projects.some((p) => p.key === session.project)) session.project = "";
}

const can = (perm) => session.isSuperAdmin || session.permissions.has(perm);

function showLogin(message) {
  $("#login-screen").hidden = false;
  $("#app-header").hidden = true;
  $("#app-main").hidden = true;
  stopSim();
  const box = $("#login-error");
  if (message) { box.textContent = message; box.hidden = false; } else { box.hidden = true; }
  $("#login-email").focus();
}

function showApp() {
  $("#login-screen").hidden = true;
  $("#app-header").hidden = false;
  $("#app-main").hidden = false;
  renderUserChip();
  renderProjectSelector();
  renderNav();
}

// handleSessionLost, oturum sunucu tarafında düştüğünde çağrılır (süre doldu,
// parola değişti, yönetici hesabı pasifleştirdi).
function handleSessionLost() {
  session.user = null;
  showLogin();
}

async function doLogin(ev) {
  ev.preventDefault();
  const btn = $("#login-submit");
  const box = $("#login-error");
  box.hidden = true;
  btn.disabled = true;
  const original = btn.textContent;
  btn.textContent = t("login.working");

  try {
    const me = await apiJSON("/api/v1/auth/login", "POST", {
      email: $("#login-email").value.trim(),
      password: $("#login-password").value,
    });
    $("#login-password").value = "";
    applySession(me);
    showApp();
    route();
  } catch (e) {
    const msg = {
      bad_credentials: t("login.badCredentials"),
      inactive: t("login.inactive"),
      rate_limited: t("login.rateLimited"),
    }[e.code] || t("login.failed");
    box.textContent = msg;
    box.hidden = false;
  } finally {
    btn.disabled = false;
    btn.textContent = original;
  }
}

async function doLogout() {
  try { await api("/api/v1/auth/logout", { method: "POST" }); } catch (_) {}
  session.user = null;
  showLogin();
}

function renderUserChip() {
  const chip = $("#user-chip");
  if (!session.user) return;
  chip.replaceChildren(
    el("span", { class: "avatar", text: (session.user.name || session.user.email).slice(0, 1).toUpperCase() }),
    el("span", { class: "uname", text: session.user.name || session.user.email }),
  );
  const menu = $("#user-menu");
  menu.replaceChildren(
    el("div", { class: "menu-head" },
      el("div", { class: "menu-name", text: session.user.name || "—" }),
      el("div", { class: "menu-mail", text: session.user.email }),
      session.isSuperAdmin ? el("div", { class: "menu-badge", text: t("user.superAdmin") }) : null),
    el("button", { class: "menu-item", text: t("user.changePassword"), onclick: openPasswordDialog }),
    el("button", { class: "menu-item danger", text: t("user.logout"), onclick: doLogout }),
  );
}

// renderProjectSelector, kullanıcının birden fazla projesi varsa kapsam
// seçici gösterir. Tek projesi olan için seçim yapmak gereksiz gürültü.
function renderProjectSelector() {
  const wrap = $("#project-wrap");
  if (session.isSuperAdmin || session.projects.length > 1) {
    const sel = el("select", { id: "project", "aria-label": t("project.label"),
      onchange: (e) => { session.project = e.target.value; refresh(); } });
    sel.append(el("option", { value: "", text: t("project.all") }));
    for (const p of session.projects) {
      sel.append(el("option", { value: p.key, text: p.name }));
    }
    sel.value = session.project;
    wrap.replaceChildren(sel);
    wrap.hidden = false;
  } else if (session.projects.length === 1) {
    wrap.replaceChildren(el("span", { class: "single-project", text: session.projects[0].name }));
    wrap.hidden = false;
  } else {
    wrap.replaceChildren();
    wrap.hidden = true;
  }
}

// renderNav, yalnızca yetkisi olan sekmeleri gösterir. Gizlemek bir güvenlik
// önlemi değil — sunucu zaten reddediyor — ama tıklandığında hata veren bir
// sekme göstermek de kötü bir arayüz.
function renderNav() {
  const tabs = [
    ["topology", "topology.read"],
    ["services", "services.read"],
    ["traces", "traces.read"],
    ["admin", "admin.manage"],
  ];
  for (const [view, perm] of tabs) {
    const btn = document.querySelector(`nav button[data-view="${view}"]`);
    if (btn) btn.hidden = !can(perm);
  }
}

// firstAllowedView, kullanıcının görebileceği ilk sekme.
function firstAllowedView() {
  if (can("topology.read")) return "topology";
  if (can("services.read")) return "services";
  if (can("traces.read")) return "traces";
  if (can("admin.manage")) return "admin";
  return null;
}

// --- parola değiştirme ---

function openPasswordDialog() {
  closeUserMenu();
  openDialog(t("pw.title"), (form) => {
    form.append(
      field(t("pw.current"), el("input", { type: "password", name: "current", required: "" })),
      field(t("pw.new"), el("input", { type: "password", name: "next", required: "", minlength: "8" })),
      field(t("pw.confirm"), el("input", { type: "password", name: "confirm", required: "", minlength: "8" })),
    );
  }, async (values, setError) => {
    if (values.next.length < 8) { setError(t("pw.tooShort")); return false; }
    if (values.next !== values.confirm) { setError(t("pw.mismatch")); return false; }
    await apiJSON("/api/v1/auth/password", "POST",
      { currentPassword: values.current, newPassword: values.next });
    session.user = null;
    showLogin(t("pw.changed"));
    return true;
  });
}

// ==========================================================================
// ORTAK DİYALOG
// ==========================================================================

let dialogSubmit = null;

// openDialog, tek bir modal iskeletini yeniden kullanır. build() alanları
// doldurur, submit(values) kaydeder; true dönerse diyalog kapanır.
function openDialog(title, build, submit, opts = {}) {
  const dlg = $("#dialog");
  const form = $("#dialog-form");
  $("#dialog-title").textContent = title;
  $("#dialog-error").hidden = true;
  form.replaceChildren();
  build(form);
  $("#dialog-submit").textContent = opts.submitLabel || t("btn.save");
  dialogSubmit = submit;
  dlg.hidden = false;
  const first = form.querySelector("input, select, textarea");
  if (first) first.focus();
}

function closeDialog() {
  $("#dialog").hidden = true;
  dialogSubmit = null;
}

async function onDialogSubmit(ev) {
  ev.preventDefault();
  if (!dialogSubmit) return;
  const form = $("#dialog-form");
  const values = {};
  for (const input of form.querySelectorAll("input, select, textarea")) {
    if (input.type === "checkbox") {
      if (input.dataset.group) {
        values[input.dataset.group] = values[input.dataset.group] || [];
        if (input.checked) values[input.dataset.group].push(input.value);
      } else {
        values[input.name] = input.checked;
      }
    } else if (input.name) {
      values[input.name] = input.value;
    }
  }
  const errBox = $("#dialog-error");
  const setError = (msg) => { errBox.textContent = msg; errBox.hidden = false; };
  errBox.hidden = true;

  const btn = $("#dialog-submit");
  btn.disabled = true;
  try {
    const done = await dialogSubmit(values, setError);
    if (done !== false) closeDialog();
  } catch (e) {
    setError(friendlyError(e));
  } finally {
    btn.disabled = false;
  }
}

// field, etiketli bir form satırı üretir.
function field(label, input, hint) {
  return el("label", { class: "field" },
    el("span", { class: "field-label", text: label }),
    input,
    hint ? el("span", { class: "field-hint", text: hint }) : null);
}

// checkList, çoklu seçim kutusu listesi. data-group ile tek isim altında
// toplanır.
function checkList(group, items, selected) {
  const box = el("div", { class: "check-list" });
  const chosen = new Set(selected || []);
  for (const item of items) {
    box.append(el("label", { class: "check-row" },
      el("input", {
        type: "checkbox", value: item.value, "data-group": group,
        ...(chosen.has(item.value) ? { checked: "" } : {}),
      }),
      el("span", {},
        el("b", { text: item.label }),
        item.hint ? el("span", { class: "check-hint", text: item.hint }) : null)));
  }
  if (!items.length) box.append(el("div", { class: "hint", text: t("admin.noDiscovered") }));
  return box;
}

// ==========================================================================
// SERVİSLER
// ==========================================================================

async function loadServices() {
  const body = $("#services-body");
  try {
    const data = await api(`/api/v1/services?from=${state.range}${scopeQuery()}`);
    markOk();
    const rows = data.services || [];
    if (!rows.length) { showState(body, "empty", t("empty.services")); return; }

    const table = el("table");
    table.append(el("thead", {}, tr([th(t("th.service")), th(t("th.kubernetes")), th(t("th.rate"), true),
      th(t("th.errors"), true), th("p50", true), th("p95", true), th("p99", true), th(t("th.calls"), true)])));
    const tbody = el("tbody");
    for (const s of rows) {
      const k8s = (s.namespace || s.workload)
        ? el("span", {}, s.namespace ? el("i", { class: "tag", text: s.namespace }) : null,
                        s.workload ? el("i", { class: "tag", text: s.workload }) : null)
        : el("span", { class: "hint", text: "—" });
      tbody.append(el("tr", { class: "clickable", onclick: () => { $("#f-service").value = s.service; location.hash = "traces"; } },
        td(el("b", { text: s.service })), td(k8s),
        tdNum(fmtRate(s.ratePerSec)),
        tdNum(el("span", { class: "pill " + errClass(s.errorRate), text: fmtPct(s.errorRate) })),
        tdNum(fmtMs(s.p50Ms)), tdNum(fmtMs(s.p95Ms)), tdNum(fmtMs(s.p99Ms)), tdNum(fmtCount(s.calls))));
    }
    table.append(tbody);
    body.replaceChildren(table);
  } catch (e) {
    if (e.status === 401) return;
    showState(body, "error", t("err.services") + ": " + friendlyError(e));
    markStale();
  }
}

// ==========================================================================
// TRACE'LER
// ==========================================================================

async function loadTraces() {
  const body = $("#traces-body");
  const params = new URLSearchParams({ from: state.range, limit: "100" });
  const svcName = $("#f-service").value.trim();
  const minMs = $("#f-minms").value.trim();
  if (svcName) params.set("service", svcName);
  if (minMs) params.set("minDurationMs", minMs);
  if ($("#f-errors").checked) params.set("onlyErrors", "true");
  if (session.project) params.set("project", session.project);

  try {
    const data = await api("/api/v1/traces?" + params.toString());
    markOk();
    const rows = data.traces || [];
    if (!rows.length) { showState(body, "empty", t("empty.traces")); return; }

    const table = el("table");
    table.append(el("thead", {}, tr([th(t("th.rootOp")), th(t("th.services")), th(t("th.duration"), true),
      th(t("th.spans"), true), th(t("th.errors"), true), th(t("th.time"), true)])));
    const tbody = el("tbody");
    for (const trace of rows) {
      const services = el("span");
      for (const s of (trace.services || []).slice(0, 4)) services.append(el("i", { class: "tag", text: s }));
      tbody.append(el("tr", { class: "clickable", onclick: () => openTrace(trace.traceId) },
        td(el("span", {}, el("b", { text: trace.rootService || tr_unknown() }),
          document.createTextNode(" "), el("span", { class: "hint", text: trace.rootName || "" }))),
        td(services), tdNum(fmtMs(trace.durationMs)), tdNum(String(trace.spans)),
        tdNum(trace.errors > 0 ? el("span", { class: "pill err", text: String(trace.errors) })
                               : el("span", { class: "hint", text: "—" })),
        tdNum(new Date(trace.start).toLocaleTimeString(LOCALE))));
    }
    table.append(tbody);
    body.replaceChildren(table);
  } catch (e) {
    if (e.status === 401) return;
    showState(body, "error", t("err.traces") + ": " + friendlyError(e));
    markStale();
  }
}

function openTrace(traceId) { location.hash = "traces/" + traceId; }

async function showTrace(traceId) {
  state.traceId = traceId;
  $("#trace-list-panel").hidden = true;
  $("#trace-detail-panel").hidden = false;
  $("#trace-id-label").textContent = traceId;
  const body = $("#trace-detail-body");
  showState(body, "empty", t("status.loading"));
  try {
    const data = await api("/api/v1/traces/" + traceId + "?from=" + state.range + scopeQuery().replace("&", "&"));
    renderWaterfall(body, data.spans || []);
    markOk();
  } catch (e) {
    if (e.status === 401) return;
    showState(body, "error", t("err.trace") + ": " + friendlyError(e));
  }
}

function renderWaterfall(body, spans) {
  if (!spans.length) { showState(body, "empty", t("empty.spans")); return; }
  const t0 = Math.min.apply(null, spans.map((s) => new Date(s.start).getTime()));
  const total = Math.max.apply(null, spans.map((s) => new Date(s.start).getTime() - t0 + s.durationMs)) || 1;

  const byParent = new Map();
  for (const s of spans) {
    const p = s.parentSpanId || "";
    if (!byParent.has(p)) byParent.set(p, []);
    byParent.get(p).push(s);
  }
  const known = new Set(spans.map((s) => s.spanId));
  const roots = spans.filter((s) => !s.parentSpanId || !known.has(s.parentSpanId));

  const container = el("div");
  const walk = (span, depth) => {
    const start = new Date(span.start).getTime() - t0;
    const isErr = span.status === "error";
    container.append(el("div", { class: "wf-row" },
      el("div", { class: "wf-name", style: "padding-left:" + (depth * 15) + "px" },
        el("span", { class: "kind", text: span.kind }),
        el("span", { class: "txt" }, el("b", { text: span.name }),
          el("span", { class: "wf-svc", text: " · " + span.service }))),
      el("div", { class: "wf-track" },
        el("div", {
          class: "wf-bar",
          style: "left:" + ((start / total) * 100) + "%;width:" + Math.max((span.durationMs / total) * 100, 0.4) +
                 "%;background:" + (isErr ? "var(--err)" : "var(--accent)"),
          title: (isErr ? t("tooltip.failed") + ": " + (span.statusMessage || "") + "\n" : "") +
                 t("tooltip.start") + " +" + fmtMs(start) + " · " + t("tooltip.duration") + " " + fmtMs(span.durationMs) +
                 (span.pod ? "\npod: " + span.pod : "") + (span.node ? "\nnode: " + span.node : ""),
        })),
      el("div", { class: "wf-dur", text: fmtMs(span.durationMs) })));

    const a = span.attributes || {};
    const detail = a["db.query.text"] || a["db.statement"] || a["url.full"] || a["http.url"];
    if (detail) {
      container.append(el("div", { class: "attrs", style: "padding-left:" + (15 + depth * 15) + "px" },
        el("div", {}, el("b", { text: ((a["db.query.text"] || a["db.statement"]) ? t("label.query") : t("label.target")) + " " }),
          document.createTextNode(truncate(detail, 150)))));
    }
    for (const kid of (byParent.get(span.spanId) || []).sort((x, y) => new Date(x.start) - new Date(y.start))) {
      walk(kid, depth + 1);
    }
  };
  for (const r of roots.sort((a, b) => new Date(a.start) - new Date(b.start))) walk(r, 0);
  body.replaceChildren(container);
}

// ==========================================================================
// İSKELET
// ==========================================================================

function switchView(view) {
  state.view = view;
  for (const b of document.querySelectorAll("nav button")) {
    if (b.dataset.view === view) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  }
  for (const s of document.querySelectorAll(".view")) s.hidden = s.id !== "view-" + view;
  if (view !== "topology") stopSim();
  else if (topo.nodes.length) startSim();
  if (view !== "traces") {
    $("#trace-list-panel").hidden = false;
    $("#trace-detail-panel").hidden = true;
    state.traceId = null;
  }
}

function route() {
  if (!session.user) return;
  const parts = location.hash.replace(/^#/, "").split("/");
  let view = parts[0], sub = parts[1];
  const allowed = { topology: "topology.read", services: "services.read", traces: "traces.read", admin: "admin.manage" };

  if (!allowed[view] || !can(allowed[view])) {
    const fallback = firstAllowedView();
    if (!fallback) {
      $("#app-main").replaceChildren(el("div", { class: "panel" },
        el("div", { class: "empty", text: t("login.noAccess") })));
      return;
    }
    view = fallback; sub = undefined;
  }

  switchView(view);
  if (view === "traces" && sub) {
    if (state.traceId !== sub) showTrace(sub);
    return;
  }
  if (view === "traces") {
    state.traceId = null;
    $("#trace-detail-panel").hidden = true;
    $("#trace-list-panel").hidden = false;
  }
  if (view === "admin") { routeAdmin(sub); return; }
  refresh();
}

function markOk() { state.lastOk = Date.now(); state.offline = false; markLive(); }
function markStale() { state.offline = true; markLive(); }
function markLive() {
  const dot = $("#livedot"), txt = $("#livetext");
  if (!dot || !txt) return;
  dot.classList.toggle("stale", !!state.offline);
  txt.textContent = state.offline ? t("live.offline")
    : t("live.auto") + (state.lastOk ? " · " + new Date(state.lastOk).toLocaleTimeString(LOCALE) : "");
}

function refresh() {
  if (!session.user) return;
  if (state.view === "topology") loadTopology();
  else if (state.view === "services") loadServices();
  else if (state.view === "traces" && !state.traceId) loadTraces();
  else if (state.view === "admin") refreshAdmin();
}

function closeUserMenu() { $("#user-menu").hidden = true; }

async function boot() {
  document.getElementById("html-root").setAttribute("lang", LANG);
  applyStaticStrings();
  $("#lang").value = LANG;

  try {
    const me = await api("/api/v1/auth/me");
    applySession(me);
    showApp();
    route();
  } catch (e) {
    showLogin();
  }
}

function init() {
  $("#lang").addEventListener("change", (e) => setLang(e.target.value));
  $("#login-form").addEventListener("submit", doLogin);
  $("#range").addEventListener("change", (e) => { state.range = e.target.value; refresh(); });
  $("#level").addEventListener("change", (e) => {
    state.level = e.target.value;
    topo.byId = new Map(); topo.selected = null; topo.sig = null;
    loadTopology();
  });
  $("#refresh").addEventListener("click", refresh);
  $("#f-apply").addEventListener("click", loadTraces);
  for (const id of ["#f-service", "#f-minms"]) {
    $(id).addEventListener("keydown", (e) => { if (e.key === "Enter") loadTraces(); });
  }
  $("#trace-back").addEventListener("click", () => { location.hash = "traces"; });

  for (const b of document.querySelectorAll("nav button")) {
    b.addEventListener("click", () => { location.hash = b.dataset.view; });
  }

  $("#user-chip").addEventListener("click", (e) => {
    e.stopPropagation();
    $("#user-menu").hidden = !$("#user-menu").hidden;
  });
  document.addEventListener("click", closeUserMenu);
  $("#user-menu").addEventListener("click", (e) => e.stopPropagation());

  $("#dialog-form").addEventListener("submit", onDialogSubmit);
  $("#dialog-cancel").addEventListener("click", closeDialog);
  $("#dialog").addEventListener("click", (e) => { if (e.target.id === "dialog") closeDialog(); });
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    if (!$("#dialog").hidden) closeDialog();
    else closeUserMenu();
  });

  initTopologyCanvas();

  window.addEventListener("hashchange", route);
  setInterval(() => { if (!document.hidden && session.user) refresh(); }, 10000);
  boot();
}

document.addEventListener("DOMContentLoaded", init);
