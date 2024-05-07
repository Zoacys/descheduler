package promutil

import (
	"fmt"
	"strconv"
	"testing"
)

func TestName(t *testing.T) {
	fmt.Println("res")
	usage, _ := GetNodeCpuUsage("192.168.137.125")
	cpuUtil, _ := ExtractCPUUsage(usage)
	cpuUsage, _ := strconv.ParseFloat(cpuUtil, 64)
	fmt.Println(cpuUsage)
	fmt.Println("res")
}
