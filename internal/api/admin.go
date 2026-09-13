package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// registerAdmin wires up the control plane endpoints. All of them need admin.manage.
func (s *Server) registerAdmin(mux *http.ServeMux) {
	admin := func(h http.HandlerFunc) http.HandlerFunc {
		return requirePermission(identity.PermAdmin, h)
	}

	mux.HandleFunc("GET /api/v1/admin/permissions", admin(s.handleListPermissions))
	mux.HandleFunc("GET /api/v1/admin/discovered-applications", admin(s.handleDiscoveredApplications))

	mux.HandleFunc("GET /api/v1/admin/users", admin(s.handleListUsers))
	mux.HandleFunc("POST /api/v1/admin/users", admin(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", admin(s.handleUpdateUser))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/password", admin(s.handleResetPassword))
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}", admin(s.handleDeleteUser))

	mux.HandleFunc("GET /api/v1/admin/role-groups", admin(s.handleListRoleGroups))
	mux.HandleFunc("POST /api/v1/admin/role-groups", admin(s.handleCreateRoleGroup))
	mux.HandleFunc("PATCH /api/v1/admin/role-groups/{id}", admin(s.handleUpdateRoleGroup))
	mux.HandleFunc("DELETE /api/v1/admin/role-groups/{id}", admin(s.handleDeleteRoleGroup))

	mux.HandleFunc("GET /api/v1/admin/projects", admin(s.handleListProjects))
	mux.HandleFunc("POST /api/v1/admin/projects", admin(s.handleCreateProject))
	mux.HandleFunc("PATCH /api/v1/admin/projects/{id}", admin(s.handleUpdateProject))
	mux.HandleFunc("DELETE /api/v1/admin/projects/{id}", admin(s.handleDeleteProject))
}

// --- yetkiler ---

func (s *Server) handleListPermissions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"permissions": identity.AllPermissions})
}

// handleDiscoveredApplications returns the services seen in telemetry and the
// projects they are assigned to.
//
// The application list is never typed by hand: the collector already knows
// which services are sending data. An administrator assigns by picking from
// the discovered list, so a typo cannot produce a project that shows nothing.
func (s *Server) handleDiscoveredApplications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.conn.Query(r.Context(), fmt.Sprintf(`
		SELECT service_name, sum(calls) AS calls, max(bucket) AS last_seen
		FROM %s.operation_stats
		WHERE bucket >= now() - INTERVAL 7 DAY
		GROUP BY service_name
		ORDER BY calls DESC
		LIMIT 1000`, s.db))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	type discovered struct {
		Service  string    `json:"service"`
		Calls    uint64    `json:"calls"`
		LastSeen time.Time `json:"lastSeen"`
		Projects []string  `json:"projects"`
	}

	projects, err := s.identity.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	assigned := map[string][]string{}
	for _, p := range projects {
		for _, svc := range p.Applications {
			assigned[svc] = append(assigned[svc], p.Name)
		}
	}

	out := []discovered{}
	for rows.Next() {
		var d discovered
		if err := rows.Scan(&d.Service, &d.Calls, &d.LastSeen); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		d.Projects = assigned[d.Service]
		if d.Projects == nil {
			d.Projects = []string{}
		}
		out = append(out, d)
	}
	writeJSON(w, map[string]any{"applications": out})
}

// --- users ---

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.identity.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"users": users})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email        string   `json:"email"`
		Name         string   `json:"name"`
		Password     string   `json:"password"`
		IsSuperAdmin bool     `json:"isSuperAdmin"`
		RoleGroupIDs []string `json:"roleGroupIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	user, err := s.identity.CreateUser(r.Context(), req.Email, req.Name, req.Password, req.IsSuperAdmin)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if err := s.identity.SetUserRoleGroups(r.Context(), user.ID, req.RoleGroupIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("user created", "email", user.Email, "by", accessFrom(r).User.Email)
	writeJSONStatus(w, http.StatusCreated, user)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string   `json:"name"`
		IsActive     bool     `json:"isActive"`
		IsSuperAdmin bool     `json:"isSuperAdmin"`
		RoleGroupIDs []string `json:"roleGroupIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	user, err := s.identity.UpdateUser(r.Context(), id, req.Name, req.IsActive, req.IsSuperAdmin)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if err := s.identity.SetUserRoleGroups(r.Context(), id, req.RoleGroupIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, user)
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.identity.SetPassword(r.Context(), r.PathValue("id"), req.Password); err != nil {
		writeIdentityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Deleting yourself leaves the administrator locked outside the door.
	if id == accessFrom(r).User.ID {
		writeErrorCode(w, http.StatusBadRequest, "you cannot delete your own account", "self_delete")
		return
	}
	if err := s.identity.DeleteUser(r.Context(), id); err != nil {
		writeIdentityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- role groups ---

func (s *Server) handleListRoleGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.identity.ListRoleGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"roleGroups": groups})
}

type roleGroupRequest struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Permissions []identity.Permission `json:"permissions"`
}

func (s *Server) handleCreateRoleGroup(w http.ResponseWriter, r *http.Request) {
	var req roleGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.identity.CreateRoleGroup(r.Context(), req.Name, req.Description, req.Permissions)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateRoleGroup(w http.ResponseWriter, r *http.Request) {
	var req roleGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.identity.UpdateRoleGroup(r.Context(), r.PathValue("id"), req.Name, req.Description, req.Permissions)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	writeJSON(w, g)
}

func (s *Server) handleDeleteRoleGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.identity.DeleteRoleGroup(r.Context(), r.PathValue("id")); err != nil {
		writeIdentityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- projeler ---

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.identity.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"projects": projects})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.identity.CreateProject(r.Context(), req.Key, req.Name, req.Description)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	s.log.Info("project created", "key", p.Key, "by", accessFrom(r).User.Email)
	writeJSONStatus(w, http.StatusCreated, p)
}

// handleUpdateProject updates the name, description, application assignments
// and the role groups with access, all in one call. The edit screen in the UI
// shows a single "save" button, so the endpoints are not split either.
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Applications []string `json:"applications"`
		RoleGroupIDs []string `json:"roleGroupIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	p, err := s.identity.UpdateProject(r.Context(), id, req.Name, req.Description)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if err := s.identity.SetProjectApplications(r.Context(), id, req.Applications); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.identity.SetProjectRoleGroups(r.Context(), id, req.RoleGroupIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if err := s.identity.DeleteProject(r.Context(), r.PathValue("id")); err != nil {
		writeIdentityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeIdentityError maps store errors onto the right HTTP code.
func writeIdentityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, "record not found", "not_found")
	case errors.Is(err, identity.ErrDuplicate):
		writeErrorCode(w, http.StatusConflict, "that name or email is already in use", "duplicate")
	case errors.Is(err, identity.ErrLastAdmin):
		writeErrorCode(w, http.StatusBadRequest, identity.ErrLastAdmin.Error(), "last_admin")
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
