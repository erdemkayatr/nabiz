package k8s

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"

	jsonpatch "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)
)

func init() {
	_ = admissionv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
}

// WebhookStats holds the injection counters.
type WebhookStats struct {
	Reviewed atomic.Uint64
	Injected atomic.Uint64
	Skipped  atomic.Uint64
	Errors   atomic.Uint64
}

// Webhook is the mutating admission webhook that injects instrumentation into
// pods.
type Webhook struct {
	cfg   InjectConfig
	log   *slog.Logger
	stats WebhookStats
}

// NewWebhook, webhook'u kurar.
func NewWebhook(cfg InjectConfig, log *slog.Logger) *Webhook {
	return &Webhook{cfg: cfg, log: log}
}

// Stats exposes the counters.
func (wh *Webhook) Stats() *WebhookStats { return &wh.stats }

// Handler wires up the /mutate and /healthz routes.
func (wh *Webhook) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mutate", wh.handleMutate)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (wh *Webhook) handleMutate(w http.ResponseWriter, r *http.Request) {
	wh.stats.Reviewed.Add(1)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 3<<20))
	if err != nil {
		wh.fail(w, nil, fmt.Errorf("could not read the request body: %w", err))
		return
	}

	var review admissionv1.AdmissionReview
	if _, _, err := codecs.UniversalDeserializer().Decode(body, nil, &review); err != nil {
		wh.fail(w, nil, fmt.Errorf("could not decode the AdmissionReview: %w", err))
		return
	}
	if review.Request == nil {
		wh.fail(w, nil, fmt.Errorf("AdmissionReview.Request is empty"))
		return
	}

	var pod corev1.Pod
	if err := json.Unmarshal(review.Request.Object.Raw, &pod); err != nil {
		wh.fail(w, &review, fmt.Errorf("could not decode the pod: %w", err))
		return
	}

	// If injection is not requested, do not touch it. The webhook never blocks
	// pod creation: observability cannot matter more than the application coming
	// up.
	if !ShouldInject(&pod) {
		wh.stats.Skipped.Add(1)
		wh.allow(w, &review, nil)
		return
	}

	mutated := pod.DeepCopy()
	container, err := Inject(mutated, wh.cfg)
	if err != nil {
		wh.stats.Errors.Add(1)
		wh.log.Error("injection failed, letting the pod through untouched",
			"namespace", review.Request.Namespace, "pod", pod.Name, "err", err)
		wh.allow(w, &review, nil)
		return
	}

	patch, err := jsonpatch.CreatePatch(review.Request.Object.Raw, mustMarshal(mutated))
	if err != nil {
		wh.stats.Errors.Add(1)
		wh.log.Error("could not produce the patch", "err", err)
		wh.allow(w, &review, nil)
		return
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		wh.stats.Errors.Add(1)
		wh.allow(w, &review, nil)
		return
	}

	wh.stats.Injected.Add(1)
	wh.log.Info("instrumentation injected",
		"namespace", review.Request.Namespace,
		"pod", podDisplayName(&pod),
		"container", container,
		"patch_ops", len(patch))

	wh.allow(w, &review, patchBytes)
}

func (wh *Webhook) allow(w http.ResponseWriter, review *admissionv1.AdmissionReview, patch []byte) {
	resp := &admissionv1.AdmissionResponse{Allowed: true}
	if review != nil && review.Request != nil {
		resp.UID = review.Request.UID
	}
	if len(patch) > 0 {
		pt := admissionv1.PatchTypeJSONPatch
		resp.Patch = patch
		resp.PatchType = &pt
	}
	writeReview(w, resp)
}

// fail returns Allowed=true even on error: the webhook itself must never take
// the cluster down.
func (wh *Webhook) fail(w http.ResponseWriter, review *admissionv1.AdmissionReview, err error) {
	wh.stats.Errors.Add(1)
	wh.log.Error("could not handle the admission request", "err", err)
	resp := &admissionv1.AdmissionResponse{
		Allowed: true,
		Result:  &metav1.Status{Message: err.Error()},
	}
	if review != nil && review.Request != nil {
		resp.UID = review.Request.UID
	}
	writeReview(w, resp)
}

func writeReview(w http.ResponseWriter, resp *admissionv1.AdmissionResponse) {
	out := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: resp,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// podDisplayName shows generateName when the pod name has not been assigned yet.
func podDisplayName(pod *corev1.Pod) string {
	if pod.Name != "" {
		return pod.Name
	}
	return pod.GenerateName + "(pending)"
}
