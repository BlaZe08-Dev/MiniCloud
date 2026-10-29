#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

REPO_ROOT = "/home/blazeee/MiniCloud-gemini"
EVIDENCE_DIR = os.path.join(REPO_ROOT, "docs/agents/reviews/P4-4e-round2-evidence")
PORT = 18092
COORD_URL = f"http://127.0.0.1:{PORT}"

def get_db_url():
    env_path = os.path.join(REPO_ROOT, ".env")
    with open(env_path) as f:
        for line in f:
            if line.startswith("MINICLOUD_TEST_DATABASE_URL="):
                return line.strip().split("=", 1)[1]
    raise RuntimeError("MINICLOUD_TEST_DATABASE_URL not found in .env")

def http_post(url, data_bytes, content_type="application/json"):
    req = urllib.request.Request(url, data=data_bytes, headers={"Content-Type": content_type}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, json.loads(resp.read().decode("utf-8", errors="replace"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(body)
        except Exception:
            parsed = {"raw": body}
        return e.code, parsed

def http_get(url):
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, json.loads(resp.read().decode("utf-8", errors="replace"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(body)
        except Exception:
            parsed = {"raw": body}
        return e.code, parsed

def wait_for_healthz(timeout_sec=10):
    start = time.time()
    while time.time() - start < timeout_sec:
        try:
            status, _ = http_get(f"{COORD_URL}/healthz")
            if status == 200:
                return True
        except Exception:
            pass
        time.sleep(0.1)
    return False

def query_db(db_url, job_id):
    helper = os.path.join(REPO_ROOT, "docs/agents/reviews/P4-4e-round2-evidence/scripts/query_db.go.txt")
    cmd = ["go", "run", helper, db_url, job_id]
    p = subprocess.run(cmd, cwd=REPO_ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if p.returncode != 0:
        return {"error": p.stderr}
    try:
        return json.loads(p.stdout)
    except Exception:
        return {"raw": p.stdout}

def kill_proc(proc, name):
    if proc.poll() is None:
        try:
            os.kill(proc.pid, signal.SIGTERM)
            for _ in range(50):
                if proc.poll() is not None:
                    break
                time.sleep(0.1)
            if proc.poll() is None:
                os.kill(proc.pid, signal.SIGKILL)
                proc.wait(timeout=5)
        except Exception as e:
            print(f"Error killing {name} (PID {proc.pid}): {e}")

def main():
    db_url = get_db_url()
    print(f"Target test DB: {db_url.split('@')[-1]}")
    
    bin_coord = os.path.join(REPO_ROOT, "bin/coordinator")
    bin_worker = os.path.join(REPO_ROOT, "bin/worker")
    if not os.path.exists(bin_coord) or not os.path.exists(bin_worker):
        print("Binaries not found. Running make build...")
        subprocess.run(["make", "build"], cwd=REPO_ROOT, check=True)

    coord_env = os.environ.copy()
    coord_env.update({
        "MINICLOUD_HTTP_ADDR": f"127.0.0.1:{PORT}",
        "MINICLOUD_STORAGE_DRIVER": "postgres",
        "MINICLOUD_DATABASE_URL": db_url,
        "MINICLOUD_HEARTBEAT_INTERVAL": "1s",
        "MINICLOUD_WORKER_TIMEOUT": "5s",
        "MINICLOUD_REAPER_INTERVAL": "1s",
        "MINICLOUD_LEASE_DURATION": "30s",
        "MINICLOUD_STARTUP_GRACE_PERIOD": "30s",
        "MINICLOUD_OUTAGE_GRACE_WINDOW": "30s",
        "MINICLOUD_SWEEPER_INTERVAL": "1s",
        "MINICLOUD_DEFAULT_MAX_ATTEMPTS": "3",
        "MINICLOUD_LOG_LEVEL": "info",
    })

    worker_env = os.environ.copy()
    worker_env.update({
        "MINICLOUD_COORDINATOR_URL": COORD_URL,
        "MINICLOUD_WORKER_ID": "worker_real_p4_4e",
        "MINICLOUD_WORKER_SLOTS": "2",
        "MINICLOUD_HEARTBEAT_INTERVAL": "1s",
        "MINICLOUD_LOG_LEVEL": "info",
    })

    coord_log = open(os.path.join(EVIDENCE_DIR, "part2_coordinator.log"), "w")
    worker_log = open(os.path.join(EVIDENCE_DIR, "part2_worker.log"), "w")

    print(f"Starting real coordinator on 127.0.0.1:{PORT}...")
    p_coord = subprocess.Popen([bin_coord], env=coord_env, stdout=coord_log, stderr=subprocess.STDOUT)
    print(f"Coordinator started with PID {p_coord.pid}")

    p_worker = None
    results = {}

    try:
        if not wait_for_healthz(15):
            raise RuntimeError(f"Coordinator failed to respond to /healthz on 127.0.0.1:{PORT}")
        print("Coordinator is healthy.")

        print("Starting real worker...")
        p_worker = subprocess.Popen([bin_worker], env=worker_env, stdout=worker_log, stderr=subprocess.STDOUT)
        print(f"Worker started with PID {p_worker.pid}")
        time.sleep(2) # settle & register

        # Verify worker registered
        st, w_list = http_get(f"{COORD_URL}/v1/workers")
        print(f"Registered workers: {w_list}")

        test_cases = [
            (
                "case_a_mid_rune_truncation",
                "65,535-byte payload ending in 3-byte characters (mid-character cut)",
                '{"m":"' + ("a" * 65227) + ("€" * 100) + '"}',
                True, # expect accepted submit
            ),
            (
                "case_b_json_escape_nul",
                "JSON string containing escape \\u0000",
                '{"m":"a\\u0000b"}',
                True,
            ),
            (
                "case_c_non_object_invalid_utf8",
                "Non-object payload returning invalid UTF-8",
                b'"a\xffb"',
                True,
            ),
            (
                "case_d1_ascii_exact_limit",
                "ASCII payload at exactly the 64 KiB limit (65,536 bytes)",
                '{"m":"' + ("a" * (65536 - 8)) + '"}', # 6 + 65528 + 2 = 65536
                True,
            ),
            (
                "case_d2_ascii_one_byte_over",
                "ASCII payload at one byte over limit (65,537 bytes)",
                '{"m":"' + ("a" * (65537 - 8)) + '"}', # 6 + 65529 + 2 = 65537
                False, # expect rejected submit
            ),
        ]

        for case_id, description, payload, expect_accepted in test_cases:
            print(f"\n=== Executing {case_id}: {description} ===")
            start_t = time.time()
            if isinstance(payload, str):
                payload_bytes = payload.encode("utf-8")
            else:
                payload_bytes = payload

            submit_body = {
                "task_type": "echo",
                "payload": json.loads(payload_bytes) if isinstance(payload, str) and not case_id.startswith("case_c") else None,
                "max_attempts": 3,
                "timeout_seconds": 30,
            }
            # To preserve exact bytes for raw payload:
            raw_req = json.dumps({"task_type": "echo", "max_attempts": 3, "timeout_seconds": 30}).encode("utf-8")
            # Inject raw payload field
            raw_req = raw_req[:-1] + b',"payload":' + payload_bytes + b'}'

            st, resp = http_post(f"{COORD_URL}/v1/jobs", raw_req)
            print(f"Submit status: {st}, response: {resp}")

            if not expect_accepted:
                elapsed = time.time() - start_t
                if st in (400, 413):
                    print(f"PASS: Correctly rejected oversize payload with HTTP {st}: {resp}")
                    results[case_id] = {
                        "description": description,
                        "status": f"REJECTED_{st}",
                        "response": resp,
                        "elapsed_sec": round(elapsed, 3),
                        "lost_attempts": 0,
                    }
                else:
                    print(f"FAIL: Expected 400/413, got {st}: {resp}")
                    results[case_id] = {
                        "description": description,
                        "status": f"UNEXPECTED_{st}",
                        "response": resp,
                        "elapsed_sec": round(elapsed, 3),
                        "lost_attempts": 0,
                    }
                continue

            if st != 201:
                print(f"FAIL: Job submission failed with {st}: {resp}")
                results[case_id] = {"error": f"Submit failed {st}", "resp": resp}
                continue

            job_id = resp["id"]
            print(f"Job submitted successfully: {job_id}. Waiting for completion...")

            # Poll for completion
            terminal = False
            last_job = None
            while time.time() - start_t < 25:
                st_get, last_job = http_get(f"{COORD_URL}/v1/jobs/{job_id}")
                if st_get == 200:
                    state = last_job.get("state")
                    if state in ("SUCCEEDED", "FAILED", "CANCELLED"):
                        terminal = True
                        break
                time.sleep(0.2)

            elapsed = time.time() - start_t
            if not terminal:
                print(f"FAIL: Job {job_id} did not reach terminal state within 25s. Current state: {last_job.get('state')}")

            # Inspect database directly
            db_info = query_db(db_url, job_id)
            print(f"Direct DB inspection: {json.dumps(db_info, indent=2)}")

            # Check attempts for LOST
            lost_count = 0
            for a in db_info.get("attempts", []):
                if a.get("state") == "LOST":
                    lost_count += 1

            results[case_id] = {
                "description": description,
                "job_id": job_id,
                "final_state": db_info.get("state"),
                "attempts_count": len(db_info.get("attempts", [])),
                "attempts": db_info.get("attempts", []),
                "result_rows_count": len(db_info.get("result_rows", [])),
                "result_rows": db_info.get("result_rows", []),
                "lost_attempts": lost_count,
                "elapsed_sec": round(elapsed, 3),
            }
            if lost_count > 0:
                print(f"FAIL: Job {job_id} had {lost_count} LOST attempts!")
            else:
                print(f"PASS: Job {job_id} ended {db_info.get('state')} with 0 LOST attempts.")

    finally:
        print("\nCleaning up processes...")
        if p_worker:
            kill_proc(p_worker, "worker")
        if p_coord:
            kill_proc(p_coord, "coordinator")
        coord_log.close()
        worker_log.close()

    # Save summary report
    out_file = os.path.join(EVIDENCE_DIR, "part2_real_worker_results.json")
    with open(out_file, "w") as f:
        json.dump(results, f, indent=2)
    print(f"\nSaved Part 2 results to {out_file}")

if __name__ == "__main__":
    main()
