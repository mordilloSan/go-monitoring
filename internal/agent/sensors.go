package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mordilloSan/go-monitoring/internal/model/system"
	"github.com/mordilloSan/go-monitoring/internal/utils"

	"github.com/shirou/gopsutil/v4/common"
	"github.com/shirou/gopsutil/v4/sensors"
)

var errTemperatureFetchTimeout = errors.New("temperature collection timed out")

// Matches sensors.TemperaturesWithContext to allow for panic recovery (gopsutil/issues/1832)
type getTempsFn func(ctx context.Context) ([]sensors.TemperatureStat, error)

type SensorConfig struct {
	context        context.Context
	sensors        map[string]struct{}
	primarySensor  string
	timeout        time.Duration
	readSem        chan struct{}
	isBlacklist    bool
	hasWildcards   bool
	skipCollection bool
	firstRun       bool
}

func (a *Agent) newSensorConfig() *SensorConfig {
	primarySensor, _ := utils.GetEnv("PRIMARY_SENSOR")
	sysSensors, _ := utils.GetEnv("SYS_SENSORS")
	sensorsEnvVal, sensorsSet := utils.GetEnv("SENSORS")
	skipCollection := sensorsSet && sensorsEnvVal == ""
	sensorsTimeout, _ := utils.GetEnv("SENSORS_TIMEOUT")

	return a.newSensorConfigWithEnv(primarySensor, sysSensors, sensorsEnvVal, sensorsTimeout, skipCollection)
}

// newSensorConfigWithEnv creates a SensorConfig with the provided environment variables
// sensorsSet indicates if the SENSORS environment variable was explicitly set (even to empty string)
func (a *Agent) newSensorConfigWithEnv(primarySensor, sysSensors, sensorsEnvVal, sensorsTimeout string, skipCollection bool) *SensorConfig {
	timeout := 2 * time.Second
	if sensorsTimeout != "" {
		if d, err := time.ParseDuration(sensorsTimeout); err == nil {
			timeout = d
		} else {
			slog.Warn("Invalid SENSORS_TIMEOUT", "value", sensorsTimeout)
		}
	}

	config := &SensorConfig{
		context:        context.Background(),
		primarySensor:  primarySensor,
		timeout:        timeout,
		readSem:        make(chan struct{}, 1),
		skipCollection: skipCollection,
		firstRun:       true,
		sensors:        make(map[string]struct{}),
	}

	// Set sensors context (allows overriding sys location for sensors)
	if sysSensors != "" {
		slog.Info("SYS_SENSORS", "path", sysSensors)
		config.context = context.WithValue(config.context,
			common.EnvKey, common.EnvMap{common.HostSysEnvKey: sysSensors},
		)
	}

	// handle blacklist
	if strings.HasPrefix(sensorsEnvVal, "-") {
		config.isBlacklist = true
		sensorsEnvVal = sensorsEnvVal[1:]
	}

	for sensor := range strings.SplitSeq(sensorsEnvVal, ",") {
		sensor = strings.TrimSpace(sensor)
		if sensor != "" {
			config.sensors[sensor] = struct{}{}
			if strings.Contains(sensor, "*") {
				config.hasWildcards = true
			}
		}
	}

	return config
}

// updateTemperatures updates the agent with the latest sensor temperatures
//
//nolint:gocognit // Temperature collection merges heterogeneous sensor sources and fallback paths.
func (a *Agent) updateTemperatures(systemStats *system.Stats) {
	// skip if sensors whitelist is set to empty string
	if a.sensorConfig.skipCollection {
		slog.Debug("Skipping temperature collection")
		return
	}

	// reset high temp
	a.systemInfoManager.systemInfo.DashboardTemp = 0

	temps, err := a.getTempsWithTimeout(getSensorTemps)
	if err != nil {
		// retry once on panic (gopsutil/issues/1832)
		if !errors.Is(err, errTemperatureFetchTimeout) {
			temps, err = a.getTempsWithTimeout(getSensorTemps)
		}
		if err != nil {
			slog.Warn("Error updating temperatures", "err", err)
			if len(systemStats.Temperatures) > 0 {
				systemStats.Temperatures = make(map[string]float64)
			}
			return
		}
	}
	slog.Debug("Temperature", "sensors", temps)

	// return if no sensors
	if len(temps) == 0 {
		return
	}

	systemStats.Temperatures = make(map[string]float64, len(temps))
	for i, sensor := range temps {
		// check for malformed strings on darwin (gopsutil/issues/1832)
		if runtime.GOOS == "darwin" && !utf8.ValidString(sensor.SensorKey) {
			continue
		}

		// scale temperature
		if sensor.Temperature != 0 && sensor.Temperature < 1 {
			sensor.Temperature = scaleTemperature(sensor.Temperature)
		}
		// skip if temperature is unreasonable
		if sensor.Temperature <= 0 || sensor.Temperature >= 200 {
			continue
		}
		sensorName := sensor.SensorKey
		if _, ok := systemStats.Temperatures[sensorName]; ok {
			// if key already exists, append int to key
			sensorName = sensorName + "_" + strconv.Itoa(i)
		}
		// skip if not in whitelist or blacklist
		if !isValidSensor(sensorName, a.sensorConfig) {
			continue
		}
		// set dashboard temperature
		switch a.sensorConfig.primarySensor {
		case "":
			a.systemInfoManager.systemInfo.DashboardTemp = max(a.systemInfoManager.systemInfo.DashboardTemp, sensor.Temperature)
		case sensorName:
			a.systemInfoManager.systemInfo.DashboardTemp = sensor.Temperature
		}
		systemStats.Temperatures[sensorName] = utils.TwoDecimals(sensor.Temperature)
	}
}

