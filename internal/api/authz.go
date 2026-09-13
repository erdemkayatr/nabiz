package api

import (
	"net/http"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// scope is the set of applications a request may see.
//
// Authorization is reduced to this one type, and every endpoint that writes a
// query uses the same filter. The "we forgot to add the filter on that one
// endpoint" bug is only prevented by the filter coming from a single place.
type scope struct {
	// unrestricted is true for a super administrator: no filter is applied.
	unrestricted bool
	// services are the visible service names. When unrestricted is false and
	// the list is empty, the user sees nothing at all.
	services []string
}

// scopeFor derives the authorization scope from a request.
//
// If the `project` query parameter is given, the scope narrows to that project;
// if that project is not among the ones the user can reach, the scope is empty.
func scopeFor(r *http.Request) scope {
	access := accessFrom(r)
	if access == nil {
		return scope{}
	}

	projectKey := r.URL.Query().Get("project")
	if projectKey == "" {
		if access.SeesEverything() {
			return scope{unrestricted: true}
		}
		return scope{services: access.Applications}
	}

	for _, p := range access.Projects {
		if p.Key == projectKey {
			return scope{services: p.Applications}
		}
	}
	// An unreachable project was asked for: an empty result rather than an
	// error. A project that does not exist and one that cannot be reached have
	// to give the same answer, or the endpoint becomes a project enumerator.
	return scope{}
}

// empty says the scope will return no data at all.
func (s scope) empty() bool { return !s.unrestricted && len(s.services) == 0 }

// allows says whether a single service is in scope.
func (s scope) allows(service string) bool {
	if s.unrestricted {
		return true
	}
	for _, svc := range s.services {
		if svc == service {
			return true
		}
	}
	return false
}

// filterServiceName produces the WHERE clause for tables carrying a
// service_name column (spans, operation_stats).
func (s scope) filterServiceName(column string) (string, []any) {
	if s.unrestricted {
		return "", nil
	}
	return " AND " + column + " IN ?", []any{s.services}
}

// filterTopology produces the WHERE clause for the service_edges table.
//
// An edge's ends can be shaped "namespace/service", so the last segment is
// taken. The edge is shown when either end is in scope: who calls a project's
// own service is the project owner's business to see.
func (s scope) filterTopology() (string, []any) {
	if s.unrestricted {
		return "", nil
	}
	return " AND (" +
			"arrayElement(splitByChar('/', client), -1) IN ?" +
			" OR arrayElement(splitByChar('/', server), -1) IN ?)",
		[]any{s.services, s.services}
}

// filterTraces produces the WHERE clause for the trace_index table.
func (s scope) filterTraces() (string, []any) {
	if s.unrestricted {
		return "", nil
	}
	return " AND hasAny(services, ?)", []any{s.services}
}

// The permissions required per endpoint.
var (
	permTopology = identity.PermTopologyRead
	permServices = identity.PermServicesRead
	permTraces   = identity.PermTracesRead
)
