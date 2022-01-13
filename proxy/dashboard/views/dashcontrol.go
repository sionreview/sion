package views

import "github.com/sionreview/sion/proxy/types"

type DashControl interface {
	Quit(string)
	GetOccupancyMode() types.InstanceOccupancyMode
}
