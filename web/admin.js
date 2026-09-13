"use strict";

// ==========================================================================
// ADMINISTRATION
//
// Three screens: users, role groups, projects. The model wires up like this:
//
//   user ──member──> role group ──bound──> project ──contains──> application
//
// A user can see an application's telemetry only if a role group they belong
// to is bound to that application's project. Permissions are never granted
// directly to a user, so the answer to "why does this person see this data"
// can always be read off a single chain.
// ==========================================================================

const admin = {
  tab: "users",
  users: [],
  roleGroups: [],
  projects: [],
  discovered: [],
  loaded: false,
};

function routeAdmin(sub) {
  admin.tab = ["users", "role-groups", "projects"].includes(sub) ? sub : "users";
  for (const b of document.querySelectorAll("#admin-tabs button")) {
    if (b.dataset.tab === admin.tab) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  }
  refreshAdmin();
}

async function refreshAdmin() {
  if (!can("admin.manage")) {
    showState($("#admin-body"), "empty", t("empty.noPermission"));
    return;
  }
  const body = $("#admin-body");
  if (!admin.loaded) showState(body, "empty", t("status.loading"));

  try {
    // The three lists reference each other (user→group, project→group), so
    // they are all refreshed together.
    const [users, groups, projects, discovered] = await Promise.all([
      api("/api/v1/admin/users"),
      api("/api/v1/admin/role-groups"),
      api("/api/v1/admin/projects"),
      api("/api/v1/admin/discovered-applications"),
    ]);
    admin.users = users.users;
    admin.roleGroups = groups.roleGroups;
    admin.projects = projects.projects;
    admin.discovered = discovered.applications;
    admin.loaded = true;
    markOk();
    renderAdmin();
  } catch (e) {
    if (e.status === 401) return;
    showState(body, "error", friendlyError(e));
    markStale();
  }
}

function renderAdmin() {
  const body = $("#admin-body");
  const newBtn = $("#admin-new");
  newBtn.hidden = false;
  switch (admin.tab) {
    case "role-groups":
      newBtn.textContent = t("admin.newRoleGroup");
      newBtn.onclick = () => openRoleGroupDialog(null);
      renderRoleGroups(body);
      break;
    case "projects":
      newBtn.textContent = t("admin.newProject");
      newBtn.onclick = () => openProjectDialog(null);
      renderProjects(body);
      break;
    default:
      newBtn.textContent = t("admin.newUser");
      newBtn.onclick = () => openUserDialog(null);
      renderUsers(body);
  }
}

// rowActions renders the edit/delete buttons at the end of a row.
function rowActions(onEdit, onDelete) {
  const cell = el("td", { class: "num actions" });
  cell.append(el("button", { class: "link-btn", text: t("btn.edit"), onclick: (e) => { e.stopPropagation(); onEdit(); } }));
  if (onDelete) {
    cell.append(el("button", { class: "link-btn danger", text: t("btn.delete"), onclick: (e) => { e.stopPropagation(); onDelete(); } }));
  }
  return cell;
}

async function confirmDelete(label, run) {
  if (!window.confirm(label + " " + t("admin.confirmDelete"))) return;
  try {
    await run();
    await refreshAdmin();
  } catch (e) {
    window.alert(friendlyError(e));
  }
}

// --- users ---

function renderUsers(body) {
  if (!admin.users.length) { showState(body, "empty", t("admin.noUsers")); return; }
  const table = el("table");
  table.append(el("thead", {}, tr([
    th(t("admin.name")), th(t("admin.email")), th(t("admin.roleGroupsOf")),
    th(t("admin.status")), th(t("admin.lastLogin")), th(""),
  ])));
  const tbody = el("tbody");
  for (const u of admin.users) {
    const groups = el("span");
    for (const g of u.roleGroups || []) groups.append(el("i", { class: "tag", text: g.name }));
    if (!(u.roleGroups || []).length) groups.append(el("span", { class: "hint", text: "—" }));

    tbody.append(el("tr", {},
      td(el("span", {}, el("b", { text: u.name || "—" }),
        u.isSuperAdmin ? el("i", { class: "tag admin-tag", text: t("admin.superAdmin") }) : null)),
      td(el("span", { class: "mono", text: u.email })),
      td(groups),
      td(el("span", { class: "pill " + (u.isActive ? "ok" : "err"), text: u.isActive ? t("admin.active") : t("admin.inactive") })),
      td(el("span", { class: "hint", text: fmtDate(u.lastLoginAt) })),
      rowActions(() => openUserDialog(u),
        u.id === session.user.id ? null : () => confirmDelete(u.email, () => api("/api/v1/admin/users/" + u.id, { method: "DELETE" })))));
  }
  table.append(tbody);
  body.replaceChildren(table);
}

