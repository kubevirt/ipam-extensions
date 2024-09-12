package udn

import (
	"context"
	"fmt"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

func FindPrimaryNetwork(ctx context.Context,
	cli client.Client,
	namespace string) (*v1.NetworkAttachmentDefinition, error) {
	nadList := v1.NetworkAttachmentDefinitionList{}
	if err := cli.List(ctx, &nadList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed listing nads for pod namespace %q: %w", namespace, err)
	}

	for _, nad := range nadList.Items {
		netConfig, err := config.NewConfig(nad.Spec.Config)
		if err != nil {
			return nil, fmt.Errorf("failed to extract the relevant NAD information: %w", err)
		}
		if netConfig.Role == config.NetworkRolePrimary {
			return ptr.To(nad), nil
		}
	}
	return nil, nil
}
