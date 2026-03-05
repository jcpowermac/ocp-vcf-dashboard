package prow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// MinOCPVersion is the minimum OCP version to include (EOL cutoff).
const MinOCPVersion = "4.12"

// versionRE extracts OCP versions like 4.18 or 5.0 from job names.
var versionRE = regexp.MustCompile(`(?:^|-)(\d+\.\d+)(?:-|$)`)

// JobRun represents a single execution of a Prow job.
type JobRun struct {
	Job            string    `json:"job"`
	State          string    `json:"state"`
	StartTime      time.Time `json:"start_time"`
	CompletionTime time.Time `json:"completion_time,omitempty"`
	URL            string    `json:"url"`
	BuildID        string    `json:"build_id"`
}

// JobSummary holds aggregated metrics for a unique job across all its runs.
type JobSummary struct {
	Job        string   `json:"job"`
	OCPVersion string   `json:"ocp_version"`
	Variant    string   `json:"variant"`
	Runs       []JobRun `json:"-"`

	// Pre-computed fields for template rendering
	LatestState    string  `json:"latest_state"`
	LatestURL      string  `json:"latest_url"`
	TotalRuns      int     `json:"total_runs"`
	FailureCount   int     `json:"failure_count"`
	FailureRate    float64 `json:"failure_rate"`
	LastSuccessAge string  `json:"last_success_age"`
	LatestStart    string  `json:"latest_start"`
	LatestDuration string  `json:"latest_duration"`
	StateSparkline string  `json:"state_sparkline"`
}

// Analyze performs the full pipeline: filter -> aggregate -> compute summaries.
func Analyze(data *prowResponse) []JobSummary {
	runs := extractVSpherePeriodicJobs(data)
	return aggregate(runs)
}

func extractVSpherePeriodicJobs(data *prowResponse) []JobRun {
	var runs []JobRun

	for _, item := range data.Items {
		if item.Spec.Type != "periodic" {
			continue
		}

		jobName := item.Spec.Job
		if !strings.Contains(strings.ToLower(jobName), "vsphere") {
			continue
		}

		ver := extractOCPVersion(jobName)
		if ver != "unknown" && ver < MinOCPVersion {
			continue
		}

		startTime := parseTime(item.Status.StartTime)
		if startTime.IsZero() {
			continue
		}

		runs = append(runs, JobRun{
			Job:            jobName,
			State:          item.Status.State,
			StartTime:      startTime,
			CompletionTime: parseTime(item.Status.CompletionTime),
			URL:            item.Status.URL,
			BuildID:        item.Status.BuildID,
		})
	}

	return runs
}

func aggregate(runs []JobRun) []JobSummary {
	byJob := make(map[string][]JobRun)
	for _, run := range runs {
		byJob[run.Job] = append(byJob[run.Job], run)
	}

	var summaries []JobSummary
	for jobName, jobRuns := range byJob {
		// Sort runs most-recent-first
		sort.Slice(jobRuns, func(i, j int) bool {
			return jobRuns[i].StartTime.After(jobRuns[j].StartTime)
		})

		s := JobSummary{
			Job:        jobName,
			OCPVersion: extractOCPVersion(jobName),
			Variant:    extractVariant(jobName),
			Runs:       jobRuns,
		}

		// Compute summary fields
		latest := jobRuns[0]
		s.LatestState = latest.State
		s.LatestURL = latest.URL
		s.TotalRuns = len(jobRuns)
		s.LatestStart = latest.StartTime.Format("Jan 02 15:04")

		if !latest.CompletionTime.IsZero() {
			dur := latest.CompletionTime.Sub(latest.StartTime)
			if dur < 0 {
				dur = 0
			}
			hours := int(dur.Hours())
			minutes := int(dur.Minutes()) % 60
			seconds := int(dur.Seconds()) % 60
			s.LatestDuration = fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
		} else {
			s.LatestDuration = "--:--:--"
		}

		for _, r := range jobRuns {
			if r.State == "failure" {
				s.FailureCount++
			}
		}
		if s.TotalRuns > 0 {
			s.FailureRate = float64(s.FailureCount) / float64(s.TotalRuns)
		}

		s.LastSuccessAge = computeLastSuccessAge(jobRuns)
		s.StateSparkline = computeSparkline(jobRuns)

		summaries = append(summaries, s)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Job < summaries[j].Job
	})

	return summaries
}

func extractOCPVersion(jobName string) string {
	matches := versionRE.FindAllStringSubmatch(jobName, -1)
	if len(matches) == 0 {
		return "unknown"
	}
	// For upgrade jobs, the target version is the last match
	return matches[len(matches)-1][1]
}

func extractVariant(jobName string) string {
	name := jobName
	for _, prefix := range []string{
		"periodic-ci-openshift-release-main-",
		"periodic-ci-openshift-",
		"openshift-",
		"release-",
	} {
		if strings.HasPrefix(name, prefix) {
			name = name[len(prefix):]
			break
		}
	}

	switch {
	case strings.Contains(name, "upgrade"):
		return "upgrade"
	case strings.Contains(name, "serial") && strings.Contains(name, "techpreview"):
		return "tp-serial"
	case strings.Contains(name, "techpreview"):
		return "techpreview"
	case strings.Contains(name, "serial"):
		return "serial"
	case strings.Contains(name, "upi"):
		return "upi"
	case strings.Contains(name, "static"):
		return "static"
	case strings.Contains(name, "csi"):
		return "csi"
	case strings.Contains(name, "zones"):
		return "zones"
	case strings.Contains(name, "assisted"):
		return "assisted"
	case strings.Contains(name, "operator"):
		return "operator"
	case strings.Contains(name, "prfinder"):
		return "prfinder"
	default:
		return "e2e"
	}
}

func parseTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

func computeLastSuccessAge(runs []JobRun) string {
	for _, r := range runs {
		if r.State == "success" {
			delta := time.Since(r.StartTime)
			hours := delta.Hours()
			if hours < 1 {
				return fmt.Sprintf("%dm ago", int(delta.Minutes()))
			}
			if hours < 48 {
				return fmt.Sprintf("%dh ago", int(hours))
			}
			return fmt.Sprintf("%dd ago", int(hours/24))
		}
	}
	return "never"
}

func computeSparkline(runs []JobRun) string {
	mapping := map[string]string{
		"success":   "S",
		"failure":   "F",
		"pending":   "P",
		"aborted":   "A",
		"error":     "E",
		"triggered": "T",
	}

	n := 6
	if len(runs) < n {
		n = len(runs)
	}

	var sb strings.Builder
	for i := 0; i < n; i++ {
		if c, ok := mapping[runs[i].State]; ok {
			sb.WriteString(c)
		} else {
			sb.WriteString("?")
		}
	}
	return sb.String()
}
