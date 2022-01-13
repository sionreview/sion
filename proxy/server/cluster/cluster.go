package cluster

import (
	"errors"

	"github.com/sionreview/sion/proxy/lambdastore"
	"github.com/sionreview/sion/proxy/server/metastore"
)

var (
	ErrUnsupported   = errors.New("unsupported")
	ErrClusterClosed = errors.New("err cluster closed")
)

type Cluster interface {
	lambdastore.InstanceManager
	lambdastore.Relocator
	metastore.ClusterManager

	Start() error
	WaitReady()
	GetPlacer() metastore.Placer
	CollectData()
	Close()
}
