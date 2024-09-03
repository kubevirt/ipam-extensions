#!/bin/bash

set -xe
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

KIND_ARGS="${KIND_ARGS:--ic -ikv -i6 -mne}"

OUTPUT_DIR=${OUTPUT_DIR:-${SCRIPT_DIR}/../.output}

OVN_KUBERNETES_REPO="${OVN_KUBERNETES_REPO:-https://github.com/ovn-org/ovn-kubernetes}"
OVN_KUBERNETES_BRANCH="${OVN_KUBERNETES_BRANCH:-master}"
OVN_KUBERNETES_DIR=${OUTPUT_DIR}/ovn-kubernetes

# from https://github.com/kubernetes-sigs/kind/releases
KIND_BIN=${OUTPUT_DIR}/kind

export KUBECONFIG=${KUBECONFIG:-${OUTPUT_DIR}/kubeconfig}

cluster_name=virt-ipam
op=$1
shift

function ensure_ovn_kubernetes() {
    if [ -d "${OVN_KUBERNETES_DIR}" ]; then
        return 0
    fi
    (   
        cd ${OUTPUT_DIR}
        git clone --depth 1 --single-branch ${OVN_KUBERNETES_REPO} -b ${OVN_KUBERNETES_BRANCH}
    )
}

function ensure_kind() {
    if [ -f ${KIND_BIN} ]; then
        return 0
    fi
    local kind_version=v0.20.0
    local arch=""
    case $(uname -m) in
        x86_64)  arch="amd64";;
        aarch64) arch="arm64" ;;
    esac
    local kind_url=https://kind.sigs.k8s.io/dl/$kind_version/kind-linux-${arch}
    curl --retry 5 -Lo ${KIND_BIN} ${kind_url}
	chmod +x ${KIND_BIN}
}

function up() {
    mkdir -p ${OUTPUT_DIR}
    ensure_ovn_kubernetes
    ensure_kind
    kind delete cluster --name $cluster_name
    (
        cd ${OVN_KUBERNETES_DIR}
        ./contrib/kind.sh --local-kind-registry ${KIND_ARGS} -cn ${cluster_name}
    )
}

function down() {
    ensure_kind
    ${KIND_BIN} delete cluster --name ${cluster_name}
    echo "down"
}

function sync() {
    local img=localhost:5000/kubevirt-ipam-controller
    local passt_img=localhost:5000/passt-binding-cni
    local tag=latest
    IMG=$img:$tag PASST_IMG=$passt_img:$tag make \
        build \
        docker-build \
        docker-push

    # Generate the manifest with the "sha256" to force kubernetes to reload the image
    sha=$(skopeo inspect --tls-verify=false docker://$img:$tag |jq -r .Digest)
    IMG=$img@$sha make deploy
    ${KUBECTL} rollout status -w -n kubevirt-ipam-controller-system deployment kubevirt-ipam-controller-manager --timeout 2m
}



$op $@
