/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"math"
	"sigs.k8s.io/descheduler/pkg/descheduler/promutil"
	"sort"

	"sigs.k8s.io/descheduler/pkg/api"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/descheduler/pkg/descheduler/evictions"
	"sigs.k8s.io/descheduler/pkg/descheduler/node"
	nodeutil "sigs.k8s.io/descheduler/pkg/descheduler/node"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
	"sigs.k8s.io/descheduler/pkg/utils"
)

// NodeUsage stores a node's info, pods on it, thresholds and its resource usage

type NodeThresholds struct {
	lowResourceThreshold  map[v1.ResourceName]*resource.Quantity
	highResourceThreshold map[v1.ResourceName]*resource.Quantity
}

type NodeInfo struct {
	NodeUsage
	thresholds NodeThresholds
}

type NodeUsage struct {
	node    *v1.Node
	usage   map[v1.ResourceName]*resource.Quantity
	allPods []*v1.Pod
}

type continueEvictionCond func(nodeInfo NodeInfo, totalAvailableUsage map[v1.ResourceName]*resource.Quantity) bool

// NodePodsMap is a set of (node, pods) pairs
type NodePodsMap map[*v1.Node][]*v1.Pod

const (
	// MinResourcePercentage is the minimum value of a resource's percentage
	MinResourcePercentage = 0
	// MaxResourcePercentage is the maximum value of a resource's percentage
	MaxResourcePercentage = 100
)

func normalizePercentage(percent api.Percentage) api.Percentage {
	if percent > MaxResourcePercentage {
		return MaxResourcePercentage
	}
	if percent < MinResourcePercentage {
		return MinResourcePercentage
	}
	return percent
}

func getNodeThresholds(
	nodes []*v1.Node,
	lowThreshold, highThreshold api.ResourceThresholds,
	resourceNames []v1.ResourceName,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
	useDeviationThresholds bool,
) map[string]NodeThresholds {
	nodeThresholdsMap := map[string]NodeThresholds{}

	averageResourceUsagePercent := api.ResourceThresholds{}
	if useDeviationThresholds {
		averageResourceUsagePercent = averageNodeBasicresources(nodes, getPodsAssignedToNode, resourceNames)
	}

	for _, node := range nodes {
		// nodeCapacity获取节点资源总量
		nodeCapacity := node.Status.Capacity
		if len(node.Status.Allocatable) > 0 {
			nodeCapacity = node.Status.Allocatable
		}

		nodeThresholdsMap[node.Name] = NodeThresholds{
			lowResourceThreshold:  map[v1.ResourceName]*resource.Quantity{},
			highResourceThreshold: map[v1.ResourceName]*resource.Quantity{},
		}

		for _, resourceName := range resourceNames {
			if useDeviationThresholds {
				cap := nodeCapacity[resourceName]
				if lowThreshold[resourceName] == MinResourcePercentage {
					nodeThresholdsMap[node.Name].lowResourceThreshold[resourceName] = &cap
					nodeThresholdsMap[node.Name].highResourceThreshold[resourceName] = &cap
				} else {
					nodeThresholdsMap[node.Name].lowResourceThreshold[resourceName] = resourceThreshold(nodeCapacity, resourceName, normalizePercentage(averageResourceUsagePercent[resourceName]-lowThreshold[resourceName]))
					nodeThresholdsMap[node.Name].highResourceThreshold[resourceName] = resourceThreshold(nodeCapacity, resourceName, normalizePercentage(averageResourceUsagePercent[resourceName]+highThreshold[resourceName]))
				}
			} else {
				nodeThresholdsMap[node.Name].lowResourceThreshold[resourceName] = resourceThreshold(nodeCapacity, resourceName, lowThreshold[resourceName])
				nodeThresholdsMap[node.Name].highResourceThreshold[resourceName] = resourceThreshold(nodeCapacity, resourceName, highThreshold[resourceName])
			}
		}

	}
	return nodeThresholdsMap
}

