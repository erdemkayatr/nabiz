package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Common errors. Endpoints pick an HTTP status code by looking at these.
var (
	ErrNotFound      = errors.New("record not found")
	ErrDuplicate     = errors.New("record already exists")
	ErrBadCredential = errors.New("wrong email or password")
	ErrInactive      = errors.New("account is inactive")
	ErrLastAdmin     = errors.New("cannot remove the last administrator in the system")
)

// Store is the control plane database.
type Store struct {
	pool   *pgxpool.Pool
	log    *slog.Logger
	sealer *Sealer
}

// Open opens the pool and verifies it is alive.
func Open(ctx context.Context, dsn string, log *slog.Logger) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("could not parse the postgres dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("could not open the postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}
	return &Store{pool: pool, log: log}, nil
}

// Close closes the pool.
func (s *Store) Close() { s.pool.Close() }

// Migrate creates the schema idempotently.
func (s *Store) Migrate(ctx context.Context) error {
	for i, stmt := range Schema {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("schema step %d failed: %w", i, err)
		}
	}
	s.log.Info("control plane schema ready")
	return nil
}

// --- bootstrap ---

// Bootstrap creates the first super administrator when there are no users, and
// returns the generated password. It uses the password given, or generates a
// random one when none is: a management panel that opens with a default
// password is worse than none.
func (s *Store) Bootstrap(ctx context.Context, email, password string) (created bool, generated string, err error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return false, "", err
	}
	if count > 0 {
		return false, "", nil
	}

	if email == "" {
		email = "admin@nabiz.local"
	}
	if password == "" {
		password = randomToken(12)
		generated = password
	}

	if _, err := s.CreateUser(ctx, email, "System Administrator", password, true); err != nil {
		return false, "", err
	}
	return true, generated, nil
}

// --- users ---

const userColumns = `u.id, u.email, u.name, u.is_super_admin, u.is_active, u.created_at, u.last_login_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.IsSuperAdmin, &u.IsActive, &u.CreatedAt, &u.LastLoginAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// ListUsers returns the users along with their role groups.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+userColumns+`,
		       COALESCE(json_agg(json_build_object('id', rg.id, 'name', rg.name)
		                ORDER BY rg.name) FILTER (WHERE rg.id IS NOT NULL), '[]')::text
		FROM users u
		LEFT JOIN user_role_groups urg ON urg.user_id = u.id
		LEFT JOIN role_groups rg ON rg.id = urg.role_group_id
		GROUP BY u.id
		ORDER BY u.is_super_admin DESC, lower(u.email)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		var u User
		var groupsJSON string
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.IsSuperAdmin, &u.IsActive,
			&u.CreatedAt, &u.LastLoginAt, &groupsJSON); err != nil {
			return nil, err
		}
		u.RoleGroups = parseRoleGroupRefs(groupsJSON)
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser reads a single user.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
}

// CreateUser creates a new user.
func (s *Store) CreateUser(ctx context.Context, email, name, password string, superAdmin bool) (*User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("email is required")
	}
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u, err := scanUser(s.pool.QueryRow(ctx, `
		INSERT INTO users (email, name, password_hash, is_super_admin)
		VALUES ($1, $2, $3, $4)
		RETURNING `+strings.ReplaceAll(userColumns, "u.", "")+``,
		email, strings.TrimSpace(name), string(hash), superAdmin))
	if isUniqueViolation(err) {
		return nil, ErrDuplicate
	}
	return u, err
}

// UpdateUser updates the name, active and administrator fields.
func (s *Store) UpdateUser(ctx context.Context, id, name string, isActive, isSuperAdmin bool) (*User, error) {
	// If the last administrator is demoted or deactivated, nobody can get into
	// the system. That door stays shut.
	if !isSuperAdmin || !isActive {
		if last, err := s.isLastActiveAdmin(ctx, id); err != nil {
			return nil, err
		} else if last {
			return nil, ErrLastAdmin
		}
	}
	return scanUser(s.pool.QueryRow(ctx, `
		UPDATE users SET name = $2, is_active = $3, is_super_admin = $4, updated_at = now()
		WHERE id = $1
		RETURNING `+strings.ReplaceAll(userColumns, "u.", "")+``,
		id, strings.TrimSpace(name), isActive, isSuperAdmin))
}

