package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

var (
	stdout   io.Writer = os.Stdout
	stderr   io.Writer = os.Stderr
	exitFunc           = os.Exit
)

func getCoordinatorURL() string {
	if url := os.Getenv("MINICLOUD_COORDINATOR_URL"); url != "" {
		return url
	}
	return "http://127.0.0.1:8080"
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		exitFunc(1)
	}

	command := os.Args[1]
	switch command {
	case "submit":
		handleSubmit(os.Args[2:])
	case "get":
		handleGet(os.Args[2:])
	case "attempts":
		handleAttempts(os.Args[2:])
	case "workers":
		handleWorkers(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", command)
		printUsage()
		exitFunc(1)
	}
}

func printUsage() {
	fmt.Fprintln(stdout, `MiniCloud CLI (mc)

Usage:
  mc submit --task <type> --payload <json> [--timeout <sec>] [--max-attempts <num>]
  mc get <job_id>
  mc attempts <job_id>
  mc workers

Environment Variables:
  MINICLOUD_COORDINATOR_URL   Coordinator HTTP API base URL (default: http://127.0.0.1:8080)`)
}

func handleSubmit(args []string) {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	taskType := fs.String("task", "", "Allowlisted task type (echo, sleep, sha256_loop)")
	payload := fs.String("payload", "{}", "JSON payload string")
	timeoutSec := fs.Int("timeout", 60, "Task execution timeout in seconds")
	maxAttempts := fs.Int("max-attempts", 0, "Maximum execution attempts (0 = the coordinator's default, MINICLOUD_DEFAULT_MAX_ATTEMPTS)")
	coordURL := fs.String("coordinator-url", getCoordinatorURL(), "Coordinator URL")

	if err := fs.Parse(args); err != nil {
		exitFunc(1)
		return
	}

	if *taskType == "" {
		fmt.Fprintln(stderr, "Error: --task is required")
		fs.Usage()
		exitFunc(1)
		return
	}

	if !json.Valid([]byte(*payload)) {
		fmt.Fprintln(stderr, "Error: --payload must be valid JSON")
		exitFunc(1)
		return
	}

	req := map[string]any{
		"task_type":       *taskType,
		"payload":         json.RawMessage(*payload),
		"timeout_seconds": *timeoutSec,
	}
	if *maxAttempts != 0 {
		req["max_attempts"] = *maxAttempts
	}
	reqBody, _ := json.Marshal(req)

	resp, err := http.Post(*coordURL+"/v1/jobs", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(stderr, "Error submitting job: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(stderr, "Job submission failed (HTTP %d): %s\n", resp.StatusCode, sanitize(string(body)))
		exitFunc(1)
		return
	}

	var job domain.Job
	if err := json.Unmarshal(body, &job); err != nil {
		fmt.Fprintln(stdout, sanitizeJSON(string(body)))
		return
	}

	fmt.Fprintf(stdout, "Job Submitted Successfully!\n")
	fmt.Fprintf(stdout, "  ID:           %s\n", job.ID)
	fmt.Fprintf(stdout, "  Task:         %s\n", sanitize(job.TaskType))
	fmt.Fprintf(stdout, "  State:        %s\n", job.State)
	fmt.Fprintf(stdout, "  Max Attempts: %d\n", job.MaxAttempts)
}

func handleGet(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "Usage: mc get <job_id>")
		exitFunc(1)
		return
	}
	jobID := args[0]
	coordURL := getCoordinatorURL()

	resp, err := http.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jobID))
	if err != nil {
		fmt.Fprintf(stderr, "Error querying job: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "Query failed (HTTP %d): %s\n", resp.StatusCode, sanitize(string(body)))
		exitFunc(1)
		return
	}

	var job domain.Job
	if err := json.Unmarshal(body, &job); err != nil {
		fmt.Fprintln(stdout, sanitizeJSON(string(body)))
		return
	}

	fmt.Fprintf(stdout, "Job Details:\n")
	fmt.Fprintf(stdout, "  ID:           %s\n", job.ID)
	fmt.Fprintf(stdout, "  Task:         %s\n", sanitize(job.TaskType))
	fmt.Fprintf(stdout, "  State:        %s\n", job.State)
	fmt.Fprintf(stdout, "  Attempt ID:   %s\n", job.CurrentAttemptID)
	fmt.Fprintf(stdout, "  Created At:   %s\n", job.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(stdout, "  Updated At:   %s\n", job.UpdatedAt.Format(time.RFC3339))
	if job.Result != nil {
		fmt.Fprintf(stdout, "  Result:\n")
		fmt.Fprintf(stdout, "    Exit Code:  %d\n", job.Result.ExitCode)
		fmt.Fprintf(stdout, "    Output:     %s\n", sanitize(job.Result.Output))
		if job.Result.Error != "" {
			fmt.Fprintf(stdout, "    Error:      %s\n", sanitize(job.Result.Error))
		}
	}
	if job.State == domain.StateFailed || job.State == domain.StateCancelled || job.State == domain.StateRetrying {
		attempts, err := fetchAttempts(coordURL, jobID)
		if err != nil {
			fmt.Fprintf(stdout, "  Failure:      attempt history unavailable (%v)\n", err)
			return
		}
		if lines := failureSummary(&job, attempts); len(lines) > 0 {
			fmt.Fprintf(stdout, "  Failure:\n")
			for _, l := range lines {
				fmt.Fprintf(stdout, "    %s\n", l)
			}
		}
	}
}

// fetchAttempts reads GET /v1/jobs/{id}/attempts.
func fetchAttempts(coordURL, jobID string) ([]*domain.Attempt, error) {
	resp, err := http.Get(fmt.Sprintf("%s/v1/jobs/%s/attempts", coordURL, jobID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var res struct {
		Attempts []*domain.Attempt `json:"attempts"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return res.Attempts, nil
}

func handleAttempts(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "Usage: mc attempts <job_id>")
		exitFunc(1)
		return
	}
	jobID := args[0]
	coordURL := getCoordinatorURL()

	resp, err := http.Get(fmt.Sprintf("%s/v1/jobs/%s/attempts", coordURL, jobID))
	if err != nil {
		fmt.Fprintf(stderr, "Error querying attempts: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "Query failed (HTTP %d): %s\n", resp.StatusCode, sanitize(string(body)))
		exitFunc(1)
		return
	}

	var res struct {
		JobID    string            `json:"job_id"`
		Attempts []*domain.Attempt `json:"attempts"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		fmt.Fprintln(stdout, sanitizeJSON(string(body)))
		return
	}

	fmt.Fprintf(stdout, "Attempts for Job %s (%d total):\n", res.JobID, len(res.Attempts))
	w := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NO\tATTEMPT ID\tWORKER\tSTATE\tCLASS\tEXIT\tSTARTED")
	for _, att := range res.Attempts {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%s\n",
			att.AttemptNo,
			att.ID,
			sanitize(att.WorkerID),
			att.State,
			att.ErrorClass,
			att.ExitCode,
			att.StartedAt.Format("15:04:05"),
		)
	}
	_ = w.Flush()
}

func handleWorkers(args []string) {
	coordURL := getCoordinatorURL()
	resp, err := http.Get(coordURL + "/v1/workers")
	if err != nil {
		fmt.Fprintf(stderr, "Error querying workers: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "Query failed (HTTP %d): %s\n", resp.StatusCode, sanitize(string(body)))
		exitFunc(1)
		return
	}

	var res struct {
		Workers []*domain.Worker `json:"workers"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		fmt.Fprintln(stdout, sanitizeJSON(string(body)))
		return
	}

	fmt.Fprintf(stdout, "Registered Workers (%d total):\n", len(res.Workers))
	w := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "WORKER ID\tSTATUS\tAVAILABLE/TOTAL\tLAST HEARTBEAT\tTASKS")
	for _, worker := range res.Workers {
		tasks := make([]string, len(worker.SupportedTasks))
		for i, t := range worker.SupportedTasks {
			tasks[i] = sanitize(t)
		}
		fmt.Fprintf(w, "%s\t%s\t%d/%d\t%s\t%v\n",
			sanitize(worker.ID),
			worker.Status,
			worker.AvailableSlots,
			worker.TotalSlots,
			worker.LastHeartbeatAt.Format("15:04:05"),
			tasks,
		)
	}
	_ = w.Flush()
}
