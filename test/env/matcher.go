package env

import (
	"context"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
	gomegatypes "github.com/onsi/gomega/types"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kubevirtv1 "kubevirt.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamclaimsv1alpha1 "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
)

// IPAMClaimsFromNamespace fetches the IPAMClaims related to namespace
func IPAMClaimsFromNamespace(namespace string) func() ([]ipamclaimsv1alpha1.IPAMClaim, error) {
	return func() ([]ipamclaimsv1alpha1.IPAMClaim, error) {
		ipamClaimList := &ipamclaimsv1alpha1.IPAMClaimList{}
		if err := Client.List(context.Background(), ipamClaimList, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		return ipamClaimList.Items, nil
	}
}

func vmiStatusConditions(vmi *kubevirtv1.VirtualMachineInstance) []kubevirtv1.VirtualMachineInstanceCondition {
	return vmi.Status.Conditions
}

type IPResult struct {
	IPs []string
	Err error
}

func MatchIPs(getIPsFunc func(vmi *kubevirtv1.VirtualMachineInstance) ([]string, error), ipsMatcher gomegatypes.GomegaMatcher) gomegatypes.GomegaMatcher {
	return WithTransform(func(vmi *kubevirtv1.VirtualMachineInstance) IPResult {
		ips, err := getIPsFunc(vmi)
		return IPResult{IPs: ips, Err: err}
	}, SatisfyAll(
		WithTransform(func(result IPResult) error { return result.Err }, Succeed()),
		WithTransform(func(result IPResult) []string { return result.IPs }, ipsMatcher),
	))
}

func BeRestarted(oldUID types.UID) gomegatypes.GomegaMatcher {
	return gstruct.PointTo(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
		"ObjectMeta": gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
			"UID": Not(Equal(oldUID)),
		}),
		"Status": gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
			"Phase": Equal(kubevirtv1.Running),
		}),
	}))
}

func BeCreated() gomegatypes.GomegaMatcher {
	return gstruct.PointTo(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
		"Status": gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
			"Created": BeTrue(),
		}),
	}))
}

func BeReady() gomegatypes.GomegaMatcher {
	return gstruct.PointTo(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
		"Status": gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
			"Ready": BeTrue(),
		}),
	}))
}

func ContainConditionVMIReady() gomegatypes.GomegaMatcher {
	return WithTransform(vmiStatusConditions,
		ContainElement(SatisfyAll(
			HaveField("Type", kubevirtv1.VirtualMachineInstanceReady),
			HaveField("Status", corev1.ConditionTrue),
		)))
}
