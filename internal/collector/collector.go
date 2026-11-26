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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	ogalert "github.com/opsgenie/opsgenie-go-sdk-v2/alert"
	"github.com/opsgenie/opsgenie-go-sdk-v2/client"
	ogheartbeat "github.com/opsgenie/opsgenie-go-sdk-v2/heartbeat"
	ogincident "github.com/opsgenie/opsgenie-go-sdk-v2/incident"
	ogschedule "github.com/opsgenie/opsgenie-go-sdk-v2/schedule"
	ogteam "github.com/opsgenie/opsgenie-go-sdk-v2/team"
	oguser "github.com/opsgenie/opsgenie-go-sdk-v2/user"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "opsgenie"
	pageSize  = 100
)

// Clients bundles all OpsGenie API clients needed by the collector.
type Clients struct {
	Alert     *ogalert.Client
	Schedule  *ogschedule.Client
	Heartbeat *ogheartbeat.Client
	Team      *ogteam.Client
	User      *oguser.Client
	Incident  *ogincident.Client
}

// errThrottled reports that OpsGenie answered HTTP 429: the rest of the
// sub-collector is skipped until the next poll.
var errThrottled = errors.New("rate limited by OpsGenie (HTTP 429)")

// subCollector is one group of OpsGenie API calls (on-call, alerts, …).
type subCollector struct {
	name    string
	collect func(context.Context, chan<- prometheus.Metric) error
}

// subCollectorState is the last poll outcome of a sub-collector. metrics keeps
// the last successful result: a failed poll doesn't blank the dashboards.
type subCollectorState struct {
	metrics     []prometheus.Metric
	success     float64
	lastSuccess time.Time
	throttled   float64
}

// Collector implements prometheus.Collector backed by the OpsGenie SDK. Run
// polls the API in the background; Collect only serves the last poll, so a
// scrape never calls OpsGenie.
type Collector struct {
	clients         Clients
	timeout         time.Duration
	requestInterval time.Duration

	mu           sync.RWMutex
	states       map[string]*subCollectorState
	pollDuration float64
	pollSuccess  float64

	// on-call metrics
	oncallInfo *prometheus.Desc

	// alert metrics
	alertsTotal *prometheus.Desc

	// heartbeat metrics
	heartbeatEnabled *prometheus.Desc
	heartbeatExpired *prometheus.Desc

	// team metrics
	teamInfo   *prometheus.Desc
	teamsTotal *prometheus.Desc

	// user metrics
	userInfo   *prometheus.Desc
	usersTotal *prometheus.Desc

	// incident metrics
	incidentsTotal *prometheus.Desc
	incidentInfo   *prometheus.Desc

	// scrape meta
	scrapeDuration       *prometheus.Desc
	scrapeSuccess        *prometheus.Desc
	collectorSuccess     *prometheus.Desc
	collectorLastSuccess *prometheus.Desc
	apiThrottled         *prometheus.Desc
}

