// MIT License
//
// Copyright (c) Microsoft Corporation. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE

package algorithm

import (
	"fmt"
	"sort"

	"github.com/microsoft/hivedscheduler/pkg/api"
	"github.com/microsoft/hivedscheduler/pkg/common"
	"k8s.io/klog"

)

// topologyAwareScheduler can schedule a set of pods on a cluster view.
// It first tries to place pods to nodes with fewer free leaf cells (i.e., packing), while trying to avoid preemptions.
// Then inside each node, it tries to allocate leaf cells with better affinity.
type topologyAwareScheduler struct {
	// a list of nodes (node-level cells or top-level cells that are lower than node level)
	cv clusterView
	// leaf cell number at each level in the cell hierarchy. we use this to
	// calculate the optimal affinity for a given leaf cell number.
	levelLeafCellNum map[CellLevel]int32
	// pack pods cross different priorities, or inside each priority. the former is for intra-VC scheduling,
	// because high-priority can avoid preemption in the whole cluster view,
	// and hence we can pack pods with different priorities.
	// the latter is for opportunistic pod scheduling (stay away from guaranteed pods),
	// because guaranteed pods can avoid preempting opportunistic pods only among buddy cells (this is decided
	// by the buddy cell allocation algorithm).
	crossPriorityPack bool
}

// NewTopologyAwareScheduler initializes the scheduler by extracting node-level cells
// (lower-level if no node-level) from a free cell list.
func NewTopologyAwareScheduler(
	ccl ChainCellList,
	levelLeafCellNum map[CellLevel]int32,
	crossPriorityPack bool) *topologyAwareScheduler {
	return &topologyAwareScheduler{
		cv:                newClusterView(ccl),
		levelLeafCellNum:  levelLeafCellNum,
		crossPriorityPack: crossPriorityPack,
	}
}

func (t *topologyAwareScheduler) Schedule(
	podLeafCellNumbers map[int32]int32,
	p CellPriority,
	suggestedNodes common.Set,
	ignoreSuggestedNodes bool) (
	podPlacements map[int32][]CellList,
	failedReason string) {

	// leaf cell numbers of the pods to schedule
	var sortedPodLeafCellNumbers []int32
	for leafCellNum, podNum := range podLeafCellNumbers {
		for i := int32(0); i < podNum; i++ {
			sortedPodLeafCellNumbers = append(sortedPodLeafCellNumbers, leafCellNum)
		}
	}
	common.SortInt32(sortedPodLeafCellNumbers)
	// sortedPodLeafCellNumbers 是 把所有需要的pod leaf cell number 平摊开来，并排序
	// 如(podNum=2, leafCellNum=3)，（podNum=2, leafCellNum=4）
	// 会变成 [3, 3, 4, 4]
	klog.Infof("Intra-vc schedule, sortedPodLeafCellNumbers: %v", sortedPodLeafCellNumbers)

	// disable preemption first (reduce preemption)
	priority := opportunisticPriority
	// 使用最低优先级去找。
	t.updateClusterView(priority, suggestedNodes, ignoreSuggestedNodes)
	// try to fit the pods to a set of nodes
	// findMpdesForPods根据cv和sortedPodLeafCellNumbers去找
	selectedNodeIndices, failedReason := findNodesForPods(t.cv, sortedPodLeafCellNumbers)
	klog.Infof("First pass findNodesForPods results: %v", selectedNodeIndices)
	// selectedNodeIndices 的 结果的长度和sortedPodLeafCellNumbers 是一致的，如[0, 0, 1, 1] 就表示 sortedPodLeafCellNumbers里的
	// 4 个 pod 分别放在 node 0, 0, 1, 1上
	// enable preemption if scheduling failed
	if selectedNodeIndices == nil && p > opportunisticPriority {
		priority = p
		t.updateClusterView(priority, suggestedNodes, ignoreSuggestedNodes)
		selectedNodeIndices, failedReason = findNodesForPods(t.cv, sortedPodLeafCellNumbers)
	}
	if selectedNodeIndices == nil {
		return nil, failedReason
	}
	// selectedNodeIndices 是 所有被选定的 node，下面在这些 node 中挑选 leaf cell
	// find leaf cells inside the selected node for each pod
	selectedNodes := make(CellList, len(sortedPodLeafCellNumbers))
	for i := 0; i < len(selectedNodeIndices); i++ {
		selectedNodes[i] = t.cv[selectedNodeIndices[i]].c
	}
	selectedLeafCells := CellList{}
	nodeAvailableLeafCells := map[Cell]CellList{}
	podPlacements = map[int32][]CellList{}
	for podIndex := 0; podIndex < len(sortedPodLeafCellNumbers); podIndex++ {
		leafCellNumber := sortedPodLeafCellNumbers[podIndex]
		n := selectedNodes[podIndex]
		// TODO: Optimize findNodesForPods and findLeafCellsInNode together to get a better placement,
		//  such as also aware intra node topology when findNodesForPods.
		selectedLeafCells, nodeAvailableLeafCells[n] = findLeafCellsInNode(n, leafCellNumber, priority, nodeAvailableLeafCells[n], t.levelLeafCellNum)
		if podPlacements[leafCellNumber] == nil {
			podPlacements[leafCellNumber] = []CellList{}
		}
		podPlacements[leafCellNumber] = append(podPlacements[leafCellNumber], selectedLeafCells)
	}
	return podPlacements, ""
}

