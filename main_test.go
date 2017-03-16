// Integration testing for docker-vde-plugin against docker.

package main

import (
	. "gopkg.in/check.v1"
	"testing"
	"github.com/wrouesnel/docker-vde-plugin/fsutil"
	"os"
	"github.com/satori/go.uuid"
	"fmt"
)

// Hook up gocheck into the "go test" runner.
func Test(t *testing.T) { TestingT(t) }

type dockerIntegration struct{}

var _ = Suite(&dockerIntegration{})

// TestNetworkLifecycle runs a typical set of workload against the plugin.
func (s *dockerIntegration) TestNetworkLifecycle(c *C) {
	// Launch the plugin in a goroutine so we can collect coverage.
	pluginExited := make(chan int)
	pluginReady := make(chan struct{})
	go func() {
		os.Args = []string{ExecutableName, "--log-level=debug"}
		result := realMain(pluginReady)
		pluginExited <- result
	}()

	c.Logf("Waiting for plugin to be ready...")
	<-pluginReady
	c.Logf("Plugin is ready. Running tests.")

	netName := fmt.Sprintf("vdetest-%s", uuid.NewV4().String())

	// Make a network with the IPAM driver
	fsutil.MustExec("docker", "network", "create",
		"--driver=vde", "--ipam-driver=vde-ipam", "--subnet=192.168.1.0/24",
		"--gateway=192.168.1.1",
		netName)

	// Make a container which exits immediately.
	fsutil.MustExec("docker", "run", "--net=" + netName, "busybox")

	// Destroy the network
	fsutil.MustExec("docker", "network", "rm" , netName)

	// Send a signal to the test binary to trigger an exit of the main process.
	p, _ := os.FindProcess(os.Getpid())
	p.Signal(os.Interrupt)

	c.Logf("Waiting for plugin to exit...")
	result := <- pluginExited
	c.Logf("Plugin exited.")
	c.Check(result, Equals, 0)
}