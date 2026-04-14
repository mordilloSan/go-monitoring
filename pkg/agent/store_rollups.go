package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/pkg/agent/utils"
)

//nolint:gocognit // Rollup maintenance encodes retention and aggregation policy in a single transaction-oriented flow.
func (s *Store) RunMaintenance(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, step := range rollupSteps {
		if step.longer != resolution10m {
			existsAfter := now.Add(-step.window + time.Minute).UnixMilli()
			exists, err := historyExistsSince(tx, "system_stats_history", step.longer, existsAfter)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
		}

		from := now.Add(-step.window).UnixMilli()
		systemStatsJSON, err := loadHistoryJSON(tx, "system_stats_history", step.shorter, from)
		if err != nil {
			return err
		}
		containerStatsJSON, err := loadHistoryJSON(tx, "container_stats_history", step.shorter, from)
		if err != nil {
			return err
		}
		if len(systemStatsJSON) < step.minShorterRows || len(containerStatsJSON) < step.minShorterRows {
			continue
		}

		systemStats, err := averageSystemStatsJSON(systemStatsJSON)
		if err != nil {
			return err
		}
		containerStats, err := averageContainerStatsJSON(containerStatsJSON)
		if err != nil {
			return err
		}

		systemRaw, err := marshalJSON(systemStats)
		if err != nil {
			return err
		}
		containerRaw, err := marshalJSON(containerStats)
		if err != nil {
			return err
		}

		capturedAt := now.UnixMilli()
		if _, err := tx.Exec(`
			INSERT INTO system_stats_history (resolution, captured_at, stats_json)
			VALUES (?, ?, ?)
		`, step.longer, capturedAt, systemRaw); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO container_stats_history (resolution, captured_at, stats_json)
			VALUES (?, ?, ?)
		`, step.longer, capturedAt, containerRaw); err != nil {
			return err
		}
	}

	if err := deleteOldHistory(tx, "system_stats_history", now); err != nil {
		return err
	}
	if err := deleteOldHistory(tx, "container_stats_history", now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func historyExistsSince(tx *sql.Tx, table, resolution string, capturedAfter int64) (bool, error) {
	var count int
	err := tx.QueryRow(fmt.Sprintf(`
		SELECT COUNT(1)
		FROM %s
		WHERE resolution = ? AND captured_at > ?
	`, table), resolution, capturedAfter).Scan(&count)
	return count > 0, err
}

func loadHistoryJSON(tx *sql.Tx, table, resolution string, capturedAfter int64) ([]string, error) {
	rows, err := tx.Query(fmt.Sprintf(`
		SELECT stats_json
		FROM %s
		WHERE resolution = ? AND captured_at > ?
		ORDER BY captured_at ASC
	`, table), resolution, capturedAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		items = append(items, raw)
	}
	return items, rows.Err()
}

func deleteOldHistory(tx *sql.Tx, table string, now time.Time) error {
	for resolution, retention := range historyRetention {
		if _, err := tx.Exec(fmt.Sprintf(`
			DELETE FROM %s
			WHERE resolution = ? AND captured_at < ?
		`, table), resolution, now.Add(-retention).UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gocognit,cyclop // System stats averaging spans many optional nested fields and rollup rules.
func averageSystemStatsJSON(items []string) (*system.Stats, error) {
	sum := &system.Stats{}
	temp := &system.Stats{}
	batterySum := 0
	count := float64(len(items))
	tempCount := float64(0)

	var cpuCoresSums []uint64
	var cpuBreakdownSums []float64

	for _, raw := range items {
		*temp = system.Stats{}
		if err := json.Unmarshal([]byte(raw), temp); err != nil {
			return nil, err
		}

		sum.Cpu += temp.Cpu
		if temp.CpuBreakdown != nil {
			if len(cpuBreakdownSums) < len(temp.CpuBreakdown) {
				cpuBreakdownSums = append(cpuBreakdownSums, make([]float64, len(temp.CpuBreakdown)-len(cpuBreakdownSums))...)
			}
			for i, value := range temp.CpuBreakdown {
				cpuBreakdownSums[i] += value
			}
		}
		sum.Mem += temp.Mem
		sum.MemUsed += temp.MemUsed
		sum.MemPct += temp.MemPct
		sum.MemBuffCache += temp.MemBuffCache
		sum.MemZfsArc += temp.MemZfsArc
		sum.Swap += temp.Swap
		sum.SwapUsed += temp.SwapUsed
		sum.DiskTotal += temp.DiskTotal
		sum.DiskUsed += temp.DiskUsed
		sum.DiskPct += temp.DiskPct
		sum.DiskReadPs += temp.DiskReadPs
		sum.DiskWritePs += temp.DiskWritePs
		sum.NetworkSent += temp.NetworkSent
		sum.NetworkRecv += temp.NetworkRecv
		sum.LoadAvg[0] += temp.LoadAvg[0]
		sum.LoadAvg[1] += temp.LoadAvg[1]
		sum.LoadAvg[2] += temp.LoadAvg[2]
		sum.Bandwidth[0] += temp.Bandwidth[0]
		sum.Bandwidth[1] += temp.Bandwidth[1]
		sum.DiskIO[0] += temp.DiskIO[0]
		sum.DiskIO[1] += temp.DiskIO[1]
		for i := range temp.DiskIoStats {
			sum.DiskIoStats[i] += temp.DiskIoStats[i]
		}
		batterySum += int(temp.Battery[0])
		sum.Battery[1] = temp.Battery[1]

		if temp.CpuCoresUsage != nil {
			if len(cpuCoresSums) < len(temp.CpuCoresUsage) {
				cpuCoresSums = append(cpuCoresSums, make([]uint64, len(temp.CpuCoresUsage)-len(cpuCoresSums))...)
			}
			for i, value := range temp.CpuCoresUsage {
				cpuCoresSums[i] += uint64(value)
			}
		}

		sum.MaxCpu = max(sum.MaxCpu, temp.MaxCpu, temp.Cpu)
		sum.MaxMem = max(sum.MaxMem, temp.MaxMem, temp.MemUsed)
		sum.MaxNetworkSent = max(sum.MaxNetworkSent, temp.MaxNetworkSent, temp.NetworkSent)
		sum.MaxNetworkRecv = max(sum.MaxNetworkRecv, temp.MaxNetworkRecv, temp.NetworkRecv)
		sum.MaxDiskReadPs = max(sum.MaxDiskReadPs, temp.MaxDiskReadPs, temp.DiskReadPs)
		sum.MaxDiskWritePs = max(sum.MaxDiskWritePs, temp.MaxDiskWritePs, temp.DiskWritePs)
		sum.MaxBandwidth[0] = max(sum.MaxBandwidth[0], temp.MaxBandwidth[0], temp.Bandwidth[0])
		sum.MaxBandwidth[1] = max(sum.MaxBandwidth[1], temp.MaxBandwidth[1], temp.Bandwidth[1])
		sum.MaxDiskIO[0] = max(sum.MaxDiskIO[0], temp.MaxDiskIO[0], temp.DiskIO[0])
		sum.MaxDiskIO[1] = max(sum.MaxDiskIO[1], temp.MaxDiskIO[1], temp.DiskIO[1])
		for i := range temp.DiskIoStats {
			sum.MaxDiskIoStats[i] = max(sum.MaxDiskIoStats[i], temp.MaxDiskIoStats[i], temp.DiskIoStats[i])
		}

		if sum.NetworkInterfaces == nil {
			sum.NetworkInterfaces = make(map[string][4]uint64, len(temp.NetworkInterfaces))
		}
		for key, value := range temp.NetworkInterfaces {
			sum.NetworkInterfaces[key] = [4]uint64{
				sum.NetworkInterfaces[key][0] + value[0],
				sum.NetworkInterfaces[key][1] + value[1],
				max(sum.NetworkInterfaces[key][2], value[2]),
				max(sum.NetworkInterfaces[key][3], value[3]),
			}
		}

		if temp.Temperatures != nil {
			if sum.Temperatures == nil {
				sum.Temperatures = make(map[string]float64, len(temp.Temperatures))
			}
			tempCount++
			for key, value := range temp.Temperatures {
				sum.Temperatures[key] += value
			}
		}

		if temp.ExtraFs != nil {
			if sum.ExtraFs == nil {
				sum.ExtraFs = make(map[string]*system.FsStats, len(temp.ExtraFs))
			}
			for key, value := range temp.ExtraFs {
				if _, ok := sum.ExtraFs[key]; !ok {
					sum.ExtraFs[key] = &system.FsStats{}
				}
				fs := sum.ExtraFs[key]
				fs.DiskTotal += value.DiskTotal
				fs.DiskUsed += value.DiskUsed
				fs.DiskWritePs += value.DiskWritePs
				fs.DiskReadPs += value.DiskReadPs
				fs.MaxDiskReadPS = max(fs.MaxDiskReadPS, value.MaxDiskReadPS, value.DiskReadPs)
				fs.MaxDiskWritePS = max(fs.MaxDiskWritePS, value.MaxDiskWritePS, value.DiskWritePs)
				fs.DiskReadBytes += value.DiskReadBytes
				fs.DiskWriteBytes += value.DiskWriteBytes
				fs.MaxDiskReadBytes = max(fs.MaxDiskReadBytes, value.MaxDiskReadBytes, value.DiskReadBytes)
				fs.MaxDiskWriteBytes = max(fs.MaxDiskWriteBytes, value.MaxDiskWriteBytes, value.DiskWriteBytes)
				for i := range value.DiskIoStats {
					fs.DiskIoStats[i] += value.DiskIoStats[i]
					fs.MaxDiskIoStats[i] = max(fs.MaxDiskIoStats[i], value.MaxDiskIoStats[i], value.DiskIoStats[i])
				}
			}
		}

		if temp.GPUData != nil {
			if sum.GPUData == nil {
				sum.GPUData = make(map[string]system.GPUData, len(temp.GPUData))
			}
			for id, value := range temp.GPUData {
				gpu, ok := sum.GPUData[id]
				if !ok {
					gpu = system.GPUData{Name: value.Name}
				}
				gpu.Temperature += value.Temperature
				gpu.MemoryUsed += value.MemoryUsed
				gpu.MemoryTotal += value.MemoryTotal
				gpu.Usage += value.Usage
				gpu.Power += value.Power
				gpu.Count += value.Count
				if value.Engines != nil {
					if gpu.Engines == nil {
						gpu.Engines = make(map[string]float64, len(value.Engines))
					}
					for engineKey, engineValue := range value.Engines {
						gpu.Engines[engineKey] += engineValue
					}
				}
				sum.GPUData[id] = gpu
			}
		}
	}

	if count > 0 {
		sum.Cpu = utils.TwoDecimals(sum.Cpu / count)
		sum.Mem = utils.TwoDecimals(sum.Mem / count)
		sum.MemUsed = utils.TwoDecimals(sum.MemUsed / count)
		sum.MemPct = utils.TwoDecimals(sum.MemPct / count)
		sum.MemBuffCache = utils.TwoDecimals(sum.MemBuffCache / count)
		sum.MemZfsArc = utils.TwoDecimals(sum.MemZfsArc / count)
		sum.Swap = utils.TwoDecimals(sum.Swap / count)
		sum.SwapUsed = utils.TwoDecimals(sum.SwapUsed / count)
		sum.DiskTotal = utils.TwoDecimals(sum.DiskTotal / count)
		sum.DiskUsed = utils.TwoDecimals(sum.DiskUsed / count)
		sum.DiskPct = utils.TwoDecimals(sum.DiskPct / count)
		sum.DiskReadPs = utils.TwoDecimals(sum.DiskReadPs / count)
		sum.DiskWritePs = utils.TwoDecimals(sum.DiskWritePs / count)
		sum.DiskIO[0] = sum.DiskIO[0] / uint64(count)
		sum.DiskIO[1] = sum.DiskIO[1] / uint64(count)
		for i := range sum.DiskIoStats {
			sum.DiskIoStats[i] = utils.TwoDecimals(sum.DiskIoStats[i] / count)
		}
		sum.NetworkSent = utils.TwoDecimals(sum.NetworkSent / count)
		sum.NetworkRecv = utils.TwoDecimals(sum.NetworkRecv / count)
		sum.LoadAvg[0] = utils.TwoDecimals(sum.LoadAvg[0] / count)
		sum.LoadAvg[1] = utils.TwoDecimals(sum.LoadAvg[1] / count)
		sum.LoadAvg[2] = utils.TwoDecimals(sum.LoadAvg[2] / count)
		sum.Bandwidth[0] = sum.Bandwidth[0] / uint64(count)
		sum.Bandwidth[1] = sum.Bandwidth[1] / uint64(count)
		sum.Battery[0] = uint8(batterySum / int(count))

		if sum.NetworkInterfaces != nil {
			for key := range sum.NetworkInterfaces {
				sum.NetworkInterfaces[key] = [4]uint64{
					sum.NetworkInterfaces[key][0] / uint64(count),
					sum.NetworkInterfaces[key][1] / uint64(count),
					sum.NetworkInterfaces[key][2],
					sum.NetworkInterfaces[key][3],
				}
			}
		}

		if sum.Temperatures != nil && tempCount > 0 {
			for key := range sum.Temperatures {
				sum.Temperatures[key] = utils.TwoDecimals(sum.Temperatures[key] / tempCount)
			}
		}

		if sum.ExtraFs != nil {
			for key := range sum.ExtraFs {
				fs := sum.ExtraFs[key]
				fs.DiskTotal = utils.TwoDecimals(fs.DiskTotal / count)
				fs.DiskUsed = utils.TwoDecimals(fs.DiskUsed / count)
				fs.DiskWritePs = utils.TwoDecimals(fs.DiskWritePs / count)
				fs.DiskReadPs = utils.TwoDecimals(fs.DiskReadPs / count)
				fs.DiskReadBytes = fs.DiskReadBytes / uint64(count)
				fs.DiskWriteBytes = fs.DiskWriteBytes / uint64(count)
				for i := range fs.DiskIoStats {
					fs.DiskIoStats[i] = utils.TwoDecimals(fs.DiskIoStats[i] / count)
				}
			}
		}

		if sum.GPUData != nil {
			for id := range sum.GPUData {
				gpu := sum.GPUData[id]
				gpu.Temperature = utils.TwoDecimals(gpu.Temperature / count)
				gpu.MemoryUsed = utils.TwoDecimals(gpu.MemoryUsed / count)
				gpu.MemoryTotal = utils.TwoDecimals(gpu.MemoryTotal / count)
				gpu.Usage = utils.TwoDecimals(gpu.Usage / count)
				gpu.Power = utils.TwoDecimals(gpu.Power / count)
				gpu.Count = utils.TwoDecimals(gpu.Count / count)
				if gpu.Engines != nil {
					for engineKey := range gpu.Engines {
						gpu.Engines[engineKey] = utils.TwoDecimals(gpu.Engines[engineKey] / count)
					}
				}
				sum.GPUData[id] = gpu
			}
		}

		if len(cpuCoresSums) > 0 {
			avg := make(system.Uint8Slice, len(cpuCoresSums))
			for i := range cpuCoresSums {
				avg[i] = uint8(math.Round(float64(cpuCoresSums[i]) / count))
			}
			sum.CpuCoresUsage = avg
		}

		if len(cpuBreakdownSums) > 0 {
			avg := make([]float64, len(cpuBreakdownSums))
			for i := range cpuBreakdownSums {
				avg[i] = utils.TwoDecimals(cpuBreakdownSums[i] / count)
			}
			sum.CpuBreakdown = avg
		}
	}

	return sum, nil
}

func averageContainerStatsJSON(items []string) ([]container.Stats, error) {
	sums := make(map[string]*container.Stats)
	count := float64(len(items))

	for _, raw := range items {
		var stats []container.Stats
		if err := json.Unmarshal([]byte(raw), &stats); err != nil {
			return nil, err
		}
		for _, stat := range stats {
			if _, ok := sums[stat.Name]; !ok {
				sums[stat.Name] = &container.Stats{Name: stat.Name}
			}
			sums[stat.Name].Cpu += stat.Cpu
			sums[stat.Name].Mem += stat.Mem

			sentBytes := stat.Bandwidth[0]
			recvBytes := stat.Bandwidth[1]
			if sentBytes == 0 && recvBytes == 0 && (stat.NetworkSent != 0 || stat.NetworkRecv != 0) {
				sentBytes = uint64(stat.NetworkSent * 1024 * 1024)
				recvBytes = uint64(stat.NetworkRecv * 1024 * 1024)
			}
			sums[stat.Name].Bandwidth[0] += sentBytes
			sums[stat.Name].Bandwidth[1] += recvBytes
		}
	}

	result := make([]container.Stats, 0, len(sums))
	for _, value := range sums {
		result = append(result, container.Stats{
			Name:      value.Name,
			Cpu:       utils.TwoDecimals(value.Cpu / count),
			Mem:       utils.TwoDecimals(value.Mem / count),
			Bandwidth: [2]uint64{uint64(float64(value.Bandwidth[0]) / count), uint64(float64(value.Bandwidth[1]) / count)},
		})
	}
	return result, nil
}