func getNodeUsage(
	nodes []*v1.Node,
	resourceNames []v1.ResourceName,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
) []NodeUsage {
	var nodeUsageList []NodeUsage

	for _, node := range nodes {
		pods, err := podutil.ListPodsOnANode(node.Name, getPodsAssignedToNode, nil)
		if err != nil {
			klog.V(2).InfoS("Node will not be processed, error accessing its pods", "node", klog.KObj(node), "err", err)
			continue
		}

		nodeUsageList = append(nodeUsageList, NodeUsage{
			node:    node,
			usage:   nodeutil.NodeUtilization(pods, resourceNames),
			allPods: pods,
		})
	}

	return nodeUsageList
}
func getNodeUsage_(
	nodes []*v1.Node,
	resourceNames []v1.ResourceName,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
) []NodeUsage {
	var nodeUsageList []NodeUsage

	for _, node := range nodes {
		pods, err := podutil.ListPodsOnANode(node.Name, getPodsAssignedToNode, nil)
		if err != nil {
			klog.V(2).InfoS("Node will not be processed, error accessing its pods", "node", klog.KObj(node), "err", err)
			continue
		}

		nodeUsageList = append(nodeUsageList, NodeUsage{
			node:  node,
			usage: promutil.NodeUtilization(node.Name, pods, resourceNames),
			//usage: Node.NodeUtilization(pods,resourceNames),
			allPods: pods,
		})
	}

	return nodeUsageList
}
func resourceThreshold(nodeCapacity v1.ResourceList, resourceName v1.ResourceName, threshold api.Percentage) *resource.Quantity {
	defaultFormat := resource.DecimalSI
	if resourceName == v1.ResourceMemory {
		defaultFormat = resource.BinarySI
	}

	resourceCapacityFraction := func(resourceNodeCapacity int64) int64 {
		// A threshold is in percentages but in <0;100> interval.
		// Performing `threshold * 0.01` will convert <0;100> interval into <0;1>.
		// Multiplying it with capacity will give fraction of the capacity corresponding to the given resource threshold in Quantity units.
		return int64(float64(threshold) * 0.01 * float64(resourceNodeCapacity))
	}

	resourceCapacityQuantity := nodeCapacity.Name(resourceName, defaultFormat)

	if resourceName == v1.ResourceCPU {
		return resource.NewMilliQuantity(resourceCapacityFraction(resourceCapacityQuantity.MilliValue()), defaultFormat)
	}
	return resource.NewQuantity(resourceCapacityFraction(resourceCapacityQuantity.Value()), defaultFormat)
}

func roundTo2Decimals(percentage float64) float64 {
	return math.Round(percentage*100) / 100
}

func resourceUsagePercentages(nodeUsage NodeUsage) map[v1.ResourceName]float64 {
	nodeCapacity := nodeUsage.node.Status.Capacity
	if len(nodeUsage.node.Status.Allocatable) > 0 {
		nodeCapacity = nodeUsage.node.Status.Allocatable
	}

	resourceUsagePercentage := map[v1.ResourceName]float64{}
	for resourceName, resourceUsage := range nodeUsage.usage {
		cap := nodeCapacity[resourceName]
		if !cap.IsZero() {
			resourceUsagePercentage[resourceName] = 100 * float64(resourceUsage.MilliValue()) / float64(cap.MilliValue())
			resourceUsagePercentage[resourceName] = roundTo2Decimals(resourceUsagePercentage[resourceName])
		}
	}

	return resourceUsagePercentage
}

