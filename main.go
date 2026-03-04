package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"k8s-vault-webhook/metrics"
	"k8s-vault-webhook/provider"
	"k8s-vault-webhook/registry"
	"k8s-vault-webhook/version"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	whhttp "github.com/slok/kubewebhook/pkg/http"
	whmetrics "github.com/slok/kubewebhook/pkg/observability/metrics"
	whcontext "github.com/slok/kubewebhook/pkg/webhook/context"
	"github.com/slok/kubewebhook/pkg/webhook/mutating"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	kubernetesConfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	// AnnotationOptOut allows workloads to explicitly opt out of secret injection.
	AnnotationOptOut = "vault.opstree.secret.manager/opt-out"
)

func (mw *mutatingWebhook) getVolumes(existingVolumes []corev1.Volume, providers []provider.SecretManager) []corev1.Volume {
	mw.logger.Debugf("Adding generic volumes to podspec")

	volumes := []corev1.Volume{
		{
			Name: "k8s-secret-injector",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		},
	}

	// Collect extra volumes from all enabled providers
	for _, p := range providers {
		extraVols := p.ExtraVolumes()
		if len(extraVols) > 0 {
			mw.logger.Debugf("Adding extra volumes from provider %s", p.Name())
			volumes = append(volumes, extraVols...)
		}
	}

	return volumes
}

func getInitContainers(originalContainers []corev1.Container, initContainersMutated bool, containersMutated bool) []corev1.Container {
	var containers = []corev1.Container{}

	if initContainersMutated || containersMutated {
		containers = append(containers, corev1.Container{
			Name:            "copy-k8s-secret-injector",
			Image:           viper.GetString("k8s_secret_injector_image"),
			ImagePullPolicy: corev1.PullPolicy(viper.GetString("k8s_secret_injector_image_pull_policy")),
			Command:         []string{"sh", "-c", "cp /usr/local/bin/k8s-secret-injector /k8s-secret/"},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "k8s-secret-injector",
					MountPath: "/k8s-secret/",
				},
			},
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		})
	}

	return containers
}

func hasSecretPrefix(value string) bool {
	return strings.HasPrefix(value, "vault:") || strings.HasPrefix(value, ">>secret:") || strings.HasPrefix(value, "secret:")
}

func (mw *mutatingWebhook) getDataFromConfigmap(cmName string, ns string) (map[string]string, error) {
	ctx := context.Background()
	configMap, err := mw.k8sClient.CoreV1().ConfigMaps(ns).Get(ctx, cmName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return configMap.Data, nil
}

func (mw *mutatingWebhook) getDataFromSecret(secretName string, ns string) (map[string][]byte, error) {
	ctx := context.Background()
	secret, err := mw.k8sClient.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}

func (mw *mutatingWebhook) lookForValueFrom(env corev1.EnvVar, ns string) (*corev1.EnvVar, error) {
	if env.ValueFrom.ConfigMapKeyRef != nil {
		data, err := mw.getDataFromConfigmap(env.ValueFrom.ConfigMapKeyRef.Name, ns)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if hasSecretPrefix(data[env.ValueFrom.ConfigMapKeyRef.Key]) {
			fromCM := corev1.EnvVar{
				Name:  env.Name,
				Value: data[env.ValueFrom.ConfigMapKeyRef.Key],
			}
			return &fromCM, nil
		}
	}
	if env.ValueFrom.SecretKeyRef != nil {
		data, err := mw.getDataFromSecret(env.ValueFrom.SecretKeyRef.Name, ns)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if hasSecretPrefix(string(data[env.ValueFrom.SecretKeyRef.Key])) {
			fromSecret := corev1.EnvVar{
				Name:  env.Name,
				Value: string(data[env.ValueFrom.SecretKeyRef.Key]),
			}
			return &fromSecret, nil
		}
	}
	return nil, nil
}

func (mw *mutatingWebhook) lookForEnvFrom(envFrom []corev1.EnvFromSource, ns string) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar

	for _, ef := range envFrom {
		if ef.ConfigMapRef != nil {
			data, err := mw.getDataFromConfigmap(ef.ConfigMapRef.Name, ns)
			if err != nil {
				if apierrors.IsNotFound(err) || (ef.ConfigMapRef.Optional != nil && *ef.ConfigMapRef.Optional) {
					continue
				} else {
					return envVars, err
				}
			}
			for key, value := range data {
				if hasSecretPrefix(value) {
					envFromCM := corev1.EnvVar{
						Name:  key,
						Value: value,
					}
					envVars = append(envVars, envFromCM)
				}
			}
		}
		if ef.SecretRef != nil {
			data, err := mw.getDataFromSecret(ef.SecretRef.Name, ns)
			if err != nil {
				if apierrors.IsNotFound(err) || (ef.SecretRef.Optional != nil && *ef.SecretRef.Optional) {
					continue
				} else {
					return envVars, err
				}
			}
			for key, value := range data {
				if hasSecretPrefix(string(value)) {
					envFromSec := corev1.EnvVar{
						Name:  key,
						Value: string(value),
					}
					envVars = append(envVars, envFromSec)
				}
			}
		}
	}
	return envVars, nil
}