// New returns a Collector ready to register with a Prometheus registry.
// timeout bounds one poll; requestInterval spaces the consecutive calls of a
// sub-collector (one per schedule, one per alert priority).
func New(clients Clients, timeout, requestInterval time.Duration) *Collector {
	desc := func(subsystem, name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, name), help, labels, nil)
	}

	c := &Collector{
		clients:         clients,
		timeout:         timeout,
		requestInterval: requestInterval,
		states:          make(map[string]*subCollectorState),

		oncallInfo: desc("oncall", "info",
			"Info metric — 1 per participant currently on call per schedule. Join on schedule_id or user_name for alerting.",
			[]string{"schedule_id", "schedule_name", "schedule_timezone", "user_name", "user_id", "user_type"},
		),

		alertsTotal: desc("alerts", "total",
			"Number of active OpsGenie alerts, partitioned by status and priority.",
			[]string{"status", "priority"},
		),

		heartbeatEnabled: desc("heartbeat", "enabled",
			"1 if the heartbeat monitor is enabled.",
			[]string{"name", "owner_team"},
		),
		heartbeatExpired: desc("heartbeat", "expired",
			"1 if the heartbeat has not pinged within its configured interval.",
			[]string{"name", "owner_team"},
		),

		teamInfo: desc("team", "info",
			"Info metric — 1 per OpsGenie team.",
			[]string{"team_id", "team_name"},
		),
		teamsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "teams", "total"),
			"Total number of OpsGenie teams.", nil, nil,
		),

		userInfo: desc("user", "info",
			"Info metric — 1 per OpsGenie user.",
			[]string{"user_id", "username", "full_name", "role"},
		),
		usersTotal: desc("users", "total",
			"Number of OpsGenie users, partitioned by role.",
			[]string{"role"},
		),

		incidentsTotal: desc("incidents", "total",
			"Number of OpsGenie incidents, partitioned by status and priority.",
			[]string{"status", "priority"},
		),
		incidentInfo: desc("incident", "info",
			"Info metric — 1 per open or acknowledged OpsGenie incident.",
			[]string{"incident_id", "message", "status", "priority", "owner_team"},
		),

		scrapeDuration: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "scrape", "duration_seconds"),
			"Total time spent fetching data from the OpsGenie API in the last poll.", nil, nil,
		),
		scrapeSuccess: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "scrape", "success"),
			"1 if the last poll completed without errors.", nil, nil,
		),
		collectorSuccess: desc("collector", "success",
			"1 if the sub-collector's last poll completed without errors (on failure its previous metrics are served).",
			[]string{"collector"},
		),
		collectorLastSuccess: desc("collector", "last_success_timestamp_seconds",
			"Unix time of the sub-collector's last successful poll.",
			[]string{"collector"},
		),
		apiThrottled: desc("api", "throttled_total",
			"Number of polls a sub-collector stopped because OpsGenie answered HTTP 429.",
			[]string{"collector"},
		),
	}
	for _, sc := range c.subCollectors() {
		c.states[sc.name] = &subCollectorState{}
	}

	return c
}

func (c *Collector) subCollectors() []subCollector {
	return []subCollector{
		{"oncall", c.collectOnCall},
		{"alerts", c.collectAlerts},
		{"heartbeats", c.collectHeartbeats},
		{"teams", c.collectTeams},
		{"users", c.collectUsers},
		{"incidents", c.collectIncidents},
	}
}

// RetryPolicy is the SDK's default retry policy minus HTTP 429: a throttled
// call returns at once and the sub-collector stops until the next poll,
// instead of spending the rate limit on retries (the SDK retries 429 four
// times by default).
func RetryPolicy(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return true, err
	}
	return resp.StatusCode == 0 || (resp.StatusCode >= 500 && resp.StatusCode != 501), nil
}

func isThrottled(err error) bool {
	var apiErr *client.ApiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
}

// wrapThrottled turns an HTTP 429 from the SDK into errThrottled.
func wrapThrottled(err error) error {
	if isThrottled(err) {
		return errThrottled
	}
	return err
}

// pause waits requestInterval before the next call of a sub-collector. The SDK
// doesn't attach the context to its HTTP requests, so this is also where the
// poll timeout takes effect.
func (c *Collector) pause(ctx context.Context) error {
	if c.requestInterval <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(c.requestInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.oncallInfo
	ch <- c.alertsTotal
	ch <- c.heartbeatEnabled
	ch <- c.heartbeatExpired
	ch <- c.teamInfo
	ch <- c.teamsTotal
	ch <- c.userInfo
	ch <- c.usersTotal
	ch <- c.incidentsTotal
	ch <- c.incidentInfo
	ch <- c.scrapeDuration
	ch <- c.scrapeSuccess
	ch <- c.collectorSuccess
	ch <- c.collectorLastSuccess
	ch <- c.apiThrottled
}

// Collect implements prometheus.Collector. It serves the last poll and never
// calls the OpsGenie API.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, sc := range c.subCollectors() {
		st := c.states[sc.name]
		for _, m := range st.metrics {
			ch <- m
		}
		ch <- prometheus.MustNewConstMetric(c.collectorSuccess, prometheus.GaugeValue, st.success, sc.name)
		ch <- prometheus.MustNewConstMetric(c.apiThrottled, prometheus.CounterValue, st.throttled, sc.name)
		if !st.lastSuccess.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.collectorLastSuccess, prometheus.GaugeValue, float64(st.lastSuccess.Unix()), sc.name)
		}
	}

	ch <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, c.pollDuration)
	ch <- prometheus.MustNewConstMetric(c.scrapeSuccess, prometheus.GaugeValue, c.pollSuccess)
}

