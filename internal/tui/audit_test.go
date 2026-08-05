package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestAuditCommandRecordsSuccessAndFailure(t *testing.T) {
	path := t.TempDir() + "/commands.jsonl"
	t.Setenv("DISPATCH_TUI_LOG", path)
	auditCommand([]string{"go", "ship it"}, "", nil)
	auditCommand([]string{"batch"}, "done ab12\n", errors.New("exit status 1"))

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	var records []commandAudit
	for s.Scan() {
		var record commandAudit
		if err := json.Unmarshal(s.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || !records[0].OK || records[1].OK || records[1].Input != "done ab12\n" {
		t.Fatalf("unexpected audit records: %+v", records)
	}
}
