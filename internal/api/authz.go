package api

import (
	"net/http"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// scope, bir isteğin görebileceği uygulama kümesidir.
//
// Yetkilendirme tek noktada bu tipe indirgenir; sorgu yazan her uç aynı
// filtreyi kullanır. "Şu uçta filtreyi eklemeyi unuttuk" hatası, ancak
// filtrenin tek bir yerden gelmesiyle engellenebilir.
type scope struct {
	// unrestricted, süper yönetici için doğrudur: filtre uygulanmaz.
	unrestricted bool
	// services, görülebilir servis adları. unrestricted false ve liste boşsa
	// kullanıcı hiçbir şey göremez.
	services []string
}

// scopeFor, istekten yetkilendirme kapsamını çıkarır.
//
// `project` sorgu parametresi verilirse kapsam o projeye daraltılır; verilen
// proje kullanıcının erişebildikleri arasında değilse boş kapsam döner.
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
	// Erişilemeyen bir proje istendi: hata yerine boş sonuç. Var olmayan ve
	// erişilemeyen proje aynı yanıtı vermeli, aksi halde uç bir proje
	// listeleyicisine dönüşür.
	return scope{}
}

// empty, kapsamın hiçbir veri döndürmeyeceğini söyler.
func (s scope) empty() bool { return !s.unrestricted && len(s.services) == 0 }

// allows, tek bir servisin kapsamda olup olmadığını söyler.
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

// filterServiceName, service_name kolonu taşıyan tablolar için WHERE eki
// üretir (spans, operation_stats).
func (s scope) filterServiceName(column string) (string, []any) {
	if s.unrestricted {
		return "", nil
	}
	return " AND " + column + " IN ?", []any{s.services}
}

// filterTopology, service_edges tablosu için WHERE eki üretir.
//
// Kenarın uçları "namespace/servis" biçiminde olabildiği için son parça
// alınır. Kenarın bir ucu bile kapsamdaysa gösterilir: bir projenin kendi
// servisini kimin çağırdığını görmek, o projenin sahibinin hakkı.
func (s scope) filterTopology() (string, []any) {
	if s.unrestricted {
		return "", nil
	}
	return " AND (" +
			"arrayElement(splitByChar('/', client), -1) IN ?" +
			" OR arrayElement(splitByChar('/', server), -1) IN ?)",
		[]any{s.services, s.services}
}

// filterTraces, trace_index tablosu için WHERE eki üretir.
func (s scope) filterTraces() (string, []any) {
	if s.unrestricted {
		return "", nil
	}
	return " AND hasAny(services, ?)", []any{s.services}
}

// permissionFor, uç bazında gereken yetkiyi verir.
var (
	permTopology = identity.PermTopologyRead
	permServices = identity.PermServicesRead
	permTraces   = identity.PermTracesRead
)
