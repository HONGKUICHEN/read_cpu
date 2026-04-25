package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultInterval = 100 * time.Millisecond
	windowDuration  = 6 * time.Minute
)

type cpuTimes struct {
	Total uint64
	Idle  uint64
}

type memorySnapshot struct {
	MemTotalKB      uint64
	MemAvailableKB  uint64
	MemUsedKB       uint64
	MemUsedPercent  float64
	SwapTotalKB     uint64
	SwapFreeKB      uint64
	SwapUsedKB      uint64
	SwapUsedPercent float64
}

type networkCounters struct {
	RXBytes   uint64
	TXBytes   uint64
	RXPackets uint64
	TXPackets uint64
	RXErrors  uint64
	TXErrors  uint64
}

type diskSnapshot struct {
	TotalBytes  uint64
	UsedBytes   uint64
	FreeBytes   uint64
	UsedPercent float64
}

type sample struct {
	TimestampUTC        string  `json:"timestamp_utc"`
	CPUPercent          float64 `json:"cpu_percent"`
	MemUsedPercent      float64 `json:"mem_used_percent"`
	MemTotalKB          uint64  `json:"mem_total_kb"`
	MemAvailableKB      uint64  `json:"mem_available_kb"`
	MemUsedKB           uint64  `json:"mem_used_kb"`
	SwapTotalKB         uint64  `json:"swap_total_kb"`
	SwapFreeKB          uint64  `json:"swap_free_kb"`
	SwapUsedKB          uint64  `json:"swap_used_kb"`
	SwapUsedPercent     float64 `json:"swap_used_percent"`
	NetRXBytes          uint64  `json:"net_rx_bytes"`
	NetTXBytes          uint64  `json:"net_tx_bytes"`
	NetRXPackets        uint64  `json:"net_rx_packets"`
	NetTXPackets        uint64  `json:"net_tx_packets"`
	NetRXErrors         uint64  `json:"net_rx_errors"`
	NetTXErrors         uint64  `json:"net_tx_errors"`
	NetRXBytesPerSec    float64 `json:"net_rx_bytes_per_sec"`
	NetTXBytesPerSec    float64 `json:"net_tx_bytes_per_sec"`
	NetRXPacketsPerSec  float64 `json:"net_rx_packets_per_sec"`
	NetTXPacketsPerSec  float64 `json:"net_tx_packets_per_sec"`
	DiskTotalBytes      uint64  `json:"disk_total_bytes"`
	DiskUsedBytes       uint64  `json:"disk_used_bytes"`
	DiskFreeBytes       uint64  `json:"disk_free_bytes"`
	DiskUsedPercent     float64 `json:"disk_used_percent"`
}

var csvHeader = []string{
	"timestamp_utc",
	"cpu_percent",
	"mem_used_percent",
	"mem_total_kb",
	"mem_available_kb",
	"mem_used_kb",
	"swap_total_kb",
	"swap_free_kb",
	"swap_used_kb",
	"swap_used_percent",
	"net_rx_bytes",
	"net_tx_bytes",
	"net_rx_packets",
	"net_tx_packets",
	"net_rx_errors",
	"net_tx_errors",
	"net_rx_bytes_per_sec",
	"net_tx_bytes_per_sec",
	"net_rx_packets_per_sec",
	"net_tx_packets_per_sec",
	"disk_total_bytes",
	"disk_used_bytes",
	"disk_free_bytes",
	"disk_used_percent",
}

