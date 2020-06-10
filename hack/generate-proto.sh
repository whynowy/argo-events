#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

source $(dirname $0)/library.sh
header "generating proto files"

cd ${REPO_ROOT}
ensure_protobuf
ensure_vendor

if [ "`command -v protoc-gen-gogo`" = "" ]; then
  go install ./vendor/github.com/gogo/protobuf/protoc-gen-gogo
fi

if [ "`command -v protoc-gen-gogofast`" = "" ]; then
  go install ./vendor/github.com/gogo/protobuf/protoc-gen-gogofast
fi

if [ "`command -v protoc-gen-grpc-gateway`" = "" ]; then
  go install ./vendor/github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway
fi

if [ "`command -v protoc-gen-swagger`" = "" ]; then
  go install github.com/grpc-ecosystem/grpc-gateway/protoc-gen-swagger
fi

if [ "`command -v goimports`" = "" ]; then
  export GO111MODULE="off"
  go get golang.org/x/tools/cmd/goimports
fi

make_fake_paths

export GOPATH="${FAKE_GOPATH}"
export GO111MODULE="off"

cd "${FAKE_REPOPATH}"

go install ./vendor/k8s.io/code-generator/cmd/go-to-protobuf

${GOPATH}/bin/go-to-protobuf \
        --go-header-file=./hack/custom-boilerplate.go.txt \
        --packages=github.com/argoproj/argo-events/pkg/apis/common \
        --apimachinery-packages=+k8s.io/apimachinery/pkg/util/intstr,+k8s.io/apimachinery/pkg/api/resource,k8s.io/apimachinery/pkg/runtime/schema,+k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/api/core/v1,k8s.io/api/policy/v1beta1 \
        --proto-import ./vendor

${GOPATH}/bin/go-to-protobuf \
        --go-header-file=./hack/custom-boilerplate.go.txt \
        --packages=github.com/argoproj/argo-events/pkg/apis/eventbus/v1alpha1,github.com/argoproj/argo-events/pkg/apis/eventsource/v1alpha1,github.com/argoproj/argo-events/pkg/apis/gateway/v1alpha1,github.com/argoproj/argo-events/pkg/apis/sensor/v1alpha1 \
        --apimachinery-packages=github.com/argoproj/argo-events/pkg/apis/common,+k8s.io/apimachinery/pkg/util/intstr,+k8s.io/apimachinery/pkg/api/resource,k8s.io/apimachinery/pkg/runtime/schema,+k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/api/core/v1,k8s.io/api/policy/v1beta1 \
        --proto-import ./vendor

# Following 2 proto files are needed
mkdir -p ${GOPATH}/src/google/api    
curl -Ls https://raw.githubusercontent.com/grpc-ecosystem/grpc-gateway/v1.12.2/third_party/googleapis/google/api/annotations.proto -o ${GOPATH}/src/google/api/annotations.proto
curl -Ls https://raw.githubusercontent.com/grpc-ecosystem/grpc-gateway/v1.12.2/third_party/googleapis/google/api/http.proto -o ${GOPATH}/src/google/api/http.proto

for f in $(find pkg -name '*.proto'); do
    protoc \
        -I /usr/local/include \
        -I . \
        -I ./vendor \
        -I ${GOPATH}/src \
        --gogofast_out=plugins=grpc:${GOPATH}/src \
        --grpc-gateway_out=logtostderr=true:${GOPATH}/src \
        --swagger_out=logtostderr=true,fqn_for_swagger_name=true:. \
        $f
done

