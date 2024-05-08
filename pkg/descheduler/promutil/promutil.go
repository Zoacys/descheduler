package promutil

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"net/http"
	"net/url"
	"strconv"
)

const promUrl = "http://222.201.144.237:9091"

// 100 - (avg(irate(node_cpu_seconds_total{mode='idle',instance="192.168.137.125:9100"}[5m])) by (instance) *100)
// GetNodeMap 返回一个不可变的节点名称和 IP 地址的映射
// todo 基于k8s api自动发现节点并根据label选择/写成配置文件Args的形式
func GetNodeMap() map[string]string {
	return map[string]string{
		"vm-arm64-node1":  "192.168.137.125",
		"vm-arm64-node2":  "192.168.137.181",
		"vm-arm64-node3":  "192.168.137.103",
		"vm-arm64-node4":  "192.168.137.184",
		"vm-arm64-node5":  "192.168.137.116",
		"vm-arm64-node6":  "192.168.137.175",
		"vm-arm64-node7":  "192.168.137.187",
		"vm-arm64-node8":  "192.168.137.148",
		"vm-arm64-node9":  "192.168.137.161",
		"vm-arm64-node10": "192.168.137.137",
	}
}

// GetNodeCpuUsage 通过 Prometheus 查询计算指定实例的 CPU 使用率
func GetNodeCpuUsage(instance string) (string, error) {
	query := fmt.Sprintf(`100 - (avg(irate(node_cpu_seconds_total{mode='idle',instance="%s:9100"}[1m])) by (instance) * 100)`, instance)
	queryEncoded := url.QueryEscape(query) // 确保查询字符串被正确编码
	promQueryURL := fmt.Sprintf("%s/api/v1/query?query=%s", promUrl, queryEncoded)

	resp, err := http.Get(promQueryURL)
	if err != nil {
		return "", err // 无法发送请求
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err // 读取响应失败
	}

	// 响应数据存储在 body 中，你可能需要对其进行解析以获取具体的使用率值
	return string(body), nil
}

// PrometheusResponse 定义了 Prometheus API 响应的结构
type PrometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric struct {
				Instance string `json:"instance"`
			} `json:"metric"`
			Value []interface{} `json:"value"` // Value 是一个包含两个元素的数组
		} `json:"result"`
	} `json:"data"`
}

// ExtractNodeCpuUsage 解析 JSON 字符串并提取 CPU 使用率值
func ExtractNodeCpuUsage(jsonStr string) (string, error) {
	var resp PrometheusResponse
	err := json.Unmarshal([]byte(jsonStr), &resp)
	if err != nil {
		return "", err // 解析错误
	}

	// 检查是否有可用的结果
	if len(resp.Data.Result) == 0 {
		return "0", fmt.Errorf("no result found in the response")
	}

	// 假设我们只关心第一个结果中的 CPU 使用率
	value, ok := resp.Data.Result[0].Value[1].(string)
	if !ok {
		return "0", fmt.Errorf("expected a string as the second element of the value array")
	}

	return value, nil
}

// GetNodeMemUsage 通过 Prometheus 查询计算指定实例的内存使用率
func GetNodeMemUsage(instance string) (string, error) {
	// 此 PromQL 查询用于计算内存使用率，可以根据实际情况调整
	query := fmt.Sprintf(`(1 - (node_memory_MemAvailable_bytes{instance="%s:9100"} / node_memory_MemTotal_bytes{instance="%s:9100"})) * 100`, instance, instance)
	queryEncoded := url.QueryEscape(query) // 确保查询字符串被正确编码
	promQueryURL := fmt.Sprintf("%s/api/v1/query?query=%s", promUrl, queryEncoded)

	resp, err := http.Get(promQueryURL)
	if err != nil {
		return "", err // 无法发送请求
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err // 读取响应失败
	}

	// 响应数据存储在 body 中，你可能需要对其进行解析以获取具体的使用率值
	return string(body), nil
}

// ExtractNodeMemoryUsage 解析 JSON 字符串并提取内存使用率值
func ExtractNodeMemoryUsage(jsonStr string) (string, error) {
	var resp PrometheusResponse
	err := json.Unmarshal([]byte(jsonStr), &resp)
	if err != nil {
		return "", err // 解析错误
	}

	// 检查是否有可用的结果
	if len(resp.Data.Result) == 0 {
		return "0", fmt.Errorf("no result found in the response")
	}

	// 假设我们只关心第一个结果中的内存使用率
	value, ok := resp.Data.Result[0].Value[1].(string)
	if !ok {
		return "0", fmt.Errorf("expected a string as the second element of the value array")
	}

	return value, nil
}

