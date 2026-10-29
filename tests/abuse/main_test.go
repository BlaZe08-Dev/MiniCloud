package abuse_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if left := sectest.Cleanup(); left > 0 {
		fmt.Fprintf(os.Stderr, "%d process(es) outlived the run\n", left)
		code = 1
	}
	os.Exit(code)
}