func main() {
	var (
		interval    = flag.Duration("interval", defaultInterval, "sampling interval, e.g. 100ms, 1s")
		logDir      = flag.String("log-dir", filepath.Join(mustProjectDir(), "logs"), "directory for CSV and JSONL outputs")
		serviceFile = flag.String("service-file", "", "write a systemd user service file to this path and exit")
		sampleNow   = flag.Duration("sample-now", 0, "sample immediately for the given duration, e.g. 3s, then flush logs and exit")
	)
	flag.Parse()

	if *interval <= 0 {
		exitf("--interval must be > 0")
	}
	if *sampleNow < 0 {
		exitf("--sample-now must be >= 0")
	}

	projectDir := mustProjectDir()
	if *serviceFile != "" {
		content, err := renderSystemdService(projectDir)
		if err != nil {
			exitErr(err)
		}
		if err := writeTextAtomic(*serviceFile, []byte(content)); err != nil {
			exitErr(err)
		}
		fmt.Printf("wrote service file: %s\n", *serviceFile)
		return
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	if *sampleNow > 0 {
		now := time.Now().UTC()
		boundary := now.Format("2006-01-02") + "-manual-" + now.Format("150405")
		if err := sampleForDuration(boundary, *sampleNow, *interval, *logDir, stop); err != nil {
			exitErr(err)
		}
		return
	}

	if err := runForever(*interval, *logDir, stop); err != nil {
		exitErr(err)
	}
}

func mustProjectDir() string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	wd, err := os.Getwd()
	if err != nil {
		exitErr(err)
	}
	return wd
}

func runForever(interval time.Duration, logDir string, stop <-chan os.Signal) error {
	var lastCompleted string

	for {
		select {
		case sig := <-stop:
			fmt.Fprintf(os.Stderr, "received signal %d, stopping\n", sig)
			return nil
		default:
		}

		now := time.Now().UTC()
		boundary, ok := activeBoundaryDate(now)
		if ok && boundary != lastCompleted {
			if err := sampleWindow(boundary, interval, logDir, stop); err != nil {
				return err
			}
			lastCompleted = boundary
			continue
		}

		wakeAt := nextWindowStart(now)
		sleepFor := wakeAt.Sub(now)
		if sleepFor > time.Minute {
			sleepFor = time.Minute
		}
		if sleepFor < time.Second {
			sleepFor = time.Second
		}
		fmt.Printf("idle until next UTC window, now=%s wake_in=%0.fs\n", now.Format(time.RFC3339Nano), sleepFor.Seconds())
		select {
		case sig := <-stop:
			fmt.Fprintf(os.Stderr, "received signal %d, stopping\n", sig)
			return nil
		case <-time.After(sleepFor):
		}
	}
}

func sampleWindow(boundary string, interval time.Duration, logDir string, stop <-chan os.Signal) error {
	start, end, err := windowForBoundary(boundary)
	if err != nil {
		return err
	}
	fmt.Printf("sampling UTC window for %s: %s -> %s\n", boundary, start.Format(time.RFC3339), end.Format(time.RFC3339))

	prevCPU, err := readCPUTimes()
	if err != nil {
		return err
	}
	prevNet, err := readNetworkCounters()
	if err != nil {
		return err
	}

	var samples []sample
	nextTick := time.Now()

	for {
		select {
		case sig := <-stop:
			fmt.Fprintf(os.Stderr, "received signal %d, stopping\n", sig)
			return nil
		default:
		}

		now := time.Now().UTC()
		if now.After(end) {
			if err := flushSamples(logDir, boundary, samples); err != nil {
				return err
			}
			fmt.Printf("finished window %s\n", boundary)
			return nil
		}

		if sleepFor := time.Until(nextTick); sleepFor > 0 {
			select {
			case sig := <-stop:
				fmt.Fprintf(os.Stderr, "received signal %d, stopping\n", sig)
				return nil
			case <-time.After(sleepFor):
			}
		}

		now = time.Now().UTC()
		currCPU, err := readCPUTimes()
		if err != nil {
			return err
		}
		currNet, err := readNetworkCounters()
		if err != nil {
			return err
		}
		mem, err := readMemorySnapshot()
		if err != nil {
			return err
		}
		disk, err := readDiskSnapshot("/")
		if err != nil {
			return err
		}

		rxBytesPerSec, txBytesPerSec, rxPacketsPerSec, txPacketsPerSec := networkRates(prevNet, currNet, interval.Seconds())

		samples = append(samples, sample{
			TimestampUTC:       now.Truncate(time.Second).Format(time.RFC3339),
			CPUPercent:         round2(cpuPercent(prevCPU, currCPU)),
			MemUsedPercent:     round2(mem.MemUsedPercent),
			MemTotalKB:         mem.MemTotalKB,
			MemAvailableKB:     mem.MemAvailableKB,
			MemUsedKB:          mem.MemUsedKB,
			SwapTotalKB:        mem.SwapTotalKB,
			SwapFreeKB:         mem.SwapFreeKB,
			SwapUsedKB:         mem.SwapUsedKB,
			SwapUsedPercent:    round2(mem.SwapUsedPercent),
			NetRXBytes:         currNet.RXBytes,
			NetTXBytes:         currNet.TXBytes,
			NetRXPackets:       currNet.RXPackets,
			NetTXPackets:       currNet.TXPackets,
			NetRXErrors:        currNet.RXErrors,
			NetTXErrors:        currNet.TXErrors,
			NetRXBytesPerSec:   round2(rxBytesPerSec),
			NetTXBytesPerSec:   round2(txBytesPerSec),
			NetRXPacketsPerSec: round2(rxPacketsPerSec),
			NetTXPacketsPerSec: round2(txPacketsPerSec),
			DiskTotalBytes:     disk.TotalBytes,
			DiskUsedBytes:      disk.UsedBytes,
			DiskFreeBytes:      disk.FreeBytes,
			DiskUsedPercent:    round2(disk.UsedPercent),
		})

		prevCPU = currCPU
		prevNet = currNet
		nextTick = nextTick.Add(interval)
	}
}

