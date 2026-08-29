package structural

import (
	"os"
	"testing"
)

// The real pair, from the migration the proving ground performed.
// Manual because it reads files a demo run leaves behind.
//
//	go test ./structural -run TestRealMigrationDiff -v \
//	 -args (needs BEFORE_DOC and AFTER_DOC set)
func TestRealMigrationDiff(t *testing.T) {
	before, after := os.Getenv("BEFORE_DOC"), os.Getenv("AFTER_DOC")
	if before == "" || after == "" {
		t.Skip("set BEFORE_DOC and AFTER_DOC to two manifest paths")
	}
	b, err := os.ReadFile(before)
	if err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(after)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", Diff(string(b), string(a)))
}
