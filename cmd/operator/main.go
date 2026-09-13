// nabiz-operator is the admission webhook that automatically injects
// instrumentation into .NET applications.
//
// It generates its own certificate at startup and updates the caBundle in the
// MutatingWebhookConfiguration; cert-manager is not needed to install it.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erdemkayatr/nabiz/internal/config"
	nabizk8s "github.com/erdemkayatr/nabiz/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	namespace := config.String("NAMESPACE", "nabiz")
	serviceName := config.String("WEBHOOK_SERVICE", "nabiz-operator")
	webhookConfigName := config.String("WEBHOOK_CONFIG", "nabiz-dotnet-injector")
	addr := config.String("WEBHOOK_ADDR", ":9443")

	cfg := nabizk8s.InjectConfig{
		InstrumentationImage: config.String("INSTRUMENTATION_IMAGE", "nabiz/dotnet-instrumentation:1.16.0"),
		CollectorEndpoint:    config.String("COLLECTOR_ENDPOINT", "http://nabiz-collector."+namespace+".svc:4317"),
		ClusterName:          config.String("CLUSTER_NAME", ""),
		DefaultSampleRatio:   config.String("DEFAULT_SAMPLE_RATIO", "1.0"),
	}

	cert, caBundle, err := nabizk8s.SelfSignedCert(serviceName, namespace, 10*365*24*time.Hour)
	if err != nil {
		log.Error("could not generate the certificate", "err", err)
		os.Exit(1)
	}

	if err := patchCABundle(ctx, webhookConfigName, caBundle, log); err != nil {
		// If the caBundle cannot be updated the webhook is never called, and
		// carrying on quietly turns into a "why is injection not working" hunt.
		log.Error("could not update the MutatingWebhookConfiguration", "err", err)
		os.Exit(1)
	}

	wh := nabizk8s.NewWebhook(cfg, log)
	srv := &http.Server{
		Addr:              addr,
		Handler:           wh.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}

	go serveStats(config.String("STATS_ADDR", ":8888"), wh, log)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("nabiz-operator running",
		"addr", addr,
		"namespace", namespace,
		"collector", cfg.CollectorEndpoint,
		"instrumentation_image", cfg.InstrumentationImage)

	if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		log.Error("the webhook stopped", "err", err)
		os.Exit(1)
	}
}

// patchCABundle writes the generated CA into every entry of the webhook configuration.
func patchCABundle(ctx context.Context, name string, caBundle []byte, log *slog.Logger) error {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("could not read the in-cluster configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("could not build the kubernetes client: %w", err)
	}

	existing, err := client.AdmissionregistrationV1().
		MutatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not read %s: %w", name, err)
	}

	ops := make([]map[string]any, 0, len(existing.Webhooks))
	for i := range existing.Webhooks {
		ops = append(ops, map[string]any{
			"op":    "replace",
			"path":  fmt.Sprintf("/webhooks/%d/clientConfig/caBundle", i),
			"value": caBundle,
		})
	}
	if len(ops) == 0 {
		return fmt.Errorf("%s contains no webhook definitions", name)
	}

	payload, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	if _, err := client.AdmissionregistrationV1().
		MutatingWebhookConfigurations().
		Patch(ctx, name, types.JSONPatchType, payload, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("could not write the caBundle: %w", err)
	}

	log.Info("caBundle updated", "webhook_config", name, "webhooks", len(ops))
	return nil
}

func serveStats(addr string, wh *nabizk8s.Webhook, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		s := wh.Stats()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]uint64{
			"reviewed": s.Reviewed.Load(),
			"injected": s.Injected.Load(),
			"skipped":  s.Skipped.Load(),
			"errors":   s.Errors.Load(),
		})
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("the stats endpoint stopped", "err", err)
	}
}
