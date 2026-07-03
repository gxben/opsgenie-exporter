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
	"time"

	ogalert "github.com/opsgenie/opsgenie-go-sdk-v2/alert"
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

// Collector implements prometheus.Collector backed by the OpsGenie SDK.
type Collector struct {
	clients Clients
	timeout time.Duration

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
	scrapeDuration *prometheus.Desc
	scrapeSuccess  *prometheus.Desc
}

// New returns a Collector ready to register with a Prometheus registry.
func New(clients Clients, timeout time.Duration) *Collector {
	desc := func(subsystem, name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, name), help, labels, nil)
	}

	return &Collector{
		clients: clients,
		timeout: timeout,

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
			"Total time spent fetching data from the OpsGenie API.", nil, nil,
		),
		scrapeSuccess: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "scrape", "success"),
			"1 if the last scrape completed without errors.", nil, nil,
		),
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
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	success := 1.0

	if err := c.collect(ch); err != nil {
		slog.Error("scrape failed", "err", err)
		success = 0
	}

	ch <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds())
	ch <- prometheus.MustNewConstMetric(c.scrapeSuccess, prometheus.GaugeValue, success)
}

func (c *Collector) collect(ch chan<- prometheus.Metric) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	var errs []error

	if err := c.collectOnCall(ctx, ch); err != nil {
		slog.Warn("on-call collection partial failure", "err", err)
		errs = append(errs, fmt.Errorf("oncall: %w", err))
	}

	if err := c.collectAlerts(ctx, ch); err != nil {
		slog.Warn("alert collection partial failure", "err", err)
		errs = append(errs, fmt.Errorf("alerts: %w", err))
	}

	if err := c.collectHeartbeats(ctx, ch); err != nil {
		slog.Warn("heartbeat collection partial failure", "err", err)
		errs = append(errs, fmt.Errorf("heartbeats: %w", err))
	}

	if err := c.collectTeams(ctx, ch); err != nil {
		slog.Warn("team collection partial failure", "err", err)
		errs = append(errs, fmt.Errorf("teams: %w", err))
	}

	if err := c.collectUsers(ctx, ch); err != nil {
		slog.Warn("user collection partial failure", "err", err)
		errs = append(errs, fmt.Errorf("users: %w", err))
	}

	if err := c.collectIncidents(ctx, ch); err != nil {
		slog.Warn("incident collection partial failure", "err", err)
		errs = append(errs, fmt.Errorf("incidents: %w", err))
	}

	return errors.Join(errs...)
}

// ─── On-call ──────────────────────────────────────────────────────────────────

func (c *Collector) collectOnCall(ctx context.Context, ch chan<- prometheus.Metric) error {
	expandFalse := false
	schedules, err := c.clients.Schedule.List(ctx, &ogschedule.ListRequest{Expand: &expandFalse})
	if err != nil {
		return fmt.Errorf("listing schedules: %w", err)
	}

	flat := false // false → API returns OnCallParticipants (structured); true → OnCallRecipients ([]string only)
	for _, sched := range schedules.Schedule {
		onCalls, err := c.clients.Schedule.GetOnCalls(ctx, &ogschedule.GetOnCallsRequest{
			ScheduleIdentifierType: ogschedule.Id,
			ScheduleIdentifier:     sched.Id,
			Flat:                   &flat,
		})
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
			result, err := c.clients.Alert.CountAlerts(ctx, &ogalert.CountAlertsRequest{
				Query: fmt.Sprintf("%s AND priority:%s", baseQuery, priority),
			})
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
		return fmt.Errorf("listing heartbeats: %w", err)
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
		return fmt.Errorf("listing teams: %w", err)
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
		result, err := c.clients.User.List(ctx, &oguser.ListRequest{
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return fmt.Errorf("listing users at offset %d: %w", offset, err)
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
		result, err := c.clients.Incident.List(ctx, &ogincident.ListRequest{
			Query:  "status:open OR status:acknowledged",
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return fmt.Errorf("listing incidents at offset %d: %w", offset, err)
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
