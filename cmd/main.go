/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	virtv1 "kubevirt.io/api/core/v1"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/kubevirt/ipam-extensions/pkg/ipamclaimswebhook"
	"github.com/kubevirt/ipam-extensions/pkg/vminetworkscontroller"
	"github.com/kubevirt/ipam-extensions/pkg/vmnetworkscontroller"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(virtv1.AddToScheme(scheme))
	utilruntime.Must(nadv1.AddToScheme(scheme))
	utilruntime.Must(ipamclaimsapi.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var enableLeaderElection bool
	var probeAddr string
	var enableHTTP2 bool
	var certDir string

	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(
		&certDir,
		"certificates-dir",
		"",
		"Specify the certificates directory for the webhook server",
	)

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancelation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookOptions := webhook.Options{
		TLSOpts: tlsOpts,
	}
	if certDir != "" {
		setupLog.Info("using certificates directory", "dir", certDir)
		webhookOptions.CertDir = certDir
	}
	webhookServer := webhook.NewServer(webhookOptions)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "71d89df3",
		WebhookServer:          webhookServer,
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
		NewCache: func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
			opts.ByObject = map[client.Object]cache.ByObject{
				&corev1.Pod{}: {
					Label:     virtLauncherSelector(),
					Transform: pruneIrrelevantPodData,
				},
			}
			return cache.New(config, opts)
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err = vmnetworkscontroller.NewVMReconciler(mgr).Setup(); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachine")
		os.Exit(1)
	}

	if err = vminetworkscontroller.NewVMIReconciler(mgr).Setup(); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineInstance")
		os.Exit(1)
	}

	if err := ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).Complete(); err != nil {
		setupLog.Error(err, "unable to create webhook controller", "controller", "Pod")
	}

	mgr.GetWebhookServer().Register(
		"/mutate-v1-pod",
		&webhook.Admission{Handler: ipamclaimswebhook.NewIPAMClaimsValet(mgr)},
	)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func virtLauncherSelector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{virtv1.AppLabel: "virt-launcher"})
}

func pruneIrrelevantPodData(obj interface{}) (interface{}, error) {
	oldPod, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil
	}

	newPod := oldPod.DeepCopy()
	newPod.ObjectMeta = metav1.ObjectMeta{
		Name:        oldPod.Name,
		Namespace:   oldPod.Namespace,
		UID:         oldPod.UID,
		Annotations: oldPod.Annotations,
		Labels:      oldPod.Labels,
	}
	newPod.Spec = corev1.PodSpec{}
	newPod.Status = corev1.PodStatus{}

	return newPod, nil
}
