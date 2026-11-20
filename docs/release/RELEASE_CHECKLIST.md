# MiniCloud Release Checklist

This checklist documents the procedure for producing official MiniCloud releases. All steps are labelled **[Proposed]** based on reverse-engineered facts from the `v1.0.0` release assets and repository conventions, as formal release instructions were not previously committed.

> **Ownership rule:** Only the repository owner executes tags and GitHub releases (Steps 5 and 6). Agents and the orchestrator never tag a commit or publish a release.

---

### Step 1 [Proposed]: Preconditions

1. **Environment preparation and branch state:**
   - Close the browser and IDE first to eliminate background CPU load and memory contention.
   - Working tree must be completely clean on `main`:
     ```bash
     git checkout main
     git pull --ff-only origin main
     test -z "$(git status -s)" || { echo "ERROR: working tree not clean"; exit 1; }
     ```
2. **Execute quality gates on `main`:**
   - Execute the gate commands in order:
     ```bash
     gofmt -l .                                                    # must be empty
     go vet ./...
     go build ./...
     go clean -testcache
     make test-docs
     env -u MINICLOUD_TEST_DATABASE_URL go test -v -count=1 ./...
     env -u MINICLOUD_TEST_DATABASE_URL make test-race
     make test-db
     make test-security
     make test-outage CONFIRM=minicloud-postgres                  # run last with every agent idle
     ```
   - Count top-level lines starting with `--- PASS`, `--- SKIP`, and `--- FAIL` to verify gate results.

---

### Step 2 [Proposed]: Version verification

1. Check compiled version in `internal/version/version.go`:
   - `internal/version.Version` must match the release tag (e.g. `v1.1.0`).
   - If overriding via build flags, pass `-ldflags "-X github.com/BlaZe08-Dev/Minicloud/internal/version.Version=<tag>"`.

---

### Step 3 [Proposed]: Build release binaries and archive

1. Propose building static binaries for `linux/amd64` using `-trimpath`:
   *(Note: Exact compiler flags used for `v1.0.0` were not recorded; the owner may compare compiled sizes against `v1.0.0` assets).*
   ```bash
   RELEASE_TAG="v1.1.0"
   DIST_DIR="minicloud-${RELEASE_TAG}-linux-amd64"
   mkdir -p "${DIST_DIR}"

   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "${DIST_DIR}/coordinator" ./cmd/coordinator
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "${DIST_DIR}/worker" ./cmd/worker
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "${DIST_DIR}/mc" ./cmd/mc
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "${DIST_DIR}/migrate" ./cmd/migrate
   cp README.md "${DIST_DIR}/"
   ```
2. Create release tarball:
   ```bash
   tar -czvf "minicloud-${RELEASE_TAG}-linux-amd64.tar.gz" "${DIST_DIR}"
   ```
3. Generate checksum file:
   ```bash
   sha256sum "minicloud-${RELEASE_TAG}-linux-amd64.tar.gz" > SHA256SUMS.txt
   ```
   *(Fact verified from v1.0.0: `SHA256SUMS.txt` contains exactly one line formatted as `<sha256>  minicloud-<version>-linux-amd64.tar.gz`).*

---

### Step 4 [Proposed]: Verify archive and binaries

1. **Verify checksum integrity:**
   ```bash
   sha256sum -c SHA256SUMS.txt
   ```
2. **Inspect archive contents:**
   - Ensure the tarball unpacks into `minicloud-${RELEASE_TAG}-linux-amd64/` holding `README.md` and the four binaries (`coordinator`, `worker`, `mc`, `migrate`).
3. **Verify binary behavior:**
   - Binaries under `cmd/` do not provide a `-version` or `--version` flag.
   - `mc help` or `mc --help` prints usage.
   - To verify worker runtime version reporting, register a test worker and check the `version` attribute in `GET /v1/workers`.

---

### Step 5 [Proposed]: Tag and GitHub release

1. **Create annotated tag on `main` merge commit:**
   ```bash
   git tag -a "${RELEASE_TAG}" -m "MiniCloud ${RELEASE_TAG}: minor release"
   git push origin "${RELEASE_TAG}"
   ```
2. **Prepare release notes outside the repository:**
   - Copy `docs/release/v1.1.0-notes.md` to a temporary file outside the repository (e.g. `/tmp/release-notes-${RELEASE_TAG}.md`).
   - Append the final gate results from the owner's verification run to that temporary file.
   - Do not put gate numbers in the committed notes.
3. **Publish GitHub release:**
   ```bash
   gh release create "${RELEASE_TAG}" \
     "minicloud-${RELEASE_TAG}-linux-amd64.tar.gz" \
     SHA256SUMS.txt \
     --title "MiniCloud ${RELEASE_TAG}" \
     --notes-file "/tmp/release-notes-${RELEASE_TAG}.md"
   ```
   *(Fact verified from v1.0.0: Release body begins with `MiniCloud <tag>: ...` and summarizes final gate verification counts).*

---

### Step 6 [Proposed]: Post-release updates

1. Update `docs/PROJECT_STATE.md`:
   - Record tag, commit hash, and final gate counts.
2. Update `README.md`:
   - Update current release badges, version statements, and download links.
3. Update the Project context (orchestrator):
   - Update project context and release state across documentation.
