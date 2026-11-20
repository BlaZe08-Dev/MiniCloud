package main

import (
	"fmt"
	"strconv"
	"strings"
)

// LoadAvg holds the 1, 5 and 15 minute load averages.
type LoadAvg struct {
	One, Five, Fifteen float64
}

// ParseLoadAvg parses the content of /proc/loadavg.
func ParseLoadAvg(content string) (LoadAvg, error) {
	f := strings.Fields(content)
	if len(f) < 3 {
		return LoadAvg{}, fmt.Errorf("unreadable load average")
	}
	var v [3]float64
	for i := 0; i < 3; i++ {
		x, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return LoadAvg{}, fmt.Errorf("unreadable load average")
		}
		v[i] = x
	}
	return LoadAvg{v[0], v[1], v[2]}, nil
}

// CheckIdle aborts when the 1-minute load average exceeds max. read is injected.
func CheckIdle(read func() (string, error), max float64) (LoadAvg, error) {
	c, err := read()
	if err != nil {
		return LoadAvg{}, fmt.Errorf("cannot read load average, refusing to run: %w", err)
	}
	la, err := ParseLoadAvg(c)
	if err != nil {
		return LoadAvg{}, err
	}
	if la.One > max {
		return la, fmt.Errorf("machine is not idle: 1-minute load average %.2f exceeds --max-load %.2f; close other work and retry (do not raise --max-load for a kept result)", la.One, max)
	}
	return la, nil
}
