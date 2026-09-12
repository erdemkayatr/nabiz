package identity

// Schema, açılışta idempotent çalıştırılan DDL'ler.
//
// Silme davranışları bilinçli: bir rol grubu silinince üyelikleri ve proje
// bağları da gider (CASCADE), ama bir kullanıcı silinince projeler durur.
var Schema = []string{
	`CREATE EXTENSION IF NOT EXISTS pgcrypto`,

	`CREATE TABLE IF NOT EXISTS users (
		id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		email          TEXT NOT NULL,
		name           TEXT NOT NULL DEFAULT '',
		password_hash  TEXT NOT NULL,
		is_super_admin BOOLEAN NOT NULL DEFAULT FALSE,
		is_active      BOOLEAN NOT NULL DEFAULT TRUE,
		created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_login_at  TIMESTAMPTZ
	)`,
	// E-posta büyük/küçük harf duyarsız benzersiz olmalı: "Ali@x.com" ile
	// "ali@x.com" iki hesap açamasın.
	`CREATE UNIQUE INDEX IF NOT EXISTS users_email_key ON users (lower(email))`,

	`CREATE TABLE IF NOT EXISTS role_groups (
		id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		permissions TEXT[] NOT NULL DEFAULT '{}',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS role_groups_name_key ON role_groups (lower(name))`,

	`CREATE TABLE IF NOT EXISTS user_role_groups (
		user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		role_group_id UUID NOT NULL REFERENCES role_groups(id) ON DELETE CASCADE,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (user_id, role_group_id)
	)`,

	`CREATE TABLE IF NOT EXISTS projects (
		id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		key         TEXT NOT NULL,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS projects_key_key ON projects (lower(key))`,

	// Uygulama = telemetride görülen service.name. Bir servis birden fazla
	// projeye atanabilir: paylaşılan altyapı servisleri gerçekte böyle.
	`CREATE TABLE IF NOT EXISTS project_applications (
		project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		service_name TEXT NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (project_id, service_name)
	)`,
	`CREATE INDEX IF NOT EXISTS project_applications_service_idx ON project_applications (service_name)`,

	`CREATE TABLE IF NOT EXISTS project_role_groups (
		project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		role_group_id UUID NOT NULL REFERENCES role_groups(id) ON DELETE CASCADE,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (project_id, role_group_id)
	)`,

	// Oturum jetonu düz metin saklanmaz: veritabanı sızarsa jetonlar
	// doğrudan kullanılabilir olmasın.
	// Projenin tanılama jetonu. Şifreli saklanır; düz metin hiç yazılmaz.
	`CREATE TABLE IF NOT EXISTS project_diagnostics (
		project_id   UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
		token_sealed TEXT NOT NULL,
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_by   TEXT NOT NULL DEFAULT ''
	)`,

	// Kendini tanıtan uygulama örnekleri.
	`CREATE TABLE IF NOT EXISTS agent_instances (
		id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		service_name  TEXT NOT NULL,
		instance_id   TEXT NOT NULL,
		hostname      TEXT NOT NULL DEFAULT '',
		k8s_pod       TEXT NOT NULL DEFAULT '',
		k8s_namespace TEXT NOT NULL DEFAULT '',
		pid           INT  NOT NULL DEFAULT 0,
		agent_version TEXT NOT NULL DEFAULT '',
		source_ip     TEXT NOT NULL DEFAULT '',
		advertised_host TEXT NOT NULL DEFAULT '',
		diag_port     INT  NOT NULL DEFAULT 0,
		diag_path     TEXT NOT NULL DEFAULT '',
		diag_ready    BOOLEAN NOT NULL DEFAULT FALSE,
		verified      BOOLEAN NOT NULL DEFAULT FALSE,
		project_id    UUID REFERENCES projects(id) ON DELETE SET NULL,
		first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_seen     TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS agent_instances_key
	   ON agent_instances (service_name, instance_id)`,
	`CREATE INDEX IF NOT EXISTS agent_instances_last_seen ON agent_instances (last_seen DESC)`,
	// Var olan kurulumlar için: kolon sonradan eklendi.
	`ALTER TABLE agent_instances ADD COLUMN IF NOT EXISTS advertised_host TEXT NOT NULL DEFAULT ''`,

	// nabiz'in çekip sakladığı dump dosyalarının kaydı.
	`CREATE TABLE IF NOT EXISTS dump_artifacts (
		id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		instance_id  UUID REFERENCES agent_instances(id) ON DELETE SET NULL,
		service_name TEXT NOT NULL DEFAULT '',
		kind         TEXT NOT NULL,
		filename     TEXT NOT NULL,
		bytes        BIGINT NOT NULL DEFAULT 0,
		status       TEXT NOT NULL DEFAULT 'pending',
		error        TEXT NOT NULL DEFAULT '',
		created_by   TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS dump_artifacts_created ON dump_artifacts (created_at DESC)`,

	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		expires_at TIMESTAMPTZ NOT NULL,
		user_agent TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id)`,
	`CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions (expires_at)`,
}
