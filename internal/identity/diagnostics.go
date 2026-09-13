package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrTokenMismatch means the token the agent presented does not match the
// project's token.
var ErrTokenMismatch = errors.New("diagnostics token does not match")

// SetSealer attaches the token encrypter. Without one, tokens cannot be stored.
func (s *Store) SetSealer(sealer *Sealer) { s.sealer = sealer }

// --- project diagnostics token ---

// SetProjectDiagnosticsToken stores the token used by the project's applications.
func (s *Store) SetProjectDiagnosticsToken(ctx context.Context, projectID, token, by string) error {
	if s.sealer == nil {
		return ErrNoSecretKey
	}
	if strings.TrimSpace(token) == "" {
		_, err := s.pool.Exec(ctx, `DELETE FROM project_diagnostics WHERE project_id = $1`, projectID)
		return err
	}
	sealed, err := s.sealer.Seal(token)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO project_diagnostics (project_id, token_sealed, updated_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO UPDATE
		SET token_sealed = EXCLUDED.token_sealed, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		projectID, sealed, by)
	return err
}

// HasProjectDiagnosticsToken reports whether a token is set. The token itself
// never travels back to the UI; only its existence is exposed.
func (s *Store) HasProjectDiagnosticsToken(ctx context.Context, projectID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM project_diagnostics WHERE project_id = $1`, projectID).Scan(&count)
	return count > 0, err
}

// projectTokenFor decrypts the tokens of the projects a service name is
// assigned to. A service can belong to more than one project, so more than one
// token can come back.
func (s *Store) projectTokenFor(ctx context.Context, serviceName string) ([]tokenMatch, error) {
	if s.sealer == nil {
		return nil, ErrNoSecretKey
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, d.token_sealed
		FROM project_applications a
		JOIN projects p ON p.id = a.project_id
		JOIN project_diagnostics d ON d.project_id = p.id
		WHERE a.service_name = $1`, serviceName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []tokenMatch{}
	for rows.Next() {
		var projectID, sealed string
		if err := rows.Scan(&projectID, &sealed); err != nil {
			return nil, err
		}
		token, err := s.sealer.Open(sealed)
		if err != nil {
			// If the key has changed, carry on and try the other projects.
			continue
		}
		out = append(out, tokenMatch{ProjectID: projectID, Token: token})
	}
	return out, rows.Err()
}

type tokenMatch struct {
	ProjectID string
	Token     string
}

// --- agent registration ---

