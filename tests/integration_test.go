package tests

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func getID(t *testing.T, output string) string {
	t.Helper()
	var result map[string]string
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\noutput: %s", err, output)
	}
	return result["id"]
}

// projectRoot returns the directory containing go.mod.
func projectRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(mustAbs(t, "../go.mod"))
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// buildBinary compiles dt to tmpDir and returns the binary path.
func buildBinary(t *testing.T, tmpDir string) string {
	t.Helper()
	bin := filepath.Join(tmpDir, "dt")
	root := projectRoot(t)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/dt/")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build dt: %v\n%s", err, out)
	}
	return bin
}

// runDT executes the dt binary with the given args, using the provided db path.
// It returns trimmed stdout.
func runDT(t *testing.T, bin, dbPath string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"--db", dbPath, "--json"}, args...)
	cmd := exec.Command(bin, fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dt %v failed: %v\noutput: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// runDTStdin executes dt with stdin input.
func runDTStdin(t *testing.T, bin, dbPath, stdin string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"--db", dbPath, "--json"}, args...)
	cmd := exec.Command(bin, fullArgs...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dt %v failed: %v\noutput: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestExitCriteria_ReadyOrder(t *testing.T) {
	tmpDir := t.TempDir()
	bin := buildBinary(t, tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create 10 tasks.
	ids := make([]string, 10)
	for i := 0; i < 10; i++ {
		out := runDT(t, bin, dbPath, "add", fmt.Sprintf("Task %d", i))
		ids[i] = getID(t, out)
	}

	// Tasks 1, 2, 3 depend on task 0 (task 0 unblocks 3 tasks).
	runDT(t, bin, dbPath, "dep", ids[1], ids[0])
	runDT(t, bin, dbPath, "dep", ids[2], ids[0])
	runDT(t, bin, dbPath, "dep", ids[3], ids[0])

	// Tasks 5, 6 depend on task 4 (task 4 unblocks 2 tasks).
	runDT(t, bin, dbPath, "dep", ids[5], ids[4])
	runDT(t, bin, dbPath, "dep", ids[6], ids[4])

	// Task 8 depends on task 7 (task 7 unblocks 1 task).
	runDT(t, bin, dbPath, "dep", ids[8], ids[7])

	// Task 9 is independent (unblocks 0).

	// Call ready.
	out := runDT(t, bin, dbPath, "ready")

	var tasks []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &tasks); err != nil {
		t.Fatalf("failed to parse ready output: %v\noutput: %s", err, out)
	}

	// Verify exactly 4 tasks are ready.
	if len(tasks) != 4 {
		t.Fatalf("expected 4 ready tasks, got %d: %s", len(tasks), out)
	}

	// Verify ordering: task 0 first (unblocks 3), task 4 second (unblocks 2).
	readyIDs := make([]string, len(tasks))
	for i, task := range tasks {
		readyIDs[i] = task["id"].(string)
	}

	if readyIDs[0] != ids[0] {
		t.Errorf("expected first ready task to be %s (Task 0, unblocks 3), got %s", ids[0], readyIDs[0])
	}
	if readyIDs[1] != ids[4] {
		t.Errorf("expected second ready task to be %s (Task 4, unblocks 2), got %s", ids[4], readyIDs[1])
	}

	// Verify all 4 ready tasks are the expected ones.
	expectedReady := map[string]bool{
		ids[0]: true,
		ids[4]: true,
		ids[7]: true,
		ids[9]: true,
	}
	for _, id := range readyIDs {
		if !expectedReady[id] {
			t.Errorf("unexpected task %s in ready list", id)
		}
	}
}

func TestExitCriteria_Batch(t *testing.T) {
	tmpDir := t.TempDir()
	bin := buildBinary(t, tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	batchInput := `add "Batch task 1"
add "Batch task 2"
add "Batch task 3"
# This is a comment

add "Batch task 4"
`

	runDTStdin(t, bin, dbPath, batchInput, "batch")

	// Verify list returns exactly 4 tasks.
	out := runDT(t, bin, dbPath, "list")

	var tasks []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &tasks); err != nil {
		t.Fatalf("failed to parse list output: %v\noutput: %s", err, out)
	}

	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks after batch, got %d: %s", len(tasks), out)
	}
}

func TestExitCriteria_JSONOutput(t *testing.T) {
	tmpDir := t.TempDir()
	bin := buildBinary(t, tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	// dt add returns {"id": "xxxx"}.
	addOut := runDT(t, bin, dbPath, "add", "JSON test task")
	var addResult map[string]string
	if err := json.Unmarshal([]byte(addOut), &addResult); err != nil {
		t.Fatalf("add output is not valid JSON: %v\noutput: %s", err, addOut)
	}
	id, ok := addResult["id"]
	if !ok || id == "" {
		t.Fatalf("add output missing 'id' field: %s", addOut)
	}

	// dt show <id> returns a JSON object with task details.
	showOut := runDT(t, bin, dbPath, "show", id)
	var showResult map[string]interface{}
	if err := json.Unmarshal([]byte(showOut), &showResult); err != nil {
		t.Fatalf("show output is not valid JSON: %v\noutput: %s", err, showOut)
	}
	taskObj, ok := showResult["task"]
	if !ok {
		t.Fatalf("show output missing 'task' field: %s", showOut)
	}
	taskMap, ok := taskObj.(map[string]interface{})
	if !ok {
		t.Fatalf("show 'task' field is not an object: %s", showOut)
	}
	if taskMap["id"] != id {
		t.Errorf("show task id mismatch: expected %s, got %v", id, taskMap["id"])
	}

	// dt list returns a JSON array.
	listOut := runDT(t, bin, dbPath, "list")
	var listResult []interface{}
	if err := json.Unmarshal([]byte(listOut), &listResult); err != nil {
		t.Fatalf("list output is not a JSON array: %v\noutput: %s", err, listOut)
	}

	// dt ready returns a JSON array.
	readyOut := runDT(t, bin, dbPath, "ready")
	var readyResult []interface{}
	if err := json.Unmarshal([]byte(readyOut), &readyResult); err != nil {
		t.Fatalf("ready output is not a JSON array: %v\noutput: %s", err, readyOut)
	}

}
