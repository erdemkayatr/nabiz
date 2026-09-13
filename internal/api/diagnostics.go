package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// dumpFetchTimeout, bir dump'ın üretilip indirilmesi için tanınan süre.
// Bellek dump'ı yüzlerce MB olabiliyor; cömert ama sınırsız değil.
const dumpFetchTimeout = 10 * time.Minute

// registerDiagnostics, tanılama uçlarını bağlar.
func (s *Server) registerDiagnostics(mux *http.ServeMux) {
	// Agent kaydı: uygulamalar kendini tanıtır. OTLP alımıyla aynı güven
	// sınırında olduğu için oturum istemez; ama dump tetiklenebilmesi için
	// kaydın proje jetonuyla doğrulanmış olması gerekir.
	mux.HandleFunc("POST /api/v1/agents/register", s.handleAgentRegister)

	diag := func(h http.HandlerFunc) http.HandlerFunc {
		return requirePermission(identity.PermDiagnostics, h)
	}
	mux.HandleFunc("GET /api/v1/diagnostics/instances", diag(s.handleListInstances))
	mux.HandleFunc("GET /api/v1/diagnostics/artifacts", diag(s.handleListArtifacts))
	mux.HandleFunc("POST /api/v1/diagnostics/instances/{id}/capture", diag(s.handleCapture))
	mux.HandleFunc("GET /api/v1/diagnostics/artifacts/{id}/download", diag(s.handleDownloadArtifact))
	mux.HandleFunc("POST /api/v1/diagnostics/artifacts/{id}/cancel", diag(s.handleCancelCapture))
	mux.HandleFunc("GET /api/v1/diagnostics/artifacts/{id}", diag(s.handleGetArtifact))
	mux.HandleFunc("DELETE /api/v1/diagnostics/artifacts/{id}", diag(s.handleDeleteArtifact))

	// Projenin tanılama jetonu yalnızca yöneticiler tarafından girilir.
	mux.HandleFunc("PUT /api/v1/admin/projects/{id}/diagnostics-token",
		requirePermission(identity.PermAdmin, s.handleSetDiagToken))
}

func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceName    string `json:"serviceName"`
		InstanceID     string `json:"instanceId"`
		Hostname       string `json:"hostname"`
		Pod            string `json:"pod"`
		Namespace      string `json:"namespace"`
		PID            int    `json:"pid"`
		AgentVersion   string `json:"agentVersion"`
		AdvertisedHost string `json:"advertisedHost"`
		DiagPort       int    `json:"diagPort"`
		DiagPath       string `json:"diagPath"`
		DiagReady      bool   `json:"diagReady"`
		Token          string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.ServiceName == "" || req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, errors.New("serviceName ve instanceId zorunlu"))
		return
	}

	instance := identity.AgentInstance{
		ServiceName:  req.ServiceName,
		InstanceID:   req.InstanceID,
		Hostname:     req.Hostname,
		K8sPod:       req.Pod,
		K8sNamespace: req.Namespace,
		PID:          req.PID,
		AgentVersion: req.AgentVersion,
		// Agent'ın iddia ettiği adres değil, bağlantının geldiği adres.
		// Sahte bir kayıt nabiz'i başka bir hedefe yönlendirememeli.
		SourceIP:       clientIP(r),
		AdvertisedHost: req.AdvertisedHost,
		DiagPort:       req.DiagPort,
		DiagPath:       req.DiagPath,
		DiagReady:      req.DiagReady,
	}

	saved, err := s.identity.RegisterAgent(r.Context(), instance, req.Token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"id": saved.ID, "verified": saved.Verified})
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	sc := scopeFor(r)
	var services []string
	if !sc.unrestricted {
		if sc.empty() {
			writeJSON(w, map[string]any{"instances": []identity.AgentInstance{}})
			return
		}
		services = sc.services
	}
	instances, err := s.identity.ListAgents(r.Context(), services, 5*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"instances": instances})
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	sc := scopeFor(r)
	var services []string
	if !sc.unrestricted {
		if sc.empty() {
			writeJSON(w, map[string]any{"artifacts": []identity.DumpArtifact{}})
			return
		}
		services = sc.services
	}
	artifacts, err := s.identity.ListArtifacts(r.Context(), services, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	used, files := s.dumpUsage()
	writeJSON(w, map[string]any{
		"artifacts": artifacts,
		"storage": map[string]any{
			"directory":     s.dumpDir,
			"usedBytes":     used,
			"quotaBytes":    s.retention.MaxBytes,
			"files":         files,
			"retentionDays": int(s.retention.MaxAge.Hours() / 24),
		},
	})
}

