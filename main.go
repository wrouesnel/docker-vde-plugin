package main

import (
	"flag"
	"os"

	"os/signal"
	"syscall"
	"strings"

	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/docker/go-plugins-helpers/network"
	"github.com/wrouesnel/go.log"
	"github.com/wrouesnel/multihttp"

	"github.com/wrouesnel/docker-vde-plugin/fsutil"
	"gopkg.in/alecthomas/kingpin.v2"
	"github.com/docker/go-plugins-helpers/sdk"
	"net/url"
	"runtime"
)

const (
	NetworkPluginName string = "vde"
)

// TODO: do some checks to make sure we properly clean up
func main() {
	dockerPluginPath := kingpin.Flag("docker-net-plugins", "Listen path for the plugin.").Default("unix:///run/docker/plugins/vde.sock,unix:///run/docker/plugins/vde-ipam.sock").String()
	socketRoot := kingpin.Flag("socket-root", "Path where networks and sockets should be created").Default("/run/docker-vde-plugin").String()
	loglevel := kingpin.Flag("log-level", "Logging Level").Default("info").String()
	logformat := kingpin.Flag("log-format", "If set use a syslog logger or JSON logging. Example: logger:syslog?appname=bob&local=7 or logger:stdout?json=true. Defaults to stderr.").Default("stderr").String()
	vdePlugBin := kingpin.Flag("vde_plug", "Path to the vde_plug binary to use.").Default("vde_plug").String()
	kingpin.Parse()

	exitCh := make(chan int)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<- sigCh
		exitCh <- 0
	}()

	// Include debugging facilities to dump stack traces.
	sigUsr := make(chan os.Signal, 1)
	signal.Notify(sigUsr, syscall.SIGUSR1)
	go func() {
		for _ = range sigUsr {
			bufSize := 8192
			for {
				stacktrace := make([]byte,bufSize)
				n := runtime.Stack(stacktrace,true)
				if n < bufSize {
					os.Stderr.Write(stacktrace)
					break
				}
				bufSize = bufSize * 2
			}
		}
	}()

	// Check for the programs we need to actually work
	fsutil.MustLookupPaths(
		"ip",
		"vde_switch",
		*vdePlugBin,
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

	driver := NewVDENetworkDriver(*socketRoot, "vde_switch", *vdePlugBin)
	ipamDriver := &IPAMDriver{driver}

	handler := sdk.NewHandler()

	network.InitMux(handler, driver)
	ipam.InitMux(handler, ipamDriver)

	// For the time being we only support serving on a unix-host since cross-
	// host or remote support doesn't make sense.
	listenAddrs := []string{}
	for _, s := range strings.Split(*dockerPluginPath,",") {
		u, err := url.Parse(s)
		if err != nil {
			log.Panicln("Could not parse URL listen path:", err)
		}

		if u.Scheme != "unix" {
			log.Panicln("Only the \"unix\" paths are currently supported.")
		}

		listenAddrs = append(listenAddrs, u.String())
	}

	//u, err := user.Lookup("root")
	//if err != nil {
	//	log.Panicln("Error looking up user identity for plugin.")
	//}
	//
	//gid, err := strconv.Atoi(u.Gid)
	//if err != nil {
	//	log.Panicln("Could not convert gid to integer:", u.Gid, err)
	//}

	// Handle listening on multiple addresses to work around docker 1.13
	// multihost bug.
	listeners, err := multihttp.Listen(listenAddrs, handler)
	// Don't block if we were already cleaning up safely.
	go func() {
		if err != nil {
			log.Errorln("Failed to start listening on a given address:", err)
			exitCh <- 1
		}
	}()

	// Wait to exit.
	exitCode := <- exitCh
	for _, l := range listeners {
		l.Close()
	}

	os.Exit(exitCode)
}
