package abuse_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"unicode/utf8"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestAbuse_WorkerVersion_InvalidUTF8_StoredAsReplacement registers a worker whose version holds raw
// invalid UTF-8 bytes. The registration is accepted (200) because the JSON decoder replaces each
// invalid byte with U+FFFD before any validation. Required: what the API returns and what
// PostgreSQL stores is valid UTF-8 text, never the raw bytes, never a 500.
func TestAbuse_WorkerVersion_InvalidUTF8_StoredAsReplacement(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		body := []byte(`{"id":"w_utf","version":"v` + "\xff\xfe" + `1","total_slots":1,"supported_tasks":["echo"]}`)
		if resp := r.Do("POST", "/v1/workers/register", body); resp.Status != http.StatusOK {
			t.Fatalf("[%s] register with invalid UTF-8 version = %s, want 200", kind, resp)
		}
		resp := r.Do("GET", "/v1/workers", nil)
		if resp.Status != http.StatusOK {
			t.Fatalf("[%s] GET /v1/workers = %s", kind, resp)
		}
		var list struct {
			Workers []struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			} `json:"workers"`
		}
		if err := json.Unmarshal(resp.Body, &list); err != nil || len(list.Workers) != 1 {
			t.Fatalf("[%s] list decode: %v body %.200s", kind, err, resp.Body)
		}
		got := list.Workers[0].Version
		t.Logf("[%s] API version = %q (hex %s)", kind, got, hex.EncodeToString([]byte(got)))
		if !utf8.ValidString(got) || got != "v��1" {
			t.Errorf("[%s] API version = %q, want valid text \"v\\uFFFD\\uFFFD1\"", kind, got)
		}
		if kind == sectest.KindPostgres {
			var stored string
			var octets int
			if err := r.PG.Pool.QueryRow(context.Background(), `SELECT version, octet_length(version) FROM workers WHERE id = 'w_utf'`).Scan(&stored, &octets); err != nil {
				t.Fatal(err)
			}
			t.Logf("[postgres] stored version = %q (%d octets, hex %s)", stored, octets, hex.EncodeToString([]byte(stored)))
			if stored != "v��1" || octets != 8 {
				t.Errorf("[postgres] stored %q (%d octets), want \"v\\uFFFD\\uFFFD1\" (8 octets)", stored, octets)
			}
		}
	})
}