// SetPassword changes the password and closes all of the user's sessions.
func (s *Store) SetPassword(ctx context.Context, id, password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, string(hash))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Once the password changes, the old sessions must stop working.
	_, err = s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
	return err
}

// DeleteUser deletes a user.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	if last, err := s.isLastActiveAdmin(ctx, id); err != nil {
		return err
	} else if last {
		return ErrLastAdmin
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// isLastActiveAdmin reports whether this user is the only active administrator.
func (s *Store) isLastActiveAdmin(ctx context.Context, id string) (bool, error) {
	var isAdmin bool
	err := s.pool.QueryRow(ctx, `SELECT is_super_admin AND is_active FROM users WHERE id = $1`, id).Scan(&isAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !isAdmin {
		return false, err
	}
	var others int
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE is_super_admin AND is_active AND id <> $1`, id).Scan(&others)
	return others == 0, err
}

// SetUserRoleGroups replaces the user's groups with the given set.
func (s *Store) SetUserRoleGroups(ctx context.Context, userID string, groupIDs []string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM user_role_groups WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for _, gid := range groupIDs {
			if _, err := tx.Exec(ctx,
				`INSERT INTO user_role_groups (user_id, role_group_id) VALUES ($1, $2)
				 ON CONFLICT DO NOTHING`, userID, gid); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- role groups ---

// ListRoleGroups returns the groups with their member and project counts.
func (s *Store) ListRoleGroups(ctx context.Context) ([]RoleGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.name, g.description, g.permissions, g.created_at,
		       (SELECT count(*) FROM user_role_groups x WHERE x.role_group_id = g.id),
		       (SELECT count(*) FROM project_role_groups y WHERE y.role_group_id = g.id)
		FROM role_groups g
		ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RoleGroup{}
	for rows.Next() {
		var g RoleGroup
		var perms []string
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &perms, &g.CreatedAt,
			&g.MemberCount, &g.ProjectCount); err != nil {
			return nil, err
		}
		g.Permissions = toPermissions(perms)
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateRoleGroup creates a new group.
func (s *Store) CreateRoleGroup(ctx context.Context, name, description string, perms []Permission) (*RoleGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("group name is required")
	}
	g, err := s.scanRoleGroup(s.pool.QueryRow(ctx, `
		INSERT INTO role_groups (name, description, permissions)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, permissions, created_at`,
		name, strings.TrimSpace(description), fromPermissions(perms)))
	if isUniqueViolation(err) {
		return nil, ErrDuplicate
	}
	return g, err
}

// UpdateRoleGroup updates a group.
func (s *Store) UpdateRoleGroup(ctx context.Context, id, name, description string, perms []Permission) (*RoleGroup, error) {
	g, err := s.scanRoleGroup(s.pool.QueryRow(ctx, `
		UPDATE role_groups SET name = $2, description = $3, permissions = $4
		WHERE id = $1
		RETURNING id, name, description, permissions, created_at`,
		id, strings.TrimSpace(name), strings.TrimSpace(description), fromPermissions(perms)))
	if isUniqueViolation(err) {
		return nil, ErrDuplicate
	}
	return g, err
}

// DeleteRoleGroup deletes a group and its bindings.
func (s *Store) DeleteRoleGroup(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM role_groups WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) scanRoleGroup(row pgx.Row) (*RoleGroup, error) {
	var g RoleGroup
	var perms []string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &perms, &g.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.Permissions = toPermissions(perms)
	return &g, nil
}

// --- projects ---

// ListProjects returns the projects with their application and role group lists.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.key, p.name, p.description, p.created_at,
		       COALESCE(array_agg(DISTINCT pa.service_name)
		                FILTER (WHERE pa.service_name IS NOT NULL), '{}'),
		       COALESCE(json_agg(DISTINCT jsonb_build_object('id', rg.id, 'name', rg.name))
		                FILTER (WHERE rg.id IS NOT NULL), '[]')::text
		FROM projects p
		LEFT JOIN project_applications pa ON pa.project_id = p.id
		LEFT JOIN project_role_groups prg ON prg.project_id = p.id
		LEFT JOIN role_groups rg ON rg.id = prg.role_group_id
		GROUP BY p.id
		ORDER BY lower(p.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		var p Project
		var groupsJSON string
		if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CreatedAt,
			&p.Applications, &groupsJSON); err != nil {
			return nil, err
		}
		p.RoleGroups = parseRoleGroupRefs(groupsJSON)
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateProject creates a new project.
func (s *Store) CreateProject(ctx context.Context, key, name, description string) (*Project, error) {
	key, name = strings.TrimSpace(key), strings.TrimSpace(name)
	if key == "" || name == "" {
		return nil, errors.New("project key and name are required")
	}
	p, err := s.scanProject(s.pool.QueryRow(ctx, `
		INSERT INTO projects (key, name, description) VALUES ($1, $2, $3)
		RETURNING id, key, name, description, created_at`,
		key, name, strings.TrimSpace(description)))
	if isUniqueViolation(err) {
		return nil, ErrDuplicate
	}
	return p, err
}

// UpdateProject updates a project.
func (s *Store) UpdateProject(ctx context.Context, id, name, description string) (*Project, error) {
	return s.scanProject(s.pool.QueryRow(ctx, `
		UPDATE projects SET name = $2, description = $3 WHERE id = $1
		RETURNING id, key, name, description, created_at`,
		id, strings.TrimSpace(name), strings.TrimSpace(description)))
}

// DeleteProject deletes a project and its bindings.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) scanProject(row pgx.Row) (*Project, error) {
	var p Project
	if err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// SetProjectApplications replaces the project's application list.
func (s *Store) SetProjectApplications(ctx context.Context, projectID string, services []string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM project_applications WHERE project_id = $1`, projectID); err != nil {
			return err
		}
		for _, svc := range services {
			svc = NormalizeServiceName(svc)
			if svc == "" {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO project_applications (project_id, service_name) VALUES ($1, $2)
				 ON CONFLICT DO NOTHING`, projectID, svc); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetProjectRoleGroups replaces the groups that can reach the project.
func (s *Store) SetProjectRoleGroups(ctx context.Context, projectID string, groupIDs []string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM project_role_groups WHERE project_id = $1`, projectID); err != nil {
			return err
		}
		for _, gid := range groupIDs {
			if _, err := tx.Exec(ctx,
				`INSERT INTO project_role_groups (project_id, role_group_id) VALUES ($1, $2)
				 ON CONFLICT DO NOTHING`, projectID, gid); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- sessions ---

// Authenticate verifies an email and password.
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	var u User
	var hash string
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, name, password_hash, is_super_admin, is_active, created_at, last_login_at
		FROM users WHERE lower(email) = lower($1)`, strings.TrimSpace(email)).
		Scan(&u.ID, &u.Email, &u.Name, &hash, &u.IsSuperAdmin, &u.IsActive, &u.CreatedAt, &u.LastLoginAt)

	if errors.Is(err, pgx.ErrNoRows) {
		// Pay the bcrypt cost even when the user does not exist, so response
		// time does not answer "is this email registered?".
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(password))
		return nil, ErrBadCredential
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrBadCredential
	}
	if !u.IsActive {
		return nil, ErrInactive
	}
	_, _ = s.pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, u.ID)
	return &u, nil
}

// CreateSession opens a session and returns the plain token. The token itself
// is never stored, only its SHA-256 digest.
func (s *Store) CreateSession(ctx context.Context, userID, userAgent string, ttl time.Duration) (string, time.Time, error) {
	token := randomToken(32)
	expires := time.Now().Add(ttl)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, user_agent) VALUES ($1, $2, $3, $4)`,
		hashToken(token), userID, expires, truncate(userAgent, 250)); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// LookupSession finds the user behind a token. Expired sessions are ignored.
func (s *Store) LookupSession(ctx context.Context, token string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.is_active`,
		hashToken(token)))
	return u, err
}

// DeleteSession closes a single session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

// PurgeExpiredSessions clears out expired sessions.
func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// --- authorization ---

// LoadAccess gathers the user's permissions, projects and reachable
// applications. The query endpoints work from this.
func (s *Store) LoadAccess(ctx context.Context, u *User) (*Access, error) {
	access := &Access{User: u, Permissions: map[Permission]bool{}, Projects: []Project{}, Applications: []string{}}

	// Every permission across the user's groups.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT unnest(g.permissions)
		FROM user_role_groups urg JOIN role_groups g ON g.id = urg.role_group_id
		WHERE urg.user_id = $1`, u.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		access.Permissions[Permission(p)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A super administrator sees every project; everyone else sees only those
	// bound to one of their groups.
	var projectRows pgx.Rows
	if u.IsSuperAdmin {
		projectRows, err = s.pool.Query(ctx, `
			SELECT p.id, p.key, p.name, p.description, p.created_at,
			       COALESCE(array_agg(DISTINCT pa.service_name)
			                FILTER (WHERE pa.service_name IS NOT NULL), '{}')
			FROM projects p
			LEFT JOIN project_applications pa ON pa.project_id = p.id
			GROUP BY p.id ORDER BY lower(p.name)`)
	} else {
		projectRows, err = s.pool.Query(ctx, `
			SELECT p.id, p.key, p.name, p.description, p.created_at,
			       COALESCE(array_agg(DISTINCT pa.service_name)
			                FILTER (WHERE pa.service_name IS NOT NULL), '{}')
			FROM projects p
			JOIN project_role_groups prg ON prg.project_id = p.id
			JOIN user_role_groups urg ON urg.role_group_id = prg.role_group_id
			LEFT JOIN project_applications pa ON pa.project_id = p.id
			WHERE urg.user_id = $1
			GROUP BY p.id ORDER BY lower(p.name)`, u.ID)
	}
	if err != nil {
		return nil, err
	}
	defer projectRows.Close()

	seen := map[string]bool{}
	for projectRows.Next() {
		var p Project
		if err := projectRows.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.Applications); err != nil {
			return nil, err
		}
		access.Projects = append(access.Projects, p)
		for _, svc := range p.Applications {
			if !seen[svc] {
				seen[svc] = true
				access.Applications = append(access.Applications, svc)
			}
		}
	}
	return access, projectRows.Err()
}

// --- helpers ---

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func toPermissions(raw []string) []Permission {
	out := make([]Permission, 0, len(raw))
	for _, p := range raw {
		if perm := Permission(p); ValidPermission(perm) {
			out = append(out, perm)
		}
	}
	return out
}

// fromPermissions drops unknown permissions, so free text from the UI or from
// a hand-written request never reaches the database.
func fromPermissions(perms []Permission) []string {
	out := make([]string, 0, len(perms))
	seen := map[Permission]bool{}
	for _, p := range perms {
		if ValidPermission(p) && !seen[p] {
			seen[p] = true
			out = append(out, string(p))
		}
	}
	return out
}

// parseRoleGroupRefs decodes the group list aggregated with json_agg on the
// SQL side. Getting it in one pass rather than a separate query is what keeps
// the user list from turning into an N+1 problem.
func parseRoleGroupRefs(raw string) []RoleGroupRef {
	out := []RoleGroupRef{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []RoleGroupRef{}
	}
	return out
}

func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// Carrying on is not safe when crypto/rand fails.
		panic("could not obtain cryptographic randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func sha256Sum(v string) []byte {
	sum := sha256.Sum256([]byte(v))
	return sum[:]
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
