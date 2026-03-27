package internal

import (
	"log/slog"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// SystemMetrics holds the collected system metrics.
type SystemMetrics struct {
	CPUUsagePercent float64
	RAMUsagePercent float64
	DiskUsage       map[string]float64
}

// GetSystemMetrics collects CPU, RAM, and disk usage statistics.
func GetSystemMetrics(logger *slog.Logger) (*SystemMetrics, error) {
	metrics := &SystemMetrics{
		DiskUsage: make(map[string]float64),
	}

	// CPU Usage. The first call returns 0, so we need a small delay.
	// A duration of 0 means non-blocking and compares the current time with the last time.
	cpuPercentages, err := cpu.Percent(time.Second, false)
	if err != nil {
		logger.Error("Failed to get CPU usage", "error", err)
		// Don't return error, just log it and continue
	}
	if len(cpuPercentages) > 0 {
		metrics.CPUUsagePercent = cpuPercentages[0]
	}

	// RAM Usage
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		logger.Error("Failed to get RAM usage", "error", err)
		// Don't return error, just log it and continue
	} else {
		metrics.RAMUsagePercent = vmStat.UsedPercent
	}

	// Disk Usage
	partitions, err := disk.Partitions(false) // false = all physical partitions
	if err != nil {
		logger.Error("Failed to get disk partitions", "error", err)
		// Don't return error, just log it and continue
	} else {
		for _, partition := range partitions {
			// In windows, it can be "C:". In linux, it can be "/".
			// We only care about fixed disk drives.
			if partition.Fstype == "NTFS" || partition.Fstype == "ext4" || partition.Fstype == "apfs" {
				usage, err := disk.Usage(partition.Mountpoint)
				if err != nil {
					logger.Warn("Failed to get usage for partition", "partition", partition.Mountpoint, "error", err)
					continue // Skip problematic partitions
				}
				metrics.DiskUsage[partition.Mountpoint] = usage.UsedPercent
			}
		}
	}

	return metrics, nil
}
