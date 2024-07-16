package main

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

func FindDefaultGatewayLinks() ([]netlink.Link, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed looking for default gw routes: %w", err)
	}
	selectedLinksMap := map[string]netlink.Link{}
	for _, r := range routes {
		if r.Dst == nil {
			selectedLink, err := netlink.LinkByIndex(r.LinkIndex)
			if err != nil {
				return nil, fmt.Errorf("failed looking for default gw links: %w", err)
			}
			selectedLinksMap[selectedLink.Attrs().Name] = selectedLink
		}
	}
	selectedLinks := []netlink.Link{}
	for _, selectedLink := range selectedLinksMap {
		selectedLinks = append(selectedLinks, selectedLink)
	}
	return selectedLinks, nil
}

func main() {
	links, err := FindDefaultGatewayLinks()
	if err != nil {
		panic(err)
	}
	fmt.Println(links)
}
