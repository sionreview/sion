package metastore

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sionreview/sion/proxy/lambdastore"
)

var _ = Describe("Placer", func() {
	It("should test chunk detect oversize", func() {
		placer := &DefaultPlacer{}

		ins := &lambdastore.Instance{}
		ins.ResetCapacity(1536000000, 1232400000)
		ins.Meta.IncreaseSize(1190681458)

		Expect(placer.testChunk(ins, 0)).To(Equal(true))
	})
})
