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

package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ogalert "github.com/opsgenie/opsgenie-go-sdk-v2/alert"
	"github.com/opsgenie/opsgenie-go-sdk-v2/client"
	ogheartbeat "github.com/opsgenie/opsgenie-go-sdk-v2/heartbeat"
	ogincident "github.com/opsgenie/opsgenie-go-sdk-v2/incident"
	ogschedule "github.com/opsgenie/opsgenie-go-sdk-v2/schedule"
	ogteam "github.com/opsgenie/opsgenie-go-sdk-v2/team"
	oguser "github.com/opsgenie/opsgenie-go-sdk-v2/user"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeOpsGenie answers the API calls the collector makes. While throttled is
// set, every on-call lookup gets HTTP 429.
type fakeOpsGenie struct {
	throttled atomic.Bool
	mu        sync.Mutex
	calls     map[string]int
}

func (f *fakeOpsGenie) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/on-calls") {
		path = "/v2/schedules/{id}/on-calls"
	}
	f.mu.Lock()
	f.calls[path]++
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if path == "/v2/schedules/{id}/on-calls" && f.throttled.Load() {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"You are making too many requests!","took":0.001,"requestId":"r"}`))
		return
	}

	switch path {
	case "/v2/schedules":
		_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"primary","timezone":"UTC"},{"id":"s2","name":"secondary","timezone":"UTC"}]}`))
	case "/v2/schedules/{id}/on-calls":
		_, _ = w.Write([]byte(`{"data":{"_parent":{"id":"s1","name":"primary"},"onCallParticipants":[{"id":"u1","name":"jane@example.com","type":"user"}]}}`))
	case "/v2/alerts/count":
		_, _ = w.Write([]byte(`{"data":{"count":2}}`))
	case "/v2/heartbeats":
		_, _ = w.Write([]byte(`{"data":{"heartbeats":[]}}`))
	case "/v2/teams", "/v2/users/", "/v1/incidents":
		_, _ = w.Write([]byte(`{"data":[]}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpsGenie) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeOpsGenie) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func newTestCollector(t *testing.T) (*Collector, *fakeOpsGenie) {
	t.Helper()

	fake := &fakeOpsGenie{calls: make(map[string]int)}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	// The SDK uses plain HTTP when the host doesn't contain "api".
	cfg := &client.Config{
		ApiKey:         "test",
		OpsGenieAPIURL: client.ApiUrl(strings.TrimPrefix(srv.URL, "http://")),
		RetryPolicy:    RetryPolicy,
	}
	cfg.ConfigureLogLevel("fatal")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	alertCli, err := ogalert.NewClient(cfg)
	must(err)
	scheduleCli, err := ogschedule.NewClient(cfg)
	must(err)
	heartbeatCli, err := ogheartbeat.NewClient(cfg)
	must(err)
	teamCli, err := ogteam.NewClient(cfg)
	must(err)
	userCli, err := oguser.NewClient(cfg)
	must(err)
	incidentCli, err := ogincident.NewClient(cfg)
	must(err)

	return New(Clients{
		Alert:     alertCli,
		Schedule:  scheduleCli,
		Heartbeat: heartbeatCli,
		Team:      teamCli,
		User:      userCli,
		Incident:  incidentCli,
	}, 10*time.Second, 0), fake
}

func TestCollectServesLastPollWithoutAPICalls(t *testing.T) {
	c, fake := newTestCollector(t)
	c.Poll(context.Background())

	before := fake.total()
	if n := testutil.CollectAndCount(c, "opsgenie_oncall_info"); n != 2 {
		t.Fatalf("opsgenie_oncall_info series = %d, want 2", n)
	}
	if got := testutil.ToFloat64(c.scrapeSuccessMetric()); got != 1 {
		t.Fatalf("opsgenie_scrape_success = %v, want 1", got)
	}
	if after := fake.total(); after != before {
		t.Fatalf("Collect made %d OpsGenie API calls, want 0", after-before)
	}
}

func TestThrottledOnCallStopsAndKeepsPreviousValues(t *testing.T) {
	c, fake := newTestCollector(t)
	c.Poll(context.Background())

	fake.throttled.Store(true)
	before := fake.count("/v2/schedules/{id}/on-calls")
	c.Poll(context.Background())

	// One 429, no SDK retry, no lookup of the second schedule.
	if n := fake.count("/v2/schedules/{id}/on-calls") - before; n != 1 {
		t.Fatalf("on-call lookups after a 429 = %d, want 1", n)
	}
	// The previous on-call values are still served.
	if n := testutil.CollectAndCount(c, "opsgenie_oncall_info"); n != 2 {
		t.Fatalf("opsgenie_oncall_info series = %d, want 2", n)
	}

	want := `
# HELP opsgenie_api_throttled_total Number of polls a sub-collector stopped because OpsGenie answered HTTP 429.
# TYPE opsgenie_api_throttled_total counter
opsgenie_api_throttled_total{collector="alerts"} 0
opsgenie_api_throttled_total{collector="heartbeats"} 0
opsgenie_api_throttled_total{collector="incidents"} 0
opsgenie_api_throttled_total{collector="oncall"} 1
opsgenie_api_throttled_total{collector="teams"} 0
opsgenie_api_throttled_total{collector="users"} 0
# HELP opsgenie_collector_success 1 if the sub-collector's last poll completed without errors (on failure its previous metrics are served).
# TYPE opsgenie_collector_success gauge
opsgenie_collector_success{collector="alerts"} 1
opsgenie_collector_success{collector="heartbeats"} 1
opsgenie_collector_success{collector="incidents"} 1
opsgenie_collector_success{collector="oncall"} 0
opsgenie_collector_success{collector="teams"} 1
opsgenie_collector_success{collector="users"} 1
# HELP opsgenie_scrape_success 1 if the last poll completed without errors.
# TYPE opsgenie_scrape_success gauge
opsgenie_scrape_success 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"opsgenie_api_throttled_total", "opsgenie_collector_success", "opsgenie_scrape_success"); err != nil {
		t.Fatal(err)
	}
}

func TestPollTimeoutStopsBetweenCalls(t *testing.T) {
	c, fake := newTestCollector(t)
	c.timeout = 50 * time.Millisecond
	c.requestInterval = time.Hour

	start := time.Now()
	c.Poll(context.Background())

	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Poll took %v, want it bounded by the poll timeout", d)
	}
	if n := fake.count("/v2/schedules/{id}/on-calls"); n != 0 {
		t.Fatalf("on-call lookups = %d, want 0 once the poll timed out", n)
	}
}

// scrapeSuccessMetric collects opsgenie_scrape_success alone, for ToFloat64.
func (c *Collector) scrapeSuccessMetric() prometheus.Collector {
	return scrapeSuccessOnly{c}
}

type scrapeSuccessOnly struct{ c *Collector }

func (s scrapeSuccessOnly) Describe(ch chan<- *prometheus.Desc) { ch <- s.c.scrapeSuccess }

func (s scrapeSuccessOnly) Collect(ch chan<- prometheus.Metric) {
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()
	ch <- prometheus.MustNewConstMetric(s.c.scrapeSuccess, prometheus.GaugeValue, s.c.pollSuccess)
}