// Run polls OpsGenie right away, then every interval until ctx is cancelled.
// Polls never overlap: a poll longer than interval delays the next one.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		c.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Poll runs every sub-collector once and stores the result for Collect.
func (c *Collector) Poll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := time.Now()
	success := 1.0

	for _, sc := range c.subCollectors() {
		metrics, err := gather(ctx, sc.collect)

		c.mu.Lock()
		st := c.states[sc.name]
		if err != nil {
			success = 0
			st.success = 0
			if errors.Is(err, errThrottled) {
				st.throttled++
			}
			slog.Warn("collection failed, serving the previous values", "collector", sc.name, "err", err)
		} else {
			st.metrics = metrics
			st.success = 1
			st.lastSuccess = time.Now()
		}
		c.mu.Unlock()
	}

	c.mu.Lock()
	c.pollDuration = time.Since(start).Seconds()
	c.pollSuccess = success
	c.mu.Unlock()
}

// gather runs one sub-collector and returns the metrics it emitted.
func gather(ctx context.Context, collect func(context.Context, chan<- prometheus.Metric) error) ([]prometheus.Metric, error) {
	ch := make(chan prometheus.Metric)
	var err error
	go func() {
		err = collect(ctx, ch)
		close(ch)
	}()

	var metrics []prometheus.Metric
	for m := range ch {
		metrics = append(metrics, m)
	}
	return metrics, err
}

// ─── On-call ──────────────────────────────────────────────────────────────────

func (c *Collector) collectOnCall(ctx context.Context, ch chan<- prometheus.Metric) error {
	expandFalse := false
	schedules, err := c.clients.Schedule.List(ctx, &ogschedule.ListRequest{Expand: &expandFalse})
	if err != nil {
		return fmt.Errorf("listing schedules: %w", wrapThrottled(err))
	}

	flat := false // false → API returns OnCallParticipants (structured); true → OnCallRecipients ([]string only)
	for _, sched := range schedules.Schedule {
		if err := c.pause(ctx); err != nil {
			return err
		}
		onCalls, err := c.clients.Schedule.GetOnCalls(ctx, &ogschedule.GetOnCallsRequest{
			ScheduleIdentifierType: ogschedule.Id,
			ScheduleIdentifier:     sched.Id,
			Flat:                   &flat,
		})
		if isThrottled(err) {
			return fmt.Errorf("getting on-call for schedule %s: %w", sched.Name, errThrottled)
		}
		if err != nil {
			slog.Warn("failed to get on-call", "schedule", sched.Name, "err", err)
			continue
		}

		// OnCallParticipants contains the expanded user list when Flat=true.
		for _, p := range onCalls.OnCallParticipants {
			ch <- prometheus.MustNewConstMetric(
				c.oncallInfo,
				prometheus.GaugeValue,
				1,
				sched.Id,
				sched.Name,
				sched.Timezone,
				p.Name,
				p.Id,
				string(p.Type),
			)
		}
	}

	return nil
}

// ─── Alerts ───────────────────────────────────────────────────────────────────

func (c *Collector) collectAlerts(ctx context.Context, ch chan<- prometheus.Metric) error {
	type key struct{ status, priority string }
	counts := make(map[key]float64)

	// OpsGenie has no "acknowledged" status value; acknowledged alerts remain
	// status:open with acknowledged:true. We expose them under the label
	// status="acknowledged" to match user expectations.
	queries := map[string]string{
		"open":         "status:open AND acknowledged:false",
		"acknowledged": "status:open AND acknowledged:true",
	}
	for status, baseQuery := range queries {
		for _, priority := range []string{"P1", "P2", "P3", "P4", "P5"} {
			if err := c.pause(ctx); err != nil {
				return err
			}
			result, err := c.clients.Alert.CountAlerts(ctx, &ogalert.CountAlertsRequest{
				Query: fmt.Sprintf("%s AND priority:%s", baseQuery, priority),
			})
			if isThrottled(err) {
				return fmt.Errorf("counting %s %s alerts: %w", status, priority, errThrottled)
			}
			if err != nil {
				slog.Warn("failed to count alerts", "status", status, "priority", priority, "err", err)
				continue
			}
			counts[key{status, priority}] = float64(result.Count)
		}
	}

	for k, v := range counts {
		ch <- prometheus.MustNewConstMetric(c.alertsTotal, prometheus.GaugeValue, v, k.status, k.priority)
	}

	return nil
}