type node struct {
	c                             Cell            // a node-level cell or a top-level cell that is lower than node level
	freeLeafCellNumAtPriority     int32           // free leaf cell number at the priority of the pod to be scheduled (lower priority considered as free)
	usedLeafCellNumSamePriority   int32           // leaf cell number used by the same priority as that of the pod to be scheduled
	usedLeafCellNumHigherPriority int32           // leaf cell number used by higher priorities than that of the pod to be scheduled
	healthy                       bool            // if the node is healthy
	suggested                     bool            // if the node is within suggested nodes
	nodeAddress                   api.CellAddress // used for logging the node address when bad or not suggested
}

// When cross-priority packing is not enabled, we count the leaf cell numbers used by the current
// priority (n.usedLeafCellNumSamePriority), and the higher priorities (n.usedLeafCellNumHigherPriority), respectively.
// When sorting the nodes, nodes with higher usedLeafCellNumSamePriority and lower usedLeafCellNumHigherPriority
// will be preferred (i.e., pack pods inside the same priority, and stay from higher priorities).
// Note that in this case, the nodes may NOT be ordered in term of total used leaf cell number,
// which may result in feasible pod placements being not found.
//
// Otherwise, n.usedLeafCellNumSamePriority is set to the total used leaf cell number,
// so that nodes with more used leaf cells will be preferred (i.e., pack pods globally across priorities).
// In this case a feasible pod placement is guaranteed to be found (as long as all nodes are in suggested nodes).
func (n *node) updateUsedLeafCellNumForPriority(p CellPriority, crossPriorityPack bool) {
	// GetUsedLeafCellNumAtPriorities() 返回的是 priority -> leaf cell count的一个map
	// usedLeafCellNumSamePriority 就 直接等于对应 priority 的 使用数
	klog.Infof("updateUsedLeafCellNumForPriority node %v pod priority: %v crossPriorityPack: %v", n.c.GetAddress(), p, crossPriorityPack)
	klog.Infof("updateUsedLeafCellNumForPriority returned UsedLeafCellNumAtPriorities", n.c.GetUsedLeafCellNumAtPriorities())
 	n.usedLeafCellNumSamePriority = n.c.GetUsedLeafCellNumAtPriorities()[p]
	// 做初始化，下面计算
	n.usedLeafCellNumHigherPriority = 0
	n.freeLeafCellNumAtPriority = n.c.GetTotalLeafCellNum()
	// crossPriorityPack 看代码目前都是true
	for priority, num := range n.c.GetUsedLeafCellNumAtPriorities() {
		if crossPriorityPack {
			if priority != p {
				n.usedLeafCellNumSamePriority += num
			}
		} else if priority > p {
			n.usedLeafCellNumHigherPriority += num
		}
		if priority >= p {
			n.freeLeafCellNumAtPriority -= num
		}
	}
	klog.Infof("updateUsedLeafCellNumForPriority node %v usedLeafCellNumSamePriority: %v usedLeafCellNumHigherPriority %v", 
		n.c.GetAddress(), n.usedLeafCellNumSamePriority, n.usedLeafCellNumHigherPriority)
	// 如果crossPriorityPack=true，那么usedLeafCellNumSamePriority实际上是usedLeafCellNum，不管priority是多少
	// 如果crossPriorityPack=false，那么usedLeafCellNumSamePriority还是正常的，和名字一致的含义：当前priority有多少used leaf cell num
	// usedLeafCellNumHigherPriority 表示比当前 priority 严格更高的 priority 的job 用了多少leafcell
	// freeLeafCellNumAtPriority 表示空闲 leaf cell number  + 严格小于当前 priority 的 job 用了多少leaf cell
}

