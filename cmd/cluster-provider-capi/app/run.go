package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	"github.com/openmcp-project/openmcp-operator/lib/clusteraccess"

	"github.com/openmcp-project/cluster-provider-capi/api/providerscheme"
	"github.com/openmcp-project/cluster-provider-capi/internal/controller"
)

func NewRunCommand(so *SharedOptions) *cobra.Command {
	opts := &RunOptions{SharedOptions: so}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the CAPI cluster provider controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.PrintRawOptions(cmd)
			if err := opts.Complete(cmd.Context()); err != nil {
				return fmt.Errorf("error completing options: %w", err)
			}
			opts.PrintCompletedOptions(cmd)
			if opts.DryRun {
				cmd.Println("=== END OF DRY RUN ===")
				return nil
			}
			return opts.Run(cmd.Context())
		},
	}
	opts.AddFlags(cmd)
	return cmd
}

type RawRunOptions struct {
	MetricsAddr     string `json:"metrics-bind-address"`
	MetricsCertPath string `json:"metrics-cert-path"`
	MetricsCertName string `json:"metrics-cert-name"`
	MetricsCertKey  string `json:"metrics-cert-key"`
	WebhookCertPath string `json:"webhook-cert-path"`
	WebhookCertName string `json:"webhook-cert-name"`
	WebhookCertKey  string `json:"webhook-cert-key"`
	EnableLeaderElection bool `json:"leader-elect"`
	ProbeAddr       string `json:"health-probe-bind-address"`
	SecureMetrics   bool   `json:"metrics-secure"`
	EnableHTTP2     bool   `json:"enable-http2"`
}

type RunOptions struct {
	*SharedOptions
	RawRunOptions

	TLSOpts              []func(*tls.Config)
	WebhookTLSOpts       []func(*tls.Config)
	MetricsServerOptions metricsserver.Options
	MetricsCertWatcher   *certwatcher.CertWatcher
	WebhookCertWatcher   *certwatcher.CertWatcher
}

func (o *RunOptions) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.MetricsAddr, "metrics-bind-address", "0", "Metrics endpoint address.")
	cmd.Flags().StringVar(&o.ProbeAddr, "health-probe-bind-address", ":8081", "Health probe address.")
	cmd.Flags().BoolVar(&o.EnableLeaderElection, "leader-elect", false, "Enable leader election.")
	cmd.Flags().BoolVar(&o.SecureMetrics, "metrics-secure", true, "Serve metrics over HTTPS.")
	cmd.Flags().StringVar(&o.WebhookCertPath, "webhook-cert-path", "", "Webhook certificate directory.")
	cmd.Flags().StringVar(&o.WebhookCertName, "webhook-cert-name", "tls.crt", "Webhook certificate filename.")
	cmd.Flags().StringVar(&o.WebhookCertKey, "webhook-cert-key", "tls.key", "Webhook key filename.")
	cmd.Flags().StringVar(&o.MetricsCertPath, "metrics-cert-path", "", "Metrics certificate directory.")
	cmd.Flags().StringVar(&o.MetricsCertName, "metrics-cert-name", "tls.crt", "Metrics certificate filename.")
	cmd.Flags().StringVar(&o.MetricsCertKey, "metrics-cert-key", "tls.key", "Metrics key filename.")
	cmd.Flags().BoolVar(&o.EnableHTTP2, "enable-http2", false, "Enable HTTP/2.")
}

