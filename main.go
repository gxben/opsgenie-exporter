// Copyright 2026 Benjamin Zores
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	ogalert "github.com/opsgenie/opsgenie-go-sdk-v2/alert"
	"github.com/opsgenie/opsgenie-go-sdk-v2/client"
	ogheartbeat "github.com/opsgenie/opsgenie-go-sdk-v2/heartbeat"
	ogincident "github.com/opsgenie/opsgenie-go-sdk-v2/incident"
	ogschedule "github.com/opsgenie/opsgenie-go-sdk-v2/schedule"
	ogteam "github.com/opsgenie/opsgenie-go-sdk-v2/team"
	oguser "github.com/opsgenie/opsgenie-go-sdk-v2/user"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gxben/opsgenie-exporter/internal/collector"
)

var version = "dev" // injected by ldflags at build time

func main() {
	var (
		listenAddr    = flag.String("web.listen-address", ":9428", "Address on which to expose metrics")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics")
		apiKey        = flag.String("opsgenie.api-key", "", "OpsGenie API key (or set OPSGENIE_API_KEY env var)")
		apiURL        = flag.String("opsgenie.api-url", "", `OpsGenie API URL (default: api.opsgenie.com; EU: api.eu.opsgenie.com)`)
		scrapeTimeout = flag.Duration("opsgenie.scrape-timeout", 60*time.Second, "Timeout for a single OpsGenie API scrape")
		showVersion   = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("opsgenie-exporter %s\n", version)
		os.Exit(0)
	}

	// API key: flag takes precedence over env var.
	key := *apiKey
	if key == "" {
		key = os.Getenv("OPSGENIE_API_KEY")
	}
	if key == "" {
		slog.Error("OpsGenie API key is required (--opsgenie.api-key or OPSGENIE_API_KEY)")
		os.Exit(1)
	}

	cfg := &client.Config{ApiKey: key}
	// Suppress internal SDK logrus chatter (e.g. "Could not set requestId").
	cfg.ConfigureLogLevel("error")
	if *apiURL != "" {
		cfg.OpsGenieAPIURL = client.ApiUrl(*apiURL)
	}

	alertCli, err := ogalert.NewClient(cfg)
	if err != nil {
		slog.Error("failed to create alert client", "err", err)
		os.Exit(1)
	}
	scheduleCli, err := ogschedule.NewClient(cfg)
	if err != nil {
		slog.Error("failed to create schedule client", "err", err)
		os.Exit(1)
	}
	heartbeatCli, err := ogheartbeat.NewClient(cfg)
	if err != nil {
		slog.Error("failed to create heartbeat client", "err", err)
		os.Exit(1)
	}
	teamCli, err := ogteam.NewClient(cfg)
	if err != nil {
		slog.Error("failed to create team client", "err", err)
		os.Exit(1)
	}
	userCli, err := oguser.NewClient(cfg)
	if err != nil {
		slog.Error("failed to create user client", "err", err)
		os.Exit(1)
	}
	incidentCli, err := ogincident.NewClient(cfg)
	if err != nil {
		slog.Error("failed to create incident client", "err", err)
		os.Exit(1)
	}

	coll := collector.New(collector.Clients{
		Alert:     alertCli,
		Schedule:  scheduleCli,
		Heartbeat: heartbeatCli,
		Team:      teamCli,
		User:      userCli,
		Incident:  incidentCli,
	}, *scrapeTimeout)

	reg := prometheus.NewRegistry()
	reg.MustRegister(coll)

	mux := http.NewServeMux()
	mux.Handle(*metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<html>
<head><title>OpsGenie Exporter</title></head>
<body>
<h1>OpsGenie Exporter</h1>
<p>Version: %s</p>
<p><a href="%s">Metrics</a></p>
</body>
</html>`, version, *metricsPath)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})

	slog.Info("starting opsgenie-exporter",
		"version", version,
		"listen", *listenAddr,
		"metrics_path", *metricsPath,
	)

	server := &http.Server{
		Addr:         *listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: *scrapeTimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