func (mw *mutatingWebhook) mutateContainers(containers []corev1.Container, podSpec *corev1.PodSpec, providers []provider.SecretManager, ns string) (bool, error) {
	mutated := false
	for i, container := range containers {
		var mutationInProgress bool
		var envVars []corev1.EnvVar
		if len(container.EnvFrom) > 0 {
			envFrom, err := mw.lookForEnvFrom(container.EnvFrom, ns) //nolint
			if err != nil {
				return false, err
			}
			envVars = append(envVars, envFrom...) //nolint
		}
		for _, env := range container.Env {
			if hasSecretPrefix(env.Value) {
				envVars = append(envVars, env) //nolint
			}
			if env.ValueFrom != nil {
				valueFrom, err := mw.lookForValueFrom(env, ns)
				if err != nil {
					return false, err
				}
				if valueFrom == nil {
					continue
				}
				envVars = append(envVars, *valueFrom) //nolint
			}
		}

		args := container.Command

		// the container has no explicitly specified command
		if len(args) == 0 {
			mw.logger.Info("No command was given - attempting to get image metadata")
			imageConfig, err := mw.registry.GetImageConfig(mw.k8sClient, ns, &container, podSpec)
			if err != nil {
				return false, err
			}

			args = append(args, imageConfig.Entrypoint...)

			// If no Args are defined we can use the Docker CMD from the image
			// https://kubernetes.io/docs/tasks/inject-data-application/define-command-argument-container/#notes
			if len(container.Args) == 0 {
				args = append(args, imageConfig.Cmd...)
			}
		}
		args = append(args, container.Args...)

		container.Command = []string{"/k8s-secret/k8s-secret-injector"}
		container.Args = args

		// Use the provider registry loop instead of hardcoded if-chains
		for _, p := range providers {
			container = p.MutateContainer(container)
			mutationInProgress = true
		}

		if !mutationInProgress {
			continue
		}
		mutated = true

		// add the volume mount for k8s-secret-injector
		container.VolumeMounts = append(container.VolumeMounts, []corev1.VolumeMount{
			{
				Name:      "k8s-secret-injector",
				MountPath: "/k8s-secret",
			},
		}...)

		containers[i] = container
	}
	return mutated, nil
}

func (mw *mutatingWebhook) mutatePod(pod *corev1.Pod, providers []provider.SecretManager, ns string, dryRun bool) error {
	mw.logger.Debugf("Successfully connected to the API")

	initContainersMutated, err := mw.mutateContainers(pod.Spec.InitContainers, &pod.Spec, providers, ns)
	if err != nil {
		return err
	}

	if initContainersMutated {
		mw.logger.Debugf("Successfully mutated pod init containers")
	} else {
		mw.logger.Debugf("No pod init containers were mutated")
	}

	containersMutated, err := mw.mutateContainers(pod.Spec.Containers, &pod.Spec, providers, ns)
	if err != nil {
		return err
	}

	if containersMutated {
		mw.logger.Debugf("Successfully mutated pod containers")
	} else {
		mw.logger.Debugf("No pod containers were mutated")
	}

	if initContainersMutated || containersMutated {
		pod.Spec.InitContainers = append(getInitContainers(pod.Spec.Containers, initContainersMutated, containersMutated), pod.Spec.InitContainers...)
		mw.logger.Debugf("Successfully appended pod init containers to spec")

		pod.Spec.Volumes = append(pod.Spec.Volumes, mw.getVolumes(pod.Spec.Volumes, providers)...)
		mw.logger.Debugf("Successfully appended pod spec volumes")
	}

	if viper.GetString("k8s_secret_injector_image_pull_secret_name") != "" {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: viper.GetString("k8s_secret_injector_image_pull_secret_name")})
	}

	return nil
}

