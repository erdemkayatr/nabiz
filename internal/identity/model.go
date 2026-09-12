// Package identity, nabiz'in denetim düzlemini tutar: kullanıcılar, rol
// grupları, projeler ve uygulama atamaları.
//
// Bu veri ClickHouse'a ait değil. Telemetri tablolarının tamamı append-only
// ve nihai tutarlı; kimlik verisi ise benzersizlik kısıtı, transaction ve
// yerinde güncelleme ister. Bu yüzden denetim düzlemi ayrı bir PostgreSQL'de
// durur.
package identity

import (
	"strings"
	"time"
)

// Permission, bir rol grubunun verebileceği yetki.
type Permission string

const (
	// PermTopologyRead, projenin topoloji grafiğini görmeyi sağlar.
	PermTopologyRead Permission = "topology.read"
	// PermServicesRead, RED metriklerini görmeyi sağlar.
	PermServicesRead Permission = "services.read"
	// PermTracesRead, trace arama ve şelale görünümünü açar.
	PermTracesRead Permission = "traces.read"
	// PermProjectManage, projeye uygulama ekleyip çıkarmayı sağlar.
	PermProjectManage Permission = "project.manage"
	// PermDiagnostics, uygulamalardan CPU profili ve bellek dump'ı almayı
	// sağlar. Ayrı bir yetki: bellek dump'ı bağlantı dizesi, jeton ve müşteri
	// verisi içerdiği için trace okumakla aynı şey değildir.
	PermDiagnostics Permission = "diagnostics.manage"
	// PermAdmin, denetim düzleminin tamamını yönetir: kullanıcı, rol grubu,
	// proje. Yalnızca sistem yöneticilerine verilir.
	PermAdmin Permission = "admin.manage"
)

// AllPermissions, arayüzün yetki seçicisini doldurmak için.
var AllPermissions = []Permission{
	PermTopologyRead, PermServicesRead, PermTracesRead, PermProjectManage,
	PermDiagnostics, PermAdmin,
}

// ValidPermission, bilinmeyen yetkilerin veritabanına sızmasını engeller.
func ValidPermission(p Permission) bool {
	for _, known := range AllPermissions {
		if known == p {
			return true
		}
	}
	return false
}

// User, sisteme giren kişi.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	IsSuperAdmin bool       `json:"isSuperAdmin"`
	IsActive     bool       `json:"isActive"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`

	// Yalnızca liste uçlarında doldurulur.
	RoleGroups []RoleGroupRef `json:"roleGroups,omitempty"`
}

// RoleGroupRef, kullanıcı listesinde rol grubunu özetler.
type RoleGroupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RoleGroup, yetki kümesi taşıyan kullanıcı grubu. Yetki doğrudan kullanıcıya
// değil gruba verilir; projeye bağlanan da gruptur.
type RoleGroup struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	CreatedAt   time.Time    `json:"createdAt"`

	MemberCount  int `json:"memberCount"`
	ProjectCount int `json:"projectCount"`
}

// Project, uygulamaların ve erişim yetkisinin toplandığı birim.
type Project struct {
	ID          string    `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`

	Applications []string       `json:"applications,omitempty"`
	RoleGroups   []RoleGroupRef `json:"roleGroups,omitempty"`
}

// AgentInstance, telemetri gönderen ve kendini tanıtan bir uygulama örneği.
type AgentInstance struct {
	ID           string `json:"id"`
	ServiceName  string `json:"serviceName"`
	InstanceID   string `json:"instanceId"`
	Hostname     string `json:"hostname,omitempty"`
	K8sPod       string `json:"pod,omitempty"`
	K8sNamespace string `json:"namespace,omitempty"`
	PID          int    `json:"pid"`
	AgentVersion string `json:"agentVersion,omitempty"`

	// DiagPort ve DiagPath, tanılama ucunun yeri. Host bilerek saklanmaz:
	// nabiz her zaman kaydın geldiği IP'yi kullanır, agent'ın iddia ettiği
	// adresi değil. Aksi halde sahte bir kayıt, nabiz'i jetonu saldırganın
	// adresine göndermeye ikna edebilirdi.
	SourceIP string `json:"sourceIp"`
	// AdvertisedHost, agent'ın bildirdiği adres. YALNIZCA doğrulanmış
	// kayıtlarda kullanılır: jetonu bilen taraf kimliğini zaten kanıtlamıştır.
	// Doğrulanmamış kayıtlarda her zaman SourceIP kullanılır, aksi halde
	// sahte bir kayıt nabiz'i başka bir hedefe yönlendirebilirdi.
	AdvertisedHost string `json:"advertisedHost,omitempty"`
	DiagPort       int    `json:"diagPort"`
	DiagPath       string `json:"diagPath"`
	DiagReady      bool   `json:"diagReady"`

	// Verified, kaydın proje jetonuyla doğrulandığını söyler. Doğrulanmamış
	// örneklerde dump tetiklenemez.
	Verified  bool      `json:"verified"`
	ProjectID string    `json:"projectId,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// DumpArtifact, nabiz'in bir uygulamadan çekip sakladığı dosya.
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

// Session, giriş yapmış bir tarayıcı oturumu.
type Session struct {
	UserID    string
	ExpiresAt time.Time
}

// Access, bir isteğin yetkilendirme bağlamıdır: kullanıcının hangi
// uygulamaların verisini görebildiği ve neler yapabildiği.
//
// Sorgu uçları ham kullanıcı yerine bunu kullanır; yetki kararı tek yerde
// hesaplanır.
type Access struct {
	User        *User
	Permissions map[Permission]bool
	// Projects, kullanıcının rol grupları üzerinden eriştiği projeler.
	Projects []Project
	// Applications, erişilebilen servis adlarının birleşimi.
	Applications []string
}

// Can, yetkiyi sorar. Süper yönetici her şeyi yapabilir.
func (a *Access) Can(p Permission) bool {
	if a == nil || a.User == nil {
		return false
	}
	if a.User.IsSuperAdmin {
		return true
	}
	return a.Permissions[p]
}

// SeesEverything, telemetri sorgularının filtresiz çalışıp çalışmayacağını
// söyler. Yalnızca süper yöneticiler için doğrudur.
func (a *Access) SeesEverything() bool {
	return a != nil && a.User != nil && a.User.IsSuperAdmin
}

// NormalizeServiceName, kullanıcıdan gelen uygulama adını temizler.
func NormalizeServiceName(s string) string {
	return strings.TrimSpace(s)
}