function openUserDialog(user) {
  const isNew = !user;
  const groupItems = admin.roleGroups.map((g) => ({
    value: g.id, label: g.name,
    hint: g.permissions.map((p) => t("perm." + p)).join(", "),
  }));

  openDialog(isNew ? t("admin.newUser") : t("admin.editUser"), (form) => {
    form.append(field(t("admin.name"), el("input", { name: "name", value: user ? user.name : "", required: "" })));
    if (isNew) {
      form.append(field(t("admin.email"), el("input", { name: "email", type: "email", required: "" })));
      form.append(field(t("admin.password"), el("input", { name: "password", type: "password", required: "", minlength: "8" })));
    } else {
      form.append(field(t("admin.email"), el("input", { value: user.email, disabled: "" })));
    }
    form.append(el("label", { class: "check-row standalone" },
      el("input", { type: "checkbox", name: "isSuperAdmin", ...(user && user.isSuperAdmin ? { checked: "" } : {}) }),
      el("span", {}, el("b", { text: t("admin.superAdmin") }),
        el("span", { class: "check-hint", text: t("perm.admin.manage") }))));
    if (!isNew) {
      form.append(el("label", { class: "check-row standalone" },
        el("input", { type: "checkbox", name: "isActive", ...(user.isActive ? { checked: "" } : {}) }),
        el("span", {}, el("b", { text: t("admin.active") }))));
    }
    form.append(el("div", { class: "field-label section", text: t("admin.roleGroupsOf") }));
    form.append(checkList("roleGroupIds", groupItems, (user && user.roleGroups || []).map((g) => g.id)));
  }, async (v) => {
    const roleGroupIds = v.roleGroupIds || [];
    if (isNew) {
      await apiJSON("/api/v1/admin/users", "POST", {
        email: v.email, name: v.name, password: v.password,
        isSuperAdmin: !!v.isSuperAdmin, roleGroupIds,
      });
    } else {
      await apiJSON("/api/v1/admin/users/" + user.id, "PATCH", {
        name: v.name, isActive: !!v.isActive, isSuperAdmin: !!v.isSuperAdmin, roleGroupIds,
      });
    }
    await refreshAdmin();
  }, { submitLabel: isNew ? t("btn.create") : t("btn.save") });
}

// --- role groups ---

function renderRoleGroups(body) {
  if (!admin.roleGroups.length) { showState(body, "empty", t("admin.noRoleGroups")); return; }
  const table = el("table");
  table.append(el("thead", {}, tr([
    th(t("admin.name")), th(t("admin.permissions")),
    th(t("admin.members"), true), th(t("admin.projectCount"), true), th(""),
  ])));
  const tbody = el("tbody");
  for (const g of admin.roleGroups) {
    const perms = el("span");
    for (const p of g.permissions) perms.append(el("i", { class: "tag", text: t("perm." + p) }));
    if (!g.permissions.length) perms.append(el("span", { class: "hint", text: "—" }));

    tbody.append(el("tr", {},
      td(el("span", {}, el("b", { text: g.name }),
        g.description ? el("div", { class: "hint", text: g.description }) : null)),
      td(perms),
      tdNum(String(g.memberCount)),
      tdNum(String(g.projectCount)),
      rowActions(() => openRoleGroupDialog(g),
        () => confirmDelete(g.name, () => api("/api/v1/admin/role-groups/" + g.id, { method: "DELETE" })))));
  }
  table.append(tbody);
  body.replaceChildren(table);
}

function openRoleGroupDialog(group) {
  const isNew = !group;
  const permItems = ["topology.read", "services.read", "traces.read", "project.manage",
                     "diagnostics.manage", "admin.manage"]
    .map((p) => ({ value: p, label: t("perm." + p), hint: p }));

  openDialog(isNew ? t("admin.newRoleGroup") : t("admin.editRoleGroup"), (form) => {
    form.append(field(t("admin.name"), el("input", { name: "name", value: group ? group.name : "", required: "" })));
    form.append(field(t("admin.description"), el("input", { name: "description", value: group ? group.description : "" })));
    form.append(el("div", { class: "field-label section", text: t("admin.permissions") },
      el("span", { class: "field-hint", text: t("admin.permsHint") })));
    form.append(checkList("permissions", permItems, group ? group.permissions : ["topology.read", "services.read", "traces.read"]));
  }, async (v) => {
    const payload = { name: v.name, description: v.description || "", permissions: v.permissions || [] };
    if (isNew) await apiJSON("/api/v1/admin/role-groups", "POST", payload);
    else await apiJSON("/api/v1/admin/role-groups/" + group.id, "PATCH", payload);
    await refreshAdmin();
  }, { submitLabel: isNew ? t("btn.create") : t("btn.save") });
}

// --- projeler ---

