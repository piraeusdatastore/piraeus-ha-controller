package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	lclient "github.com/LINBIT/golinstor/client"
	lmonitor "github.com/LINBIT/golinstor/monitor"
	"github.com/henvic/ctxsignal"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/consts"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/hacontroller"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/k8s"
)

type args struct {
	loglevel                   int32
	kubeconfig                 string
	newResourceGracePeriod     time.Duration
	knownResourceGracePeriod   time.Duration
	reconcileInterval          time.Duration
	podLabelSelector           string
	attacherName               string
	enableLeaderElection       bool
	leaderElectionResourceName string
	leaderElectionLeaseName    string
	leaderElectionNamespace    string
	leaderElectionHealthzPort  int
}

func main() {
	args := parseArgs()

	log.SetLevel(log.Level(args.loglevel))
	log.WithField("version", consts.Version).Infof("starting " + consts.Name)

	ctx, cancel := ctxsignal.WithTermination(context.Background())
	defer cancel()

	kubeClient, err := k8s.Clientset(args.kubeconfig)
	if err != nil {
		log.WithError(err).Fatal("failed to create kubernetes client")
	}

	linstorClient, err := lclient.NewClient(lclient.Log(log.StandardLogger()))
	if err != nil {
		log.WithError(err).Fatal("failed to create LINSTOR client")
	}

	lostRUMonitor, err := lmonitor.NewLostResourceUser(ctx, linstorClient, lmonitor.WithDelay(args.newResourceGracePeriod, args.knownResourceGracePeriod))
	if err != nil {
		log.WithError(err).Fatal("failed to set up lost resource monitor")
	}

	defer lostRUMonitor.Stop()

	recorder := eventRecorder(kubeClient)

	haControllerOpts := []hacontroller.Option{
		hacontroller.WithEventRecorder(recorder),
		hacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: args.podLabelSelector}),
		hacontroller.WithAttacherName(args.attacherName),
		hacontroller.WithReconcileInterval(args.reconcileInterval),
	}

	if args.enableLeaderElection {
		leaderElector, err := leaderElector(ctx, kubeClient, args.leaderElectionLeaseName, args.leaderElectionNamespace, args.leaderElectionResourceName, args.leaderElectionHealthzPort, recorder)
		if err != nil {
			log.WithError(err).Fatal("failed to create leader elector")
		}
		haControllerOpts = append(haControllerOpts, hacontroller.WithLeaderElector(leaderElector))
	}

	haController := hacontroller.NewHAController(consts.Name, kubeClient, lostRUMonitor.C, haControllerOpts...)

	err = haController.Run(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to run HA Controller")
	}
}

func leaderElector(ctx context.Context, kubeClient kubernetes.Interface, identity, namespace, name string, healthPort int, recorder record.EventRecorder) (*leaderelection.LeaderElector, error) {
	lockCfg := resourcelock.ResourceLockConfig{
		Identity:      identity,
		EventRecorder: recorder,
	}

	leaderElectionLock, err := resourcelock.New(resourcelock.LeasesResourceLock, namespace, name, nil, kubeClient.CoordinationV1(), lockCfg)
	if err != nil {
		return nil, err
	}

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: leaderElectionLock,
		Name: consts.Name,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(_ context.Context) { log.Info("gained leader status") },
			OnNewLeader:      func(identity string) { log.WithField("leader", identity).Info("new leader") },
			OnStoppedLeading: func() { log.Info("leader status lost") },
		},
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	healthzAdapter := leaderelection.NewLeaderHealthzAdaptor(0)
	healthzAdapter.SetLeaderElection(elector)

	http.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		err := healthzAdapter.Check(request)
		if err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
		} else {
			writer.WriteHeader(http.StatusOK)
		}
	})

	go func() {
		err := http.ListenAndServe(fmt.Sprintf(":%d", healthPort), nil)
		if err != nil {
			log.WithError(err).Error("failed to serve /healthz endpoint")
		}
	}()

	go elector.Run(ctx)

	return elector, nil
}

// Creates a new event recorder that forwards to Kubernetes to persist "kind: Event" objects
func eventRecorder(kubeClient kubernetes.Interface) record.EventRecorder {
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&v1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	source := corev1.EventSource{Component: consts.Name}
	return broadcaster.NewRecorder(scheme.Scheme, source)
}

func parseArgs() *args {
	args := &args{}

	flag.Int32Var(&args.loglevel, "v", int32(log.InfoLevel), "set log level")
	flag.StringVar(&args.kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	flag.DurationVar(&args.newResourceGracePeriod, "new-resource-grace-period", consts.DefaultNewResourceGracePeriod, "grace period for newly created resources after which promotable resources will be considered lost")
	flag.DurationVar(&args.knownResourceGracePeriod, "known-resource-grace-period", consts.DefaultKnownResourceGracePeriod, "grace period for known resources after which promotable resources will be considered lost")
	flag.DurationVar(&args.reconcileInterval, "reconcile-interval", consts.DefaultReconcileInterval, "time between reconciliation runs")
	flag.StringVar(&args.podLabelSelector, "pod-label-selector", consts.DefaultPodLabelSelector, "labels selector for pods to consider")
	flag.StringVar(&args.attacherName, "attacher-name", consts.DefaultAttacherName, "name of the attacher to consider")
	flag.BoolVar(&args.enableLeaderElection, "leader-election", false, "use kubernetes leader election")
	flag.StringVar(&args.leaderElectionResourceName, "leader-election-resource-name", consts.Name, "name for leader election resource")
	flag.StringVar(&args.leaderElectionLeaseName, "leader-election-lease-name", "", "name for leader election lease (unique for each pod)")
	flag.StringVar(&args.leaderElectionNamespace, "leader-election-namespace", "", "namespace for leader election")
	flag.IntVar(&args.leaderElectionHealthzPort, "leader-election-healtz-port", 8080, "port to use for serving the /healthz endpoint")

	flag.Parse()

	return args
}
