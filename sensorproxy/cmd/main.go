package main

import (
	"flag"
	"os"

	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"k8s.io/client-go/kubernetes"

	"github.com/argoproj/argo-events/common"
	sensorclientset "github.com/argoproj/argo-events/pkg/client/sensor/clientset/versioned"
	"github.com/argoproj/argo-events/sensorproxy"
)

var (
	namespace        string
	namespaced       bool
	managedNamespace string
)

func init() {
	ns, defined := os.LookupEnv("NAMESPACE")
	if !defined {
		panic(errors.New("required environment variable NAMESPACE not defined"))
	}
	namespace = ns

	flag.BoolVar(&namespaced, "namespaced", false, "run the proxy server as namespaced mode")
	flag.StringVar(&managedNamespace, "managed-namespace", namespace, "namespace that proxy server manages, defaults to the installation namespace")
	flag.Parse()
}

func main() {
	kubeConfig, _ := os.LookupEnv(common.EnvVarKubeConfig)
	restConfig, err := common.GetClientConfig(kubeConfig)
	if err != nil {
		panic(err)
	}
	kubeClientset := kubernetes.NewForConfigOrDie(restConfig)
	sensorClientset := sensorclientset.NewForConfigOrDie(restConfig)

	opts := sensorproxy.ProxyServerOpts{
		Namespace:        namespace,
		Namespaced:       namespaced,
		ManagedNamespace: managedNamespace,
		KubeClientset:    kubeClientset,
		SensorClientset:  sensorClientset,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyServer := sensorproxy.NewProxyServer(opts)
	if err = proxyServer.Run(ctx, common.SensorProxyPort, func(url string) {}); err != nil {
		panic(err)
	}
}
