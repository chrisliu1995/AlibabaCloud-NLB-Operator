package main

import (
	"flag"
	"os"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/controller"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/provider"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = nlbv1.SchemeBuilder.AddToScheme(scheme)
}

func main() {
	var (
		metricsAddr             string
		enableLeaderElection    bool
		probeAddr               string
		accessKeyId             string
		accessKeySecret         string
		regionId                string
		endpoint                string
		maxConcurrentReconciles int
		getListenerQPS          float64
		createListenerQPS       float64
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", 5, "Maximum number of concurrent reconciles for NLB controller")
	flag.StringVar(&accessKeyId, "access-key-id", os.Getenv("ACCESS_KEY_ID"), "Alibaba Cloud Access Key ID")
	flag.StringVar(&accessKeySecret, "access-key-secret", os.Getenv("ACCESS_KEY_SECRET"), "Alibaba Cloud Access Key Secret")
	flag.StringVar(&regionId, "region-id", os.Getenv("REGION_ID"), "Alibaba Cloud Region ID")
	flag.StringVar(&endpoint, "endpoint", "", "Alibaba Cloud NLB API endpoint")
	flag.Float64Var(&getListenerQPS, "get-listener-qps", 18.0, "Local QPS limit for GetListenerAttribute API (token-bucket, burst=5)")
	flag.Float64Var(&createListenerQPS, "create-listener-qps", 3.0, "Local QPS limit for CreateListener API (token-bucket, burst=5)")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Validate required parameters
	if accessKeyId == "" || accessKeySecret == "" || regionId == "" {
		setupLog.Error(nil, "Missing required parameters: ACCESS_KEY_ID, ACCESS_KEY_SECRET, or REGION_ID")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "nlb-operator.alibabacloud.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create NLB client
	nlbClient, err := provider.NewNLBClient(endpoint, accessKeyId, accessKeySecret, regionId)
	if err != nil {
		setupLog.Error(err, "unable to create NLB client")
		os.Exit(1)
	}

	// Initialize per-interface local rate limiter for GetListenerAttribute.
	nlbClient.GetListenerLimiter = rate.NewLimiter(rate.Limit(getListenerQPS), 5)
	// Initialize per-interface local rate limiter for CreateListener.
	nlbClient.CreateListenerLimiter = rate.NewLimiter(rate.Limit(createListenerQPS), 5)

	// Setup NLB controller
	if err = (&controller.NLBReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorderFor("nlb-controller"),
		NLBClient:               nlbClient,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NLB")
		os.Exit(1)
	}

	// Setup ServerGroup controller
	if err = (&controller.ServerGroupReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorderFor("servergroup-controller"),
		NLBClient:               nlbClient,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ServerGroup")
		os.Exit(1)
	}

	// Setup Listener controller
	if err = (&controller.ListenerReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorderFor("listener-controller"),
		NLBClient:               nlbClient,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Listener")
		os.Exit(1)
	}

	// Add health check
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	// Add readiness check
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
