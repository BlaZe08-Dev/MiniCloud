package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

type AttemptInfo struct {
	AttemptNo   int    `json:"attempt_no"`
	State       string `json:"state"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	ErrorClass  string `json:"error_class,omitempty"`
	Error       string `json:"error,omitempty"`
	OutputBytes int    `json:"output_bytes"`
}

type ResultInfo struct {
	Count       int    `json:"count"`
	ExitCode    int    `json:"exit_code"`
	OutputBytes int    `json:"output_bytes"`
	OutputTail  string `json:"output_tail"`
	Error       string `json:"error,omitempty"`
}

type JobInspection struct {
	JobID      string        `json:"job_id"`
	State      string        `json:"state"`
	Attempts   []AttemptInfo `json:"attempts"`
	ResultRows []ResultInfo  `json:"result_rows"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: query_db <db_url> <job_id>\n")
		os.Exit(1)
	}
	dbURL := os.Args[1]
	jobID := os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	insp := JobInspection{JobID: jobID}
	err = conn.QueryRow(ctx, "SELECT state FROM jobs WHERE id = $1", jobID).Scan(&insp.State)
	if err != nil {
		fmt.Fprintf(os.Stderr, "job query error: %v\n", err)
		os.Exit(1)
	}

	rows, err := conn.Query(ctx, "SELECT attempt_no, state, exit_code, error_class, error, length(coalesce(output, '')) FROM attempts WHERE job_id = $1 ORDER BY attempt_no", jobID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a AttemptInfo
			var errClass, errSummary *string
			if err := rows.Scan(&a.AttemptNo, &a.State, &a.ExitCode, &errClass, &errSummary, &a.OutputBytes); err == nil {
				if errClass != nil {
					a.ErrorClass = *errClass
				}
				if errSummary != nil {
					a.Error = *errSummary
				}
				insp.Attempts = append(insp.Attempts, a)
			}
		}
	}

	resRows, err := conn.Query(ctx, "SELECT exit_code, coalesce(output, ''), coalesce(error, '') FROM results WHERE job_id = $1", jobID)
	if err == nil {
		defer resRows.Close()
		for resRows.Next() {
			var r ResultInfo
			var fullOutput string
			if err := resRows.Scan(&r.ExitCode, &fullOutput, &r.Error); err == nil {
				r.OutputBytes = len(fullOutput)
				if len(fullOutput) > 60 {
					r.OutputTail = fullOutput[len(fullOutput)-60:]
				} else {
					r.OutputTail = fullOutput
				}
				r.Count = 1
				insp.ResultRows = append(insp.ResultRows, r)
			}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(insp)
}