// getTempsWithPanicRecovery wraps sensors.TemperaturesWithContext to recover from panics (gopsutil/issues/1832)
func (a *Agent) getTempsWithPanicRecovery(ctx context.Context, getTemps getTempsFn) (temps []sensors.TemperatureStat, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	// get sensor data (error ignored intentionally as it may be only with one sensor)
	temps, _ = getTemps(ctx)
	return
}

func (c *SensorConfig) tryStartRead() bool {
	if c.readSem == nil {
		c.readSem = make(chan struct{}, 1)
	}
	select {
	case c.readSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *SensorConfig) finishRead() {
	if c.readSem == nil {
		return
	}
	select {
	case <-c.readSem:
	default:
	}
}

func (a *Agent) getTempsWithTimeout(getTemps getTempsFn) ([]sensors.TemperatureStat, error) {
	type result struct {
		temps []sensors.TemperatureStat
		err   error
	}

	// Use a longer timeout on the first run to allow for initialization
	// (e.g. slow sensor subsystem startup)
	timeout := a.sensorConfig.timeout
	if a.sensorConfig.firstRun {
		a.sensorConfig.firstRun = false
		timeout = 10 * time.Second
	}

	if !a.sensorConfig.tryStartRead() {
		return nil, errTemperatureFetchTimeout
	}

	// Derive a timeout-aware context so a context-aware gopsutil call cancels
	// when the timeout fires. The worker goroutine still survives the parent's
	// return if gopsutil ignores ctx, but this gate prevents one stuck read from
	// growing into one new goroutine per poll.
	ctx, cancel := context.WithTimeout(a.sensorConfig.context, timeout)
	defer cancel()

	resultCh := make(chan result, 1)
	go func() {
		defer a.sensorConfig.finishRead()
		temps, err := a.getTempsWithPanicRecovery(ctx, getTemps)
		select {
		case resultCh <- result{temps: temps, err: err}:
		case <-ctx.Done():
		}
	}()

	select {
	case res := <-resultCh:
		return res.temps, res.err
	case <-ctx.Done():
		return nil, errTemperatureFetchTimeout
	}
}

// isValidSensor checks if a sensor is valid based on the sensor name and the sensor config
func isValidSensor(sensorName string, config *SensorConfig) bool {
	// if no sensors configured, everything is valid
	if len(config.sensors) == 0 {
		return true
	}

	// Exact match - return true if whitelist, false if blacklist
	if _, exactMatch := config.sensors[sensorName]; exactMatch {
		return !config.isBlacklist
	}

	// If no wildcards, return true if blacklist, false if whitelist
	if !config.hasWildcards {
		return config.isBlacklist
	}

	// Check for wildcard patterns
	for pattern := range config.sensors {
		if !strings.Contains(pattern, "*") {
			continue
		}
		if match, _ := path.Match(pattern, sensorName); match {
			return !config.isBlacklist
		}
	}

	return config.isBlacklist
}

// scaleTemperature scales temperatures in fractional values to reasonable Celsius values
func scaleTemperature(temp float64) float64 {
	if temp > 1 {
		return temp
	}
	scaled100 := temp * 100
	scaled1000 := temp * 1000

	if scaled100 >= 15 && scaled100 <= 95 {
		return scaled100
	} else if scaled1000 >= 15 && scaled1000 <= 95 {
		return scaled1000
	}
	return scaled100
}
