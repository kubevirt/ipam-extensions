package udn

import (
	"fmt"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
)

func GetK8sPodDefaultNetworkSelection(
	multusDefaultNetAnnotationValue, podNamespace string) (*v1.NetworkSelectionElement, error) {
	networks, err := nadutils.ParseNetworkAnnotation(multusDefaultNetAnnotationValue, podNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CRD object: %v", err)
	}
	if len(networks) > 1 {
		return nil, fmt.Errorf("more than one default network is specified: %s", multusDefaultNetAnnotationValue)
	}

	if len(networks) == 1 {
		return networks[0], nil
	}

	return nil, nil
}