// ─── Heartbeats ───────────────────────────────────────────────────────────────

func (c *Collector) collectHeartbeats(ctx context.Context, ch chan<- prometheus.Metric) error {
	result, err := c.clients.Heartbeat.List(ctx)
	if err != nil {
		return fmt.Errorf("listing heartbeats: %w", wrapThrottled(err))
	}

	boolVal := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}

	for _, hb := range result.Heartbeats {
		ch <- prometheus.MustNewConstMetric(c.heartbeatEnabled, prometheus.GaugeValue, boolVal(hb.Enabled), hb.Name, hb.OwnerTeam.Name)
		ch <- prometheus.MustNewConstMetric(c.heartbeatExpired, prometheus.GaugeValue, boolVal(hb.Expired), hb.Name, hb.OwnerTeam.Name)
	}

	return nil
}

// ─── Teams ────────────────────────────────────────────────────────────────────

func (c *Collector) collectTeams(ctx context.Context, ch chan<- prometheus.Metric) error {
	result, err := c.clients.Team.List(ctx, &ogteam.ListTeamRequest{})
	if err != nil {
		return fmt.Errorf("listing teams: %w", wrapThrottled(err))
	}

	ch <- prometheus.MustNewConstMetric(c.teamsTotal, prometheus.GaugeValue, float64(len(result.Teams)))

	for _, t := range result.Teams {
		ch <- prometheus.MustNewConstMetric(c.teamInfo, prometheus.GaugeValue, 1, t.Id, t.Name)
	}

	return nil
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (c *Collector) collectUsers(ctx context.Context, ch chan<- prometheus.Metric) error {
	roleCounts := make(map[string]float64)

	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := c.clients.User.List(ctx, &oguser.ListRequest{
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return fmt.Errorf("listing users at offset %d: %w", offset, wrapThrottled(err))
		}

		for _, u := range result.Users {
			roleName := ""
			if u.Role != nil {
				roleName = u.Role.RoleName
			}
			roleCounts[roleName]++
			ch <- prometheus.MustNewConstMetric(
				c.userInfo,
				prometheus.GaugeValue,
				1,
				u.Id, u.Username, u.FullName, roleName,
			)
		}

		if len(result.Users) < pageSize {
			break
		}
		offset += pageSize
	}

	for role, count := range roleCounts {
		ch <- prometheus.MustNewConstMetric(c.usersTotal, prometheus.GaugeValue, count, role)
	}

	return nil
}

// ─── Incidents ────────────────────────────────────────────────────────────────

func (c *Collector) collectIncidents(ctx context.Context, ch chan<- prometheus.Metric) error {
	type key struct{ status, priority string }
	counts := make(map[key]float64)

	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := c.clients.Incident.List(ctx, &ogincident.ListRequest{
			Query:  "status:open OR status:acknowledged",
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return fmt.Errorf("listing incidents at offset %d: %w", offset, wrapThrottled(err))
		}

		for _, inc := range result.Incidents {
			priority := string(inc.Priority)
			k := key{inc.Status, priority}
			counts[k]++

			ch <- prometheus.MustNewConstMetric(
				c.incidentInfo,
				prometheus.GaugeValue,
				1,
				inc.Id, inc.Message, inc.Status, priority, inc.OwnerTeam,
			)
		}

		if len(result.Incidents) < pageSize {
			break
		}
		offset += pageSize
	}

	for k, v := range counts {
		ch <- prometheus.MustNewConstMetric(c.incidentsTotal, prometheus.GaugeValue, v, k.status, k.priority)
	}

	return nil
}
