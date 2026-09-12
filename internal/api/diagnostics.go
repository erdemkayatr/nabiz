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
	writeJSON(w, map[string]any{"artifacts": artifacts, "storage": s.dumpDir})
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

	fail := func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		s.log.Error("dump alınamadı", "artifact", artifactID, "err", message)
		_ = s.identity.FailArtifact(ctx, artifactID, message)
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
		fail("uygulamadan dump istenemedi: %v", err)
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
		fail("dosya indirilemedi: %v", err)
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