func sampleForDuration(boundary string, duration, interval time.Duration, logDir string, stop <-chan os.Signal) error {
	start := time.Now().UTC()
	end := start.Add(duration)
	fmt.Printf("sampling immediate window for %s: %s -> %s\n", boundary, start.Format(time.RFC3339), end.Format(time.RFC3339))

	prevCPU, err := readCPUTimes()
	if err != nil {
		return err
	}
	prevNet, err := readNetworkCounters()
	if err != nil {
		return err
	}

	var samples []sample
	nextTick := time.Now()

	for {
		select {
		case sig := <-stop:
			fmt.Fprintf(os.Stderr, "received signal %d, stopping\n", sig)
			return nil
		default:
		}

		now := time.Now().UTC()
		if now.After(end) {
			if err := flushSamples(logDir, boundary, samples); err != nil {
				return err
			}
			fmt.Printf("finished immediate window %s\n", boundary)
			return nil
		}

		if sleepFor := time.Until(nextTick); sleepFor > 0 {
			select {
			case sig := <-stop:
				fmt.Fprintf(os.Stderr, "received signal %d, stopping\n", sig)
				return nil
			case <-time.After(sleepFor):
			}
		}

		now = time.Now().UTC()
		currCPU, err := readCPUTimes()
		if err != nil {
			return err
		}
		currNet, err := readNetworkCounters()
		if err != nil {
			return err
		}
		mem, err := readMemorySnapshot()
		if err != nil {
			return err
		}
		disk, err := readDiskSnapshot("/")
		if err != nil {
			return err
		}

		rxBytesPerSec, txBytesPerSec, rxPacketsPerSec, txPacketsPerSec := networkRates(prevNet, currNet, interval.Seconds())

		samples = append(samples, sample{
			TimestampUTC:       now.Format(time.RFC3339Nano),
			CPUPercent:         round2(cpuPercent(prevCPU, currCPU)),
			MemUsedPercent:     round2(mem.MemUsedPercent),
			MemTotalKB:         mem.MemTotalKB,
			MemAvailableKB:     mem.MemAvailableKB,
			MemUsedKB:          mem.MemUsedKB,
			SwapTotalKB:        mem.SwapTotalKB,
			SwapFreeKB:         mem.SwapFreeKB,
			SwapUsedKB:         mem.SwapUsedKB,
			SwapUsedPercent:    round2(mem.SwapUsedPercent),
			NetRXBytes:         currNet.RXBytes,
			NetTXBytes:         currNet.TXBytes,
			NetRXPackets:       currNet.RXPackets,
			NetTXPackets:       currNet.TXPackets,
			NetRXErrors:        currNet.RXErrors,
			NetTXErrors:        currNet.TXErrors,
			NetRXBytesPerSec:   round2(rxBytesPerSec),
			NetTXBytesPerSec:   round2(txBytesPerSec),
			NetRXPacketsPerSec: round2(rxPacketsPerSec),
			NetTXPacketsPerSec: round2(txPacketsPerSec),
			DiskTotalBytes:     disk.TotalBytes,
			DiskUsedBytes:      disk.UsedBytes,
			DiskFreeBytes:      disk.FreeBytes,
			DiskUsedPercent:    round2(disk.UsedPercent),
		})

		prevCPU = currCPU
		prevNet = currNet
		nextTick = nextTick.Add(interval)
	}
}

func readCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()

	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return cpuTimes{}, err
	}
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("unexpected /proc/stat line: %q", strings.TrimSpace(line))
	}

	var values []uint64
	for _, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimes{}, err
		}
		values = append(values, v)
	}

	var total uint64
	for _, v := range values {
		total += v
	}

	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}

	return cpuTimes{Total: total, Idle: idle}, nil
}

func cpuPercent(prev, curr cpuTimes) float64 {
	totalDelta := curr.Total - prev.Total
	idleDelta := curr.Idle - prev.Idle
	if totalDelta == 0 {
		return 0
	}
	used := totalDelta - idleDelta
	return math.Max(0, math.Min(100, float64(used)*100/float64(totalDelta)))
}

func readMemorySnapshot() (memorySnapshot, error) {
	data, err := parseMeminfo()
	if err != nil {
		return memorySnapshot{}, err
	}

	memTotal := data["MemTotal"]
	memAvailable := data["MemAvailable"]
	memUsed := memTotal - memAvailable
	swapTotal := data["SwapTotal"]
	swapFree := data["SwapFree"]
	swapUsed := swapTotal - swapFree

	return memorySnapshot{
		MemTotalKB:      memTotal,
		MemAvailableKB:  memAvailable,
		MemUsedKB:       memUsed,
		MemUsedPercent:  percent(memUsed, memTotal),
		SwapTotalKB:     swapTotal,
		SwapFreeKB:      swapFree,
		SwapUsedKB:      swapUsed,
		SwapUsedPercent: percent(swapUsed, swapTotal),
	}, nil
}

func parseMeminfo() (map[string]uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		valueFields := strings.Fields(parts[1])
		if len(valueFields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(valueFields[0], 10, 64)
		if err != nil {
			return nil, err
		}
		result[parts[0]] = v
	}
	return result, scanner.Err()
}

func readNetworkCounters() (networkCounters, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return networkCounters{}, err
	}
	defer f.Close()

	var counters networkCounters
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo <= 2 {
			continue
		}
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" || iface == "" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		rxPackets, _ := strconv.ParseUint(fields[1], 10, 64)
		rxErrors, _ := strconv.ParseUint(fields[2], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		txPackets, _ := strconv.ParseUint(fields[9], 10, 64)
		txErrors, _ := strconv.ParseUint(fields[10], 10, 64)

		counters.RXBytes += rxBytes
		counters.RXPackets += rxPackets
		counters.RXErrors += rxErrors
		counters.TXBytes += txBytes
		counters.TXPackets += txPackets
		counters.TXErrors += txErrors
	}
	return counters, scanner.Err()
}

func networkRates(prev, curr networkCounters, seconds float64) (float64, float64, float64, float64) {
	if seconds <= 0 {
		return 0, 0, 0, 0
	}
	return nonNegativeRate(curr.RXBytes, prev.RXBytes, seconds),
		nonNegativeRate(curr.TXBytes, prev.TXBytes, seconds),
		nonNegativeRate(curr.RXPackets, prev.RXPackets, seconds),
		nonNegativeRate(curr.TXPackets, prev.TXPackets, seconds)
}

