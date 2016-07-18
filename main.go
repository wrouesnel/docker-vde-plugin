package main

import (
	"github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"

	"flag"
	"os"

	"github.com/wrouesnel/docker-vde-plugin/fsutil"
	"gopkg.in/alecthomas/kingpin.v2"
)

const (
	PluginName string = "vde"
)

// TODO: do some checks to make sure we properly clean up
func main() {
	dockerPluginPath := kingpin.Flag("docker-net-plugins", "Listen path for the plugin.").Default("unix:///run/docker/plugins/vde.sock").URL()
	socketRoot := kingpin.Flag("socket-root", "Path where networks and sockets should be created").Default("/run/docker-vde-plugin").String()
	loglevel := kingpin.Flag("log-level", "Logging Level").Default("info").String()
	logformat := kingpin.Flag("log-format", "If set use a syslog logger or JSON logging. Example: logger:syslog?appname=bob&local=7 or logger:stdout?json=true. Defaults to stderr.").Default("stderr").String()
	kingpin.Parse()

	// Check for the programs we need to actually work
	fsutil.MustLookupPaths(
		"ip",
		"vde_switch",
		"vde_plug2tap",
		"slirpvde",
	)

	flag.Set("log.level", *loglevel)
	flag.Set("log.format", *logformat)

	if !fsutil.PathExists(*socketRoot) {
		err := os.MkdirAll(*socketRoot, os.FileMode(0777))
		if err != nil {
			log.Panicln("socket-root does not exist.")
		}
	} else if !fsutil.PathIsDir(*socketRoot) {
		log.Panicln("socket-root exists but is not a directory.")
	}

	log.Infoln("VDE default socket directories:", *socketRoot)
	log.Infoln("Docker Plugin Path:", *dockerPluginPath)

	driver := NewVDENetworkDriver(*socketRoot)
	handler := network.NewHandler(driver)

	handler.ServeUnix("root", PluginName)
}
