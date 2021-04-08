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
	apiv2 "github.com/microsoft/hivedscheduler/pkg/api/v2"
)

// skuCell type for selected level cell in virtual cluster view
type skuCell struct {
	cell                          Cell            // within cell level, maybe higher or lower than node level
	freeLeafCellNumAtPriority     int32           // free leaf cell number at the priority of the pod to be scheduled (lower priority considered as free)
	usedLeafCellNumAtPriority     int32           // used leaf cell number at the priority of the pod to be scheduler
	usedLeafCellNumHigherPriority int32           // used leaf cell number by higher priorities than the pod to be scheduler
	healthy                       bool            // if the cell is healthy
	address                       api.CellAddress // used for logging the cell address when bad or not suggested
}

// virtual cluster view
type skuClusterView []*skuCell

// skuScheduler can schedule a pod group of pods with arbitrary cell types on a cluster view.
// It first tries to place pod group without preemption, then enable preemption if schedule failed.
// For each try, it will find within cells for each pod group, then find cells for each pods with better affinity.
type skuScheduler struct {
	// cell list for each level in a chain.
	chainCellList ChainCellList
	// leaf cell number at each level in the cell hierarchy. we use this to
	// calculate the optimal affinity for a given leaf cell number.
	levelLeafCellNum map[CellLevel]int32
	// cell type to cell level in a chain.
	cellLevels map[api.CellType]CellLevel
	// pack pods cross different priorities, or inside each priority. the former is for intra-VC scheduling,
	// because high-priority can avoid preemption in the whole cluster view,
	// and hence we can pack pods with different priorities.
	// the latter is for opportunistic pod scheduling (stay away from guaranteed pods),
	// because guaranteed pods can avoid preempting opportunistic pods only among buddy cells (this is decided
	// by the buddy cell allocation algorithm).
	crossPriorityPack bool
}

// NewSkuScheduler initializes the scheduler
func NewSkuScheduler(
	chainCellList ChainCellList,
	levelLeafCellNum map[CellLevel]int32,
	cellLevels map[api.CellType]CellLevel,
	crossPriorityPack bool) *skuScheduler {

	return &skuScheduler{
		chainCellList:     chainCellList,
		levelLeafCellNum:  levelLeafCellNum,
		cellLevels:        cellLevels,
		crossPriorityPack: crossPriorityPack,
	}
}

func (s *skuScheduler) Schedule(
	podRootGroup *apiv2.PodGroupSpec,
	priority CellPriority) (
	placement PodGroupPlacement,
	failedReason string) {

	// sort pods in descending order by couting leaf cell number
	s.sortPodGroup(podRootGroup)

	// disable preemption first to reduce preemption, try to schedule
	placement, failedReason = s.findCellsForPodGroup(podRootGroup, opportunisticPriority, nil, nil)

	// enable preemption if scheduling failed
	if failedReason != "" && priority > opportunisticPriority {
		placement, failedReason = s.findCellsForPodGroup(podRootGroup, priority, nil, nil)
	}

	// convert cells to leaf cells in placement
	if failedReason == "" {
		for iter := placement.Iterator(); iter.HasNext(); {
			cells, leafCells := iter.Next(), CellList{}
			for _, c := range *cells {
				currLevelCells := CellList{c}
				for currLevelCells[0].GetLevel() > CellLevel(1) {
					childLevelCells := CellList{}
					for _, cc := range currLevelCells {
						childLevelCells = append(childLevelCells, cc.GetChildren()...)
					}
					currLevelCells = childLevelCells
				}
				leafCells = append(leafCells, currLevelCells...)
			}
			*cells = leafCells
		}
	}

	return placement, failedReason
}

func (s *skuScheduler) sortPodGroup(podGroup *apiv2.PodGroupSpec) {
	sort.SliceStable(podGroup.Pods, func(i, j int) bool {
		return s.countLeafCellNums(podGroup.Pods[i]) > s.countLeafCellNums(podGroup.Pods[j])
	})
	sortedPods := []apiv2.PodGroupMemberSpec{}
	for _, p := range podGroup.Pods {
		for i := int32(0); i < p.PodMinNumber; i++ {
			sortedPods = append(sortedPods, p)
		}
	}
	podGroup.Pods = sortedPods

	sort.SliceStable(podGroup.ChildGroups, func(i, j int) bool {
		return s.countLeafCellNums(podGroup.ChildGroups[i]) > s.countLeafCellNums(podGroup.ChildGroups[j])
	})
	for _, g := range podGroup.ChildGroups {
		s.sortPodGroup(g)
	}
}

