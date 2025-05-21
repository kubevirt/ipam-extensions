#!/bin/bash -ex
#
# Copyright 2025 Red Hat, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

# This script install deny-all NetworkPolicy that affects kubevirt-ipam-controller namespace.

readonly ns="$(${KUBECTL} get pod -l app=ipam-virt-workloads -A -o=custom-columns=NS:.metadata.namespace --no-headers | head -1)"
[[ -z "${ns}" ]] && echo "FATAL: kubevirt-ipam-controller pods not found. Make sure kubevirt-ipam-controller is installed" && exit 1

readonly np_name="deny-all"
${KUBECTL} -n "${ns}" get networkpolicy "${np_name}" &> /dev/null ||
  cat <<EOF | ${KUBECTL} -n "${ns}" apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ${np_name}
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
  ingress: []
  egress: []
EOF
