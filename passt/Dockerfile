FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG KUBEVIRT_VERSION

ENV CNI_PATH="/cni"

RUN mkdir -p ${CNI_PATH}
ADD https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-passt-binding ${CNI_PATH}/network-passt-binding
RUN chmod +x ${CNI_PATH}/network-passt-binding
