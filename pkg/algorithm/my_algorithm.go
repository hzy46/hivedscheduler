package algorithm

import (
	"fmt"
	// "net/http"
	"sort"
	// "testing"
	"github.com/microsoft/hivedscheduler/pkg/api"

	core "k8s.io/api/core/v1"
	"github.com/microsoft/hivedscheduler/pkg/internal"
	"github.com/microsoft/hivedscheduler/pkg/common"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func sortChains(chains []CellChain) {
	var chainsTemp []string
	for _, c := range chains {
		chainsTemp = append(chainsTemp, string(c))
	}
	sort.Strings(chainsTemp)
	for i := range chains {
		chains[i] = CellChain(chainsTemp[len(chainsTemp)-i-1])
	}
}


func setHealthyNodes(h *HivedAlgorithm) {
	for _, ccl := range h.fullCellList {
		for _, c := range ccl[CellLevel(len(ccl))] {
			nodes, _ := c.(*PhysicalCell).GetPhysicalPlacement()
			for _, n := range nodes {
				h.setHealthyNode(n)
			}
		}
	}
}

func getAllNodes(h *HivedAlgorithm) []string {
	var allNodes []string
	for _, ccl := range h.fullCellList {
		for _, c := range ccl[CellLevel(len(ccl))] {
			allNodes = append(allNodes, c.(*PhysicalCell).nodes...)
		}
	}
	return allNodes	
}



func initAll(h *HivedAlgorithm) {
	// sort chains of each leaf cell type for stability of the test
	for _, chains := range h.cellChains {
		sortChains(chains)
	}
	setHealthyNodes(h)
	common.InitLogger()
}

func printConfig( h *HivedAlgorithm) {
	fmt.Printf("Cluster PhysicalCells: \n")
	for chain, ccl := range h.fullCellList {
		// 这个fullCellList保存的是所有用户定义的PhysicalCell，相同的会合成一个chain。
		// 例如用户定义了V100-Node、V100-Node、4-V100-Node，保存的时候会变成两个Chain，Chain的名字就是V100-Node和4-V100-Node
		// 实际的数据类型是map[CellChain]map[CellLevel]CellList
		// level从1开始往上，1是最底层，即leafcell。
		fmt.Printf("%v\n", chain)
		fmt.Printf("%v\n", ccl)
	}
	fmt.Printf("Virtual Clusters: \n")
	for vc, vcs := range h.vcSchedulers {
		fmt.Printf("\n%v\n", vc)
		// 对于VC来说，Chain也是引用的PhysicalCell中用户给定的名字
		// 相同的Chain会自动合并。
		// 在写VC定义时，有个硬性要求是第一个域是PhysicalCell，实际就表示绑在哪个Physical Chain上。
		// 例如用户在PhysicalCell中定义4-V100-Node，在VC里面写4-V100-Node.V100-Node和4-V100-Node.V100-Node.V100-CPU-Socket，
		// 实际上这两个都是归属于4-V100-Node这个Chain的。
		// 另外，就算Physical Cell的底层是一样的，但他们也是不同的chain。
		// 如写 4-V100-Node.V100-Node和3-V100-Node.V100-Node，虽然底层是一样的，但实际是在不同的Chain
		// 在VC中，保留的是逻辑上的Cell。如 VC1/9。
		for chain, ccl := range vcs.getNonPinnedFullCellList() {
			fmt.Printf("%v\n", chain)
			fmt.Printf("%v", ccl)
		}
		// Pinned Cell就比较简单
		fmt.Printf("Pinned cells\n")
		for pid, ccl := range vcs.getPinnedCells() {
			fmt.Printf(string(pid))
			fmt.Printf("%v\n", ccl)
		}
	}
	fmt.Printf("\n")
	fmt.Printf("LeafCells and Chains\n")
	for leafCellType, chains := range h.cellChains {
		fmt.Printf("LeafCell %v: Chains %v \n", leafCellType, chains)
	}
	fmt.Printf("\n")
}


func printPsr(psr *internal.PodScheduleResult) {
	if psr.PodWaitInfo != nil {
		fmt.Printf("Psr PodWaitInfo: %v", psr.PodWaitInfo)
	}
	if psr.PodPreemptInfo != nil {
		var nameList []string
		for _, pod := range psr.PodPreemptInfo.VictimPods {
			nameList = append(nameList, pod.ObjectMeta.Name)
		}
		fmt.Printf("Psr PodPreemptInfo: { VictimPods: %v }", nameList)
	}
	if psr.PodBindInfo != nil {
		fmt.Printf("Psr PodBindInfo: { Node: %v, LeafCellIsolation: %v, CellChain: %v }\n", 
			psr.PodBindInfo.Node, psr.PodBindInfo.LeafCellIsolation, psr.PodBindInfo.CellChain)
	}
}


func test1Schedule1( h *HivedAlgorithm, allNodes []string) {
	task := "test1Schedule1"
	groupName := task + "-group"
	podName := task + "-pod"
	group := &api.AffinityGroupSpec{
		Name:    groupName,
		Members: []api.AffinityGroupMemberSpec{{PodNumber: 1, LeafCellNumber: 1}},
	}
	si := common.ToYaml(api.PodSchedulingSpec{
		VirtualCluster:       "VC1",
		Priority:             0,
		LazyPreemptionEnable: true,
		PinnedCellId:         "",
		LeafCellType:         "DGX2-V100",
		LeafCellNumber:       1,
		AffinityGroup:        group,
	})
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        podName,
			Namespace:   "test",
			UID:         types.UID(podName),
			Annotations: map[string]string{ "hivedscheduler.microsoft.com/pod-scheduling-spec": si },
		},
	}
	psr := h.Schedule(pod, allNodes, internal.PreemptingPhase)
	if psr.PodBindInfo != nil {
		allocatedPod := internal.NewBindingPod(pod, psr.PodBindInfo)
		h.AddAllocatedPod(allocatedPod)
	}
	fmt.Printf("%v\n", task)
	printPsr(&psr)
	fmt.Print("\n")
}