func (s *skuScheduler) countLeafCellNums(x interface{}) int32 {
	count := int32(0)
	switch p := x.(type) {
	case apiv2.PodGroupMemberSpec:
		count = s.levelLeafCellNum[s.cellLevels[p.CellsPerPod.CellType]] * p.CellsPerPod.CellNumber
	case []apiv2.PodGroupMemberSpec:
		for _, pp := range p {
			count += s.countLeafCellNums(pp)
		}
	case *apiv2.PodGroupSpec:
		count += s.countLeafCellNums(p.Pods) + s.countLeafCellNums(p.ChildGroups)
	case []*apiv2.PodGroupSpec:
		for _, pp := range p {
			count += s.countLeafCellNums(pp)
		}
	}
	return count
}

func (s *skuScheduler) findCellsForPodGroup(
	podGroup *apiv2.PodGroupSpec,
	priority CellPriority,
	within *skuCell,
	allocated *PodGroupPlacement) (
	placement PodGroupPlacement,
	failedReason string) {

	placement, failedReason = PodGroupPlacement{}, ""

	cv := s.createSkuClusterView(within, s.cellLevels[podGroup.WithinOneCell], priority)
	for _, withinCell := range cv {
		if len(podGroup.Pods) > 0 && !withinCell.healthy {
			return PodGroupPlacement{}, fmt.Sprintf(
				"have to use at least one bad cell %v", withinCell.address)
		}
		placement.podsPlacement, failedReason = s.findCellsForPods(podGroup.Pods, priority, withinCell, allocated)
		if failedReason == "" {
			for _, childGroup := range podGroup.ChildGroups {
				childPodsPlacement, childFailedReason := s.findCellsForPodGroup(childGroup, priority, withinCell, &placement)
				if childFailedReason != "" {
					placement.childGroupsPlacement, failedReason = nil, childFailedReason
					break
				}
				if placement.childGroupsPlacement == nil {
					placement.childGroupsPlacement = []*PodGroupPlacement{}
				}
				placement.childGroupsPlacement = append(placement.childGroupsPlacement, &childPodsPlacement)
			}
			if failedReason == "" {
				break
			}
		}
	}
	return placement, failedReason
}

func (s *skuScheduler) findCellsForPods(
	pods []apiv2.PodGroupMemberSpec,
	priority CellPriority,
	within *skuCell,
	allocated *PodGroupPlacement) (
	placement []CellList,
	failedReason string) {

	placement, failedReason = []CellList{}, ""

	allocatedCells := CellList{}
	for iter := allocated.Iterator(); iter.HasNext(); {
		for _, c := range *iter.Next() {
			allocatedCells = append(allocatedCells, c)
		}
	}

	cv := skuClusterView{within}
	nodeLevel := s.getNodeLevel()
	if within.cell.GetLevel() > nodeLevel {
		cv = s.createSkuClusterView(within, nodeLevel, priority)
	}

	withinCellIndex, podIndex := 0, 0
	for podIndex < len(pods) {
		if withinCellIndex >= len(cv) {
			return nil, "insufficient capacity"
		}
		withinCell := cv[withinCellIndex]
		if !withinCell.healthy {
			return nil, fmt.Sprintf(
				"have to use at least one bad cell %v", withinCell.address)
		}
		podPlacement := s.findCellsForPod(pods[podIndex], priority, withinCell, allocatedCells)
		if podPlacement == nil {
			withinCellIndex++
		} else {
			placement = append(placement, podPlacement)
			allocatedCells = append(allocatedCells, podPlacement...)
			podIndex++
		}
	}

	return placement, failedReason
}

