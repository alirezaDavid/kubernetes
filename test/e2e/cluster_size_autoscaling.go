/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package e2e

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/test/e2e/framework"

	"github.com/golang/glog"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	defaultTimeout   = 3 * time.Minute
	resizeTimeout    = 5 * time.Minute
	scaleUpTimeout   = 5 * time.Minute
	scaleDownTimeout = 15 * time.Minute
)

var _ = framework.KubeDescribe("Cluster size autoscaling [Slow]", func() {
	f := framework.NewDefaultFramework("autoscaling")
	var nodeCount int
	var coresPerNode int
	var memCapacityMb int
	var originalSizes map[string]int

	BeforeEach(func() {
		framework.SkipUnlessProviderIs("gce")

		nodes := framework.GetReadySchedulableNodesOrDie(f.Client)
		nodeCount = len(nodes.Items)
		Expect(nodeCount).NotTo(BeZero())
		cpu := nodes.Items[0].Status.Capacity[api.ResourceCPU]
		mem := nodes.Items[0].Status.Capacity[api.ResourceMemory]
		coresPerNode = int((&cpu).MilliValue() / 1000)
		memCapacityMb = int((&mem).Value() / 1024 / 1024)

		originalSizes = make(map[string]int)
		sum := 0
		for _, mig := range strings.Split(framework.TestContext.CloudConfig.NodeInstanceGroup, ",") {
			size, err := GroupSize(mig)
			framework.ExpectNoError(err)
			By(fmt.Sprintf("Initial size of %s: %d", mig, size))
			originalSizes[mig] = size
			sum += size
		}
		Expect(nodeCount).Should(Equal(sum))
	})

	It("shouldn't increase cluster size if pending pod it too large [Feature:ClusterSizeAutoscalingScaleUp]", func() {
		ReserveMemory(f, "memory-reservation", 1, memCapacityMb, false)
		// Verify, that cluster size is not changed.
		// TODO: find a better way of verification that the cluster size will remain unchanged using events.
		time.Sleep(scaleUpTimeout)
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size <= nodeCount }, scaleDownTimeout))
		framework.ExpectNoError(framework.DeleteRC(f.Client, f.Namespace.Name, "memory-reservation"))
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size <= nodeCount }, scaleDownTimeout))
	})

	It("should increase cluster size if pending pods are small [Feature:ClusterSizeAutoscalingScaleUp]", func() {
		ReserveMemory(f, "memory-reservation", 100, nodeCount*memCapacityMb, false)
		// Verify, that cluster size is increased
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size >= nodeCount+1 }, scaleUpTimeout))
		framework.ExpectNoError(framework.DeleteRC(f.Client, f.Namespace.Name, "memory-reservation"))
		restoreSizes(originalSizes)
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size <= nodeCount }, scaleDownTimeout))
	})

	It("should increase cluster size if pods are pending due to host port conflict [Feature:ClusterSizeAutoscalingScaleUp]", func() {
		CreateHostPortPods(f, "host-port", nodeCount+2, false)
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size >= nodeCount+2 }, scaleUpTimeout))
		framework.ExpectNoError(framework.DeleteRC(f.Client, f.Namespace.Name, "host-port"))
		restoreSizes(originalSizes)
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size <= nodeCount }, scaleDownTimeout))

	})

	It("should correctly handle pending and scale down after deletion [Feature:ClusterSizeAutoscalingScaleDown]", func() {
		By("Small pending pods increase cluster size")
		ReserveMemory(f, "memory-reservation", 100, nodeCount*memCapacityMb, false)
		// Verify, that cluster size is increased
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size >= nodeCount+1 }, scaleUpTimeout))
		framework.ExpectNoError(framework.DeleteRC(f.Client, f.Namespace.Name, "memory-reservation"))
		framework.ExpectNoError(WaitForClusterSizeFunc(f.Client,
			func(size int) bool { return size < nodeCount+1 }, scaleDownTimeout))
	})
})

func CreateHostPortPods(f *framework.Framework, id string, replicas int, expectRunning bool) {
	By(fmt.Sprintf("Running RC which reserves host port"))
	config := &framework.RCConfig{
		Client:    f.Client,
		Name:      id,
		Namespace: f.Namespace.Name,
		Timeout:   defaultTimeout,
		Image:     framework.GetPauseImageName(f.Client),
		Replicas:  replicas,
		HostPorts: map[string]int{"port1": 4321},
	}
	err := framework.RunRC(*config)
	if expectRunning {
		framework.ExpectNoError(err)
	}

}

func ReserveCpu(f *framework.Framework, id string, replicas, millicores int) {
	By(fmt.Sprintf("Running RC which reserves %v millicores", millicores))
	request := int64(millicores / replicas)
	config := &framework.RCConfig{
		Client:     f.Client,
		Name:       id,
		Namespace:  f.Namespace.Name,
		Timeout:    defaultTimeout,
		Image:      framework.GetPauseImageName(f.Client),
		Replicas:   replicas,
		CpuRequest: request,
	}
	framework.ExpectNoError(framework.RunRC(*config))
}

func ReserveMemory(f *framework.Framework, id string, replicas, megabytes int, expectRunning bool) {
	By(fmt.Sprintf("Running RC which reserves %v MB of memory", megabytes))
	request := int64(1024 * 1024 * megabytes / replicas)
	config := &framework.RCConfig{
		Client:     f.Client,
		Name:       id,
		Namespace:  f.Namespace.Name,
		Timeout:    defaultTimeout,
		Image:      framework.GetPauseImageName(f.Client),
		Replicas:   replicas,
		MemRequest: request,
	}
	err := framework.RunRC(*config)
	if expectRunning {
		framework.ExpectNoError(err)
	}
}

// WaitForClusterSize waits until the cluster size matches the given function.
func WaitForClusterSizeFunc(c *client.Client, sizeFunc func(int) bool, timeout time.Duration) error {
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(20 * time.Second) {
		nodes, err := c.Nodes().List(api.ListOptions{FieldSelector: fields.Set{
			"spec.unschedulable": "false",
		}.AsSelector()})
		if err != nil {
			glog.Warningf("Failed to list nodes: %v", err)
			continue
		}
		numNodes := len(nodes.Items)

		// Filter out not-ready nodes.
		framework.FilterNodes(nodes, func(node api.Node) bool {
			return framework.IsNodeConditionSetAsExpected(&node, api.NodeReady, true)
		})
		numReady := len(nodes.Items)

		if numNodes == numReady && sizeFunc(numReady) {
			glog.Infof("Cluster has reached the desired size")
			return nil
		}
		glog.Infof("Waiting for cluster, current size %d, not ready nodes %d", numNodes, numNodes-numReady)
	}
	return fmt.Errorf("timeout waiting %v for appropriate cluster size", timeout)
}

func restoreSizes(sizes map[string]int) {
	By(fmt.Sprintf("Restoring initial size of the cluster"))
	for mig, desiredSize := range sizes {
		currentSize, err := GroupSize(mig)
		framework.ExpectNoError(err)
		if desiredSize != currentSize {
			By(fmt.Sprintf("Setting size of %s to %d", mig, desiredSize))
			err = ResizeGroup(mig, int32(desiredSize))
			framework.ExpectNoError(err)
		}
	}
}
