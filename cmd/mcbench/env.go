package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func cmdOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func readLoad() (string, error) {
	b, err := os.ReadFile("/proc/loadavg")
	return string(b), err
}

func cpuInfo() (string, int) {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unknown", 0
	}
	model, cores := "unknown", 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "model name") && model == "unknown" {
			if i := strings.Index(l, ":"); i >= 0 {
				model = strings.TrimSpace(l[i+1:])
			}
		}
		if strings.HasPrefix(l, "processor") {
			cores++
		}
	}
	return model, cores
}

func ramKB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				v, _ := strconv.ParseInt(f[1], 10, 64)
				return v
			}
		}
	}
	return 0
}

// postgresVersion runs the single read-only query SHOW server_version (ADR-0021 Q2).
func postgresVersion(dbURL string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return "unknown"
	}
	defer c.Close(ctx)
	var v string
	if err := c.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "unknown"
	}
	return v
}

// collectEnv fills the static part of the environment record.
func collectEnv(dbURL string) Environment {
	model, cores := cpuInfo()
	return Environment{
		Commit:      cmdOut("git", "rev-parse", "HEAD"),
		Dirty:       cmdOut("git", "status", "--porcelain") != "",
		GoVersion:   runtime.Version(),
		OS:          cmdOut("uname", "-s"),
		Kernel:      cmdOut("uname", "-r"),
		CPUModel:    model,
		CPUCores:    cores,
		RAMTotalKB:  ramKB(),
		PostgresVer: postgresVersion(dbURL),
	}
}
