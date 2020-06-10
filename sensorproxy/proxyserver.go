package sensorproxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_logrus "github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	log "github.com/sirupsen/logrus"
	"github.com/soheilhy/cmux"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/argoproj/argo-events/common"
	grpcutil "github.com/argoproj/argo-events/common/grpc"
	sensorpkg "github.com/argoproj/argo-events/pkg/apiclient/sensor"
	"github.com/argoproj/argo-events/pkg/client/sensor/clientset/versioned"
	"github.com/argoproj/argo/util/json"
)

const (
	// MaxGRPCMessageSize contains max grpc message size
	MaxGRPCMessageSize = 100 * 1024 * 1024

	secretServerKey  = "server-key.pem"
	secretServerCert = "server-cert.pem"
	secretCACert     = "ca-cert.pem"
)

type proxyServer struct {
	namespace        string
	namespaced       bool
	managedNamespace string
	kubeClientset    *kubernetes.Clientset
	sensorClientset  *versioned.Clientset
	stopCh           chan struct{}

	tlsConfig *tls.Config

	logger *log.Logger
}

// ProxyServerOpts holds the arguments of proxy server
type ProxyServerOpts struct {
	Namespace        string
	Namespaced       bool
	ManagedNamespace string
	KubeClientset    *kubernetes.Clientset
	SensorClientset  *versioned.Clientset
}

// NewProxyServer returns a proxyServer
func NewProxyServer(opts ProxyServerOpts) *proxyServer {
	return &proxyServer{
		namespace:        opts.Namespace,
		namespaced:       opts.Namespaced,
		managedNamespace: opts.ManagedNamespace,
		kubeClientset:    opts.KubeClientset,
		sensorClientset:  opts.SensorClientset,
		stopCh:           make(chan struct{}),
		logger:           common.NewArgoEventsLogger(),
	}
}

var backoff = wait.Backoff{
	Steps:    5,
	Duration: 500 * time.Millisecond,
	Factor:   1.0,
	Jitter:   0.1,
}

func (ps *proxyServer) Run(ctx context.Context, port int, browserOpenFunc func(string)) error {
	ps.logger.Info("Starting sensor proxy server...")

	grpcServer := ps.newGRPCServer()
	httpServer := ps.newHTTPServer(ctx, port)

	// TODO: TLS is not working, fix it later
	// tlsConfig, err := ps.makeTLSConfig(ctx)
	// if err != nil {
	// 	ps.logger.Error("failed to config TLS")
	// 	return err
	// }
	// ps.tlsConfig = tlsConfig

	// Start listener
	var conn net.Listener
	var listerErr error
	address := fmt.Sprintf(":%d", port)
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		conn, listerErr = net.Listen("tcp", address)
		if listerErr != nil {
			ps.logger.Warnf("failed to listen: %v", listerErr)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		ps.logger.Error(err)
		return err
	}
	if ps.tlsConfig != nil {
		conn = tls.NewListener(conn, ps.tlsConfig)
	}

	// Cmux is used to support servicing gRPC and HTTP1.1+JSON on the same port
	tcpm := cmux.New(conn)
	httpL := tcpm.Match(cmux.HTTP1Fast())
	grpcL := tcpm.Match(cmux.Any())

	go func() { ps.checkServeErr("grpcServer", grpcServer.Serve(grpcL)) }()
	go func() { ps.checkServeErr("httpServer", httpServer.Serve(httpL)) }()
	go func() { ps.checkServeErr("tcpm", tcpm.Serve()) }()
	url := "http://localhost" + address
	if ps.tlsConfig != nil {
		url = "https://localhost" + address
	}
	ps.logger.Infof("Sensor Proxy Server started successfully on %s", url)
	browserOpenFunc(url)
	<-ps.stopCh
	return nil
}

func (ps *proxyServer) newGRPCServer() *grpc.Server {
	serverLog := log.NewEntry(log.StandardLogger())
	sOpts := []grpc.ServerOption{
		// Set both the send and receive the bytes limit to be 100MB
		// The proper way to achieve high performance is to have pagination
		// while we work toward that, we can have high limit first
		grpc.MaxRecvMsgSize(MaxGRPCMessageSize),
		grpc.MaxSendMsgSize(MaxGRPCMessageSize),
		grpc.ConnectionTimeout(300 * time.Second),
		grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			grpc_logrus.UnaryServerInterceptor(serverLog),
			grpcutil.PanicLoggerUnaryServerInterceptor(serverLog),
			grpcutil.ErrorTranslationUnaryServerInterceptor,
		)),
		grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(
			grpc_logrus.StreamServerInterceptor(serverLog),
			grpcutil.PanicLoggerStreamServerInterceptor(serverLog),
			grpcutil.ErrorTranslationStreamServerInterceptor,
		)),
	}

	grpcServer := grpc.NewServer(sOpts...)
	sensorpkg.RegisterSensorServiceServer(grpcServer, NewSensorServer(ps.sensorClientset, ps.namespaced, ps.managedNamespace, ps.logger))
	return grpcServer
}