// isNamespaceExcluded checks if the given namespace is in the exclusion list.
func (mw *mutatingWebhook) isNamespaceExcluded(ns string) bool {
	excluded := viper.GetStringSlice("excluded_namespaces")
	for _, e := range excluded {
		if strings.EqualFold(e, ns) {
			return true
		}
	}
	return false
}

// SecretsMutator if object is Pod mutate pod specs
// return a stop boolean to stop executing the chain and also an error.
func (mw *mutatingWebhook) SecretsMutator(ctx context.Context, obj metav1.Object) (bool, error) {
	startTime := time.Now()
	ns := whcontext.GetAdmissionRequest(ctx).Namespace
	req := whcontext.GetAdmissionRequest(ctx)

	// Create a base logger with structured fields for this admission request
	reqLogger := mw.logger.WithFields(logrus.Fields{
		"namespace": ns,
		"uid":       req.UID,
		"operation": string(req.Operation),
	})

	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.AdmissionDuration.WithLabelValues(ns).Observe(duration)
		reqLogger.WithField("duration_sec", duration).Debug("Admission logic complete")
	}()

	// Safety: skip excluded namespaces (e.g., kube-system)
	if mw.isNamespaceExcluded(ns) {
		reqLogger.Info("Skipping mutation: namespace is excluded")
		metrics.ExcludedPodsTotal.WithLabelValues(ns, "namespace_exclusion").Inc()
		return false, nil
	}

	annotations := obj.GetAnnotations()

	// Safety: explicit workload opt-out via annotation
	if strings.EqualFold(annotations[AnnotationOptOut], "true") {
		reqLogger.Info("Skipping mutation: workload opted out via annotation")
		metrics.ExcludedPodsTotal.WithLabelValues(ns, "annotation_opt_out").Inc()
		return false, nil
	}

	providers := mw.providerRegistry.BuildEnabledProviders(annotations)

	if len(providers) == 0 {
		return false, nil
	}

	// Build a provider list string for logging
	var providerNames []string
	for _, p := range providers {
		providerNames = append(providerNames, p.Name())
	}

	reqLogger = reqLogger.WithField("providers", strings.Join(providerNames, ","))
	reqLogger.Info("Validating secret manager configurations")

	// Validate all enabled providers
	for _, p := range providers {
		if err := p.Validate(); err != nil {
			reqLogger.WithFields(logrus.Fields{
				"failed_provider": p.Name(),
				"error":           err.Error(),
			}).Error("Provider validation failed")
			metrics.MutationsTotal.WithLabelValues(p.Name(), ns, "failed").Inc()
			return true, err
		}
	}

	switch v := obj.(type) {
	case *corev1.Pod:
		podName := v.Name
		if podName == "" {
			podName = v.GenerateName
		}

		reqLogger = reqLogger.WithField("pod", podName)
		reqLogger.Info("Executing pod mutation")

		err := mw.mutatePod(v, providers, ns, whcontext.IsAdmissionRequestDryRun(ctx))
		if err != nil {
			reqLogger.WithError(err).Error("Pod mutation failed")
			for _, p := range providers {
				metrics.MutationsTotal.WithLabelValues(p.Name(), ns, "failed").Inc()
			}
			return true, err
		}

		reqLogger.Info("Pod mutation completely successfully")
		for _, p := range providers {
			metrics.MutationsTotal.WithLabelValues(p.Name(), ns, "success").Inc()
		}
		return false, nil
	default:
		return false, nil
	}
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
}

func (mw *mutatingWebhook) serveMetrics(addr string) {
	mw.logger.Infof("Telemetry on http://%s", addr)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(addr, mux)
	if err != nil {
		mw.logger.Fatalf("error serving telemetry: %s", err)
	}
}

func handlerFor(config mutating.WebhookConfig, mutator mutating.Mutator, recorder whmetrics.Recorder, logger logrus.FieldLogger) http.Handler {
	webhook, err := mutating.NewWebhook(config, mutator, nil, recorder, logger)
	if err != nil {
		logger.Fatalf("error creating webhook: %s", err)
	}

	handler, err := whhttp.HandlerFor(webhook)
	if err != nil {
		logger.Fatalf("error creating webhook: %s", err)
	}

	return handler
}