func test1Schedule2( h *HivedAlgorithm, allNodes []string) {
	task := "test1Schedule2"
	groupName := task + "-group"
	podName := task + "-pod"
	group := &api.AffinityGroupSpec{
		Name:    groupName,
		Members: []api.AffinityGroupMemberSpec{{PodNumber: 1, LeafCellNumber: 1}},
	}
	si := common.ToYaml(api.PodSchedulingSpec{
		VirtualCluster:       "VC1",
		Priority:             1,
		LazyPreemptionEnable: true,
		PinnedCellId:         "",
		LeafCellType:         "DGX2-V100",
		LeafCellNumber:       1,
		AffinityGroup:        group,
	})
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        podName,
			Namespace:   "test",
			UID:         types.UID(podName),
			Annotations: map[string]string{ "hivedscheduler.microsoft.com/pod-scheduling-spec": si },
		},
	}
	psr := h.Schedule(pod, allNodes, internal.PreemptingPhase)
	if psr.PodBindInfo != nil {
		allocatedPod := internal.NewBindingPod(pod, psr.PodBindInfo)
		h.AddAllocatedPod(allocatedPod)
	}
	fmt.Printf("%v\n", task)
	printPsr(&psr)
	fmt.Print("\n")
}


func test1Schedule3( h *HivedAlgorithm, allNodes []string) {
	task := "test1Schedule3"
	groupName := task + "-group"
	podName := task + "-pod"
	group := &api.AffinityGroupSpec{
		Name:    groupName,
		Members: []api.AffinityGroupMemberSpec{{PodNumber: 1, LeafCellNumber: 8}},
	}
	si := common.ToYaml(api.PodSchedulingSpec{
		VirtualCluster:       "VC1",
		Priority:             2,
		LazyPreemptionEnable: true,
		PinnedCellId:         "",
		LeafCellType:         "DGX2-V100",
		LeafCellNumber:       8,
		AffinityGroup:        group,
	})
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        podName,
			Namespace:   "test",
			UID:         types.UID(podName),
			Annotations: map[string]string{ "hivedscheduler.microsoft.com/pod-scheduling-spec": si },
		},
	}
	psr := h.Schedule(pod, allNodes, internal.PreemptingPhase)
	if psr.PodBindInfo != nil {
		allocatedPod := internal.NewBindingPod(pod, psr.PodBindInfo)
		h.AddAllocatedPod(allocatedPod)
	}
	fmt.Printf("%v\n", task)
	printPsr(&psr)
	fmt.Print("\n")
}

