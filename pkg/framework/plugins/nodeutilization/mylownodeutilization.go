package nodeutilization

import (
	"context"
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/descheduler/pkg/descheduler/evictions"
	nodeutil "sigs.k8s.io/descheduler/pkg/descheduler/node"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
)

const MyLowNodeUtilizationPluginName = "MyLowNodeUtilization"

type MyLowNodeUtilization struct {
	handle    frameworktypes.Handle
	args      *LowNodeUtilizationArgs
	podFilter func(pod *v1.Pod) bool
}

var _ frameworktypes.BalancePlugin = &MyLowNodeUtilization{}

func NewMyLowNodeUtilization(args runtime.Object, handle frameworktypes.Handle) (frameworktypes.Plugin, error) {
	lowNodeUtilizationArgsArgs, ok := args.(*LowNodeUtilizationArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type LowNodeUtilizationArgs, got %T", args)
	}
	podFilter, err := podutil.NewOptions().
		WithFilter(handle.Evictor().Filter).
		BuildFilterFunc()

	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}

	return &MyLowNodeUtilization{
		handle:    handle,
		args:      lowNodeUtilizationArgsArgs,
		podFilter: podFilter,
	}, nil

}

func (m *MyLowNodeUtilization) Name() string {
	return MyLowNodeUtilizationPluginName
}

func (m *MyLowNodeUtilization) Balance(ctx context.Context, nodes []*v1.Node) *frameworktypes.Status {
	useDeviationThresholds := m.args.UseDeviationThresholds
	thresholds := m.args.Thresholds
	targetThresholds := m.args.TargetThresholds

	klog.V(1).InfoS("自定义插件启动成功")

	// check if Pods/CPU/Mem are set, if not, set them to 100
	if _, ok := thresholds[v1.ResourcePods]; !ok {
		if useDeviationThresholds {
			thresholds[v1.ResourcePods] = MinResourcePercentage
			targetThresholds[v1.ResourcePods] = MinResourcePercentage
		} else {
			thresholds[v1.ResourcePods] = MaxResourcePercentage
			targetThresholds[v1.ResourcePods] = MaxResourcePercentage
		}
	}
	if _, ok := thresholds[v1.ResourceCPU]; !ok {
		if useDeviationThresholds {
			thresholds[v1.ResourceCPU] = MinResourcePercentage
			targetThresholds[v1.ResourceCPU] = MinResourcePercentage
		} else {
			thresholds[v1.ResourceCPU] = MaxResourcePercentage
			targetThresholds[v1.ResourceCPU] = MaxResourcePercentage
		}
	}
	if _, ok := thresholds[v1.ResourceMemory]; !ok {
		if useDeviationThresholds {
			thresholds[v1.ResourceMemory] = MinResourcePercentage
			targetThresholds[v1.ResourceMemory] = MinResourcePercentage
		} else {
			thresholds[v1.ResourceMemory] = MaxResourcePercentage
			targetThresholds[v1.ResourceMemory] = MaxResourcePercentage
		}
	}

	resourceNames := getResourceNames(thresholds)

	lowNodes, sourceNodes := classifyNodes(
		getNodeUsage_(nodes, resourceNames, m.handle.GetPodsAssignedToNodeFunc()),
		getNodeThresholds(nodes, thresholds, targetThresholds, resourceNames, m.handle.GetPodsAssignedToNodeFunc(), useDeviationThresholds),
		// The node has to be schedulable (to be able to move workload there)
		func(node *v1.Node, usage NodeUsage, threshold NodeThresholds) bool {
			if nodeutil.IsNodeUnschedulable(node) {
				klog.V(2).InfoS("Node is unschedulable, thus not considered as underutilized", "node", klog.KObj(node))
				return false
			}
			return isNodeWithLowUtilization(usage, threshold.lowResourceThreshold)
		},
		func(node *v1.Node, usage NodeUsage, threshold NodeThresholds) bool {
			return isNodeAboveTargetUtilization(usage, threshold.highResourceThreshold)
		},
	)

	// log message for nodes with low utilization
	underutilizationCriteria := []interface{}{
		"CPU", thresholds[v1.ResourceCPU],
		"Mem", thresholds[v1.ResourceMemory],
		"Pods", thresholds[v1.ResourcePods],
	}
	for name := range thresholds {
		if !nodeutil.IsBasicResource(name) {
			underutilizationCriteria = append(underutilizationCriteria, string(name), int64(thresholds[name]))
		}
	}

	klog.V(1).InfoS("Criteria for a node under utilization", underutilizationCriteria...)
	klog.V(1).InfoS("Number of underutilized nodes", "totalNumber", len(lowNodes))

	// log message for over utilized nodes
	overutilizationCriteria := []interface{}{
		"CPU", targetThresholds[v1.ResourceCPU],
		"Mem", targetThresholds[v1.ResourceMemory],
		"Pods", targetThresholds[v1.ResourcePods],
	}

	for name := range targetThresholds {
		if !nodeutil.IsBasicResource(name) {
			overutilizationCriteria = append(overutilizationCriteria, string(name), int64(targetThresholds[name]))
		}
	}

	klog.V(1).InfoS("Criteria for a node above target utilization", overutilizationCriteria...)
	klog.V(1).InfoS("Number of overutilized nodes", "totalNumber", len(sourceNodes))

	// source node = overutilized node
	if len(lowNodes) == 0 {
		klog.V(1).InfoS("No node is underutilized, nothing to do here, you might tune your thresholds further")
		return nil
	}

	if len(lowNodes) <= m.args.NumberOfNodes {
		klog.V(1).InfoS("Number of nodes underutilized is less or equal than NumberOfNodes, nothing to do here", "underutilizedNodes", len(lowNodes), "numberOfNodes", m.args.NumberOfNodes)
		return nil
	}

	if len(lowNodes) == len(nodes) {
		klog.V(1).InfoS("All nodes are underutilized, nothing to do here")
		return nil
	}

	if len(sourceNodes) == 0 {
		klog.V(1).InfoS("All nodes are under target utilization, nothing to do here")
		return nil
	}

	// stop if node utilization drops below target threshold or any of required capacity (cpu, memory, pods) is moved
	continueEvictionCond := func(nodeInfo NodeInfo, totalAvailableUsage map[v1.ResourceName]*resource.Quantity) bool {
		if !isNodeAboveTargetUtilization(nodeInfo.NodeUsage, nodeInfo.thresholds.highResourceThreshold) {
			return false
		}
		for name := range totalAvailableUsage {
			if totalAvailableUsage[name].CmpInt64(0) < 1 {
				return false
			}
		}
		return true
	}
	sortNodesByUsage(sourceNodes, false)

	evictPodsFromSourceNodes_(
		ctx,
		m.args.EvictableNamespaces,
		sourceNodes,
		lowNodes,
		m.handle.Evictor(),
		evictions.EvictOptions{StrategyName: LowNodeUtilizationPluginName},
		m.podFilter,
		resourceNames,
		continueEvictionCond,
	)

	return nil
}