func (o *RunOptions) Complete(ctx context.Context) error {
	if err := o.SharedOptions.Complete(); err != nil {
		return err
	}

	disableHTTP2 := func(c *tls.Config) {
		c.NextProtos = []string{"http/1.1"}
	}
	if !o.EnableHTTP2 {
		o.TLSOpts = append(o.TLSOpts, disableHTTP2)
	}
	o.WebhookTLSOpts = o.TLSOpts

	if len(o.WebhookCertPath) > 0 {
		var err error
		o.WebhookCertWatcher, err = certwatcher.New(
			filepath.Join(o.WebhookCertPath, o.WebhookCertName),
			filepath.Join(o.WebhookCertPath, o.WebhookCertKey),
		)
		if err != nil {
			return fmt.Errorf("failed to initialize webhook certificate watcher: %w", err)
		}
		o.WebhookTLSOpts = append(o.WebhookTLSOpts, func(c *tls.Config) {
			c.GetCertificate = o.WebhookCertWatcher.GetCertificate
		})
	}

	o.MetricsServerOptions = metricsserver.Options{
		BindAddress:   o.MetricsAddr,
		SecureServing: o.SecureMetrics,
		TLSOpts:       o.TLSOpts,
	}
	if o.SecureMetrics {
		o.MetricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if len(o.MetricsCertPath) > 0 {
		var err error
		o.MetricsCertWatcher, err = certwatcher.New(
			filepath.Join(o.MetricsCertPath, o.MetricsCertName),
			filepath.Join(o.MetricsCertPath, o.MetricsCertKey),
		)
		if err != nil {
			return fmt.Errorf("failed to initialize metrics certificate watcher: %w", err)
		}
		o.MetricsServerOptions.TLSOpts = append(o.MetricsServerOptions.TLSOpts, func(c *tls.Config) {
			c.GetCertificate = o.MetricsCertWatcher.GetCertificate
		})
	}

	return nil
}

func (o *RunOptions) Run(ctx context.Context) error {
	scheme := providerscheme.InstallOperatorAPIsPlatform(runtime.NewScheme())
	// also register CAPI types so the controller can watch clusterv1.Cluster
	if err := clusterv1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("unable to add cluster-api scheme: %w", err)
	}

	if err := o.PlatformCluster.InitializeClient(scheme); err != nil {
		return err
	}

	log := o.Log.WithName("setup")
	log.Info("Environment", "value", o.Environment)
	log.Info("ProviderName", "value", o.ProviderName)

	providerSystemNamespace := os.Getenv(openmcpconst.EnvVariablePodNamespace)
	if providerSystemNamespace == "" {
		return fmt.Errorf("environment variable %s is not set", openmcpconst.EnvVariablePodNamespace)
	}

	clusterAccessManager := clusteraccess.NewClusterAccessManager(o.PlatformCluster.Client(), o.ProviderName, providerSystemNamespace)
	clusterAccessManager.WithLogger(&log).
		WithInterval(10 * time.Second).
		WithTimeout(30 * time.Minute)

	mgr, err := ctrl.NewManager(o.PlatformCluster.RESTConfig(), ctrl.Options{
		Scheme:                 o.PlatformCluster.Scheme(),
		Metrics:                o.MetricsServerOptions,
		WebhookServer:          webhook.NewServer(webhook.Options{TLSOpts: o.WebhookTLSOpts}),
		HealthProbeBindAddress: o.ProbeAddr,
		LeaderElection:         o.EnableLeaderElection,
		LeaderElectionID:       "capi.cluster.open-control-plane.io",
	})
	if err != nil {
		return fmt.Errorf("unable to create manager: %w", err)
	}
	if err := mgr.Add(o.PlatformCluster.Cluster()); err != nil {
		return fmt.Errorf("unable to add platform cluster to manager: %w", err)
	}

	if err := controller.NewProviderConfigReconciler(o.PlatformCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to add ProviderConfigReconciler: %w", err)
	}
	if err := controller.NewAccessRequestReconciler(o.PlatformCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to add AccessRequestReconciler: %w", err)
	}
	if err := controller.NewClusterReconciler(o.PlatformCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to add ClusterReconciler: %w", err)
	}

	if o.MetricsCertWatcher != nil {
		if err := mgr.Add(o.MetricsCertWatcher); err != nil {
			return fmt.Errorf("unable to add metrics cert watcher: %w", err)
		}
	}
	if o.WebhookCertWatcher != nil {
		if err := mgr.Add(o.WebhookCertWatcher); err != nil {
			return fmt.Errorf("unable to add webhook cert watcher: %w", err)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	log.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}
	return nil
}

func (o *RunOptions) PrintRawOptions(cmd *cobra.Command)       { o.SharedOptions.PrintRaw(cmd) }
func (o *RunOptions) PrintCompletedOptions(cmd *cobra.Command) { o.SharedOptions.PrintCompleted(cmd) }
