package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/telekom/das-schiff-network-operator/pkg/monitoring"
	"github.com/telekom/das-schiff-network-operator/pkg/version"
	"github.com/vishvananda/netns"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func setupPrometheusRegistry() (*prometheus.Registry, error) {
	// Create a new registry.
	reg := prometheus.NewRegistry()

	// Add Go module build info.
	reg.MustRegister(collectors.NewBuildInfoCollector())
	reg.MustRegister(collectors.NewGoCollector())

	collector, err := monitoring.NewDasSchiffNetworkOperatorCollector(
		map[string]bool{
			"frr":     true,
			"netlink": true,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to create collector %w", err)
	}
	reg.MustRegister(collector)

	return reg, nil
}

func main() {
	var opts zap.Options
	var addr string

	version.Get().Print(os.Args[0])

	opts.Development = true
	opts.BindFlags(flag.CommandLine)

	flag.StringVar(&addr, "listen-address", ":7082", "The address to listen on for HTTP requests.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Setup a new registry.
	reg, err := setupPrometheusRegistry()
	if err != nil {
		log.Fatal(fmt.Errorf("prometheus registry setup error: %v", err))
	}
	fmt.Println("configured Prometheus registry")

	// Expose the registered metrics via HTTP.
	http.Handle("/metrics", promhttp.HandlerFor(
		reg,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
			Timeout:           time.Minute,
		},
	))

	server := http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 20 * time.Second,
		ReadTimeout:       time.Minute,
	}

	// Save current (non-default) netns
	currentNS, err := netns.Get()
	if err != nil {
		log.Fatalf("failed to get current netns: %v", err)
	}
	defer currentNS.Close()

	// Switch to default netns to bind the listener
	defaultNS, err := netns.GetFromPath("/proc/1/ns/net")
	if err != nil {
		log.Fatalf("failed to get default netns: %v", err)
	}
	defer defaultNS.Close()

	if err := netns.Set(defaultNS); err != nil {
		log.Fatalf("failed to switch to default netns: %v", err)
	}

	// Create listener in default netns
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	// Switch back to original (non-default) netns
	if err := netns.Set(currentNS); err != nil {
		log.Fatalf("failed to switch back to original netns: %v", err)
	}

	fmt.Println("created server, starting...", "Addr", server.Addr,
		"ReadHeaderTimeout", server.ReadHeaderTimeout, "ReadTimeout", server.ReadTimeout)

	// Run server
	if err := server.Serve(listener); err != nil {
		log.Fatal(fmt.Errorf("failed to start server: %v", err))
	}
}