func readDiskSnapshot(path string) (diskSnapshot, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskSnapshot{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	used := total - free
	return diskSnapshot{
		TotalBytes:  total,
		UsedBytes:   used,
		FreeBytes:   free,
		UsedPercent: percent(used, total),
	}, nil
}

func flushSamples(logDir, boundary string, samples []sample) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	csvPath := filepath.Join(logDir, boundary+".csv")
	jsonlPath := filepath.Join(logDir, boundary+".jsonl")

	if err := writeTextAtomic(csvPath, []byte(renderCSV(samples))); err != nil {
		return err
	}
	if err := writeTextAtomic(jsonlPath, []byte(renderJSONL(samples))); err != nil {
		return err
	}
	return nil
}

func renderCSV(samples []sample) string {
	var b strings.Builder
	b.WriteString(strings.Join(csvHeader, ","))
	b.WriteByte('\n')
	for _, s := range samples {
		row := []string{
			s.TimestampUTC,
			formatFloat(s.CPUPercent),
			formatFloat(s.MemUsedPercent),
			strconv.FormatUint(s.MemTotalKB, 10),
			strconv.FormatUint(s.MemAvailableKB, 10),
			strconv.FormatUint(s.MemUsedKB, 10),
			strconv.FormatUint(s.SwapTotalKB, 10),
			strconv.FormatUint(s.SwapFreeKB, 10),
			strconv.FormatUint(s.SwapUsedKB, 10),
			formatFloat(s.SwapUsedPercent),
			strconv.FormatUint(s.NetRXBytes, 10),
			strconv.FormatUint(s.NetTXBytes, 10),
			strconv.FormatUint(s.NetRXPackets, 10),
			strconv.FormatUint(s.NetTXPackets, 10),
			strconv.FormatUint(s.NetRXErrors, 10),
			strconv.FormatUint(s.NetTXErrors, 10),
			formatFloat(s.NetRXBytesPerSec),
			formatFloat(s.NetTXBytesPerSec),
			formatFloat(s.NetRXPacketsPerSec),
			formatFloat(s.NetTXPacketsPerSec),
			strconv.FormatUint(s.DiskTotalBytes, 10),
			strconv.FormatUint(s.DiskUsedBytes, 10),
			strconv.FormatUint(s.DiskFreeBytes, 10),
			formatFloat(s.DiskUsedPercent),
		}
		b.WriteString(strings.Join(row, ","))
		b.WriteByte('\n')
	}
	return b.String()
}

func renderJSONL(samples []sample) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, s := range samples {
		_ = enc.Encode(s)
	}
	return b.String()
}

func renderSystemdService(projectDir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`[Unit]
Description=Daily UTC midnight CPU, memory, network, and disk monitor

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s/run.sh
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, projectDir, filepath.Dir(exe)), nil
}

func writeTextAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func windowForBoundary(boundary string) (time.Time, time.Time, error) {
	end, err := time.ParseInLocation("2006-01-02", boundary, time.UTC)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end = end.Add(5 * time.Minute)
	start := end.Add(-windowDuration)
	return start, end, nil
}

func activeBoundaryDate(now time.Time) (string, bool) {
	today := now.Format("2006-01-02")
	start, end, _ := windowForBoundary(today)
	if !now.Before(start) && !now.After(end) {
		return today, true
	}
	tomorrow := now.Add(24 * time.Hour).Format("2006-01-02")
	start, end, _ = windowForBoundary(tomorrow)
	if !now.Before(start) && !now.After(end) {
		return tomorrow, true
	}
	return "", false
}

func nextWindowStart(now time.Time) time.Time {
	today := now.Format("2006-01-02")
	start, end, _ := windowForBoundary(today)
	if now.Before(start) {
		return start
	}
	if !now.After(end) {
		return now
	}
	tomorrow := now.Add(24 * time.Hour).Format("2006-01-02")
	start, _, _ = windowForBoundary(tomorrow)
	return start
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

func nonNegativeRate(curr, prev uint64, seconds float64) float64 {
	if curr < prev || seconds <= 0 {
		return 0
	}
	return float64(curr-prev) / seconds
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
