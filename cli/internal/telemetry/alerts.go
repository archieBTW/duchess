package telemetry

import (
	"fmt"
	"strings"

	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/models"
)

// EvaluateAlerts inspects a DiscoveryReport against alert thresholds and returns
// proactive health warnings and critical findings (disk, RAM, CPU, load, failed services,
// unhealthy/stopped containers, and expiring TLS certificates).
func EvaluateAlerts(rep *models.DiscoveryReport, th config.AlertThresholds) []models.Alert {
	if rep == nil || rep.Error != "" {
		return nil
	}
	if th.DiskWarnPct <= 0 {
		th = config.Default().Alerts
	}
	var alerts []models.Alert

	if st := rep.Stats; st != nil {
		if st.CPUUsagePercent >= th.CPUWarnPct {
			sev := "warning"
			if st.CPUUsagePercent >= 95 {
				sev = "critical"
			}
			alerts = append(alerts, models.Alert{
				Severity: sev,
				Category: "cpu",
				Message:  fmt.Sprintf("High CPU usage: %.1f%%", st.CPUUsagePercent),
			})
		}
		if st.MemUsagePercent >= th.MemWarnPct {
			sev := "warning"
			if st.MemUsagePercent >= 95 {
				sev = "critical"
			}
			alerts = append(alerts, models.Alert{
				Severity: sev,
				Category: "memory",
				Message:  fmt.Sprintf("High memory usage: %.1f%%", st.MemUsagePercent),
			})
		}
		if st.CPUCores > 0 && st.Load1 > float64(st.CPUCores)*th.LoadFactor {
			alerts = append(alerts, models.Alert{
				Severity: "warning",
				Category: "load",
				Message:  fmt.Sprintf("High load avg %.2f on %d vCPU", st.Load1, st.CPUCores),
			})
		}
		for _, d := range st.Disks {
			if d.UsePercent >= th.DiskCritPct {
				alerts = append(alerts, models.Alert{
					Severity: "critical",
					Category: "disk",
					Subject:  d.MountPoint,
					Message:  fmt.Sprintf("Disk %s at %.0f%% (critical)", d.MountPoint, d.UsePercent),
				})
			} else if d.UsePercent >= th.DiskWarnPct {
				alerts = append(alerts, models.Alert{
					Severity: "warning",
					Category: "disk",
					Subject:  d.MountPoint,
					Message:  fmt.Sprintf("Disk %s at %.0f%%", d.MountPoint, d.UsePercent),
				})
			}
		}
	}

	for _, svc := range rep.Services {
		if strings.EqualFold(svc.ActiveState, "failed") {
			alerts = append(alerts, models.Alert{
				Severity: "critical",
				Category: "service",
				Subject:  svc.Name,
				Message:  fmt.Sprintf("Systemd service %s is failed", svc.Name),
			})
		}
	}

	for _, c := range rep.Containers {
		state := strings.ToLower(c.State)
		status := strings.ToLower(c.Status)
		if strings.Contains(status, "unhealthy") || state == "restarting" || state == "dead" {
			alerts = append(alerts, models.Alert{
				Severity: "critical",
				Category: "container",
				Subject:  c.Name,
				Message:  fmt.Sprintf("Container %s is %s (%s)", c.Name, c.State, c.Status),
			})
		} else if state == "exited" && !strings.Contains(status, "exited (0)") {
			alerts = append(alerts, models.Alert{
				Severity: "warning",
				Category: "container",
				Subject:  c.Name,
				Message:  fmt.Sprintf("Container %s exited abnormally (%s)", c.Name, c.Status),
			})
		}
	}

	for _, app := range rep.WebApps {
		if app.CertExpiryDays != nil && *app.CertExpiryDays <= th.CertWarnDays {
			sev := "warning"
			if *app.CertExpiryDays <= 3 {
				sev = "critical"
			}
			msg := fmt.Sprintf("TLS cert for %s expires in %d day(s)", app.Name, *app.CertExpiryDays)
			if *app.CertExpiryDays < 0 {
				msg = fmt.Sprintf("TLS cert for %s EXPIRED %d day(s) ago", app.Name, -*app.CertExpiryDays)
			}
			alerts = append(alerts, models.Alert{
				Severity: sev,
				Category: "tls",
				Subject:  app.Name,
				Message:  msg,
			})
		}
	}

	return alerts
}
