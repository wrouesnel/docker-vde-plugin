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
	"github.com/wrouesnel/docker-vde-plugin/assets"
	"gopkg.in/alecthomas/kingpin.v2"
	"github.com/docker/go-plugins-helpers/sdk"
	"net/url"
	"runtime"
	"io/ioutil"
	"path"
	"net"
)

const (
	ExecutableName = "docker-net-plugins"
	NetworkPluginName string = "vde"
)

func main() {
	os.Exit(realMain(nil))
}

// realMain is the actual main function, separated out for testing purposes.
// notifyStart may be nil, but if supplied will be closed once the plugin
// has fully initialized.
func realMain(notifyStart chan<- struct{}) int {
	dockerPluginPath := kingpin.Flag(ExecutableName, "Listen path for the plugin.").Default("unix:///run/docker/plugins/vde.sock,unix:///run/docker/plugins/vde-ipam.sock").String()
	socketRoot := kingpin.Flag("socket-root", "Path where networks and sockets should be created").Default("/run/docker-vde-plugin").String()
	loglevel := kingpin.Flag("log-level", "Logging Level").Default("info").String()
	logformat := kingpin.Flag("log-format", "If set use a syslog logger or JSON logging. Example: logger:syslog?appname=bob&local=7 or logger:stdout?json=true. Defaults to stderr.").Default("stderr").String()
	vdePlugBin := kingpin.Flag("vde_plug", "Path to the vde_plug binary to use. If not specified, a precompiled version is unpacked.").String()
	vdeSwitchBin := kingpin.Flag("vde_switch", "Path to the vde_switch binary to use. If not specified, a precompiled version is unpacked.").String()
	kingpin.Parse()

	flag.Set("log.level", *loglevel)
	flag.Set("log.format", *logformat)

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

	// Do we need an extraction directory for unpacking internal binaries?
	var extractDir string
	if *vdePlugBin == "" || *vdeSwitchBin == "" {
		var err error
		extractDir, err = ioutil.TempDir("", "docker-vde-plugin")
		if err != nil {
			log.Panicln("could not make a temporary directory for internal binary extraction")
		}
		log.Debugln("Will extract files to:", extractDir)
		// Clean up on exit.
		defer func () {
			log.Debugln("Removing", extractDir)
			os.RemoveAll(extractDir)
		}()
	}

	// If nothing specified, we want to unpack and sudo own from ourselves.
	var actualVdePlugBin, actualVdeSwitchBin string
	log.Debugln("Have pre-compiled assets:", strings.Join(assets.AssetNames(), " "))
	if *vdePlugBin != "" {
		actualVdePlugBin = *vdePlugBin
	} else {
		// Unpack the version in the plugin.
		log.Debugln("Unpacking vde_plug binary...")
		actualVdePlugBin = path.Join(extractDir, "vde_plug")
		ioutil.WriteFile(actualVdePlugBin, assets.MustAsset("vde_plug"), os.FileMode(0700))
		defer func() {
			log.Debugln("Removing", actualVdePlugBin)
			os.Remove(actualVdePlugBin)
		}()
	}

	if *vdeSwitchBin != "" {
		actualVdeSwitchBin = *vdeSwitchBin
	} else {
		// Unpack the version in the plugin.
		log.Debugln("Unpacking vde_switch binary...")
		actualVdeSwitchBin = path.Join(extractDir, "vde_switch")
		ioutil.WriteFile(actualVdeSwitchBin, assets.MustAsset("vde_switch"), os.FileMode(0700))
		defer func() {
			log.Debugln("Removing", actualVdeSwitchBin)
			os.Remove(actualVdeSwitchBin)
		}()
	}

	log.Debugln("Using vde_plug binary:", actualVdePlugBin)
	log.Debugln("Using vde_switch binary:", actualVdeSwitchBin)

	// Check for the programs we need to actually work
	fsutil.MustLookupPaths(
		"ip",
		actualVdePlugBin,
		actualVdeSwitchBin,
	)

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

	driver := NewVDENetworkDriver(*socketRoot, actualVdeSwitchBin, actualVdePlugBin)
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

		// Check if the listen addrs exist already and appear to be in use.
		if fsutil.PathExists(u.Path) {
			// Try and connect to it.
			if _, err := net.Dial("unix", u.Path); err == nil {
				log.Panicln("Bind path already in use by listening process:", u.Path)
			} else {
				log.Warnln("Cleaning up left-over socket:", u.Path)
				os.Remove(u.Path)
			}
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

	log.Infoln("Plugin listening and ready.")
	if notifyStart != nil {
		log.Debugln("Closing notify channel.")
		close(notifyStart)
	}

	// Wait to exit.
	exitCode := <- exitCh
	for _, l := range listeners {
		l.Close()
	}

	return exitCode
}