type clusterView []*node

func newClusterView(ccl ChainCellList) clusterView {
	var l CellLevel
	// TODO: currently if a top-level cell is lower than node level, it will be considered as a single node.
	// For example, 2 single leaf-level cells are considered as 2 nodes each with 1 leaf cell.
	// We cannot merge them because the 2 cells might be mapped to different physical nodes.
	// We plan to support using multiple cells in a best-effort manner (for example, schedule a 2-leaf-cell pod
	// on 2 1-leaf-cell cells, if we can find 2 1-leaf-cell cells that can be mapped to the same physical node).
	for l = CellLevel(1); l <= CellLevel(len(ccl)); l++ {
		if ccl[l][0].AtOrHigherThanNode() {
			break
		}
	}
	cv := clusterView{}
	for ; l >= lowestLevel; l-- {
		for _, c := range ccl[l] {
			// 每个node，找不高于node level的父亲
			// 要么找到node level，要么找到最top level
			// 这里感觉还是有很多重复计算的
			if !cv.containsCell(ancestorNoHigherThanNode(c)) {
				cv = append(cv, &node{c: c})
			}
		}
	}
	// cv就是包含了所有node和top-level的cell（比node低）
	return cv
}

// ancestorNoHigherThanNode finds an ancestor at a level no higher than node level for a cell.
// If the input cell is at node (or higher) level, will return the cell itself.
func ancestorNoHigherThanNode(c Cell) Cell {
	if c.AtOrHigherThanNode() || c.GetParent() == nil {
		return c
	} else {
		return ancestorNoHigherThanNode(c.GetParent())
	}
}

func (cv clusterView) containsCell(c Cell) bool {
	for _, n := range cv {
		if CellEqual(c, n.c) {
			return true
		}
	}
	return false
}

// Methods for sorting nodes in a clusterView.
func (cv clusterView) Len() int {
	return len(cv)
}

// We sort the nodes in decreasing significance of:
// (1) if the node is healthy (avoid unhealthy),
// (2) if the node is suggested (avoid non-suggested),
// (3) usedLeafCellNumSamePriority (more is preferred),
// (4) usedLeafCellNumHigherPriority (less is preferred).
func (cv clusterView) Less(i int, j int) bool {
	if cv[i].healthy != cv[j].healthy {
		return cv[i].healthy
	} else if cv[i].suggested != cv[j].suggested {
		return cv[i].suggested
	} else if cv[i].usedLeafCellNumSamePriority > cv[j].usedLeafCellNumSamePriority {
		return true
	} else if cv[i].usedLeafCellNumSamePriority < cv[j].usedLeafCellNumSamePriority {
		return false
	} else if cv[i].usedLeafCellNumHigherPriority < cv[j].usedLeafCellNumHigherPriority {
		return true
	} else {
		return false
	}
}

func (cv clusterView) Swap(i int, j int) {
	cv[i], cv[j] = cv[j], cv[i]
}

// updateClusterView updates the leaf cell numbers of the nodes for the sorting.
func (t *topologyAwareScheduler) updateClusterView(
	p CellPriority,
	suggestedNodes common.Set,
	ignoreSuggestedNodes bool) {
	klog.Infof("updateClusterView priority: %v crossPriorityPack: %v", p, t.crossPriorityPack)

	for _, n := range t.cv {
		// 根据priority去update每个node-level cell的view
		n.updateUsedLeafCellNumForPriority(p, t.crossPriorityPack)
		// update每个node-level cell是否是suggested的
		n.healthy, n.suggested, n.nodeAddress = nodeHealthyAndInSuggested(n, suggestedNodes, ignoreSuggestedNodes)
	}
}

func nodeHealthyAndInSuggested(
	n *node,
	suggestedNodes common.Set,
	ignoreSuggestedNodes bool) (
	healthy bool,
	suggested bool,
	addr api.CellAddress) {

	switch v := n.c.(type) {
	case *PhysicalCell:
		nodeNames, _ := v.GetPhysicalPlacement()
		return v.IsHealthy(),
			ignoreSuggestedNodes || suggestedNodes.Contains(nodeNames[0]),
			n.c.GetAddress()
	case *VirtualCell:
		if pn := v.GetPhysicalCell(); pn != nil {
			nodeNames, _ := pn.GetPhysicalPlacement()
			return pn.IsHealthy(),
				ignoreSuggestedNodes || suggestedNodes.Contains(nodeNames[0]),
				pn.GetAddress()
		}
	}
	return true, true, ""
}

