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

// dumpFetchTimeout is how long a dump has to be produced and downloaded.
// A memory dump can be hundreds of megabytes: generous, but not unbounded.
const dumpFetchTimeout = 10 * time.Minute

// registerDiagnostics wires up the diagnostics endpoints.
func (s *Server) registerDiagnostics(mux *http.ServeMux) {
	// Agent registration: applications announce themselves. It sits in the
	// same trust boundary as OTLP ingest, so it needs no session; but a
	// registration has to be verified against the project token before a dump
	// can be triggered against it.
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

	// A project's diagnostics token is entered by administrators only.
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
		writeError(w, http.StatusBadRequest, errors.New("serviceName and instanceId are required"))
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
		// The address the connection came from, not the one the agent claims.
		// A forged registration must not be able to redirect nabiz elsewhere.
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

// handleCapture asks an application for a dump and pulls the file into nabiz.
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
		writeError(w, http.StatusBadRequest, errors.New("kind must be 'cpu' or 'memory'"))
		return
	}

	instance, token, err := s.identity.GetAgent(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, identity.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, "instance not found", "not_found")
		return
	case errors.Is(err, identity.ErrTokenMismatch):
		writeErrorCode(w, http.StatusPreconditionFailed,
			"this instance is not verified: set a diagnostics token on the project and restart the application",
			"unverified")
		return
	case errors.Is(err, identity.ErrNoSecretKey):
		writeErrorCode(w, http.StatusPreconditionFailed,
			"NABIZ_SECRET_KEY is not set: the token cannot be stored", "no_secret_key")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	access := accessFrom(r)
	if !scopeFor(r).allows(instance.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "you do not have access to this service", "forbidden")
		return
	}

	artifactID, err := s.identity.CreateArtifact(r.Context(),
		instance.ID, instance.ServiceName, req.Kind, access.User.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.log.Info("requesting dump",
		"kind", req.Kind, "service", instance.ServiceName, "pod", instance.K8sPod,
		"instance", instance.InstanceID, "by", access.User.Email)

	// The client is not kept waiting: a memory dump can take minutes and the
	// browser would time out. The record comes back as "pending" and the UI
	// polls for the result.
	go s.fetchDump(instance, token, req.Kind, req.Seconds, req.Type, artifactID, access.User.Email)

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"artifactId": artifactID,
		"status":     "pending",
	})
}

// fetchDump asks the application to produce the dump and pulls the file to disk.
func (s *Server) fetchDump(instance *identity.AgentInstance, token, kind string,
	seconds int, dumpType, artifactID, by string) {

	ctx, cancel := context.WithTimeout(context.Background(), dumpFetchTimeout)
	defer cancel()

	// The cancel function is registered so a stop request can interrupt this
	// wait. The entry is removed when the job ends; otherwise the cancel
	// functions of finished jobs would pile up in the map.
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
		// Reporting a cancelled job as "failed" would be misleading: the user
		// stopped it deliberately. The context is cancelled, so the database
		// write that follows has to be independent of it.
		clean := context.WithoutCancel(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			s.log.Info("dump stopped", "artifact", artifactID)
			_ = s.identity.CancelArtifact(clean, artifactID)
			return
		}
		s.log.Error("dump failed", "artifact", artifactID, "err", message)
		_ = s.identity.FailArtifact(clean, artifactID, message)
	}

	base := instance.DiagPath
	if base == "" {
		base = "/nabiz/diag"
	}
	// A verified registration may report its own address: it has already proven
	// that it knows the token. If it reports none, the IP the registration came
	// from is used. Unverified registrations never reach this point anyway —
	// GetAgent rejects them.
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

	// 1) Ask it to produce the file.
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
		fail("the application returned no filename: %s", created.Error)
		return
	}

	// 2) Fetch the file.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/"+created.ID, nil)
	if err != nil {
		fail("could not build the download request: %v", err)
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
		fail("downloading is disabled in the application (diagnostics.allowDownload = false); the file stayed on %s", instance.K8sPod)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fail("download failed: HTTP %d", resp.StatusCode)
		return
	}

	filename := fmt.Sprintf("%s_%s_%s", instance.ServiceName, artifactID[:8], created.ID)
	path := filepath.Join(s.dumpDir, filename)
	file, err := os.Create(path)
	if err != nil {
		fail("could not create the file: %v", err)
		return
	}

	// Written as a stream: buffering a 500 MB dump in memory would bring nabiz
	// down before the system it monitors.
	written, err := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		fail("could not write the file: %v", err)
		return
	}
	if closeErr != nil {
		fail("could not close the file: %v", closeErr)
		return
	}

	if err := s.identity.CompleteArtifact(ctx, artifactID, filename, written); err != nil {
		s.log.Error("could not update the dump record", "artifact", artifactID, "err", err)
	}
	s.log.Info("dump collected", "artifact", artifactID, "service", instance.ServiceName,
		"bytes", written, "by", by)
}

