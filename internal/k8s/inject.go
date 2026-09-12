// Package k8s, .NET enstrümantasyonunu pod'lara otomatik enjekte eden
// admission webhook'unu barındırır.
//
// Enjeksiyon, uygulama imajına dokunmaz: enstrümantasyon paylaşımlı bir
// emptyDir'e init container ile kopyalanır, uygulama container'ına da yalnızca
// ortam değişkeni eklenir. Geri almak, annotation'ı silip pod'u yeniden
// başlatmaktan ibarettir.
package k8s

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// AnnotationInject, pod'a .NET enstrümantasyonu enjekte edilmesini ister.
	AnnotationInject = "nabiz.io/inject-dotnet"
	// AnnotationServiceName, OTEL_SERVICE_NAME'i elle belirler. Yoksa
	// workload adından türetilir.
	AnnotationServiceName = "nabiz.io/service-name"
	// AnnotationContainer, birden fazla container varsa hangisinin
	// enstrümante edileceğini söyler.
	AnnotationContainer = "nabiz.io/container"
	// AnnotationSampleRatio, agent tarafı örnekleme oranı ("0.1" gibi).
	AnnotationSampleRatio = "nabiz.io/sample-ratio"
	// AnnotationLibc, uygulama imajı Alpine tabanlıysa "musl" yapılır.
	// Varsayılan glibc'dir; resmi .NET imajları Debian tabanlı.
	AnnotationLibc = "nabiz.io/libc"
	// AnnotationInjected, iki kez enjeksiyonu önleyen işaret.
	AnnotationInjected = "nabiz.io/injected"

	volumeName    = "nabiz-dotnet-auto"
	mountPath     = "/nabiz-auto"
	initContainer = "nabiz-dotnet-init"
)

// InjectConfig, webhook'un enjeksiyon davranışı.
type InjectConfig struct {
	// InstrumentationImage, auto-instrumentation dosyalarını taşıyan imaj.
	InstrumentationImage string
	// CollectorEndpoint, OTLP hedefi (ör. http://nabiz-collector.nabiz:4317).
	CollectorEndpoint string
	// ClusterName, tüm span'lere eklenen k8s.cluster.name.
	ClusterName string
	// DefaultSampleRatio, annotation yoksa kullanılacak oran.
	DefaultSampleRatio string
}

// ShouldInject, pod'un enjeksiyon isteyip istemediğini söyler.
func ShouldInject(pod *corev1.Pod) bool {
	if pod.Annotations[AnnotationInjected] == "true" {
		return false
	}
	switch strings.ToLower(pod.Annotations[AnnotationInject]) {
	case "true", "dotnet", "yes", "1":
		return true
	}
	return false
}

// Inject, pod'u yerinde değiştirir. Hangi container'ın enstrümante edildiğini
// döndürür.
func Inject(pod *corev1.Pod, cfg InjectConfig) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod'da container yok")
	}

	idx, err := targetContainer(pod)
	if err != nil {
		return "", err
	}
	target := &pod.Spec.Containers[idx]

	addVolume(pod)
	addInitContainer(pod, cfg)

	target.VolumeMounts = appendVolumeMount(target.VolumeMounts, corev1.VolumeMount{
		Name:      volumeName,
		MountPath: mountPath,
	})
	target.Env = mergeEnv(target.Env, instrumentationEnv(pod, target, cfg))

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationInjected] = "true"

	return target.Name, nil
}

func targetContainer(pod *corev1.Pod) (int, error) {
	name := pod.Annotations[AnnotationContainer]
	if name == "" {
		return 0, nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("annotation'daki container bulunamadı: %s", name)
}

func addVolume(pod *corev1.Pod) {
	for _, v := range pod.Spec.Volumes {
		if v.Name == volumeName {
			return
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         volumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
}

// initScript, enstrümantasyonu paylaşımlı birime kopyalar ve doğru mimari
// dizinine "native" adıyla symlink atar. Mimari, ancak pod'un çalışacağı
// node'da bilinebildiği için webhook'ta değil burada çözülür; Kubernetes'in
// $(VAR) ikamesi de node bilgisine erişemez.
func initScript(pod *corev1.Pod) string {
	libc := ""
	if strings.EqualFold(pod.Annotations[AnnotationLibc], "musl") {
		libc = "musl-"
	}
	return "set -e; " +
		"cp -a /otel/. " + mountPath + "/; " +
		"case \"$(uname -m)\" in aarch64|arm64) A=arm64;; *) A=x64;; esac; " +
		"ln -sfn " + mountPath + "/linux-" + libc + "${A} " + mountPath + "/native"
}

func addInitContainer(pod *corev1.Pod, cfg InjectConfig) {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == initContainer {
			return
		}
	}
	runAsNonRoot := true
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:    initContainer,
		Image:   cfg.InstrumentationImage,
		Command: []string{"/bin/sh", "-c", initScript(pod)},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeName,
			MountPath: mountPath,
		}},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &runAsNonRoot,
			ReadOnlyRootFilesystem:   boolPtr(true),
			AllowPrivilegeEscalation: boolPtr(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	})
}