// classifyNodes classifies the nodes into low-utilization or high-utilization nodes. If a node lies between
// low and high thresholds, it is simply ignored.
func classifyNodes(
	nodeUsages []NodeUsage,
	nodeThresholds map[string]NodeThresholds,
	lowThresholdFilter, highThresholdFilter func(node *v1.Node, usage NodeUsage, threshold NodeThresholds) bool,
) ([]NodeInfo, []NodeInfo) {
	lowNodes, highNodes := []NodeInfo{}, []NodeInfo{}

	for _, nodeUsage := range nodeUsages {
		nodeInfo := NodeInfo{
			NodeUsage:  nodeUsage,
			thresholds: nodeThresholds[nodeUsage.node.Name],
		}
		if lowThresholdFilter(nodeUsage.node, nodeUsage, nodeThresholds[nodeUsage.node.Name]) {
			klog.InfoS("Node is underutilized", "node", klog.KObj(nodeUsage.node), "usage", nodeUsage.usage, "usagePercentage", resourceUsagePercentages(nodeUsage))
			lowNodes = append(lowNodes, nodeInfo)
		} else if highThresholdFilter(nodeUsage.node, nodeUsage, nodeThresholds[nodeUsage.node.Name]) {
			klog.InfoS("Node is overutilized", "node", klog.KObj(nodeUsage.node), "usage", nodeUsage.usage, "usagePercentage", resourceUsagePercentages(nodeUsage))
			highNodes = append(highNodes, nodeInfo)
		} else {
			klog.InfoS("Node is appropriately utilized", "node", klog.KObj(nodeUsage.node), "usage", nodeUsage.usage, "usagePercentage", resourceUsagePercentages(nodeUsage))
		}
	}

	return lowNodes, highNodes
}

// evictPodsFromSourceNodes evicts pods based on priority, if all the pods on the node have priority, if not
// evicts them based on QoS as fallback option.
// TODO: @ravig Break this function into smaller functions.
func evictPodsFromSourceNodes(
	ctx context.Context,
	evictableNamespaces *api.Namespaces,
	sourceNodes, destinationNodes []NodeInfo,
	podEvictor frameworktypes.Evictor,
	evictOptions evictions.EvictOptions,
	podFilter func(pod *v1.Pod) bool,
	resourceNames []v1.ResourceName,
	continueEviction continueEvictionCond,
) {
	// upper bound on total number of pods/cpu/memory and optional extended resources to be moved
	totalAvailableUsage := map[v1.ResourceName]*resource.Quantity{
		v1.ResourcePods:   {},
		v1.ResourceCPU:    {},
		v1.ResourceMemory: {},
	}

	taintsOfDestinationNodes := make(map[string][]v1.Taint, len(destinationNodes))
	for _, node := range destinationNodes {
		taintsOfDestinationNodes[node.node.Name] = node.node.Spec.Taints

		for _, name := range resourceNames {
			if _, ok := totalAvailableUsage[name]; !ok {
				totalAvailableUsage[name] = resource.NewQuantity(0, resource.DecimalSI)
			}
			totalAvailableUsage[name].Add(*node.thresholds.highResourceThreshold[name])
			totalAvailableUsage[name].Sub(*node.usage[name])
		}
	}

	// log message in one line
	keysAndValues := []interface{}{
		"CPU", totalAvailableUsage[v1.ResourceCPU].MilliValue(),
		"Mem", totalAvailableUsage[v1.ResourceMemory].Value(),
		"Pods", totalAvailableUsage[v1.ResourcePods].Value(),
	}
	for name := range totalAvailableUsage {
		if !node.IsBasicResource(name) {
			keysAndValues = append(keysAndValues, string(name), totalAvailableUsage[name].Value())
		}
	}
	klog.V(1).InfoS("Total capacity to be moved", keysAndValues...)

	for _, node := range sourceNodes {
		klog.V(3).InfoS("Evicting pods from node", "node", klog.KObj(node.node), "usage", node.usage)

		nonRemovablePods, removablePods := classifyPods(node.allPods, podFilter)
		klog.V(2).InfoS("Pods on node", "node", klog.KObj(node.node), "allPods", len(node.allPods), "nonRemovablePods", len(nonRemovablePods), "removablePods", len(removablePods))

		if len(removablePods) == 0 {
			klog.V(1).InfoS("No removable pods on node, try next node", "node", klog.KObj(node.node))
			continue
		}

		klog.V(1).InfoS("Evicting pods based on priority, if they have same priority, they'll be evicted based on QoS tiers")
		// sort the evictable Pods based on priority. This also sorts them based on QoS. If there are multiple pods with same priority, they are sorted based on QoS tiers.
		podutil.SortPodsBasedOnPriorityLowToHigh(removablePods)
		evictPods(ctx, evictableNamespaces, removablePods, node, totalAvailableUsage, taintsOfDestinationNodes, podEvictor, evictOptions, continueEviction)

	}
}

