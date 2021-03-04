import (
	"fmt"
	"net/http"
	"sort"
	"testing"
	"github.com/microsoft/hivedscheduler/pkg/api"

	core "k8s.io/api/core/v1"
	"github.com/microsoft/hivedscheduler/pkg/internal"
	"github.com/microsoft/hivedscheduler/pkg/common"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type hivedAlgorithmTester interface {
	SchedulePod(podName string, pss v2.api.PodSchedulingSpec, isDryRun bool)
	AssertPodScheduleSucceed(podName string, psr internal.PodScheduleResult)
	AssertPodScheduleFail(podName string)

	SetAllNodesToHealthy()
	SetAllNodesToBad()
	SetNodeToBad(nodeName string)
	SetAllNodesToHealthy(nodeName string)

	ExecuteCasesFromYaml(yamlFilename string)
}


func (a, b, c)

func (a=x, b=y, c=z)

func (a, b, c)

- method: SchedulePod
  parameters:
  - pod1
  - VirtualCluster: vc1
    Priority:             0,
    LazyPreemptionEnable: true,
	PinnedCellId:         "",
	LeafCellType:         "DGX2-V100",
	LeafCellNumber:       1,
	AffinityGroup:
		Name:    "group1",
		Members:
		- PodNumber: 1
		- LeafCellNumber: 1
   - false
- method: AssertPodScheduleSucceed
  parameters:
  - pod1
  - psr:
  ……
……