function renderProjects(body) {
  const wrap = el("div");
  wrap.append(el("div", { class: "panel-note", text: t("admin.projectsHint") }));

  if (!admin.projects.length) {
    wrap.append(el("div", { class: "empty", text: t("admin.noProjects") }));
    body.replaceChildren(wrap);
    return;
  }

  const table = el("table");
  table.append(el("thead", {}, tr([
    th(t("admin.name")), th(t("admin.key")), th(t("admin.applications")), th(t("admin.roleGroupsOf")), th(""),
  ])));
  const tbody = el("tbody");
  for (const p of admin.projects) {
    const apps = el("span");
    for (const a of p.applications || []) apps.append(el("i", { class: "tag", text: a }));
    if (!(p.applications || []).length) apps.append(el("span", { class: "hint", text: "—" }));

    const groups = el("span");
    for (const g of p.roleGroups || []) groups.append(el("i", { class: "tag", text: g.name }));
    if (!(p.roleGroups || []).length) groups.append(el("span", { class: "hint", text: "—" }));

    tbody.append(el("tr", {},
      td(el("span", {}, el("b", { text: p.name }),
        p.description ? el("div", { class: "hint", text: p.description }) : null)),
      td(el("span", { class: "mono", text: p.key })),
      td(apps), td(groups),
      rowActions(() => openProjectDialog(p),
        () => confirmDelete(p.name, () => api("/api/v1/admin/projects/" + p.id, { method: "DELETE" })))));
  }
  table.append(tbody);
  wrap.append(table);
  body.replaceChildren(wrap);
}

function openProjectDialog(project) {
  const isNew = !project;

  openDialog(isNew ? t("admin.newProject") : t("admin.editProject"), (form) => {
    if (isNew) {
      form.append(field(t("admin.key"), el("input", { name: "key", required: "", pattern: "[a-z0-9-]+" }), t("admin.keyHint")));
    } else {
      form.append(field(t("admin.key"), el("input", { value: project.key, disabled: "" })));
    }
    form.append(field(t("admin.name"), el("input", { name: "name", value: project ? project.name : "", required: "" })));
    form.append(field(t("admin.description"), el("input", { name: "description", value: project ? project.description : "" })));

    if (isNew) return; // Application and group assignment come after the project exists.

    // Discovered services plus the ones already assigned to this project. A
    // service that is assigned but no longer sending telemetry must not drop
    // off the list, or saving would silently remove it.
    const known = new Map(admin.discovered.map((d) => [d.service, d]));
    for (const a of project.applications || []) if (!known.has(a)) known.set(a, { service: a, calls: 0, projects: [] });

    const appItems = [...known.values()].map((d) => {
      const others = (d.projects || []).filter((n) => n !== project.name);
      const bits = [];
      if (d.calls) bits.push(fmtCount(d.calls) + " " + t("tooltip.calls"));
      if (others.length) bits.push(t("admin.assignedTo") + ": " + others.join(", "));
      return { value: d.service, label: d.service, hint: bits.join(" · ") };
    });

    form.append(el("div", { class: "field-label section", text: t("admin.applications") },
      el("span", { class: "field-hint", text: t("admin.appsHint") })));
    form.append(checkList("applications", appItems, project.applications || []));

    form.append(el("div", { class: "field-label section", text: t("admin.diagToken") },
      el("span", { class: "field-hint", text: t("admin.diagTokenHint") })));
    form.append(el("input", { name: "diagToken", type: "password", placeholder: "••••••••" }));

    form.append(el("div", { class: "field-label section", text: t("admin.roleGroupsOf") },
      el("span", { class: "field-hint", text: t("admin.groupsHint") })));
    form.append(checkList("roleGroupIds",
      admin.roleGroups.map((g) => ({ value: g.id, label: g.name, hint: g.permissions.map((p) => t("perm." + p)).join(", ") })),
      (project.roleGroups || []).map((g) => g.id)));
  }, async (v) => {
    if (isNew) {
      const created = await apiJSON("/api/v1/admin/projects", "POST",
        { key: v.key, name: v.name, description: v.description || "" });
      await refreshAdmin();
      // Open the new project straight into the assignment screen: leaving the
      // user at an empty list after "create" is doing half the job.
      const fresh = admin.projects.find((p) => p.id === created.id);
      if (fresh) openProjectDialog(fresh);
      return;
    }
    await apiJSON("/api/v1/admin/projects/" + project.id, "PATCH", {
      name: v.name, description: v.description || "",
      applications: v.applications || [], roleGroupIds: v.roleGroupIds || [],
    });
    // The token is only sent when it was filled in: leaving it empty has to
    // mean "do not change", not "delete".
    if (v.diagToken) {
      await apiJSON("/api/v1/admin/projects/" + project.id + "/diagnostics-token", "PUT",
        { token: v.diagToken });
    }
    await refreshAdmin();
  }, { submitLabel: isNew ? t("btn.create") : t("btn.save") });
}