func test1Schedule4( h *HivedAlgorithm, allNodes []string) {
	task := "test1Schedule4"
	groupName := task + "-group"
	podName := task + "-pod"
	group := &api.AffinityGroupSpec{
		Name:    groupName,
		Members: []api.AffinityGroupMemberSpec{{PodNumber: 1, LeafCellNumber: 1}},
	}
	si := common.ToYaml(api.PodSchedulingSpec{
		VirtualCluster:       "VC1",
		Priority:             -1,
		LazyPreemptionEnable: true,
		PinnedCellId:         "",
		LeafCellType:         "DGX2-V100",
		LeafCellNumber:       1,
		AffinityGroup:        group,
	})
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        podName,
			Namespace:   "test",
			UID:         types.UID(podName),
			Annotations: map[string]string{ "hivedscheduler.microsoft.com/pod-scheduling-spec": si },
		},
	}
	psr := h.Schedule(pod, allNodes, internal.PreemptingPhase)
	if psr.PodBindInfo != nil {
		allocatedPod := internal.NewBindingPod(pod, psr.PodBindInfo)
		h.AddAllocatedPod(allocatedPod)
	}
	fmt.Printf("%v\n", task)
	printPsr(&psr)
	fmt.Print("\n")
}


func test1Schedule5_1( h *HivedAlgorithm, allNodes []string) {
	task := "test1Schedule5"
	groupName := task + "-group"
	podName := task + "-pod1"
	group := &api.AffinityGroupSpec{
		Name:    groupName,
		Members: []api.AffinityGroupMemberSpec{{PodNumber: 2, LeafCellNumber: 16}},
	}
	si := common.ToYaml(api.PodSchedulingSpec{
		VirtualCluster:       "VC1",
		Priority:             1,
		LazyPreemptionEnable: true,
		PinnedCellId:         "VC1-YQW-DGX2",
		LeafCellType:         "DGX2-V100",
		LeafCellNumber:       16,
		AffinityGroup:        group,
	})
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        podName,
			Namespace:   "test",
			UID:         types.UID(podName),
			Annotations: map[string]string{ "hivedscheduler.microsoft.com/pod-scheduling-spec": si },
		},
	}
	psr := h.Schedule(pod, allNodes, internal.PreemptingPhase)
	if psr.PodBindInfo != nil {
		allocatedPod := internal.NewBindingPod(pod, psr.PodBindInfo)
		h.AddAllocatedPod(allocatedPod)
	}
	fmt.Printf("%v\n", task)
	printPsr(&psr)
	fmt.Print("\n")
}



func test1Schedule5_2( h *HivedAlgorithm, allNodes []string) {
	task := "test1Schedule5"
	groupName := task + "-group"
	podName := task + "-pod2"
	group := &api.AffinityGroupSpec{
		Name:    groupName,
		Members: []api.AffinityGroupMemberSpec{{PodNumber: 2, LeafCellNumber: 16}},
	}
	si := common.ToYaml(api.PodSchedulingSpec{
		VirtualCluster:       "VC1",
		Priority:             1,
		LazyPreemptionEnable: true,
		PinnedCellId:         "VC1-YQW-DGX2",
		LeafCellType:         "DGX2-V100",
		LeafCellNumber:       16,
		AffinityGroup:        group,
	})
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        podName,
			Namespace:   "test",
			UID:         types.UID(podName),
			Annotations: map[string]string{ "hivedscheduler.microsoft.com/pod-scheduling-spec": si },
		},
	}
	psr := h.Schedule(pod, allNodes, internal.PreemptingPhase)
	if psr.PodBindInfo != nil {
		allocatedPod := internal.NewBindingPod(pod, psr.PodBindInfo)
		h.AddAllocatedPod(allocatedPod)
	}
	fmt.Printf("%v\n", task)
	printPsr(&psr)
	fmt.Print("\n")
}





func MyTest1(configFilePath string) {
	sConfig := api.NewConfig(api.InitRawConfig(&configFilePath))
	h := NewHivedAlgorithm(sConfig)
	initAll(h)
	allNodes := getAllNodes(h)
	printConfig(h)
	// 搜索chain（这个chain是随机的，应该不能依赖！）
	test1Schedule1(h, allNodes)
	// 同样搜索，放到上面的旁边
	test1Schedule2(h, allNodes)
	// 同一个node 同一个pod的 best affinity
	test1Schedule3(h, allNodes)
	// oppo job ：尽量和oppojob靠近；会远离现有job
	test1Schedule4(h, allNodes)
	// Pinned Cell 比较简单
	test1Schedule5_1(h, allNodes)
	test1Schedule5_2(h, allNodes)
}