// findNodesForPods finds a set of nodes that can accommodate the leaf cell requirements of the pods.
func findNodesForPods(cv clusterView, leafCellNums []int32) (pickedNodeIndices []int32, failedReason string) {
	// sort the nodes according to leaf cell numbers in each node.
	// this is achieved through the Less method defined in type clusterView.
	// TODO: Ensure Opportunistic Pods also can always can find the solution, regardless of
	//  the iteration order.
	//  For example:
	//   1. clusterView = 2-leaf-cell Node, 1-leaf-cell Node
	//   2. leafCellNums = 1-leaf-cell Pod, 2-leaf-cell Pod
	//   First 1-leaf-cell Pod may allocate to 2-leaf-cell Node, but the latter pod cannot be fitted anymore.
	// 这里的排序要参考上面的函数Less
	// 优先选择：Healthy的、Suggested的、usedLeafCellNumSamePriority大的、usedLeafCellNumHigherPriority小的
	// 结合：
	//    如果crossPriorityPack=true，那么usedLeafCellNumSamePriority实际上是usedLeafCellNum，不管priority是多少
	//    如果crossPriorityPack=false，那么usedLeafCellNumSamePriority还是正常的，和名字一致的含义：当前priority有多少used leaf cell num
	// 可知crossPriorityPack=true时，就是尽量找那些已经有任务的node，在此之上，远离更高优先级的任务
	// crossPriorityPack=false时，尽量找和已经有和当前任务priority一样任务的node 
	// 看代码 正常job的crossPriorityPack 目前都是true，oppo job的
	// 在正常job， crossPriorityPack = true的情况下：
	//     第一遍， priority 会设置为 -1，此时usedLeafCellNumSamePriority = 所有priority的任务占用的leaf cell number；
	//     usedLeafCellNumHigherPriority = 非 oppo 任务占用的leaf cell number; 这一遍不会抢占任何任务。
	//     第二遍， priority 是正常值，此时 usedLeafCellNumSamePriority = 所有priority的任务占用的leaf cell number
	//     usedLeafCellNumHigherPriority = 比当前priority高的任务占用的leaf cell number;
	//  此外，oppo job是不分vc进行schedule的, crossPriorityPack = false 意味着usedLeafCellNumSamePriority是node上oppo job的个数，
	//  usedLeafCellNumHigherPriority 是其他所有job的个数
	sort.Stable(cv)
	// 输出结果
	klog.Infof("findNodesForPods leaf cell nums %v", leafCellNums)
	for _, node := range cv {
		klog.Infof("findNodesForPods, sorted node: %v address: %v usedLeafCellNumSamePriority (usedLeafCellNum): %v usedLeafCellNumHigherPriority: %v", 
			node.c.GetAddress(), node.nodeAddress, node.usedLeafCellNumSamePriority, node.usedLeafCellNumHigherPriority)
	}
	pickedNodeIndices = make([]int32, len(leafCellNums)) // indices of the currently picked nodes
	podIndex := 0
	pickedLeafCellNum := int32(0)
	var n *node
	for nodeIndex := 0; nodeIndex < len(cv); {
		n = cv[nodeIndex]
		// freeLeafCellNumAtPriority是去除了大于等于当前priority任务后，当前node剩余的leaf Cell Num，相当于是当前完全free的 + 可以通过preemption变成free的
		// pickedLeafCellNum是在当前node已经选了多少leafCell
		// 注意上面的for循环中的nodeIndex是不会自增的
		if n.freeLeafCellNumAtPriority-pickedLeafCellNum >= leafCellNums[podIndex] {
			// fail when encountering a node that is either bad or not within suggested nodes
			if !n.healthy {
				return nil, fmt.Sprintf(
					"have to use at least one bad node %v", n.nodeAddress)
			}
			if !n.suggested {
				return nil, fmt.Sprintf(
					"have to use at least one non-suggested node %v", n.nodeAddress)
			}
			pickedNodeIndices[podIndex] = int32(nodeIndex)
			pickedLeafCellNum += leafCellNums[podIndex]
			podIndex++
			if podIndex == len(leafCellNums) {
				return pickedNodeIndices, ""
			}
		} else {
			// 自增nodeIndex
			pickedLeafCellNum = 0
			nodeIndex++
		}
	}
	return nil, "insufficient capacity"
}

