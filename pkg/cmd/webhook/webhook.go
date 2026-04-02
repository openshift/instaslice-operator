package webhook

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/openshift/instaslice-operator/pkg/version"
	"github.com/openshift/instaslice-operator/pkg/webhook"
	"github.com/spf13/cobra"
	k8sapiflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
)

const (
	WebhookName string = "das-webhook"
)

var (
	useTLS          bool
	tlsCert         string
	tlsKey          string
	caCert          string
	listenAddress   string
	listenPort      int
	testHooks       bool
	tlsMinVersion   string
	tlsCipherSuites []string
)

func NewWebhook(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Run: func(cmd *cobra.Command, args []string) {
			startServer()
		},
	}
	cmd.Use = "serve"
	cmd.Version = fmt.Sprintf("%v", version.Get().GitVersion)
	cmd.Short = "Start the Cluster Instaslice Operator"
	cmd.Flags().BoolVar(&useTLS, "tls", false, "Use TLS? Must specify -tlskey, -tlscert, -cacert")
	cmd.Flags().StringVar(&tlsCert, "tlscert", "", "File containing the x509 Certificate for HTTPS")
	cmd.Flags().StringVar(&tlsKey, "tlskey", "", "File containing the x509 private key")
	cmd.Flags().StringVar(&caCert, "cacert", "", "File containing the x509 CA cert for HTTPS")
	cmd.Flags().StringVar(&listenAddress, "listen", "0.0.0.0", "Listen address")
	cmd.Flags().IntVar(&listenPort, "port", 8443, "Secure port that the webhook listens on")
	cmd.Flags().BoolVar(&testHooks, "testHooks", false, "Test webhook URI uniqueness and quit")

	// TLS security profile flags - values injected by operator per OCPSTRAT-2611
	cmd.Flags().StringVar(&tlsMinVersion, "tls-min-version", "VersionTLS12",
		"Minimum TLS version (e.g., VersionTLS12, VersionTLS13)")
	cmd.Flags().StringSliceVar(&tlsCipherSuites, "tls-cipher-suites", nil,
		"Comma-separated list of TLS cipher suites (IANA names)")

	return cmd
}

// registerGenericWebhooks registers all Kueue integration webhooks
func registerGenericWebhooks() {
	// Kubeflow training jobs
	webhooks := []webhook.GenericWebhook{
		webhook.NewPyTorchJobWebhook(),
		webhook.NewMPIJobWebhook(),
		webhook.NewTFJobWebhook(),
		webhook.NewXGBoostJobWebhook(),
		webhook.NewPaddleJobWebhook(),
		webhook.NewJaxJobWebhook(),
		// Ray
		webhook.NewRayJobWebhook(),
		webhook.NewRayClusterWebhook(),
		// JobSet
		webhook.NewJobSetWebhook(),
		// Long-running workloads
		webhook.NewDeploymentWebhook(),
		webhook.NewStatefulSetWebhook(),
		webhook.NewLeaderWorkerSetWebhook(),
		// CodeFlare
		webhook.NewAppWrapperWebhook(),
	}

	for _, hook := range webhooks {
		dispatcher := webhook.NewGenericDispatcher(hook)
		http.HandleFunc(hook.GetURI(), dispatcher.HandleRequest)
		klog.InfoS("Registered webhook", "name", hook.Name(), "uri", hook.GetURI())
	}
}

func startServer() {
	// Pod webhook (existing)
	podHook := webhook.NewWebhook()
	podDispatcher := webhook.NewDispatcher(podHook)

	http.HandleFunc(podHook.GetURI(), podDispatcher.HandleRequest)
	http.HandleFunc(podHook.GetReadinessURI(), podDispatcher.HandleReadiness)
	http.HandleFunc(podHook.GetHealthzURI(), podDispatcher.HandleHealthz)

	// Job webhook (for Kueue integration - transforms Jobs before Kueue creates Workloads)
	jobHook := webhook.NewJobWebhook()
	jobDispatcher := webhook.NewGenericDispatcher(jobHook)

	http.HandleFunc(jobHook.GetURI(), jobDispatcher.HandleRequest)
	klog.InfoS("Registered Job webhook", "uri", jobHook.GetURI())

	// Register all Kueue integration webhooks using generic dispatcher
	registerGenericWebhooks()

	if testHooks {
		os.Exit(0)
	}

	bindAddress := net.JoinHostPort(listenAddress, strconv.Itoa(listenPort))
	klog.InfoS("Instaslice Webhook", "version", version.Get().GitVersion)
	klog.InfoS("HTTP server running", "address", bindAddress)

	server := &http.Server{Addr: bindAddress}
	var err error
	if useTLS {
		var cafile []byte
		cafile, err = os.ReadFile(caCert)
		if err != nil {
			klog.ErrorS(err, "could not read CA cert file")
			os.Exit(1)
		}
		certpool := x509.NewCertPool()
		certpool.AppendCertsFromPEM(cafile)

		// Build TLS config from CLI flags (injected by operator per OCPSTRAT-2611)
		tlsCfg := buildTLSConfigFromFlags()
		tlsCfg.RootCAs = certpool

		server.TLSConfig = tlsCfg
		err = server.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil {
		klog.ErrorS(err, "error serving connection")
		os.Exit(1)
	}
}

// buildTLSConfigFromFlags builds a tls.Config from CLI flags.
// The operator injects --tls-min-version and --tls-cipher-suites based on
// the cluster's APIServer.spec.tlsSecurityProfile (OCPSTRAT-2611).
func buildTLSConfigFromFlags() *tls.Config {
	// Parse min TLS version
	minVersionID, err := k8sapiflag.TLSVersion(tlsMinVersion)
	if err != nil {
		klog.Warningf("Invalid TLS min version %q, using TLS 1.2: %v", tlsMinVersion, err)
		minVersionID = tls.VersionTLS12
	}

	// Parse cipher suites
	var cipherSuiteIDs []uint16
	if len(tlsCipherSuites) > 0 {
		cipherSuiteIDs, err = k8sapiflag.TLSCipherSuites(tlsCipherSuites)
		if err != nil {
			klog.Warningf("Invalid cipher suites, using Go defaults: %v", err)
			cipherSuiteIDs = nil
		}
	}

	klog.InfoS("TLS configuration from operator",
		"minVersion", tlsMinVersion,
		"cipherSuites", strings.Join(tlsCipherSuites, ","))

	return &tls.Config{
		MinVersion:   minVersionID,
		CipherSuites: cipherSuiteIDs,
	}
}
