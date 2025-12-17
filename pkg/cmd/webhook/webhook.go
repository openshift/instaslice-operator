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

	"github.com/openshift/instaslice-operator/pkg/version"
	"github.com/openshift/instaslice-operator/pkg/webhook"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

const (
	WebhookName string = "das-webhook"
)

var (
	useTLS        bool
	tlsCert       string
	tlsKey        string
	caCert        string
	listenAddress string
	listenPort    int
	testHooks     bool
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

	// Workload webhook (for Kueue integration)
	workloadHook := webhook.NewWorkloadWebhook()
	workloadDispatcher := webhook.NewGenericDispatcher(workloadHook)

	http.HandleFunc(workloadHook.GetURI(), workloadDispatcher.HandleRequest)
	klog.InfoS("Registered Workload webhook", "uri", workloadHook.GetURI())

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

		server.TLSConfig = &tls.Config{
			RootCAs: certpool,
		}
		err = server.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil {
		klog.ErrorS(err, "error serving connection")
		os.Exit(1)
	}
}