// findLeafCellsInNode finds a set of leaf cells with the best affinity in a node for a pod.
func findLeafCellsInNode(
	n Cell,
	leafCellNum int32,
	p CellPriority,
	availableLeafCells CellList,
	levelLeafCellNum map[CellLevel]int32) (CellList, CellList) {

	// levelLeafCellNum 是对这个cell chain来说的
	// 表示每个 level 上会有几个 leaf cell
	// 1 一定对应 1，就是底层的
	// 如果一个chain只包含node，且一个node定义了switch、socket的8gpu node就是[1:1 2:2 3:4 4:8]
	// 如果node不定义switch和socket，那就是[1:1 2:8]

	// 这里affinity的定义：给定leaf cell的位置，这些leaf cell的最近公共祖先的Cell level (cell level的底部是1，依次累加)
	// 希望能找到最低的affinity

	// 注意是对同一个pod去找，不同pod依次考虑
	// 例如node的level是[1:1 2:2 3:4 4:8]，index 0 gpu被占
	// 如果是 1 个 2 gpu pod，则会占到 2,3 卡。因为 2 ，3卡的affinity level比 1,2低。
	// 但如果是两个 1 gpu pod，则会占到 1，2 卡，因为它们是分别考虑

	klog.Infof("findLeafCellsInNode cell: %v leafCellNum: %v priority: %v availableLeafCells: %v levelLeafCellNum: %v",
		n.GetAddress(), leafCellNum, p, availableLeafCells, levelLeafCellNum)
	// indices of the currently picked leaf cells
	currentLeafCellIndices := make([]int32, leafCellNum)
	// affinity of the currently picked leaf cells, defined as the lowest common ancestor
	// of the leaf cells in the cell hierarchy (lower level means better affinity)
	currentAffinity := make(CellList, leafCellNum)
	// leaf cells with the best affinity ever seen
	bestAffinityLeafCells := make(CellList, leafCellNum)
	// indices of the leaf cells with the best affinity ever seen
	bestAffinityLeafCellIndices := make([]int32, leafCellNum)
	// the best affinity ever seen (i.e., lowest level of lowest common ancestor of a set of leaf cells)
	bestAffinity := highestLevel
	// the optimal affinity for the leaf cell number, i.e., the lowest possible of the lowest common ancestor of leaf cells
	// 理论上的最佳affinity
	optimalAffinity := getOptimalAffinity(leafCellNum, levelLeafCellNum)

	// 如果没有availableLeafCells的话，就分从node里取（这个发生在第一次，以后就不用取了）
	if availableLeafCells == nil {
		availableLeafCells = CellList{}
		preemptibleLeafCells := CellList{}
		availableLeafCells, preemptibleLeafCells = getLeafCellsFromNode(n, p, availableLeafCells, preemptibleLeafCells)
		// free leaf cells will be used first (before preemptible leaf cells)
		availableLeafCells = append(availableLeafCells, preemptibleLeafCells...)
	}
	// 这个availableLeafCells就是所有free或可以preempte的leaf cell的数组
	// 其中free cell一定在前面
	// 算法先不具体看了
	klog.Infof("findLeafCellsInNode real availableLeafCells: %v", availableLeafCells)
	availableLeafCellIndex := int32(0)
	searchLeafCellIndex := int32(0)
	var leafCell Cell
	for {
		for availableLeafCellIndex < int32(len(availableLeafCells)) {
			leafCell = availableLeafCells[availableLeafCellIndex]
			currentLeafCellIndices[searchLeafCellIndex] = availableLeafCellIndex
			if searchLeafCellIndex == 0 {
				currentAffinity[searchLeafCellIndex] = leafCell
			} else {
				currentAffinity[searchLeafCellIndex] = findLCA(leafCell, currentAffinity[searchLeafCellIndex-1])
				// pruning: if the current LCA has been higher than the lowest ever,
				// the node will be skipped
				if (currentAffinity[searchLeafCellIndex] == nil && bestAffinity < highestLevel) ||
					(currentAffinity[searchLeafCellIndex] != nil && currentAffinity[searchLeafCellIndex].GetLevel() > bestAffinity) {
					availableLeafCellIndex++
					continue
				}
			}
			if searchLeafCellIndex == leafCellNum-1 {
				foundOptimalAffinity := false
				bestAffinity, foundOptimalAffinity = checkCurrentLeafCells(
					currentAffinity[len(currentAffinity)-1].GetLevel(),
					availableLeafCells,
					currentLeafCellIndices,
					bestAffinity,
					bestAffinityLeafCells,
					bestAffinityLeafCellIndices,
					optimalAffinity)
				if foundOptimalAffinity {
					// early stop: return if the solution is optimal (i.e., all buddies)
					availableLeafCells = removePickedLeafCells(availableLeafCells, bestAffinityLeafCellIndices)
					return bestAffinityLeafCells, availableLeafCells
				}
			} else {
				searchLeafCellIndex++
			}
			availableLeafCellIndex++
		}
		searchLeafCellIndex--
		if searchLeafCellIndex < 0 {
			if bestAffinity == highestLevel {
				// Unreachable
				panic(fmt.Sprintf("Assert Failure: failed to allocate %v leaf cells in picked node %v", leafCellNum, n.GetAddress()))
			}
			availableLeafCells = removePickedLeafCells(availableLeafCells, bestAffinityLeafCellIndices)
			return bestAffinityLeafCells, availableLeafCells
		}
		availableLeafCellIndex = currentLeafCellIndices[searchLeafCellIndex] + 1
	}
}