func evictPodsFromSourceNodes_(
	ctx context.Context,
	evictableNamespaces *api.Namespaces,
	sourceNodes, destinationNodes []NodeInfo,
	podEvictor frameworktypes.Evictor,
	evictOptions evictions.EvictOptions,
	podFilter func(pod *v1.Pod) bool,
	resourceNames []v1.ResourceName,
	continueEviction continueEvictionCond,
) {
	// upper bound on total number of pods/cpu/memory and optional extended resources to be moved
	totalAvailableUsage := map[v1.ResourceName]*resource.Quantity{
		v1.ResourcePods:   {},
		v1.ResourceCPU:    {},
		v1.ResourceMemory: {},
	}

	taintsOfDestinationNodes := make(map[string][]v1.Taint, len(destinationNodes))
	for _, node := range destinationNodes {
		taintsOfDestinationNodes[node.node.Name] = node.node.Spec.Taints

		for _, name := range resourceNames {
			if _, ok := totalAvailableUsage[name]; !ok {
				totalAvailableUsage[name] = resource.NewQuantity(0, resource.DecimalSI)
			}
			// + target - current
			totalAvailableUsage[name].Add(*node.thresholds.highResourceThreshold[name])
			totalAvailableUsage[name].Sub(*node.usage[name])
		}
	}

	// log message in one line
	keysAndValues := []interface{}{
		"CPU", totalAvailableUsage[v1.ResourceCPU].MilliValue(),
		"Mem", totalAvailableUsage[v1.ResourceMemory].Value(),
		"Pods", totalAvailableUsage[v1.ResourcePods].Value(),
	}
	for name := range totalAvailableUsage {
		if !node.IsBasicResource(name) {
			keysAndValues = append(keysAndValues, string(name), totalAvailableUsage[name].Value())
		}
	}
	klog.V(1).InfoS("Total capacity to be moved", keysAndValues...)

	DoEvict(ctx, evictableNamespaces, sourceNodes, totalAvailableUsage, taintsOfDestinationNodes, podEvictor, evictOptions, continueEviction, destinationNodes)
}

