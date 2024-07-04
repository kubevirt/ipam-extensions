package ipamclaim

import "fmt"

func GenerateName(vmName, networkName string) string {
	return fmt.Sprintf("%s.%s", vmName, networkName)
}