func newK8SClient() (kubernetes.Interface, error) {
	kubeConfig, err := kubernetesConfig.GetConfig()
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(kubeConfig)
}

func init() {
	viper.SetDefault("k8s_secret_injector_image", "quay.io/opstree/k8s-secret-injector:4.0")
	viper.SetDefault("k8s_secret_injector_image_pull_policy", string(corev1.PullIfNotPresent))
	viper.SetDefault("k8s_secret_injector_image_pull_secret_name", "")
	viper.SetDefault("tls_cert_file", "")
	viper.SetDefault("tls_private_key_file", "")
	viper.SetDefault("listen_address", ":8443")
	viper.SetDefault("debug", "false")
	viper.SetDefault("enable_json_log", "false")
	viper.SetDefault("telemetry_listen_address", "")
	viper.SetDefault("excluded_namespaces", []string{"kube-system", "kube-public", "kube-node-lease"})
	viper.SetDefault("shutdown_timeout", "15s")
	viper.AutomaticEnv()
}

func main() {
	var logger logrus.FieldLogger
	{
		log := logrus.New()

		if viper.GetBool("enable_json_log") {
			log.SetFormatter(&logrus.JSONFormatter{})
		}

		if viper.GetBool("debug") {
			log.SetLevel(logrus.DebugLevel)
			log.Debug("Debug mode enabled")
		}

		logger = log.WithField("app", "k8s-secret-injector")
	}
	fmt.Printf("K8s Vault Webhook Version: %s\n", version.GetVersion())
	fmt.Printf("K8s Secret Injector Version: %s\n", viper.GetString("k8s_secret_injector_image"))

	// Initialize Custom Prometheus Metrics
	metrics.InitMetrics()
	logger.Info("Custom Prometheus metrics initialized")

	k8sClient, err := newK8SClient()
	if err != nil {
		logger.Fatalf("error creating k8s client: %s", err)
	}

	mutatingWebhook := mutatingWebhook{
		k8sClient:        k8sClient,
		registry:         registry.NewRegistry(),
		logger:           logger,
		providerRegistry: newProviderRegistry(),
	}

	logger.Infof("Excluded namespaces: %v", viper.GetStringSlice("excluded_namespaces"))

	mutator := mutating.MutatorFunc(mutatingWebhook.SecretsMutator)

	metricsRecorder := whmetrics.NewPrometheus(prometheus.DefaultRegisterer)

	podHandler := handlerFor(mutating.WebhookConfig{Name: "k8s-secret-injector-pods", Obj: &corev1.Pod{}}, mutator, metricsRecorder, logger)

	mux := http.NewServeMux()
	mux.Handle("/pods", podHandler)
	mux.Handle("/healthz", http.HandlerFunc(healthzHandler))

	telemetryAddress := viper.GetString("telemetry_listen_address")
	listenAddress := viper.GetString("listen_address")
	tlsCertFile := viper.GetString("tls_cert_file")
	tlsPrivateKeyFile := viper.GetString("tls_private_key_file")

	if len(telemetryAddress) > 0 {
		// Serving metrics without TLS on separated address
		go mutatingWebhook.serveMetrics(telemetryAddress)
	} else {
		mux.Handle("/metrics", promhttp.Handler())
	}

	// Graceful shutdown: use http.Server so we can call Shutdown()
	server := &http.Server{
		Addr:    listenAddress,
		Handler: mux,
	}

	// Channel to listen for OS signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Start the server in a goroutine
	go func() {
		if tlsCertFile == "" && tlsPrivateKeyFile == "" {
			logger.Infof("Listening on http://%s", listenAddress)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("error serving webhook: %s", err)
			}
		} else {
			logger.Infof("Listening on https://%s", listenAddress)
			if err := server.ListenAndServeTLS(tlsCertFile, tlsPrivateKeyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("error serving webhook: %s", err)
			}
		}
	}()

	// Block until we receive a signal
	sig := <-stop
	logger.Infof("Received signal %v, shutting down gracefully...", sig)

	shutdownTimeout, _ := time.ParseDuration(viper.GetString("shutdown_timeout"))
	if shutdownTimeout == 0 {
		shutdownTimeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %s", err)
	}

	logger.Info("Server exited gracefully")
}