// RegisterAgent handles an application instance announcing itself.
//
// The registration is verified against the token set on the service's project.
// An unverified registration is still stored, so the operator can see it, but
// dumps cannot be triggered against it: otherwise a forged registration could
// talk nabiz into sending the token to an attacker's address.
func (s *Store) RegisterAgent(ctx context.Context, in AgentInstance, presentedToken string) (*AgentInstance, error) {
	verified := false
	projectID := ""

	if presentedToken != "" {
		matches, err := s.projectTokenFor(ctx, in.ServiceName)
		if err != nil && !errors.Is(err, ErrNoSecretKey) {
			return nil, err
		}
		for _, m := range matches {
			if constantTimeEqual(m.Token, presentedToken) {
				verified = true
				projectID = m.ProjectID
				break
			}
		}
	}

	var project any
	if projectID != "" {
		project = projectID
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO agent_instances (
			service_name, instance_id, hostname, k8s_pod, k8s_namespace, pid,
			agent_version, source_ip, advertised_host, diag_port, diag_path,
			diag_ready, verified, project_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (service_name, instance_id) DO UPDATE SET
			hostname = EXCLUDED.hostname, k8s_pod = EXCLUDED.k8s_pod,
			k8s_namespace = EXCLUDED.k8s_namespace, pid = EXCLUDED.pid,
			agent_version = EXCLUDED.agent_version, source_ip = EXCLUDED.source_ip,
			advertised_host = EXCLUDED.advertised_host,
			diag_port = EXCLUDED.diag_port, diag_path = EXCLUDED.diag_path,
			diag_ready = EXCLUDED.diag_ready, verified = EXCLUDED.verified,
			project_id = EXCLUDED.project_id, last_seen = now()
		RETURNING id, first_seen, last_seen`,
		in.ServiceName, in.InstanceID, in.Hostname, in.K8sPod, in.K8sNamespace, in.PID,
		in.AgentVersion, in.SourceIP, in.AdvertisedHost, in.DiagPort, in.DiagPath,
		in.DiagReady, verified, project)

	out := in
	out.Verified = verified
	out.ProjectID = projectID
	if err := row.Scan(&out.ID, &out.FirstSeen, &out.LastSeen); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAgents returns instances ordered by when they were last seen. Anything
// older than maxAge is dropped: listing dead pods misleads the operator.
func (s *Store) ListAgents(ctx context.Context, services []string, maxAge time.Duration) ([]AgentInstance, error) {
	query := `
		SELECT id, service_name, instance_id, hostname, k8s_pod, k8s_namespace, pid,
		       agent_version, source_ip, advertised_host, diag_port, diag_path,
		       diag_ready, verified, COALESCE(project_id::text, ''), first_seen, last_seen
		FROM agent_instances
		WHERE last_seen > now() - $1::interval`
	args := []any{fmt.Sprintf("%d seconds", int(maxAge.Seconds()))}

	if services != nil {
		query += ` AND service_name = ANY($2)`
		args = append(args, services)
	}
	query += ` ORDER BY service_name, last_seen DESC`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AgentInstance{}
	for rows.Next() {
		var a AgentInstance
		if err := rows.Scan(&a.ID, &a.ServiceName, &a.InstanceID, &a.Hostname, &a.K8sPod,
			&a.K8sNamespace, &a.PID, &a.AgentVersion, &a.SourceIP, &a.AdvertisedHost,
			&a.DiagPort, &a.DiagPath, &a.DiagReady, &a.Verified, &a.ProjectID,
			&a.FirstSeen, &a.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgent returns a single instance along with the token needed to trigger it.
func (s *Store) GetAgent(ctx context.Context, id string) (*AgentInstance, string, error) {
	var a AgentInstance
	err := s.pool.QueryRow(ctx, `
		SELECT id, service_name, instance_id, hostname, k8s_pod, k8s_namespace, pid,
		       agent_version, source_ip, advertised_host, diag_port, diag_path,
		       diag_ready, verified, COALESCE(project_id::text, ''), first_seen, last_seen
		FROM agent_instances WHERE id = $1`, id).
		Scan(&a.ID, &a.ServiceName, &a.InstanceID, &a.Hostname, &a.K8sPod, &a.K8sNamespace,
			&a.PID, &a.AgentVersion, &a.SourceIP, &a.AdvertisedHost, &a.DiagPort, &a.DiagPath,
			&a.DiagReady, &a.Verified, &a.ProjectID, &a.FirstSeen, &a.LastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if !a.Verified {
		return &a, "", ErrTokenMismatch
	}

	matches, err := s.projectTokenFor(ctx, a.ServiceName)
	if err != nil {
		return &a, "", err
	}
	for _, m := range matches {
		if m.ProjectID == a.ProjectID {
			return &a, m.Token, nil
		}
	}
	return &a, "", ErrTokenMismatch
}

// --- dump records ---

// CreateArtifact opens a dump record.
func (s *Store) CreateArtifact(ctx context.Context, instanceID, serviceName, kind, by string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO dump_artifacts (instance_id, service_name, kind, filename, created_by, status)
		VALUES ($1, $2, $3, '', $4, 'pending') RETURNING id`,
		instanceID, serviceName, kind, by).Scan(&id)
	return id, err
}

// CompleteArtifact marks the record as finished.
func (s *Store) CompleteArtifact(ctx context.Context, id, filename string, bytes int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE dump_artifacts SET filename = $2, bytes = $3, status = 'ready' WHERE id = $1`,
		id, filename, bytes)
	return err
}

// FailArtifact marks the record as failed.
func (s *Store) FailArtifact(ctx context.Context, id, message string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE dump_artifacts SET status = 'failed', error = $2 WHERE id = $1`, id, truncate(message, 500))
	return err
}

// ListArtifacts returns the stored dumps.
func (s *Store) ListArtifacts(ctx context.Context, services []string, limit int) ([]DumpArtifact, error) {
	query := `
		SELECT id, COALESCE(instance_id::text, ''), service_name, kind, filename,
		       bytes, status, error, created_by, created_at
		FROM dump_artifacts`
	args := []any{}
	if services != nil {
		query += ` WHERE service_name = ANY($1)`
		args = append(args, services)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DumpArtifact{}
	for rows.Next() {
		var a DumpArtifact
		// The order has to match the SELECT exactly; pgx takes any for these,
		// so the compiler cannot catch a mismatch.
		if err := rows.Scan(&a.ID, &a.InstanceID, &a.ServiceName, &a.Kind, &a.Filename,
			&a.Bytes, &a.Status, &a.Error, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetArtifact returns a single record.
func (s *Store) GetArtifact(ctx context.Context, id string) (*DumpArtifact, error) {
	var a DumpArtifact
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(instance_id::text, ''), service_name, kind, filename,
		       bytes, status, error, created_by, created_at
		FROM dump_artifacts WHERE id = $1`, id).
		Scan(&a.ID, &a.InstanceID, &a.ServiceName, &a.Kind, &a.Filename,
			&a.Bytes, &a.Status, &a.Error, &a.CreatedBy, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// CancelArtifact marks the record as stopped by the user. Only a record that
// is still pending can be stopped: once the file has landed, the job is done.
func (s *Store) CancelArtifact(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE dump_artifacts SET status = 'cancelled', error = '' WHERE id = $1 AND status = 'pending'`, id)
	return err
}

// DeleteArtifact deletes the record.
func (s *Store) DeleteArtifact(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM dump_artifacts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func constantTimeEqual(a, b string) bool {
	sa, sb := sha256Sum(a), sha256Sum(b)
	var diff byte
	for i := range sa {
		diff |= sa[i] ^ sb[i]
	}
	return diff == 0
}