func (s *skuScheduler) findCellsForPod(
	pod apiv2.PodGroupMemberSpec,
	priority CellPriority,
	within *skuCell,
	allocatedCells CellList) CellList {

	currLevel := s.cellLevels[pod.CellsPerPod.CellType]
	freeCells, preemptibleCells := CellList{}, CellList{}
	freeCells, preemptibleCells = getFreeCellsAtLevel(
		within.cell, currLevel, priority, allocatedCells, freeCells, preemptibleCells)
	// free leaf cells will be used first (before preemptible leaf cells)
	freeCells = append(freeCells, preemptibleCells...)
	if pod.CellsPerPod.CellNumber > int32(len(freeCells)) {
		return nil
	}

	var freeCell Cell
	freeCellIndex, searchCellIndex := int32(0), int32(0)
	// indices of the currently picked cells
	currentCellIndices := make([]int32, pod.CellsPerPod.CellNumber)
	// affinity of the currently picked cells, defined as the lowest common ancestor
	// of the leaf cells in the cell hierarchy (lower level means better affinity)
	currentAffinity := make(CellList, pod.CellsPerPod.CellNumber)
	// cells with the best affinity ever seen
	bestAffinityCells := make(CellList, pod.CellsPerPod.CellNumber)
	// indices of the cells with the best affinity ever seen
	bestAffinityCellIndices := make([]int32, pod.CellsPerPod.CellNumber)
	// the best affinity ever seen (i.e., lowest level of lowest common ancestor of a set of cells)
	bestAffinity := highestLevel
	// the optimal affinity for the cell number, i.e., the lowest possible of the lowest common ancestor of cells
	optimalAffinity := CellLevel(1)
	for l := CellLevel(currLevel); l <= CellLevel(len(s.levelLeafCellNum)); l++ {
		if s.levelLeafCellNum[l] >= s.levelLeafCellNum[currLevel]*pod.CellsPerPod.CellNumber {
			optimalAffinity = l
			break
		}
	}

	for {
		for freeCellIndex < int32(len(freeCells)) {
			freeCell = freeCells[freeCellIndex]
			currentCellIndices[searchCellIndex] = freeCellIndex
			if searchCellIndex == 0 {
				currentAffinity[searchCellIndex] = freeCell
			} else {
				currentAffinity[searchCellIndex] = findLCA(freeCell, currentAffinity[searchCellIndex-1])
				// pruning: if the current LCA has been higher than the lowest ever,
				// the node will be skipped
				if (currentAffinity[searchCellIndex] == nil && bestAffinity < highestLevel) ||
					(currentAffinity[searchCellIndex] != nil && currentAffinity[searchCellIndex].GetLevel() > bestAffinity) {
					freeCellIndex++
					continue
				}
			}
			if searchCellIndex == pod.CellsPerPod.CellNumber-1 {
				foundOptimalAffinity := false
				bestAffinity, foundOptimalAffinity = checkCurrentCells(
					currentAffinity[len(currentAffinity)-1].GetLevel(),
					freeCells,
					currentCellIndices,
					bestAffinity,
					bestAffinityCells,
					bestAffinityCellIndices,
					optimalAffinity)
				if foundOptimalAffinity {
					// early stop: return if the solution is optimal (i.e., all buddies)
					return bestAffinityCells
				}
			} else {
				searchCellIndex++
			}
			freeCellIndex++
		}
		searchCellIndex--
		if searchCellIndex < 0 {
			if bestAffinity == highestLevel {
				// Unreachable
				panic(fmt.Sprintf("Assert Failure: failed to allocate %v cells in cell %v", pod.CellsPerPod.CellNumber, within.address))
			}
			return bestAffinityCells
		}
		freeCellIndex = currentCellIndices[searchCellIndex] + 1
	}
}

func (s *skuScheduler) getNodeLevel() CellLevel {
	for l := CellLevel(1); l <= CellLevel(len(s.chainCellList)); l++ {
		if s.chainCellList[l][0].AtOrHigherThanNode() {
			return l
		}
	}
	return -1
}

// getFreeCellsAtLevel collects free cells and preemptible cells at given level according to the priority.
func getFreeCellsAtLevel(
	cell Cell,
	level CellLevel,
	priority CellPriority,
	allocatedCells CellList,
	freeCells CellList,
	preemptibleCells CellList) (
	CellList, CellList) {

	if cell.GetLevel() > level {
		for _, c := range cell.GetChildren() {
			freeCells, preemptibleCells = getFreeCellsAtLevel(
				c, level, priority, allocatedCells, freeCells, preemptibleCells)
		}
	} else if cell.GetLevel() == level {
		isAllocated := false
		for _, c := range allocatedCells {
			if isAncestor(cell, c) || isAncestor(c, cell) {
				isAllocated = true
				break
			}
		}
		if !isAllocated {
			if cell.GetPriority() == freePriority {
				freeCells = append(freeCells, cell)
			} else if cell.GetPriority() < priority {
				preemptibleCells = append(preemptibleCells, cell)
			}
		}
	}
	return freeCells, preemptibleCells
}