// instrumentationEnv, CLR profiler'ı devreye alan ve topoloji için gereken
// k8s boyutlarını taşıyan ortam değişkenlerini üretir.
//
// k8s bilgisi downward API ile pod'un kendisinden alınır; collector sıcak
// yolda Kubernetes API'sine hiç gitmez. Topolojinin k8s boyutu bu yüzden
// bedavaya gelir.
func instrumentationEnv(pod *corev1.Pod, target *corev1.Container, cfg InjectConfig) []corev1.EnvVar {
	sampleRatio := pod.Annotations[AnnotationSampleRatio]
	if sampleRatio == "" {
		sampleRatio = cfg.DefaultSampleRatio
	}

	resourceAttrs := []string{
		"k8s.namespace.name=$(NABIZ_K8S_NAMESPACE)",
		"k8s.pod.name=$(NABIZ_K8S_POD)",
		"k8s.node.name=$(NABIZ_K8S_NODE)",
		"k8s.container.name=" + target.Name,
	}
	if cfg.ClusterName != "" {
		resourceAttrs = append(resourceAttrs, "k8s.cluster.name="+cfg.ClusterName)
	}

	env := []corev1.EnvVar{
		// --- downward API: topolojinin k8s boyutları ---
		fieldEnv("NABIZ_K8S_NAMESPACE", "metadata.namespace"),
		fieldEnv("NABIZ_K8S_POD", "metadata.name"),
		fieldEnv("NABIZ_K8S_NODE", "spec.nodeName"),

		// --- CLR profiler ---
		{Name: "CORECLR_ENABLE_PROFILING", Value: "1"},
		{Name: "CORECLR_PROFILER", Value: "{918728DD-259F-4A6A-AC2B-B85E1B658318}"},
		{Name: "CORECLR_PROFILER_PATH", Value: mountPath + "/native/OpenTelemetry.AutoInstrumentation.Native.so"},
		{Name: "DOTNET_ADDITIONAL_DEPS", Value: mountPath + "/AdditionalDeps"},
		{Name: "DOTNET_SHARED_STORE", Value: mountPath + "/store"},
		{Name: "DOTNET_STARTUP_HOOKS", Value: mountPath + "/net/OpenTelemetry.AutoInstrumentation.StartupHook.dll"},
		{Name: "OTEL_DOTNET_AUTO_HOME", Value: mountPath},

		// --- dışa aktarım ---
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: cfg.CollectorEndpoint},
		{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "grpc"},
		{Name: "OTEL_METRICS_EXPORTER", Value: "none"},
		{Name: "OTEL_LOGS_EXPORTER", Value: "none"},

		// --- performans ---
		// Örnekleme kararı agent'ta verilir: düşürülen span hiç serileştirilmez,
		// ağa çıkmaz, collector'ı meşgul etmez. parentbased olması, bir trace'in
		// tüm servislerde aynı kararı almasını garanti eder.
		{Name: "OTEL_TRACES_SAMPLER", Value: "parentbased_traceidratio"},
		{Name: "OTEL_TRACES_SAMPLER_ARG", Value: sampleRatio},
		// Batch exporter: istek yolunda ağ çağrısı yok.
		{Name: "OTEL_BSP_SCHEDULE_DELAY", Value: "2000"},
		{Name: "OTEL_BSP_MAX_QUEUE_SIZE", Value: "8192"},
		{Name: "OTEL_BSP_MAX_EXPORT_BATCH_SIZE", Value: "1024"},

		{Name: "OTEL_SERVICE_NAME", Value: serviceName(pod, target)},
		{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: strings.Join(resourceAttrs, ",")},
	}
	return env
}

// serviceName, servis adını annotation'dan, yoksa pod adından türetir.
func serviceName(pod *corev1.Pod, target *corev1.Container) string {
	if v := pod.Annotations[AnnotationServiceName]; v != "" {
		return v
	}
	if v := pod.Labels["app.kubernetes.io/name"]; v != "" {
		return v
	}
	if v := pod.Labels["app"]; v != "" {
		return v
	}
	if pod.GenerateName != "" {
		return strings.TrimSuffix(pod.GenerateName, "-")
	}
	return target.Name
}

func fieldEnv(name, path string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: path},
		},
	}
}

// mergeEnv, var olan değişkenlerin üzerine yazmaz: kullanıcının kendi
// ayarı her zaman kazanır.
func mergeEnv(existing, added []corev1.EnvVar) []corev1.EnvVar {
	present := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		present[e.Name] = struct{}{}
	}
	for _, e := range added {
		if _, ok := present[e.Name]; ok {
			continue
		}
		existing = append(existing, e)
	}
	return existing
}

func appendVolumeMount(mounts []corev1.VolumeMount, m corev1.VolumeMount) []corev1.VolumeMount {
	for _, existing := range mounts {
		if existing.Name == m.Name {
			return mounts
		}
	}
	return append(mounts, m)
}

func boolPtr(b bool) *bool { return &b }
