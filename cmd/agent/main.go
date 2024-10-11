package main

import (
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	k8scli "k8s.io/component-base/cli"
	"k8s.io/klog/v2"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/agent"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

func NewAgentCommand() *cobra.Command {
	cfgflags := genericclioptions.NewConfigFlags(false)
	var drbdStatusInterval, reconcileInterval, resyncInterval, operationsTimeout, failOverTimeout time.Duration
	var deletionGraceSeconds int64
	var nodeName, healthzBindAddress, satellitePodLabel string
	var failOverUnsafePods, disableNodeTaints bool

	cmd := &cobra.Command{
		Use:     "node-agent",
		Version: metadata.Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer cancel()

			cfg, err := cfgflags.ToRESTConfig()
			if err != nil {
				return err
			}

			selector, err := labels.Parse(satellitePodLabel)
			if err != nil {
				return err
			}

			ag, err := agent.NewAgent(&agent.Options{
				RestConfig:           cfg,
				DeletionGraceSec:     deletionGraceSeconds,
				ReconcileInterval:    reconcileInterval,
				ResyncInterval:       resyncInterval,
				OperationTimeout:     operationsTimeout,
				DrbdStatusInterval:   drbdStatusInterval,
				FailOverTimeout:      failOverTimeout,
				NodeName:             nodeName,
				FailOverUnsafePods:   failOverUnsafePods,
				SatellitePodSelector: selector,
				DisableNodeTaints:    disableNodeTaints,
			})
			if err != nil {
				return err
			}

			if healthzBindAddress != "" {
				listener, err := net.Listen("tcp", healthzBindAddress)
				if err != nil {
					return err
				}

				mux := &http.ServeMux{}
				mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
					ag.Healthz(writer)
				})

				go func() {
					err := http.Serve(listener, mux)
					klog.Errorf("error serving /healthz endpoint: %s", err)
					cancel()
				}()
			}

			return ag.Run(ctx)
		},
	}

	cfgflags.AddFlags(cmd.Flags())
	cmd.Flags().DurationVar(&drbdStatusInterval, "drbd-status-interval", 5*time.Second, "time between DRBD status updates")
	cmd.Flags().DurationVar(&failOverTimeout, "fail-over-timeout", 5*time.Second, "timeout before starting fail-over process")
	cmd.Flags().DurationVar(&operationsTimeout, "operations-timeout", 30*time.Second, "default timeout for operations")
	cmd.Flags().DurationVar(&reconcileInterval, "reconcile-interval", 10*time.Second, "maximum interval between reconciliation attempts")
	cmd.Flags().DurationVar(&resyncInterval, "resync-interval", 15*time.Minute, "how often the internal object cache should be resynchronized")
	cmd.Flags().Int64Var(&deletionGraceSeconds, "grace-period-seconds", 10, "default grace period for deleting k8s objects, in seconds")
	cmd.Flags().StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "the name of node this is running on. defaults to the NODE_NAME environment variable")
	cmd.Flags().StringVar(&healthzBindAddress, "health-bind-address", ":8000", "the address to bind to for the /healthz endpoint")
	cmd.Flags().StringVar(&satellitePodLabel, "satellite-pod-label", "app.kubernetes.io/component=linstor-satellite", "the connection name reported by DRBD is one of the Pods with this label")
	cmd.Flags().BoolVar(&failOverUnsafePods, "fail-over-unsafe-pods", false, "fail over Pods that use storage with unknown fail over properties")
	cmd.Flags().BoolVar(&disableNodeTaints, "disable-node-taints", false, "prevent nodes from being tainted")
	return cmd
}

func main() {
	exitcode := k8scli.Run(NewAgentCommand())
	os.Exit(exitcode)
}