// handleCapture, uygulamadan dump ister ve dosyayı nabiz'e çeker.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		Seconds int    `json:"seconds"`
		Type    string `json:"type"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Kind != "cpu" && req.Kind != "memory" {
		writeError(w, http.StatusBadRequest, errors.New("kind 'cpu' ya da 'memory' olmalı"))
		return
	}

	instance, token, err := s.identity.GetAgent(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, identity.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, "örnek bulunamadı", "not_found")
		return
	case errors.Is(err, identity.ErrTokenMismatch):
		writeErrorCode(w, http.StatusPreconditionFailed,
			"bu örnek doğrulanmadı: projeye tanılama jetonu tanımlayın ve uygulamayı yeniden başlatın",
			"unverified")
		return
	case errors.Is(err, identity.ErrNoSecretKey):
		writeErrorCode(w, http.StatusPreconditionFailed,
			"NABIZ_SECRET_KEY tanımlı değil: jeton saklanamıyor", "no_secret_key")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	access := accessFrom(r)
	if !scopeFor(r).allows(instance.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "bu servise erişiminiz yok", "forbidden")
		return
	}

	artifactID, err := s.identity.CreateArtifact(r.Context(),
		instance.ID, instance.ServiceName, req.Kind, access.User.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.log.Info("dump isteniyor",
		"kind", req.Kind, "service", instance.ServiceName, "pod", instance.K8sPod,
		"instance", instance.InstanceID, "by", access.User.Email)

	// İstemciyi bekletmiyoruz: bellek dump'ı dakikalar sürebilir ve tarayıcı
	// zaman aşımına uğrar. Kayıt "pending" olarak döner, arayüz listeyi
	// tazeleyerek sonucu görür.
	go s.fetchDump(instance, token, req.Kind, req.Seconds, req.Type, artifactID, access.User.Email)

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"artifactId": artifactID,
		"status":     "pending",
	})
}

// fetchDump, uygulamadan dump'ı üretmesini ister ve dosyayı diske çeker.
func (s *Server) fetchDump(instance *identity.AgentInstance, token, kind string,
	seconds int, dumpType, artifactID, by string) {

	ctx, cancel := context.WithTimeout(context.Background(), dumpFetchTimeout)
	defer cancel()

	// Durdurma isteği bu bekleyişi kesebilsin diye iptal fonksiyonunu
	// kaydediyoruz. İş bittiğinde kayıt silinir; aksi halde tamamlanmış
	// işlerin iptal fonksiyonları haritada birikir.
	s.inflightMu.Lock()
	s.inflight[artifactID] = cancel
	s.inflightMu.Unlock()
	defer func() {
		s.inflightMu.Lock()
		delete(s.inflight, artifactID)
		s.inflightMu.Unlock()
	}()

	fail := func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		// İptal edilmiş bir işi "başarısız" diye raporlamak yanıltıcı olur:
		// kullanıcı bilerek durdurdu. Context iptal edildiği için sonraki
		// veritabanı yazması da ondan bağımsız olmalı.
		clean := context.WithoutCancel(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			s.log.Info("dump durduruldu", "artifact", artifactID)
			_ = s.identity.CancelArtifact(clean, artifactID)
			return
		}
		s.log.Error("dump alınamadı", "artifact", artifactID, "err", message)
		_ = s.identity.FailArtifact(clean, artifactID, message)
	}

	base := instance.DiagPath
	if base == "" {
		base = "/nabiz/diag"
	}
	// Doğrulanmış bir kayıt kendi adresini bildirebilir: jetonu bildiğini
	// kanıtlamış taraftır. Bildirmediyse kaydın geldiği IP kullanılır.
	// Doğrulanmamış kayıtlar buraya zaten ulaşamaz (GetAgent reddediyor).
	host := instance.SourceIP
	if instance.AdvertisedHost != "" {
		host = instance.AdvertisedHost
	}
	root := fmt.Sprintf("http://%s%s", net.JoinHostPort(host,
		strconv.Itoa(instance.DiagPort)), base)

	query := ""
	if kind == "cpu" {
		if seconds <= 0 {
			seconds = 20
		}
		query = "?seconds=" + strconv.Itoa(seconds)
	} else if dumpType != "" {
		query = "?type=" + dumpType
	}

	client := &http.Client{Timeout: dumpFetchTimeout}

	// 1) Üretmesini iste.
	var created struct {
		ID    string `json:"id"`
		Bytes int64  `json:"bytes"`
		Error string `json:"error"`
	}
	if err := doJSON(ctx, client, http.MethodPost, root+"/"+kind+query, token, &created); err != nil {
		fail("%s", explainFetchError(err, host, instance.DiagPort))
		return
	}
	if created.ID == "" {
		fail("uygulama dosya adı döndürmedi: %s", created.Error)
		return
	}

	// 2) Dosyayı çek.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/"+created.ID, nil)
	if err != nil {
		fail("indirme isteği kurulamadı: %v", err)
		return
	}
	req.Header.Set("X-Nabiz-Token", token)
	resp, err := client.Do(req)
	if err != nil {
		fail("%s", explainFetchError(err, host, instance.DiagPort))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		fail("uygulamada indirme kapalı (diagnostics.allowDownload = false); dosya %s üzerinde kaldı", instance.K8sPod)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fail("indirme başarısız: HTTP %d", resp.StatusCode)
		return
	}

	filename := fmt.Sprintf("%s_%s_%s", instance.ServiceName, artifactID[:8], created.ID)
	path := filepath.Join(s.dumpDir, filename)
	file, err := os.Create(path)
	if err != nil {
		fail("dosya oluşturulamadı: %v", err)
		return
	}

	// Akış halinde yazılır: 500 MB'lık bir dump'ı belleğe almak nabiz'i
	// izlediği sistemden önce düşürürdü.
	written, err := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		fail("dosya yazılamadı: %v", err)
		return
	}
	if closeErr != nil {
		fail("dosya kapatılamadı: %v", closeErr)
		return
	}

	if err := s.identity.CompleteArtifact(ctx, artifactID, filename, written); err != nil {
		s.log.Error("dump kaydı güncellenemedi", "artifact", artifactID, "err", err)
	}
	s.log.Info("dump alındı", "artifact", artifactID, "service", instance.ServiceName,
		"bytes", written, "by", by)
}

// handleCancelCapture, koşan bir dump işlemini durdurur.
//
// İki taraflı: nabiz kendi beklemesini keser, ayrıca uygulamaya da durdurma
// isteği gönderir. CPU profili gerçekten kesilir; bellek dump'ı runtime
// yazmaya başladıysa kesilemez ve yanıt bunu açıkça söyler.
func (s *Server) handleCancelCapture(w http.ResponseWriter, r *http.Request) {
	artifactID := r.PathValue("id")
	artifact, err := s.identity.GetArtifact(r.Context(), artifactID)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !scopeFor(r).allows(artifact.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "bu servise erişiminiz yok", "forbidden")
		return
	}

	if artifact.Status != "pending" {
		writeErrorCode(w, http.StatusConflict, "bu iş zaten bitmiş", "not_running")
		return
	}

	reached, agentStopped, reason := s.askAgentToStop(r.Context(), artifact)

	// Kendi beklememizi yalnızca uygulamaya ulaşamadığımızda kesiyoruz.
	//
	// Uygulama CPU profilini kesebildiyse elinde o ana kadarki örneklerle
	// geçerli bir dosya var; beklemeyi bırakırsak kullanıcının topladığı
	// veriyi çöpe atmış oluruz. Bellek dump'ı kesilemiyorsa da beklemek
	// doğrusu: süreç dosyayı yazmaya devam ediyor, kaydı "iptal" diye
	// işaretlemek gerçeğe aykırı olurdu.
	local := false
	if !reached {
		s.inflightMu.Lock()
		cancel, running := s.inflight[artifactID]
		s.inflightMu.Unlock()
		if running {
			cancel()
			local = true
		}
		reason = "uygulamaya ulaşılamadı; nabiz beklemeyi bıraktı"
	}

	s.log.Info("dump durdurma isteği", "artifact", artifactID,
		"ulasildi", reached, "agent_durdu", agentStopped, "yerel", local,
		"by", accessFrom(r).User.Email)

	writeJSON(w, map[string]any{
		"cancelled":    agentStopped || local,
		"agentStopped": agentStopped,
		"partial":      agentStopped,
		"reason":       reason,
	})
}

// askAgentToStop, uygulamaya durdurma isteği gönderir.
func (s *Server) askAgentToStop(ctx context.Context, artifact *identity.DumpArtifact) (reached, stopped bool, reason string) {
	if artifact.InstanceID == "" {
		return false, false, ""
	}
	instance, token, err := s.identity.GetAgent(ctx, artifact.InstanceID)
	if err != nil {
		return false, false, ""
	}
	host := instance.SourceIP
	if instance.AdvertisedHost != "" {
		host = instance.AdvertisedHost
	}
	base := instance.DiagPath
	if base == "" {
		base = "/nabiz/diag"
	}
	url := fmt.Sprintf("http://%s%s/cancel",
		net.JoinHostPort(host, strconv.Itoa(instance.DiagPort)), base)

	var result struct {
		Cancelled bool   `json:"cancelled"`
		Reason    string `json:"reason"`
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if err := doJSON(ctx, client, http.MethodPost, url, token, &result); err != nil {
		s.log.Warn("durdurma isteği uygulamaya ulaşmadı", "artifact", artifact.ID, "err", err)
		return false, false, ""
	}
	return true, result.Cancelled, result.Reason
}

// handleGetArtifact, tek kaydın durumunu verir; arayüz ilerleme penceresini
// bununla tazeler.
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	artifact, err := s.identity.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !scopeFor(r).allows(artifact.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "bu servise erişiminiz yok", "forbidden")
		return
	}
	writeJSON(w, artifact)
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	artifact, err := s.identity.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !scopeFor(r).allows(artifact.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "bu servise erişiminiz yok", "forbidden")
		return
	}
	if artifact.Status != "ready" {
		writeErrorCode(w, http.StatusConflict, "dosya henüz hazır değil", "not_ready")
		return
	}

	path := filepath.Join(s.dumpDir, filepath.Base(artifact.Filename))
	file, err := os.Open(path)
	if err != nil {
		writeErrorCode(w, http.StatusNotFound, "dosya diskte bulunamadı", "missing_file")
		return
	}
	defer file.Close()

	s.log.Info("dump indiriliyor", "artifact", artifact.ID, "by", accessFrom(r).User.Email)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+artifact.Filename+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Bytes, 10))
	_, _ = io.Copy(w, file)
}

func (s *Server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	artifact, err := s.identity.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !scopeFor(r).allows(artifact.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "bu servise erişiminiz yok", "forbidden")
		return
	}
	if artifact.Filename != "" {
		_ = os.Remove(filepath.Join(s.dumpDir, filepath.Base(artifact.Filename)))
	}
	if err := s.identity.DeleteArtifact(r.Context(), artifact.ID); err != nil {
		writeIdentityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetDiagToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err := s.identity.SetProjectDiagnosticsToken(
		r.Context(), r.PathValue("id"), req.Token, accessFrom(r).User.Email)
	if errors.Is(err, identity.ErrNoSecretKey) {
		writeErrorCode(w, http.StatusPreconditionFailed,
			"NABIZ_SECRET_KEY tanımlı değil: jeton şifrelenemediği için saklanmıyor", "no_secret_key")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// explainFetchError, ham ağ hatasını operatörün ne yapacağını söyleyen bir
// mesaja çevirir. "dial tcp 192.168.65.1:5199: connect: connection refused"
// doğru ama işe yaramaz bir cümle.
func explainFetchError(err error, host string, port int) string {
	text := err.Error()
	target := fmt.Sprintf("%s:%d", host, port)

	switch {
	case strings.Contains(text, "connection refused"):
		return fmt.Sprintf("uygulamaya ulaşılamadı (%s): örnek kapanmış olabilir "+
			"ya da tanılama ucu bu portta dinlemiyor", target)
	case strings.Contains(text, "no such host"):
		return fmt.Sprintf("adres çözümlenemedi (%s): advertisedHost yanlış olabilir", target)
	case strings.Contains(text, "i/o timeout") || strings.Contains(text, "context deadline exceeded"):
		return fmt.Sprintf("uygulama yanıt vermedi (%s): ağ erişimi engelli olabilir "+
			"ya da dump beklenenden uzun sürdü", target)
	case strings.Contains(text, "HTTP 401"):
		return "uygulama jetonu reddetti: projedeki tanılama jetonu uygulamadaki " +
			"nabiz.json değeriyle aynı mı?"
	case strings.Contains(text, "HTTP 404"):
		return fmt.Sprintf("tanılama ucu bulunamadı (%s): uygulamada "+
			"Nabiz.Agent.Diagnostics kurulu ve diagnostics.enabled açık mı?", target)
	default:
		return "uygulamadan dump alınamadı: " + text
	}
}

// doJSON, jetonlu bir istek atıp JSON yanıtı çözer.
func doJSON(ctx context.Context, client *http.Client, method, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Nabiz-Token", token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return jsonUnmarshal(body, out)
}
