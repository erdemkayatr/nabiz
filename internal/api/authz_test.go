package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// requestWith builds a request carrying the given authorization context.
func requestWith(access *identity.Access, url string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	if access == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), ctxAccess, access))
}

func userAccess(apps []string, projects ...identity.Project) *identity.Access {
	return &identity.Access{
		User:         &identity.User{ID: "u1", Email: "a@b.c"},
		Permissions:  map[identity.Permission]bool{identity.PermTopologyRead: true},
		Applications: apps,
		Projects:     projects,
	}
}

func adminAccess() *identity.Access {
	return &identity.Access{
		User:        &identity.User{ID: "u0", Email: "admin@x", IsSuperAdmin: true},
		Permissions: map[identity.Permission]bool{},
	}
}

// A request without a session must see nothing. The default scope has to be
// "nothing", not "everything".
func TestScopeWithoutSessionIsEmpty(t *testing.T) {
	sc := scopeFor(requestWith(nil, "/api/v1/services"))
	if !sc.empty() {
		t.Fatal("a request without a session must get an empty scope")
	}
	if sc.unrestricted {
		t.Fatal("a request without a session got an unrestricted scope")
	}
}

func TestSuperAdminIsUnrestricted(t *testing.T) {
	sc := scopeFor(requestWith(adminAccess(), "/api/v1/services"))
	if !sc.unrestricted {
		t.Fatal("a super administrator must be unrestricted")
	}
	if sc.empty() {
		t.Fatal("the super administrator's scope looks empty")
	}
	if f, _ := sc.filterServiceName("service_name"); f != "" {
		t.Errorf("a filter was produced for a super administrator: %q", f)
	}
}

func TestScopeLimitsToAssignedApplications(t *testing.T) {
	sc := scopeFor(requestWith(userAccess([]string{"api", "worker"}), "/api/v1/services"))
	if sc.unrestricted {
		t.Fatal("an ordinary user got an unrestricted scope")
	}
	if !sc.allows("api") || !sc.allows("worker") {
		t.Error("assigned applications fell outside the scope")
	}
	if sc.allows("gizli-servis") {
		t.Error("an unassigned application entered the scope")
	}
}

func TestProjectlessUserSeesNothing(t *testing.T) {
	sc := scopeFor(requestWith(userAccess(nil), "/api/v1/services"))
	if !sc.empty() {
		t.Fatal("a user assigned to no project can see data")
	}
}

// A project filter has to narrow the scope, never widen it.
func TestProjectParameterNarrowsScope(t *testing.T) {
	access := userAccess([]string{"api", "worker"},
		identity.Project{Key: "alfa", Applications: []string{"api"}},
		identity.Project{Key: "beta", Applications: []string{"worker"}})

	sc := scopeFor(requestWith(access, "/api/v1/services?project=alfa"))
	if !sc.allows("api") || sc.allows("worker") {
		t.Errorf("the project filter did not narrow: %v", sc.services)
	}
}

// Asking for an unreachable project must return an empty result rather than an
// error: otherwise the endpoint becomes a tool for enumerating projects.
func TestUnknownProjectYieldsEmptyScopeNotError(t *testing.T) {
	access := userAccess([]string{"api"}, identity.Project{Key: "alfa", Applications: []string{"api"}})
	sc := scopeFor(requestWith(access, "/api/v1/services?project=baskasinin-projesi"))
	if !sc.empty() {
		t.Fatal("data came back for an unreachable project")
	}
}

func TestServiceNameFilterBindsServices(t *testing.T) {
	sc := scope{services: []string{"api", "worker"}}
	clause, args := sc.filterServiceName("service_name")
	if !strings.Contains(clause, "service_name IN ?") {
		t.Errorf("beklenmeyen filtre: %q", clause)
	}
	if len(args) != 1 {
		t.Fatalf("argument count %d, expected 1", len(args))
	}
	if svcs, ok := args[0].([]string); !ok || len(svcs) != 2 {
		t.Errorf("the service list was not bound: %#v", args[0])
	}
}

// The topology filter must also match ends shaped "namespace/service".
func TestTopologyFilterMatchesNamespacedEndpoints(t *testing.T) {
	sc := scope{services: []string{"api"}}
	clause, args := sc.filterTopology()
	if !strings.Contains(clause, "splitByChar('/', client)") ||
		!strings.Contains(clause, "splitByChar('/', server)") {
		t.Errorf("both ends of the edge are not filtered: %q", clause)
	}
	if !strings.Contains(clause, " OR ") {
		t.Error("an edge must show when only one end is in scope")
	}
	if len(args) != 2 {
		t.Errorf("argument count %d, expected 2", len(args))
	}
}

func TestTraceFilterUsesHasAny(t *testing.T) {
	sc := scope{services: []string{"api"}}
	clause, args := sc.filterTraces()
	if !strings.Contains(clause, "hasAny(services, ?)") {
		t.Errorf("beklenmeyen trace filtresi: %q", clause)
	}
	if len(args) != 1 {
		t.Errorf("argument count %d, expected 1", len(args))
	}
}

// --- permission logic ---

func TestSuperAdminCanDoEverything(t *testing.T) {
	a := adminAccess()
	for _, p := range identity.AllPermissions {
		if !a.Can(p) {
			t.Errorf("the super administrator did not get the %s permission", p)
		}
	}
}

func TestPermissionsComeOnlyFromRoleGroups(t *testing.T) {
	a := userAccess(nil)
	if !a.Can(identity.PermTopologyRead) {
		t.Error("a permission coming from a group was not recognised")
	}
	if a.Can(identity.PermAdmin) {
		t.Error("an admin permission that was never granted was recognised")
	}
	if a.Can(identity.PermTracesRead) {
		t.Error("a trace permission that was never granted was recognised")
	}
}

func TestNilAccessCanDoNothing(t *testing.T) {
	var a *identity.Access
	if a.Can(identity.PermTopologyRead) {
		t.Error("a nil authorization context granted a permission")
	}
	if a.SeesEverything() {
		t.Error("a nil authorization context can see everything")
	}
}

// Unknown permission names must never reach the database.
func TestUnknownPermissionsRejected(t *testing.T) {
	if identity.ValidPermission("bir.sey.uydurma") {
		t.Error("a made-up permission was treated as valid")
	}
	for _, p := range identity.AllPermissions {
		if !identity.ValidPermission(p) {
			t.Errorf("%s was treated as invalid", p)
		}
	}
}

// --- login attempt limiter ---

func TestLoginLimiterBlocksAfterMax(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow("ip|mail") {
			t.Fatalf("attempt %d was blocked, the limit is 3", i+1)
		}
	}
	if l.allow("ip|mail") {
		t.Error("an attempt was allowed even though the limit was exceeded")
	}
	// A different source must not be affected.
	if !l.allow("baska-ip|mail") {
		t.Error("one source's limit locked out another")
	}
	// A successful login must clear the counter.
	l.clear("ip|mail")
	if !l.allow("ip|mail") {
		t.Error("the counter was not cleared after a successful login")
	}
}
