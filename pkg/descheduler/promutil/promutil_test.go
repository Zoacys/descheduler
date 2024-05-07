package promutil

import (
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/descheduler/test"
	"strconv"
	"testing"
)

func TestPod(t *testing.T) {
	jsonStr, _ := GetPodCpuUsage("pod-demo2")
	//cpuUtil, _ := ExtractNodeCpuUsage(usage)
	var resp PrometheusResponse
	//cpuUsage, _ := strconv.ParseFloat(cpuUtil, 64)
	err := json.Unmarshal([]byte(jsonStr), &resp)
	// 解析错误
	if err != nil {
		return
	}

	if len(resp.Data.Result) == 0 {
		return
	}
	value, ok := resp.Data.Result[0].Value[1].(string)
	if !ok {
		return
	}
	cpuUsage, _ := strconv.ParseFloat(value, 64)

	quantity := resource.NewMilliQuantity(int64(cpuUsage*1000), resource.DecimalSI)

	println(value)
	println(quantity)

	//resource.NewMilliQuantity(int(s))
	//println(usage)
	//println(cpuUtil)
	//println(cpuUsage)

}

func TestNode(t *testing.T) {
	//usage, _ := GetPodCpuUsage("pod-demo2")
	//cpuUtil, _ := ExtractNodeCpuUsage(usage)
	//cpuUsage, _ := strconv.ParseFloat(cpuUtil, 64)

	usage, _ := GetNodeCpuUsage("192.168.137.103")
	cpuUtil, _ := ExtractNodeCpuUsage(usage)
	cpuUsage, _ := strconv.ParseFloat(cpuUtil, 64)

	println(usage)
	println(cpuUtil)
	println(cpuUsage)

}

func TestName(t *testing.T) {
	pod := test.BuildTestPod("pod-demo2", 400, 0, "n1NodeName", test.SetRSOwnerRef)
	quantity := GetResourceRealQuantity(pod, v1.ResourceCPU)
	//println(quantity)
	fmt.Println(quantity)
}

func TestMem(t *testing.T) {
	pod := test.BuildTestPod("pod-demo2", 400, 0, "n1NodeName", test.SetRSOwnerRef)
	//usage, _ := GetPodMemUsage(pod.Name)
	//fmt.Println(usage)
	quantity := GetResourceRealQuantity(pod, v1.ResourceMemory)
	fmt.Println(quantity)
}
