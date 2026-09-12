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

// requestWith, verilen yetkilendirme bağlamına sahip bir istek üretir.
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

// Oturumsuz bir istek hiçbir şey görmemeli. Kapsamın varsayılanı "her şey"
// değil "hiçbir şey" olmak zorunda.
func TestScopeWithoutSessionIsEmpty(t *testing.T) {
	sc := scopeFor(requestWith(nil, "/api/v1/services"))
	if !sc.empty() {
		t.Fatal("oturumsuz istek boş kapsam almalı")
	}
	if sc.unrestricted {
		t.Fatal("oturumsuz istek sınırsız kapsam aldı")
	}
}

func TestSuperAdminIsUnrestricted(t *testing.T) {
	sc := scopeFor(requestWith(adminAccess(), "/api/v1/services"))
	if !sc.unrestricted {
		t.Fatal("süper yönetici sınırsız olmalı")
	}
	if sc.empty() {
		t.Fatal("süper yöneticinin kapsamı boş görünüyor")
	}
	if f, _ := sc.filterServiceName("service_name"); f != "" {
		t.Errorf("süper yönetici için filtre üretildi: %q", f)
	}
}

func TestScopeLimitsToAssignedApplications(t *testing.T) {
	sc := scopeFor(requestWith(userAccess([]string{"api", "worker"}), "/api/v1/services"))
	if sc.unrestricted {
		t.Fatal("normal kullanıcı sınırsız kapsam aldı")
	}
	if !sc.allows("api") || !sc.allows("worker") {
		t.Error("atanmış uygulamalar kapsam dışı kaldı")
	}
	if sc.allows("gizli-servis") {
		t.Error("atanmamış uygulama kapsama girdi")
	}
}

func TestProjectlessUserSeesNothing(t *testing.T) {
	sc := scopeFor(requestWith(userAccess(nil), "/api/v1/services"))
	if !sc.empty() {
		t.Fatal("hiçbir projeye atanmamış kullanıcı veri görebiliyor")
	}
}

// Proje filtresi kapsamı daraltmalı, genişletmemeli.
func TestProjectParameterNarrowsScope(t *testing.T) {
	access := userAccess([]string{"api", "worker"},
		identity.Project{Key: "alfa", Applications: []string{"api"}},
		identity.Project{Key: "beta", Applications: []string{"worker"}})

	sc := scopeFor(requestWith(access, "/api/v1/services?project=alfa"))
	if !sc.allows("api") || sc.allows("worker") {
		t.Errorf("proje filtresi daraltmadı: %v", sc.services)
	}
}

// Erişilemeyen bir proje istendiğinde hata değil boş sonuç dönmeli: aksi
// halde uç, var olan projeleri saymaya yarayan bir araca dönüşür.
func TestUnknownProjectYieldsEmptyScopeNotError(t *testing.T) {
	access := userAccess([]string{"api"}, identity.Project{Key: "alfa", Applications: []string{"api"}})
	sc := scopeFor(requestWith(access, "/api/v1/services?project=baskasinin-projesi"))
	if !sc.empty() {
		t.Fatal("erişilemeyen proje için veri döndü")
	}
}

func TestServiceNameFilterBindsServices(t *testing.T) {
	sc := scope{services: []string{"api", "worker"}}
	clause, args := sc.filterServiceName("service_name")
	if !strings.Contains(clause, "service_name IN ?") {
		t.Errorf("beklenmeyen filtre: %q", clause)
	}
	if len(args) != 1 {
		t.Fatalf("argüman sayısı %d, 1 bekleniyordu", len(args))
	}
	if svcs, ok := args[0].([]string); !ok || len(svcs) != 2 {
		t.Errorf("servis listesi bağlanmadı: %#v", args[0])
	}
}

// Topoloji filtresi "namespace/servis" biçimindeki uçları da eşleştirmeli.
func TestTopologyFilterMatchesNamespacedEndpoints(t *testing.T) {
	sc := scope{services: []string{"api"}}
	clause, args := sc.filterTopology()
	if !strings.Contains(clause, "splitByChar('/', client)") ||
		!strings.Contains(clause, "splitByChar('/', server)") {
		t.Errorf("kenarın iki ucu da filtrelenmiyor: %q", clause)
	}
	if !strings.Contains(clause, " OR ") {
		t.Error("kenarın yalnızca bir ucu kapsamdaysa da görünmeli")
	}
	if len(args) != 2 {
		t.Errorf("argüman sayısı %d, 2 bekleniyordu", len(args))
	}
}

func TestTraceFilterUsesHasAny(t *testing.T) {
	sc := scope{services: []string{"api"}}
	clause, args := sc.filterTraces()
	if !strings.Contains(clause, "hasAny(services, ?)") {
		t.Errorf("beklenmeyen trace filtresi: %q", clause)
	}
	if len(args) != 1 {
		t.Errorf("argüman sayısı %d, 1 bekleniyordu", len(args))
	}
}

// --- yetki mantığı ---

func TestSuperAdminCanDoEverything(t *testing.T) {
	a := adminAccess()
	for _, p := range identity.AllPermissions {
		if !a.Can(p) {
			t.Errorf("süper yönetici %s yetkisini alamadı", p)
		}
	}
}

func TestPermissionsComeOnlyFromRoleGroups(t *testing.T) {
	a := userAccess(nil)
	if !a.Can(identity.PermTopologyRead) {
		t.Error("gruptan gelen yetki tanınmadı")
	}
	if a.Can(identity.PermAdmin) {
		t.Error("verilmemiş yönetici yetkisi tanındı")
	}
	if a.Can(identity.PermTracesRead) {
		t.Error("verilmemiş trace yetkisi tanındı")
	}
}

func TestNilAccessCanDoNothing(t *testing.T) {
	var a *identity.Access
	if a.Can(identity.PermTopologyRead) {
		t.Error("nil yetkilendirme bağlamı yetki verdi")
	}
	if a.SeesEverything() {
		t.Error("nil yetkilendirme bağlamı her şeyi görebiliyor")
	}
}

// Bilinmeyen yetki adları veritabanına yazılmamalı.
func TestUnknownPermissionsRejected(t *testing.T) {
	if identity.ValidPermission("bir.sey.uydurma") {
		t.Error("uydurma yetki geçerli sayıldı")
	}
	for _, p := range identity.AllPermissions {
		if !identity.ValidPermission(p) {
			t.Errorf("%s geçersiz sayıldı", p)
		}
	}
}

// --- giriş deneme sınırlayıcı ---

func TestLoginLimiterBlocksAfterMax(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow("ip|mail") {
			t.Fatalf("%d. deneme engellendi, sınır 3", i+1)
		}
	}
	if l.allow("ip|mail") {
		t.Error("sınır aşıldığı halde denemeye izin verildi")
	}
	// Farklı kaynak etkilenmemeli.
	if !l.allow("baska-ip|mail") {
		t.Error("bir kaynağın sınırı diğerini de kilitledi")
	}
	// Başarılı giriş sayacı temizlemeli.
	l.clear("ip|mail")
	if !l.allow("ip|mail") {
		t.Error("başarılı girişten sonra sayaç temizlenmedi")
	}
}