func DoEvict(ctx context.Context, evictableNamespaces *api.Namespaces, sourceNodes []NodeInfo, totalAvailableUsage map[v1.ResourceName]*resource.Quantity,
	taintsOfDestinationNodes map[string][]v1.Taint, podEvictor frameworktypes.Evictor, evictOptions evictions.EvictOptions, continueEviction continueEvictionCond,
	destinationNodes []NodeInfo) {
	var allPodsToEvict []*v1.Pod
	var allPlacementBeforeEvict map[*v1.Pod]*v1.Node
	var allPodsResourceUsage map[*v1.Pod]map[v1.ResourceName]resource.Quantity
	var cpuUsageOfSourceNodes map[*v1.Node]resource.Quantity
	klog.V(1).InfoS("待迁移pod选择开始")
	for _, node := range sourceNodes {
		cpuUsageOfSourceNodes[node.NodeUsage.node] = (node.NodeUsage.usage[v1.ResourceCPU]).DeepCopy()
		klog.V(3).InfoS("Evicting pods from node", "node", klog.KObj(node.node), "usage", node.usage)
		//todo 上次未解决removable的原因
		nonRemovablePods, removablePods := classifyPods(node.allPods, isPodRemovable)
		klog.V(2).InfoS("Pods on node", "node", klog.KObj(node.node), "allPods", len(node.allPods), "nonRemovablePods", len(nonRemovablePods), "removablePods", len(removablePods))

		if len(removablePods) == 0 {
			klog.V(1).InfoS("No removable pods on node, try next node", "node", klog.KObj(node.node))
			continue
		}

		klog.V(1).InfoS("Evicting pods based on priority, if they have same priority, they'll be evicted based on QoS tiers")
		// sort the evictable Pods based on priority. This also sorts them based on QoS. If there are multiple pods with same priority, they are sorted based on QoS tiers.
		podutil.SortPodsBasedOnPriorityLowToHigh(removablePods)
		// //todo other method(ACO,PSO,MFHS)

		selectEvictedPods(
			ctx, evictableNamespaces, removablePods, node, totalAvailableUsage, taintsOfDestinationNodes, podEvictor, evictOptions, continueEviction,
			&allPodsToEvict, &allPlacementBeforeEvict, &allPodsResourceUsage)

	}
	klog.V(1).InfoS("待迁移pod选择结束，迁移pod总数为:", len(allPodsToEvict))

	klog.V(1).InfoS("目的主机选择开始")

	var failedPods []*v1.Pod
	var targetHostMaping map[*v1.Pod]*v1.Node
	//先默认采用LFHS;todo 增加其他算法
	for _, pod := range allPodsToEvict {
		var selectedNode *v1.Node
		var selectedNodeInfo *NodeInfo
		for _, destinationNode := range destinationNodes {
			var isSatisfied bool = true
			// 装箱约束
			for name, quantity := range allPodsResourceUsage[pod] {
				nodeResourceAfterPlace := destinationNode.NodeUsage.usage[name].DeepCopy()
				nodeResourceAfterPlace.Add(quantity)
				if nodeResourceAfterPlace.Cmp(destinationNode.node.Status.Capacity[name]) > 0 {
					isSatisfied = false
					break
				}
			}
			// cpu
			if isSatisfied {
				nodeResource := destinationNode.NodeUsage.usage[v1.ResourceCPU].DeepCopy()
				// 只能从高负载到低负载
				if nodeResource.Cmp(cpuUsageOfSourceNodes[allPlacementBeforeEvict[pod]]) > 0 {
					break
				} else {
					// LFHS选择
					if selectedNode == nil || selectedNodeInfo.usage[v1.ResourceCPU].Cmp(nodeResource) > 0 {
						selectedNode = destinationNode.node
						selectedNodeInfo = &destinationNode
					}
				}
			}
		}
		// pod重调度成功
		if selectedNode == nil {
			failedPods = append(failedPods, pod)
		} else {
			// 选择成功，修改Pod 的nodeSelector指定
			targetHostMaping[pod] = selectedNode
			// e.g. nodeName=vm-arm64-node1; kubernetes.io/hostname=vm-arm64-node1
			pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": selectedNode.Name}
			// 执行驱逐
			if podEvictor.Evict(ctx, pod, evictOptions) {
				klog.V(3).InfoS("Evicted pods", "pod", klog.KObj(pod))

				for name, quantity := range selectedNodeInfo.usage {
					selectedNodeInfo.usage[name].Add(*quantity)
				}
			}
		}
	}
	klog.V(1).InfoS("目的主机选择结束")
}
func isPodRemovable(pod *v1.Pod) bool {
	// 如果 Pod 的命名空间是 "deschedule"，则返回 true，表示它是可移除的
	return pod.Namespace == "deschedule"
}
func selectEvictedPods(
	ctx context.Context,
	evictableNamespaces *api.Namespaces,
	inputPods []*v1.Pod,
	nodeInfo NodeInfo,
	totalAvailableUsage map[v1.ResourceName]*resource.Quantity,
	taintsOfLowNodes map[string][]v1.Taint,
	podEvictor frameworktypes.Evictor,
	evictOptions evictions.EvictOptions,
	continueEviction continueEvictionCond,
	allPodsToEvict *[]*v1.Pod,
	allPlacementBeforeEvict *map[*v1.Pod]*v1.Node,
	allPodsResourceUsage *map[*v1.Pod]map[v1.ResourceName]resource.Quantity,
) {
	//podsToEvict := make([]*v1.Pod, 0)
	//placementBeforeEvict := make(map[*v1.Pod]*v1.Node)
	var excludedNamespaces sets.Set[string]
	if evictableNamespaces != nil {
		excludedNamespaces = sets.New(evictableNamespaces.Exclude...)
	}

	// 确定待迁移pod
	if continueEviction(nodeInfo, totalAvailableUsage) {
		for _, pod := range inputPods {
			if !utils.PodToleratesTaints(pod, taintsOfLowNodes) {
				klog.V(3).InfoS("Skipping eviction for pod, doesn't tolerate node taint", "pod", klog.KObj(pod))
				continue
			}

			preEvictionFilterWithOptions, err := podutil.NewOptions().
				WithFilter(podEvictor.PreEvictionFilter).
				WithoutNamespaces(excludedNamespaces).
				BuildFilterFunc()
			if err != nil {
				klog.ErrorS(err, "could not build preEvictionFilter with namespace exclusion")
				continue
			}

			if preEvictionFilterWithOptions(pod) {
				*allPodsToEvict = append(*allPodsToEvict, pod)
				(*allPlacementBeforeEvict)[pod] = nodeInfo.node
				//if podEvictor.Evict(ctx, pod, evictOptions) {
				klog.V(3).InfoS("Evicted pods", "pod", klog.KObj(pod))

				for name := range totalAvailableUsage {
					// 可以先减掉，之后如果没有确定目标主机再加回来
					if name == v1.ResourcePods {
						nodeInfo.usage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
						totalAvailableUsage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
					} else {
						//todo 暂时request = limit
						// 这里改为普罗米修斯获取到的标量，非百分比！
						quantity := utils.GetResourceRequestQuantity(pod, name)
						nodeInfo.usage[name].Sub(quantity)
						totalAvailableUsage[name].Sub(quantity)

						//podsResourceUsage[pod][name] = quantity
						(*allPodsResourceUsage)[pod][name] = quantity
					}
				}

				keysAndValues := []interface{}{
					"node", nodeInfo.node.Name,
					"CPU", nodeInfo.usage[v1.ResourceCPU].MilliValue(),
					"Mem", nodeInfo.usage[v1.ResourceMemory].Value(),
					"Pods", nodeInfo.usage[v1.ResourcePods].Value(),
				}
				for name := range totalAvailableUsage {
					if !nodeutil.IsBasicResource(name) {
						keysAndValues = append(keysAndValues, string(name), totalAvailableUsage[name].Value())
					}
				}
				klog.V(3).InfoS("Updated node usage", keysAndValues...)
				// check if pods can be still evicted
				if !continueEviction(nodeInfo, totalAvailableUsage) {
					break
				}
				//}
			}
			if podEvictor.NodeLimitExceeded(nodeInfo.node) {
			}
		}
	}
	//return podsToEvict, placementBeforeEvict, podsResourceUsage
	// 确定迁移主机
	//klog.V(1).InfoS("目标节点选择开始")
	//// 此处使用LFHS（多维） todo 其他算法
	//klog.V(1).InfoS("目标节点选择结束")

}
func evictPods(
	ctx context.Context,
	evictableNamespaces *api.Namespaces,
	inputPods []*v1.Pod,
	nodeInfo NodeInfo,
	totalAvailableUsage map[v1.ResourceName]*resource.Quantity,
	taintsOfLowNodes map[string][]v1.Taint,
	podEvictor frameworktypes.Evictor,
	evictOptions evictions.EvictOptions,
	continueEviction continueEvictionCond,
) {
	var excludedNamespaces sets.Set[string]
	if evictableNamespaces != nil {
		excludedNamespaces = sets.New(evictableNamespaces.Exclude...)
	}

	if continueEviction(nodeInfo, totalAvailableUsage) {
		for _, pod := range inputPods {
			if !utils.PodToleratesTaints(pod, taintsOfLowNodes) {
				klog.V(3).InfoS("Skipping eviction for pod, doesn't tolerate node taint", "pod", klog.KObj(pod))
				continue
			}

			preEvictionFilterWithOptions, err := podutil.NewOptions().
				WithFilter(podEvictor.PreEvictionFilter).
				WithoutNamespaces(excludedNamespaces).
				BuildFilterFunc()
			if err != nil {
				klog.ErrorS(err, "could not build preEvictionFilter with namespace exclusion")
				continue
			}

			if preEvictionFilterWithOptions(pod) {
				if podEvictor.Evict(ctx, pod, evictOptions) {
					klog.V(3).InfoS("Evicted pods", "pod", klog.KObj(pod))

					for name := range totalAvailableUsage {
						if name == v1.ResourcePods {
							nodeInfo.usage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
							totalAvailableUsage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
						} else {
							quantity := utils.GetResourceRequestQuantity(pod, name)
							nodeInfo.usage[name].Sub(quantity)
							totalAvailableUsage[name].Sub(quantity)
						}
					}

					keysAndValues := []interface{}{
						"node", nodeInfo.node.Name,
						"CPU", nodeInfo.usage[v1.ResourceCPU].MilliValue(),
						"Mem", nodeInfo.usage[v1.ResourceMemory].Value(),
						"Pods", nodeInfo.usage[v1.ResourcePods].Value(),
					}
					for name := range totalAvailableUsage {
						if !nodeutil.IsBasicResource(name) {
							keysAndValues = append(keysAndValues, string(name), totalAvailableUsage[name].Value())
						}
					}

					klog.V(3).InfoS("Updated node usage", keysAndValues...)
					// check if pods can be still evicted
					if !continueEviction(nodeInfo, totalAvailableUsage) {
						break
					}
				}
			}
			if podEvictor.NodeLimitExceeded(nodeInfo.node) {
				return
			}
		}
	}
}
func evictPods_(
	ctx context.Context,
	evictableNamespaces *api.Namespaces,
	inputPods []*v1.Pod,
	nodeInfo NodeInfo,
	totalAvailableUsage map[v1.ResourceName]*resource.Quantity,
	taintsOfLowNodes map[string][]v1.Taint,
	podEvictor frameworktypes.Evictor,
	evictOptions evictions.EvictOptions,
	continueEviction continueEvictionCond,
	destinationNodes []NodeInfo,
) {
	var excludedNamespaces sets.Set[string]
	if evictableNamespaces != nil {
		excludedNamespaces = sets.New(evictableNamespaces.Exclude...)
	}
	//podsToEvict := make([]*v1.Pod, 0)

	if continueEviction(nodeInfo, totalAvailableUsage) {
		for _, pod := range inputPods {
			if !utils.PodToleratesTaints(pod, taintsOfLowNodes) {
				klog.V(3).InfoS("Skipping eviction for pod, doesn't tolerate node taint", "pod", klog.KObj(pod))
				continue
			}

			preEvictionFilterWithOptions, err := podutil.NewOptions().
				WithFilter(podEvictor.PreEvictionFilter).
				WithoutNamespaces(excludedNamespaces).
				BuildFilterFunc()
			if err != nil {
				klog.ErrorS(err, "could not build preEvictionFilter with namespace exclusion")
				continue
			}

			if preEvictionFilterWithOptions(pod) {

				if podEvictor.Evict(ctx, pod, evictOptions) {
					klog.V(3).InfoS("Evicted pods", "pod", klog.KObj(pod))

					for name := range totalAvailableUsage {
						if name == v1.ResourcePods {
							nodeInfo.usage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
							totalAvailableUsage[name].Sub(*resource.NewQuantity(1, resource.DecimalSI))
						} else {
							//todo 暂时request = limit
							quantity := utils.GetResourceRequestQuantity(pod, name)
							nodeInfo.usage[name].Sub(quantity)
							totalAvailableUsage[name].Sub(quantity)
						}
					}

					keysAndValues := []interface{}{
						"node", nodeInfo.node.Name,
						"CPU", nodeInfo.usage[v1.ResourceCPU].MilliValue(),
						"Mem", nodeInfo.usage[v1.ResourceMemory].Value(),
						"Pods", nodeInfo.usage[v1.ResourcePods].Value(),
					}
					for name := range totalAvailableUsage {
						if !nodeutil.IsBasicResource(name) {
							keysAndValues = append(keysAndValues, string(name), totalAvailableUsage[name].Value())
						}
					}

					klog.V(3).InfoS("Updated node usage", keysAndValues...)
					// check if pods can be still evicted
					if !continueEviction(nodeInfo, totalAvailableUsage) {
						break
					}
				}
			}
			if podEvictor.NodeLimitExceeded(nodeInfo.node) {
				return
			}
		}
	}
}