// newHTTPServer returns the HTTP server to serve HTTP/HTTPS requests. This is implemented
// using grpc-gateway as a proxy to the gRPC server.
func (ps *proxyServer) newHTTPServer(ctx context.Context, port int) *http.Server {
	endpoint := fmt.Sprintf(":%d", port)

	mux := http.NewServeMux()
	httpServer := http.Server{
		Addr:      endpoint,
		Handler:   mux,
		TLSConfig: ps.tlsConfig,
	}
	dialOpts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxGRPCMessageSize)),
	}
	// dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(ps.tlsConfig)))
	dialOpts = append(dialOpts, grpc.WithInsecure())

	// HTTP 1.1+JSON Server
	// grpc-ecosystem/grpc-gateway is used to proxy HTTP requests to the corresponding gRPC call
	// NOTE: if a marshaller option is not supplied, grpc-gateway will default to the jsonpb from
	// golang/protobuf. Which does not support types such as time.Time. gogo/protobuf does support
	// time.Time, but does not support custom UnmarshalJSON() and MarshalJSON() methods. Therefore
	// we use our own Marshaler
	gwMuxOpts := runtime.WithMarshalerOption(runtime.MIMEWildcard, new(json.JSONMarshaler))
	gwmux := runtime.NewServeMux(gwMuxOpts)
	mustRegisterGWHandler(ctx, sensorpkg.RegisterSensorServiceHandlerFromEndpoint, gwmux, endpoint, dialOpts)
	mux.Handle("/api/", gwmux)
	return &httpServer
}

type registerFunc func(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error

// mustRegisterGWHandler is a convenience function to register a gateway handler
func mustRegisterGWHandler(ctx context.Context, register registerFunc, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) {
	err := register(ctx, mux, endpoint, opts)
	if err != nil {
		panic(err)
	}
}

// checkServeErr checks the error from a .Serve() call to decide if it was a graceful shutdown
func (ps *proxyServer) checkServeErr(name string, err error) {
	if err != nil {
		if ps.stopCh == nil {
			// a nil stopCh indicates a graceful shutdown
			ps.logger.Infof("graceful shutdown %s: %v", name, err)
		} else {
			ps.logger.Fatalf("%s: %v", name, err)
		}
	} else {
		ps.logger.Infof("graceful shutdown %s", name)
	}
}

func (ps *proxyServer) getOrCreateKeyCertsFromSecret(ctx context.Context, client kubernetes.Interface) (serverKey, serverCert, caCert []byte, err error) {
	secret, err := client.CoreV1().Secrets(ps.namespace).Get(common.SensorProxyCertSecretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, nil, err
		}
		ps.logger.Info("secret not found, creating a new one")
		newSecret, err := ps.generateSecret(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		secret, err = client.CoreV1().Secrets(ps.namespace).Create(newSecret)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, nil, nil, err
		}
		// Something else might have created it, try getting it again
		secret, err = client.CoreV1().Secrets(ps.namespace).Get(common.SensorProxyCertSecretName, metav1.GetOptions{})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	var ok bool
	if serverKey, ok = secret.Data[secretServerKey]; !ok {
		return nil, nil, nil, errors.New("server key missing")
	}
	if serverCert, ok = secret.Data[secretServerCert]; !ok {
		return nil, nil, nil, errors.New("server cert missing")
	}
	if caCert, ok = secret.Data[secretCACert]; !ok {
		return nil, nil, nil, errors.New("ca cert missing")
	}
	return serverKey, serverCert, caCert, nil
}

// Generate secret
func (ps *proxyServer) generateSecret(ctx context.Context) (*corev1.Secret, error) {
	hosts := []string{"localhost"}
	hosts = append(hosts, common.SensorProxyServiceName)
	hosts = append(hosts, fmt.Sprintf("%s.%s", common.SensorProxyServiceName, ps.namespace))
	hosts = append(hosts, fmt.Sprintf("%s.%s.svc.cluster.local", common.SensorProxyServiceName, ps.namespace))
	serverKey, serverCert, caCert, err := common.CreateCerts(ctx, hosts, time.Now().Add(time.Hour*24*365*10))
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.SensorProxyCertSecretName,
			Namespace: ps.namespace,
		},
		Data: map[string][]byte{
			secretServerKey:  serverKey,
			secretServerCert: serverCert,
			secretCACert:     caCert,
		},
	}, nil
}

func (ps *proxyServer) makeTLSConfig(ctx context.Context) (*tls.Config, error) {
	serverKey, serverCert, _, err := ps.getOrCreateKeyCertsFromSecret(ctx, ps.kubeClientset)
	if err != nil {
		ps.logger.Errorf("error getting/generating tls secrets, %v", err)
		return nil, err
	}
	cert, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		ps.logger.Errorf("error parsing key pair, %v", err)
		return nil, err
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}, nil
}
