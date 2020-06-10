package sensorproxy

import (
	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sensorpkg "github.com/argoproj/argo-events/pkg/apiclient/sensor"
	"github.com/argoproj/argo-events/pkg/apis/sensor/v1alpha1"
	"github.com/argoproj/argo-events/pkg/client/sensor/clientset/versioned"
)

type sensorServer struct {
	sensorClientset  *versioned.Clientset
	namespaced       bool
	managedNamespace string
	authConfig       *viper.Viper
	logger           *log.Logger
}

// NewSensorServer returns a sensorServer
func NewSensorServer(sensorClientset *versioned.Clientset, namespaced bool, managedNamespace string, logger *log.Logger) sensorpkg.SensorServiceServer {
	v := viper.New()
	v.SetConfigName("auth-secrets")
	v.SetConfigType("yaml")
	v.AddConfigPath("/etc/sensor-proxy/")
	err := v.ReadInConfig()
	if err != nil {
		logger.Fatalf("fatal error config file: %v", err)
	}
	v.WatchConfig()
	v.OnConfigChange(func(e fsnotify.Event) {
		logger.Infof("Config file changed: %s", e.Name)
	})
	return &sensorServer{sensorClientset: sensorClientset, authConfig: v, namespaced: namespaced, managedNamespace: managedNamespace, logger: logger}
}

func (ss *sensorServer) GetSensorStatus(ctx context.Context, req *sensorpkg.SensorStatusGetRequest) (*v1alpha1.SensorStatus, error) {
	err := ss.validateRequest(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	sensorGetOption := metav1.GetOptions{}
	if req.GetOptions != nil {
		sensorGetOption = *req.GetOptions
	}
	sensor, err := ss.sensorClientset.ArgoprojV1alpha1().Sensors(req.GetNamespace()).Get(req.GetName(), sensorGetOption)
	if err != nil {
		ss.logger.Error("error getting sensor")
		return nil, err
	}
	return &sensor.Status, nil
}

func (ss *sensorServer) UpdateSensorStatus(ctx context.Context, req *sensorpkg.SensorStatusUpdateRequest) (*v1alpha1.SensorStatus, error) {
	err := ss.validateRequest(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	sensor, err := ss.sensorClientset.ArgoprojV1alpha1().Sensors(req.GetNamespace()).Get(req.GetName(), metav1.GetOptions{})
	if err != nil {
		ss.logger.Error("error getting sensor")
		return nil, err
	}
	if !equality.Semantic.DeepEqual(sensor.Status, *req.GetStatus()) {
		sensor.Status = *req.GetStatus()
		sensor, err = ss.sensorClientset.ArgoprojV1alpha1().Sensors(req.GetNamespace()).Update(sensor)
		if err != nil {
			ss.logger.Error("error updating sensor")
			return nil, err
		}
	}
	return req.GetStatus(), nil
}

func getAuthHeader(md metadata.MD) string {
	// looks for the HTTP header `Authorization: ...`
	for _, t := range md.Get("authorization") {
		return t
	}
	return ""
}

func (ss *sensorServer) validateRequest(ctx context.Context, namespace string) error {
	if ss.namespaced && ss.managedNamespace != namespace {
		ss.logger.Errorf("namespace %s is not under management", namespace)
		return status.Errorf(codes.PermissionDenied, "namespace %s is not under management", namespace)
	}
	if !ss.authConfig.InConfig(namespace) {
		ss.logger.Errorf("namespace %s missing in auth config", namespace)
		return status.Errorf(codes.NotFound, "namespace %s missing in auth config", namespace)
	}
	md, _ := metadata.FromIncomingContext(ctx)
	authorization := getAuthHeader(md)
	if len(authorization) == 0 {
		ss.logger.Error("no authorization token found")
		return status.Error(codes.Unauthenticated, "no authorization token found")
	}
	if ss.authConfig.GetString(namespace) != authorization {
		ss.logger.Error("invalid authorization token")
		return status.Error(codes.PermissionDenied, "invalid authorization token")
	}
	return nil
}
