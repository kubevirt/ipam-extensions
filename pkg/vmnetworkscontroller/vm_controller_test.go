package vmnetworkscontroller

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VM Controller test suite")
}

var _ = Describe("VM IPAM controller", Serial, func() {

})