// checkCurrentCells checks if the currently picked cells have the lowest LCA.
// It also checks if the solution is optimal (if the leaf cells are all buddies).
func checkCurrentCells(
	affinity CellLevel,
	freeCells CellList,
	currentCellIndices []int32,
	bestAffinity CellLevel,
	bestAffinityCells CellList,
	bestAffinityCellIndices []int32,
	optimalAffinity CellLevel) (CellLevel, bool) {

	if affinity < bestAffinity {
		copy(bestAffinityCellIndices, currentCellIndices)
		for i := 0; i < len(currentCellIndices); i++ {
			bestAffinityCells[i] = freeCells[currentCellIndices[i]]
		}
		if affinity == optimalAffinity {
			return affinity, true
		} else {
			return affinity, false
		}
	}
	return bestAffinity, false
}

func (s *skuScheduler) createSkuClusterView(
	within *skuCell,
	withinLevel CellLevel,
	priority CellPriority) skuClusterView {

	cv := skuClusterView{}
	for l := withinLevel; l >= CellLevel(1); l-- {
		for _, c := range s.chainCellList[l] {
			if (within != nil && !isAncestor(within.cell, c)) ||
				cv.containsAncestor(ancestorNoHigherThanLevel(withinLevel, c)) {
				continue
			}
			cell := &skuCell{
				cell:                          c,
				freeLeafCellNumAtPriority:     c.GetTotalLeafCellNum(),
				usedLeafCellNumAtPriority:     0,
				usedLeafCellNumHigherPriority: 0,
				healthy:                       true,
				address:                       "",
			}
			for p, num := range c.GetUsedLeafCellNumAtPriorities() {
				if p >= priority {
					cell.freeLeafCellNumAtPriority -= num
				}
				if s.crossPriorityPack {
					cell.usedLeafCellNumAtPriority += num
				} else {
					if p == priority {
						cell.usedLeafCellNumAtPriority += num
					}
					if p > priority {
						cell.usedLeafCellNumHigherPriority += num
					}
				}
			}
			switch v := c.(type) {
			case *PhysicalCell:
				cell.healthy = v.IsHealthy()
				cell.address = c.GetAddress()
			case *VirtualCell:
				if pn := v.GetPhysicalCell(); pn != nil {
					cell.healthy = pn.IsHealthy()
					cell.address = pn.GetAddress()
				}
			}
			cv = append(cv, cell)
		}
	}
	sort.Stable(cv)
	return cv
}

// Len method for sorting sku cells in cluster view
func (cv skuClusterView) Len() int {
	return len(cv)
}

// Less method for sorting sku cells in cluster view
// sort in the following order:
// 1. cell health (prefer healthy)
// 1. cell level (prefer lower)
// 2. usedLeafCellNumAtPriority (prefer higher)
// 3. usedLeafCellNumHigherPriority (prefer lower)
func (cv skuClusterView) Less(i, j int) bool {
	if cv[i].healthy != cv[j].healthy {
		return cv[i].healthy
	}
	if cv[i].cell.GetLevel() != cv[j].cell.GetLevel() {
		return cv[i].cell.GetLevel() < cv[j].cell.GetLevel()
	}
	if cv[i].usedLeafCellNumAtPriority != cv[j].usedLeafCellNumAtPriority {
		return cv[i].usedLeafCellNumAtPriority > cv[j].usedLeafCellNumAtPriority
	}
	if cv[i].usedLeafCellNumHigherPriority != cv[j].usedLeafCellNumHigherPriority {
		return cv[i].usedLeafCellNumHigherPriority < cv[j].usedLeafCellNumHigherPriority
	}
	return true
}

// Swap method for sorting sku cells in cluster view
func (cv skuClusterView) Swap(i int, j int) {
	cv[i], cv[j] = cv[j], cv[i]
}

func (cv skuClusterView) containsAncestor(cell Cell) bool {
	for _, withinCell := range cv {
		if CellEqual(cell, withinCell.cell) {
			return true
		}
	}
	return false
}

func ancestorNoHigherThanLevel(withinLevel CellLevel, cell Cell) Cell {
	if cell.GetLevel() >= withinLevel || cell.GetParent() == nil {
		return cell
	} else {
		return ancestorNoHigherThanLevel(withinLevel, cell.GetParent())
	}
}

func isAncestor(ancestor Cell, cell Cell) bool {
	if CellEqual(ancestor, cell) {
		return true
	}
	if cell.GetLevel() >= ancestor.GetLevel() || cell.GetParent() == nil {
		return false
	}
	return isAncestor(ancestor, cell.GetParent())
}