//	func GetResourceRequestQuantity(pod *v1.Pod, resourceName v1.ResourceName) resource.Quantity {
//		requestQuantity := resource.Quantity{}
//
//		switch resourceName {
//		case v1.ResourceCPU:
//			requestQuantity = resource.Quantity{Format: resource.DecimalSI}
//		case v1.ResourceMemory, v1.ResourceStorage, v1.ResourceEphemeralStorage:
//			requestQuantity = resource.Quantity{Format: resource.BinarySI}
//		default:
//			requestQuantity = resource.Quantity{Format: resource.DecimalSI}
//		}
//
// }
func NodeUtilization(node *v1.Node, pods []*v1.Pod, resourceNames []v1.ResourceName) map[v1.ResourceName]*resource.Quantity {
	totalReqs := map[v1.ResourceName]*resource.Quantity{
		v1.ResourceCPU:    resource.NewMilliQuantity(0, resource.DecimalSI),
		v1.ResourceMemory: resource.NewQuantity(0, resource.BinarySI),
		v1.ResourcePods:   resource.NewQuantity(int64(len(pods)), resource.DecimalSI),
	}
	for _, name := range resourceNames {
		if !IsBasicResource(name) {
			totalReqs[name] = resource.NewQuantity(0, resource.DecimalSI)
		}
	}
	//req, _ := utils.PodRequestsAndLimits(pod)
	for _, name := range resourceNames {
		//quantity, ok := req[name]
		if name != v1.ResourcePods {
			if name == v1.ResourceCPU {
				usage, _ := GetNodeCpuUsage(GetNodeMap()[node.Name])
				cpuUtil, _ := ExtractNodeCpuUsage(usage)
				cpuUsage, _ := strconv.ParseFloat(cpuUtil, 64)
				v, _ := node.Status.Capacity.Name(v1.ResourceCPU, resource.DecimalSI).AsInt64()
				cpuUsage = cpuUsage / 100 * 1000 * float64(v) //实值，非百分比；todo 但规格写死了,可改为node.status.capacity或者allocatable
				//totalReqs[name].Add()
				quantity := resource.NewMilliQuantity(int64(cpuUsage), resource.DecimalSI)
				totalReqs[name] = quantity
			} else if name == v1.ResourceMemory {
				usage, _ := GetNodeMemUsage(GetNodeMap()[node.Name])
				memoryUtil, _ := ExtractNodeMemoryUsage(usage)
				memUsage, _ := strconv.ParseFloat(memoryUtil, 64)
				v, _ := node.Status.Capacity.Name(v1.ResourceMemory, resource.BinarySI).AsInt64()
				memUsage = memUsage / 100 * float64(v)
				//memUsage = memUsage / 100 * 15.58 * 1024 * 1024  //实值，非百分比；todo 但规格写死了
				quantity := resource.NewQuantity(int64(memUsage), resource.BinarySI)
				totalReqs[name] = quantity
			}
		}
	}

	return totalReqs
}

func IsBasicResource(name v1.ResourceName) bool {
	switch name {
	case v1.ResourceCPU, v1.ResourceMemory, v1.ResourcePods:
		return true
	default:
		return false
	}
}

func GetResourceRealQuantity(pod *v1.Pod, resourceName v1.ResourceName) resource.Quantity {
	realQuantity := resource.Quantity{}
	var resp PrometheusResponse
	switch resourceName {
	case v1.ResourceCPU:
		realQuantity = resource.Quantity{Format: resource.DecimalSI}
		jsonStr, _ := GetPodCpuUsage(pod.Name)
		err := json.Unmarshal([]byte(jsonStr), &resp)
		//todo 解析错误
		if err != nil {
		}
		value, _ := resp.Data.Result[0].Value[1].(string)
		cpuUsage, _ := strconv.ParseFloat(value, 64)
		quantity := resource.NewMilliQuantity(int64(cpuUsage*1000), resource.DecimalSI)
		realQuantity.Add(*quantity)
	case v1.ResourceMemory, v1.ResourceStorage, v1.ResourceEphemeralStorage:
		realQuantity = resource.Quantity{Format: resource.BinarySI}
		jsonStr, _ := GetPodMemUsage(pod.Name)
		err := json.Unmarshal([]byte(jsonStr), &resp)
		if err != nil {
		}
		value, _ := resp.Data.Result[0].Value[1].(string)
		memUsage, _ := strconv.Atoi(value)
		quantity := resource.NewQuantity(int64(memUsage), resource.DecimalSI)
		realQuantity.Add(*quantity)
	default:
		realQuantity = resource.Quantity{Format: resource.DecimalSI}
	}
	return realQuantity

}

func GetPodCpuUsage(podName string) (string, error) {
	query := fmt.Sprintf(`irate(container_cpu_usage_seconds_total{pod="%s",container="",image=""}[1m])`, podName)
	//query := fmt.Sprintf(`irate(node_cpu_seconds_total{mode='idle',instance="%s:9100"}[1m])) `, instance)
	queryEncoded := url.QueryEscape(query) // 确保查询字符串被正确编码
	promQueryURL := fmt.Sprintf("%s/api/v1/query?query=%s", promUrl, queryEncoded)

	resp, err := http.Get(promQueryURL)
	if err != nil {
		return "", err // 无法发送请求
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err // 读取响应失败
	}

	// 响应数据存储在 body 中，你可能需要对其进行解析以获取具体的使用率值
	return string(body), nil
}

func GetPodMemUsage(podName string) (string, error) {
	// 此 PromQL 查询用于计算内存使用率，可以根据实际情况调整
	query := fmt.Sprintf(`container_memory_usage_bytes{image="",pod="%s"}`, podName)
	queryEncoded := url.QueryEscape(query) // 确保查询字符串被正确编码
	promQueryURL := fmt.Sprintf("%s/api/v1/query?query=%s", promUrl, queryEncoded)

	resp, err := http.Get(promQueryURL)
	if err != nil {
		return "", err // 无法发送请求
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err // 读取响应失败
	}

	// 响应数据存储在 body 中，你可能需要对其进行解析以获取具体的使用率值
	return string(body), nil
}