// sortNodesByUsage sorts nodes based on usage according to the given plugin.
func sortNodesByUsage(nodes []NodeInfo, ascending bool) {
	sort.Slice(nodes, func(i, j int) bool {
		ti := nodes[i].usage[v1.ResourceMemory].Value() + nodes[i].usage[v1.ResourceCPU].MilliValue() + nodes[i].usage[v1.ResourcePods].Value()
		tj := nodes[j].usage[v1.ResourceMemory].Value() + nodes[j].usage[v1.ResourceCPU].MilliValue() + nodes[j].usage[v1.ResourcePods].Value()

		// extended resources
		for name := range nodes[i].usage {
			if !nodeutil.IsBasicResource(name) {
				ti = ti + nodes[i].usage[name].Value()
				tj = tj + nodes[j].usage[name].Value()
			}
		}

		// Return ascending order for HighNodeUtilization plugin
		if ascending {
			return ti < tj
		}

		// Return descending order for LowNodeUtilization plugin
		return ti > tj
	})
}

// isNodeAboveTargetUtilization checks if a node is overutilized
// At least one resource has to be above the high threshold
func isNodeAboveTargetUtilization(usage NodeUsage, threshold map[v1.ResourceName]*resource.Quantity) bool {
	for name, nodeValue := range usage.usage {
		// usage.highResourceThreshold[name] < nodeValue
		if threshold[name].Cmp(*nodeValue) == -1 {
			return true
		}
	}
	return false
}

