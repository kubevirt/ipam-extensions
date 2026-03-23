#!/bin/bash

set -xe
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

KIND_ARGS="${KIND_ARGS:--ic -ikv -i6 -mne -nse -uae}"

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

function patch_ovnk_local_registry() {
    # OVNK's kind.yaml.j2 uses deprecated containerdConfigPatches with
    # registry.mirrors, silently ignored by containerd 2.x.
    # Remove that block (note: upstream template has typo "registy").
    sed -i '/use_local_regist.*true/,/{%- endif %}/d' \
        ${OVN_KUBERNETES_DIR}/contrib/kind.yaml.j2

    # Inject hosts.toml setup into OVNK's connect_local_registry(), which
    # runs after cluster creation but before OVN image install/deploy.
    local patch_file
    patch_file=$(mktemp)
    cat > "${patch_file}" <<'ENDPATCH'

    # Configure containerd hosts.toml for the local registry (containerd 2.x)
    local registry_dir="/etc/containerd/certs.d/localhost:${KIND_LOCAL_REGISTRY_PORT}"
    for node in $(kind get nodes --name "${KIND_CLUSTER_NAME}"); do
        docker exec "${node}" mkdir -p "${registry_dir}"
        docker exec "${node}" sh -c \
            "printf '[host.\"http://${KIND_LOCAL_REGISTRY_NAME}:5000\"]\n' > ${registry_dir}/hosts.toml"
    done
ENDPATCH
    sed -i '/^EOF$/r '"${patch_file}" ${OVN_KUBERNETES_DIR}/contrib/kind.sh
    rm -f "${patch_file}"
}

function up() {
    mkdir -p ${OUTPUT_DIR}
    ensure_ovn_kubernetes
    patch_ovnk_local_registry
    ensure_kind
    kind delete cluster --name $cluster_name
    (
        cd ${OVN_KUBERNETES_DIR}
        ./contrib/kind.sh --local-kind-registry ${KIND_ARGS} -cn ${cluster_name} --opt-out-kv-ipam
    )
}

function down() {
    ensure_kind
    ${KIND_BIN} delete cluster --name ${cluster_name}
    echo "down"
}

function sync() {
    local img=localhost:5000/kubevirt-ipam-controller
    local tag=latest
    IMG=$img:$tag make \
        build \
        docker-build \
        docker-push

    # Generate the manifest with the "sha256" to force kubernetes to reload the image
    sha=$(skopeo inspect --tls-verify=false docker://$img:$tag |jq -r .Digest)
    IMG=$img@$sha make deploy

    # Ensure the project network-polices are valid by installing an additional deny-all network-policy affecting the project namespace
    ./hack/install-deny-all-net-pol.sh

    ${KUBECTL} rollout status -w -n kubevirt-ipam-controller-system deployment kubevirt-ipam-controller-manager --timeout 2m
}



$op $@
