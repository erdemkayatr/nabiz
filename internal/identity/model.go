// Package identity holds nabiz's control plane: users, role groups, projects
// and application assignments.
//
// This data does not belong in ClickHouse. The telemetry tables are all
// append-only and eventually consistent, whereas identity data needs uniqueness
// constraints, transactions and in-place updates. The control plane therefore
// lives in a separate PostgreSQL.
package identity

import (
	"strings"
	"time"
)

// Permission is a capability a role group can grant.
type Permission string

const (
	// PermTopologyRead allows viewing the project's topology graph.
	PermTopologyRead Permission = "topology.read"
	// PermServicesRead allows viewing RED metrics.
	PermServicesRead Permission = "services.read"
	// PermTracesRead opens trace search and the waterfall view.
	PermTracesRead Permission = "traces.read"
	// PermProjectManage allows adding and removing a project's applications.
	PermProjectManage Permission = "project.manage"
	// PermDiagnostics allows taking CPU profiles and memory dumps from
	// applications. It is a separate permission: a memory dump contains
	// connection strings, tokens and customer data, so it is not the same thing
	// as reading traces.
	PermDiagnostics Permission = "diagnostics.manage"
	// PermAdmin manages the whole control plane: users, role groups, projects.
	// Granted to system administrators only.
	PermAdmin Permission = "admin.manage"
)

// AllPermissions populates the UI's permission picker.
var AllPermissions = []Permission{
	PermTopologyRead, PermServicesRead, PermTracesRead, PermProjectManage,
	PermDiagnostics, PermAdmin,
}

// ValidPermission keeps unknown permissions from leaking into the database.
func ValidPermission(p Permission) bool {
	for _, known := range AllPermissions {
		if known == p {
			return true
		}
	}
	return false
}

// User is a person who signs in.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	IsSuperAdmin bool       `json:"isSuperAdmin"`
	IsActive     bool       `json:"isActive"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`

	// Only populated on list endpoints.
	RoleGroups []RoleGroupRef `json:"roleGroups,omitempty"`
}

// RoleGroupRef summarises a role group in the user list.
type RoleGroupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RoleGroup is a group of users carrying a set of permissions. Permissions are
// granted to the group rather than directly to a user, and it is the group that
// binds to a project.
type RoleGroup struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	CreatedAt   time.Time    `json:"createdAt"`

	MemberCount  int `json:"memberCount"`
	ProjectCount int `json:"projectCount"`
}

// Project is the unit that groups applications and access.
type Project struct {
	ID          string    `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`

	Applications []string       `json:"applications,omitempty"`
	RoleGroups   []RoleGroupRef `json:"roleGroups,omitempty"`
}

// AgentInstance is an application instance that sends telemetry and registers itself.
type AgentInstance struct {
	ID           string `json:"id"`
	ServiceName  string `json:"serviceName"`
	InstanceID   string `json:"instanceId"`
	Hostname     string `json:"hostname,omitempty"`
	K8sPod       string `json:"pod,omitempty"`
	K8sNamespace string `json:"namespace,omitempty"`
	PID          int    `json:"pid"`
	AgentVersion string `json:"agentVersion,omitempty"`

	// DiagPort and DiagPath locate the diagnostics endpoint. The host is
	// deliberately not stored: nabiz always uses the IP the registration came
	// from, never the address the agent claims. Otherwise a forged registration
	// could talk nabiz into sending the token to an attacker's address.
	SourceIP string `json:"sourceIp"`
	// AdvertisedHost is the address the agent reports. It is used ONLY for
	// verified registrations: a party that knows the token has already proven
	// its identity. Unverified registrations always fall back to SourceIP, or a
	// forged registration could redirect nabiz to another target.
	AdvertisedHost string `json:"advertisedHost,omitempty"`
	DiagPort       int    `json:"diagPort"`
	DiagPath       string `json:"diagPath"`
	DiagReady      bool   `json:"diagReady"`

	// Verified says the registration was validated against the project token.
	// Dumps cannot be triggered on unverified instances.
	Verified  bool      `json:"verified"`
	ProjectID string    `json:"projectId,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// DumpArtifact is a file nabiz fetched from an application and stored.
type DumpArtifact struct {
	ID          string    `json:"id"`
	InstanceID  string    `json:"instanceId"`
	ServiceName string    `json:"serviceName"`
	Kind        string    `json:"kind"`
	Filename    string    `json:"filename"`
	Bytes       int64     `json:"bytes"`
	CreatedAt   time.Time `json:"createdAt"`
	CreatedBy   string    `json:"createdBy"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
}

// Session is a signed-in browser session.
type Session struct {
	UserID    string
	ExpiresAt time.Time
}

// Access is a request's authorization context: which applications' data the
// user can see, and what they are allowed to do.
//
// Query endpoints use this rather than the raw user, so the authorization
// decision is computed in exactly one place.
type Access struct {
	User        *User
	Permissions map[Permission]bool
	// Projects are the projects the user reaches through their role groups.
	Projects []Project
	// Applications is the union of reachable service names.
	Applications []string
}

// Can asks whether a permission is held. A super administrator can do anything.
func (a *Access) Can(p Permission) bool {
	if a == nil || a.User == nil {
		return false
	}
	if a.User.IsSuperAdmin {
		return true
	}
	return a.Permissions[p]
}

// SeesEverything says whether telemetry queries run unfiltered. True only for
// super administrators.
func (a *Access) SeesEverything() bool {
	return a != nil && a.User != nil && a.User.IsSuperAdmin
}

// NormalizeServiceName cleans up an application name supplied by a user.
func NormalizeServiceName(s string) string {
	return strings.TrimSpace(s)
}