// getOptimalAffinity calculates the optimal affinity for a given leaf cell number.
func getOptimalAffinity(leafCellNum int32, levelLeafCellNum map[CellLevel]int32) CellLevel {
	for l := CellLevel(1); l <= CellLevel(len(levelLeafCellNum)); l++ {
		if levelLeafCellNum[l] >= leafCellNum {
			return l
		}
	}

	// Unreachable
	panic(fmt.Sprintf("Assert Failure: pod allocated a node but exceeds the capacity of the current chain"))
}

// checkCurrentLeafCells checks if the currently picked leaf cells have the lowest LCA. It also checks if the solution
// is optimal (if the leaf cells are all buddies).
func checkCurrentLeafCells(
	affinity CellLevel,
	leafCells CellList,
	currentIndices []int32,
	bestAffinity CellLevel,
	bestAffinityLeafCells CellList,
	bestAffinityLeafCellIndices []int32,
	optimalAffinity CellLevel) (CellLevel, bool) {

	if affinity < bestAffinity {
		copy(bestAffinityLeafCellIndices, currentIndices)
		for i := 0; i < len(currentIndices); i++ {
			bestAffinityLeafCells[i] = leafCells[currentIndices[i]]
		}
		if affinity == optimalAffinity {
			return affinity, true
		} else {
			return affinity, false
		}
	}
	return bestAffinity, false
}

// removePickedLeafCells remove picked leaf cells from the available leaf cell list.
func removePickedLeafCells(leafCells CellList, indices []int32) CellList {
	for i, index := range indices {
		offset := int32(i)
		if i < len(indices)-1 {
			nextIndex := indices[i+1]
			copy(leafCells[index-offset:nextIndex-offset-1], leafCells[index+1:nextIndex])
		} else {
			copy(leafCells[index-offset:], leafCells[index+1:])
		}
	}
	for i := len(leafCells) - len(indices); i < len(leafCells); i++ {
		leafCells[i] = nil
	}
	return leafCells[:len(leafCells)-len(indices)]
}

// findLCA finds the lowest common ancestor of two cells (nil if they have no LCA).
func findLCA(lower Cell, higher Cell) Cell {
	for lower.GetLevel() < higher.GetLevel() {
		if lower.GetParent() == nil {
			return nil
		}
		lower = lower.GetParent()
	}
	if CellEqual(lower, higher) {
		return lower
	}
	for !CellEqual(lower.GetParent(), higher.GetParent()) {
		if lower.GetParent() == nil || higher.GetParent() == nil {
			return nil
		}
		lower = lower.GetParent()
		higher = higher.GetParent()
	}
	return lower.GetParent()
}

// getLeafCellsFromNode collects free leaf cells and preemptible leaf cells according to the priority.
func getLeafCellsFromNode(c Cell, p CellPriority, freeLeafCells CellList, preemptibleLeafCells CellList) (CellList, CellList) {
	if c.GetLevel() > 1 {
		for _, cc := range c.GetChildren() {
			freeLeafCells, preemptibleLeafCells = getLeafCellsFromNode(cc, p, freeLeafCells, preemptibleLeafCells)
		}
	} else if c.GetPriority() == freePriority {
		freeLeafCells = append(freeLeafCells, c)
	} else if c.GetPriority() < p {
		preemptibleLeafCells = append(preemptibleLeafCells, c)
	}
	return freeLeafCells, preemptibleLeafCells
}