// handleCancelCapture stops a running dump job.
//
// It works from both ends: nabiz can interrupt its own wait, and it also sends
// a stop request to the application. A CPU profile really is interrupted; a
// memory dump cannot be once the runtime has started writing, and the response
// says so plainly.
func (s *Server) handleCancelCapture(w http.ResponseWriter, r *http.Request) {
	artifactID := r.PathValue("id")
	artifact, err := s.identity.GetArtifact(r.Context(), artifactID)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !scopeFor(r).allows(artifact.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "you do not have access to this service", "forbidden")
		return
	}

	if artifact.Status != "pending" {
		writeErrorCode(w, http.StatusConflict, "this job has already finished", "not_running")
		return
	}

	reached, agentStopped, reason := s.askAgentToStop(r.Context(), artifact)

	// Our own wait is interrupted only when the application cannot be reached.
	//
	// If the application managed to stop the CPU profile, it holds a valid file
	// with the samples collected so far; giving up the wait would throw away
	// the data the user collected. When a memory dump cannot be interrupted,
	// waiting is also the right answer: the process is still writing the file,
	// and marking the record "cancelled" would not be true.
	local := false
	if !reached {
		s.inflightMu.Lock()
		cancel, running := s.inflight[artifactID]
		s.inflightMu.Unlock()
		if running {
			cancel()
			local = true
		}
		reason = "the application could not be reached; nabiz stopped waiting"
	}

	s.log.Info("dump stop requested", "artifact", artifactID,
		"reached", reached, "agent_stopped", agentStopped, "local", local,
		"by", accessFrom(r).User.Email)

	writeJSON(w, map[string]any{
		"cancelled":    agentStopped || local,
		"agentStopped": agentStopped,
		"partial":      agentStopped,
		"reason":       reason,
	})
}

// askAgentToStop sends a stop request to the application.
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
		s.log.Warn("the stop request never reached the application", "artifact", artifact.ID, "err", err)
		return false, false, ""
	}
	return true, result.Cancelled, result.Reason
}

// handleGetArtifact returns one record's status; the UI refreshes the progress
// dialog from it.
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	artifact, err := s.identity.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !scopeFor(r).allows(artifact.ServiceName) {
		writeErrorCode(w, http.StatusForbidden, "you do not have access to this service", "forbidden")
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
		writeErrorCode(w, http.StatusForbidden, "you do not have access to this service", "forbidden")
		return
	}
	if artifact.Status != "ready" {
		writeErrorCode(w, http.StatusConflict, "the file is not ready yet", "not_ready")
		return
	}

	path := filepath.Join(s.dumpDir, filepath.Base(artifact.Filename))
	file, err := os.Open(path)
	if err != nil {
		writeErrorCode(w, http.StatusNotFound, "the file was not found on disk", "missing_file")
		return
	}
	defer file.Close()

	s.log.Info("dump being downloaded", "artifact", artifact.ID, "by", accessFrom(r).User.Email)
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
		writeErrorCode(w, http.StatusForbidden, "you do not have access to this service", "forbidden")
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
			"NABIZ_SECRET_KEY is not set: the token cannot be encrypted, so it is not stored", "no_secret_key")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// explainFetchError turns a raw network error into a message that tells the
// operator what to do. "dial tcp 192.168.65.1:5199: connect: connection
// refused" is accurate and useless.
func explainFetchError(err error, host string, port int) string {
	text := err.Error()
	target := fmt.Sprintf("%s:%d", host, port)

	switch {
	case strings.Contains(text, "connection refused"):
		return fmt.Sprintf("could not reach the application (%s): the instance may "+
			"be gone, or the diagnostics endpoint is not listening on this port", target)
	case strings.Contains(text, "no such host"):
		return fmt.Sprintf("could not resolve the address (%s): advertisedHost may be wrong", target)
	case strings.Contains(text, "i/o timeout") || strings.Contains(text, "context deadline exceeded"):
		return fmt.Sprintf("the application did not respond (%s): network access may be "+
			"blocked, or the dump took longer than expected", target)
	case strings.Contains(text, "HTTP 401"):
		return "the application rejected the token: does the project's diagnostics " +
			"token match the value in the application's nabiz.json?"
	case strings.Contains(text, "HTTP 404"):
		return fmt.Sprintf("diagnostics endpoint not found (%s): is "+
			"Nabiz.Agent.Diagnostics installed and diagnostics.enabled turned on?", target)
	default:
		return "could not take a dump from the application: " + text
	}
}

// doJSON makes a token-authenticated request and decodes the JSON response.
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