// isNodeWithLowUtilization checks if a node is underutilized
// All resources have to be below the low threshold
func isNodeWithLowUtilization(usage NodeUsage, threshold map[v1.ResourceName]*resource.Quantity) bool {
	for name, nodeValue := range usage.usage {
		// usage.lowResourceThreshold[name] < nodeValue
		if threshold[name].Cmp(*nodeValue) == -1 {
			return false
		}
	}

	return true
}

// getResourceNames returns list of resource names in resource thresholds
func getResourceNames(thresholds api.ResourceThresholds) []v1.ResourceName {
	resourceNames := make([]v1.ResourceName, 0, len(thresholds))
	for name := range thresholds {
		resourceNames = append(resourceNames, name)
	}
	return resourceNames
}

func classifyPods(pods []*v1.Pod, filter func(pod *v1.Pod) bool) ([]*v1.Pod, []*v1.Pod) {
	var nonRemovablePods, removablePods []*v1.Pod

	for _, pod := range pods {
		if !filter(pod) {
			nonRemovablePods = append(nonRemovablePods, pod)
		} else {
			removablePods = append(removablePods, pod)
		}
	}

	return nonRemovablePods, removablePods
}

func averageNodeBasicresources(nodes []*v1.Node, getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc, resourceNames []v1.ResourceName) api.ResourceThresholds {
	total := api.ResourceThresholds{}
	average := api.ResourceThresholds{}
	numberOfNodes := len(nodes)
	for _, node := range nodes {
		pods, err := podutil.ListPodsOnANode(node.Name, getPodsAssignedToNode, nil)
		if err != nil {
			numberOfNodes--
			continue
		}
		usage := nodeutil.NodeUtilization(pods, resourceNames)
		nodeCapacity := node.Status.Capacity
		if len(node.Status.Allocatable) > 0 {
			nodeCapacity = node.Status.Allocatable
		}
		for resource, value := range usage {
			nodeCapacityValue := nodeCapacity[resource]
			if resource == v1.ResourceCPU {
				total[resource] += api.Percentage(value.MilliValue()) / api.Percentage(nodeCapacityValue.MilliValue()) * 100.0
			} else {
				total[resource] += api.Percentage(value.Value()) / api.Percentage(nodeCapacityValue.Value()) * 100.0
			}
		}
	}
	for resource, value := range total {
		average[resource] = value / api.Percentage(numberOfNodes)
	}
	return average
}
