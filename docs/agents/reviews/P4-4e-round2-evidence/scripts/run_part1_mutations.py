#!/usr/bin/env python3
import os
import subprocess
import sys
import time

REPO_ROOT = "/home/blazeee/MiniCloud-gemini"
EVIDENCE_DIR = os.path.join(REPO_ROOT, "docs/agents/reviews/P4-4e-round2-evidence")

def run_cmd(cmd, cwd=REPO_ROOT, env=None):
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    p = subprocess.run(cmd, shell=True, cwd=cwd, env=merged_env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    return p.returncode, p.stdout

def record_execution(out_path, cmd, rc, output):
    with open(out_path, "w") as f:
        f.write(f"=== COMMAND: {cmd} ===\n")
        f.write(f"=== TIMESTAMP: {time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())} ===\n")
        f.write(output)
        if not output.endswith("\n"):
            f.write("\n")
        f.write(f"=== EXIT CODE: {rc} ===\n")

def check_clean_git():
    rc, out = run_cmd("git diff --stat HEAD -- ':!docs' ':!reports'")
    if out.strip():
        print(f"FATAL: Working tree dirty outside docs/ and reports/:\n{out}")
        sys.exit(1)

def apply_and_test(mut_num, mut_name, target_file, mutate_func, test_cmd):
    print(f"\n--- Running Mutation {mut_num}: {mut_name} ---")
    check_clean_git()
    
    # Read target file
    full_target = os.path.join(REPO_ROOT, target_file)
    with open(full_target, "r") as f:
        orig_content = f.read()

    # Mutate
    mut_content = mutate_func(orig_content)
    if mut_content == orig_content:
        print(f"FATAL: Mutation {mut_num} produced no change!")
        sys.exit(1)

    with open(full_target, "w") as f:
        f.write(mut_content)

    # Record diff
    rc_diff, diff_out = run_cmd(f"git diff {target_file}")
    diff_file = os.path.join(EVIDENCE_DIR, f"mut_{mut_num:02d}_{mut_name}.diff")
    with open(diff_file, "w") as f:
        f.write(diff_out)
    print(f"Saved diff to {diff_file}")

    # Run red test
    print(f"Executing red test: {test_cmd}")
    rc_red, out_red = run_cmd(test_cmd)
    red_file = os.path.join(EVIDENCE_DIR, f"mut_{mut_num:02d}_red.txt")
    record_execution(red_file, test_cmd, rc_red, out_red)
    print(f"Red test exit code: {rc_red}")
    if rc_red == 0:
        print(f"WARNING: Mutation {mut_num} SURVIVED test {test_cmd}!")
    else:
        print(f"Mutation {mut_num} KILLED by test {test_cmd}")

    # Restore
    with open(full_target, "w") as f:
        f.write(orig_content)
    run_cmd(f"git checkout {target_file}")
    check_clean_git()

    time.sleep(2)

    # Run green test to prove restore
    print(f"Executing green test to prove restore: {test_cmd}")
    rc_green, out_green = run_cmd(test_cmd)
    green_file = os.path.join(EVIDENCE_DIR, f"mut_{mut_num:02d}_green.txt")
    record_execution(green_file, test_cmd, rc_green, out_green)
    print(f"Green test exit code: {rc_green}")
    if rc_green != 0:
        print(f"FATAL: Restored test failed for mutation {mut_num}!")
        sys.exit(1)

    time.sleep(1)

    return rc_red != 0

def main():
    os.makedirs(EVIDENCE_DIR, exist_ok=True)
    check_clean_git()

    mutations = []

    # 1. ClampText: remove the NUL replacement
    def m1(s):
        target = "(r == utf8.RuneError && w == 1) || r == 0"
        assert target in s
        return s.replace(target, "(r == utf8.RuneError && w == 1)")
    mutations.append((1, "clamptext_remove_nul_replacement", "internal/executor/truncate.go", m1, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestClampText"))

    # 2. ClampText: measure length before the replacement pass
    def m2(s):
        # Move "if len(s) <= limit { return s }" to before the replacement pass
        check_block = "\tif len(s) <= limit {\n\t\treturn s\n\t}\n"
        assert check_block in s
        s_without = s.replace(check_block, "")
        first_if = "\tif !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {\n"
        assert first_if in s_without
        return s_without.replace(first_if, check_block + first_if)
    mutations.append((2, "clamptext_measure_before_replacement", "internal/executor/truncate.go", m2, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestClampText"))

    # 3. ClampText: cut without the rune-boundary loop
    def m3(s):
        target = "\tfor cut > 0 && !utf8.RuneStart(s[cut]) {\n\t\tcut--\n\t}\n"
        assert target in s
        return s.replace(target, "")
    mutations.append((3, "clamptext_cut_without_rune_boundary", "internal/executor/truncate.go", m3, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestClampText"))

    # 4. executor Execute: remove the clamp on the success path
    def m4(s):
        target = "return ClampText(output, MaxOutputSizeBytes), 0, nil"
        assert target in s
        return s.replace(target, "return output, 0, nil")
    mutations.append((4, "executor_execute_remove_success_clamp", "internal/executor/executor.go", m4, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestExecutor_OutputCapped"))

    # 5. executor Execute: remove the clamp on the failure path
    def m5(s):
        target = "return ClampText(output, MaxOutputSizeBytes), 1, err"
        assert target in s
        return s.replace(target, "return output, 1, err")
    mutations.append((5, "executor_execute_remove_failure_clamp", "internal/executor/executor.go", m5, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestRegistryExecute_OutputCapOnFailure"))

    # 6. worker ExecuteAndReport: remove the output/error clamp
    def m6(s):
        target = "\toutput = executor.ClampText(output, executor.MaxOutputSizeBytes)\n\terrSummary := \"\"\n\tif err != nil {\n\t\terrSummary = executor.ClampText(err.Error(), executor.MaxErrorSizeBytes)\n\t}"
        assert target in s
        repl = "\t// output unclamped\n\terrSummary := \"\"\n\tif err != nil {\n\t\terrSummary = err.Error()\n\t}"
        return s.replace(target, repl)
    mutations.append((6, "worker_executeandreport_remove_clamp", "internal/worker/client.go", m6, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./tests/abuse -run TestAbuse_OversizeOutput_ErrorPathUncapped"))

    # 7. fallback: remove the fallbackSent guard (loop)
    def m7(s):
        target = "if !fallbackSent && isValidationRejection(status, code) {"
        assert target in s
        return s.replace(target, "if isValidationRejection(status, code) {")
    mutations.append((7, "worker_fallback_remove_guard_loop", "internal/worker/client.go", m7, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_FallbackAlsoRejected_NoLoop"))

    # 8. fallback: add MALFORMED_JSON back to isValidationRejection
    def m8(s):
        target = 'case "INVALID_REQUEST", "OUTPUT_TOO_LARGE":'
        assert target in s
        return s.replace(target, 'case "INVALID_REQUEST", "OUTPUT_TOO_LARGE", "MALFORMED_JSON":')
    mutations.append((8, "worker_fallback_add_malformed_json", "internal/worker/client.go", m8, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_MalformedJSON_NoFallback"))

    # 9. fallback: keep the original output instead of empty
    def m9(s):
        target = 'exitCode, output = 1, ""'
        assert target in s
        return s.replace(target, 'exitCode = 1')
    mutations.append((9, "worker_fallback_keep_original_output", "internal/worker/client.go", m9, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_SendsOneFallbackFailureReport"))

    # 10. fallback: error class not PERMANENT
    def m10(s):
        target = "errorClass = domain.ClassPermanent"
        assert target in s
        return s.replace(target, "errorClass = domain.ClassTransient")
    mutations.append((10, "worker_fallback_error_class_not_permanent", "internal/worker/client.go", m10, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_SendsOneFallbackFailureReport"))

    # 11. fallback: send without continuing renewal
    def m11(s):
        # Stop renewals right before deliverResult in ExecuteAndReport
        target = "\t// deliverResult drops the result without any request when the attempt was declared lost.\n\ta.deliverResult(ctx, run, assignment, exitCode, output, errSummary, errorClass)"
        assert target in s
        repl = "\tstopRenew()\n\twg.Wait()\n\t// deliverResult drops the result without any request when the attempt was declared lost.\n\ta.deliverResult(ctx, run, assignment, exitCode, output, errSummary, errorClass)"
        return s.replace(target, repl)
    mutations.append((11, "worker_fallback_stop_renewal_early", "internal/worker/client.go", m11, "env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_SendsOneFallbackFailureReport"))

    # 12. PostgreSQL SKIP LOCKED removal (PG-M01 again)
    def m12(s):
        target = "FOR UPDATE SKIP LOCKED"
        assert target in s
        return s.replace(target, "FOR UPDATE")
    # For PG-M01, make test-db must be run
    mutations.append((12, "postgres_dispatch_remove_skiplocked", "internal/storage/postgres/repository.go", m12, "make test-db"))

    results = []
    for mut_num, name, target_file, mutate_func, test_cmd in mutations:
        killed = apply_and_test(mut_num, name, target_file, mutate_func, test_cmd)
        results.append((mut_num, name, killed))

    print("\n================== SUMMARY ==================")
    for num, name, killed in results:
        status = "KILLED" if killed else "SURVIVED"
        print(f"Mutation {num:02d} ({name}): {status}")

if __name__ == "__main__":
    main()
