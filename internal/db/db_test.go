package db

import (
	"os"
	"path/filepath"
	"testing"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "sub", "test.db")
}

func TestOpenCreatesFile(t *testing.T) {
	path := tempDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}
}

func TestWALMode(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	var mode string
	if err := d.q.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", mode)
	}
}

func TestForeignKeysOn(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	var fk int
	if err := d.q.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("expected foreign_keys=1, got %d", fk)
	}
}

func TestTablesExist(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	tables := []string{"tasks", "deps", "notes"}
	for _, tbl := range tables {
		var name string
		err := d.q.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", tbl, err)
		}
	}
}

func TestBeginTxCommit(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx()
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	task, err := tx.AddTask("tx task", "tx desc", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask in tx: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify task visible via original db after commit
	got, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask after commit: %v", err)
	}
	if got.Title != "tx task" {
		t.Errorf("expected 'tx task', got %q", got.Title)
	}
}

func TestBeginTxRollback(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx()
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	task, err := tx.AddTask("rollback task", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask in tx: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify task NOT visible via original db after rollback
	_, err = d.GetTask(task.ID)
	if err == nil {
		t.Fatal("expected error for task after rollback, got nil")
	}
}

func TestAddTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, err := d.AddTask("my task", "some desc", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	if len(task.ID) != 4 {
		t.Errorf("expected 4-char ID, got %q (len %d)", task.ID, len(task.ID))
	}
	if task.Title != "my task" {
		t.Errorf("expected title 'my task', got %q", task.Title)
	}
	if task.Description != "some desc" {
		t.Errorf("expected description 'some desc', got %q", task.Description)
	}
	if task.Status != "open" {
		t.Errorf("expected status 'open', got %q", task.Status)
	}
}

func TestAddTask_WithParent(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	parent, err := d.AddTask("parent", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask parent failed: %v", err)
	}

	child, err := d.AddTask("child", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child failed: %v", err)
	}

	if child.ParentID == nil {
		t.Fatal("expected parent_id to be set")
	}
	if *child.ParentID != parent.ID {
		t.Errorf("expected parent_id %q, got %q", parent.ID, *child.ParentID)
	}
}

func TestAddTask_WithAfter(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	blocker, err := d.AddTask("blocker", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask blocker failed: %v", err)
	}

	blocked, err := d.AddTask("blocked", "", "", blocker.ID, nil)
	if err != nil {
		t.Fatalf("AddTask blocked failed: %v", err)
	}

	blockers, err := d.GetBlockers(blocked.ID)
	if err != nil {
		t.Fatalf("GetBlockers failed: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(blockers))
	}
	if blockers[0].ID != blocker.ID {
		t.Errorf("expected blocker ID %q, got %q", blocker.ID, blockers[0].ID)
	}
}

func TestGetTask_NotFound(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	_, err = d.GetTask("xxxx")
	if err == nil {
		t.Fatal("expected error for non-existent task")
	}
}

func TestAddDep(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, err := d.AddTask("task A", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask A: %v", err)
	}
	b, err := d.AddTask("task B", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask B: %v", err)
	}

	if err := d.AddDep(a.ID, b.ID); err != nil {
		t.Fatalf("AddDep: %v", err)
	}

	blockers, err := d.GetBlockers(b.ID)
	if err != nil {
		t.Fatalf("GetBlockers: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(blockers))
	}
	if blockers[0].ID != a.ID {
		t.Errorf("expected blocker %q, got %q", a.ID, blockers[0].ID)
	}
}

func TestAddDep_CycleDetection(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "", nil)
	b, _ := d.AddTask("B", "", "", "", nil)
	c, _ := d.AddTask("C", "", "", "", nil)

	if err := d.AddDep(a.ID, b.ID); err != nil {
		t.Fatalf("A->B: %v", err)
	}
	if err := d.AddDep(b.ID, c.ID); err != nil {
		t.Fatalf("B->C: %v", err)
	}

	// C->A should create a cycle
	if err := d.AddDep(c.ID, a.ID); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestAddDep_SelfCycle(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "", nil)

	if err := d.AddDep(a.ID, a.ID); err == nil {
		t.Fatal("expected self-cycle error, got nil")
	}
}

func TestRemoveDep(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "", nil)
	b, _ := d.AddTask("B", "", "", "", nil)

	if err := d.AddDep(a.ID, b.ID); err != nil {
		t.Fatalf("AddDep: %v", err)
	}

	if err := d.RemoveDep(a.ID, b.ID); err != nil {
		t.Fatalf("RemoveDep: %v", err)
	}

	blockers, err := d.GetBlockers(b.ID)
	if err != nil {
		t.Fatalf("GetBlockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("expected 0 blockers after remove, got %d", len(blockers))
	}
}

func TestRemoveDep_NotFound(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	if err := d.RemoveDep("xxxx", "yyyy"); err == nil {
		t.Fatal("expected error for non-existent dep")
	}
}

func TestGetBlocking(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "", nil)
	b, _ := d.AddTask("B", "", "", "", nil)
	c, _ := d.AddTask("C", "", "", "", nil)

	// A blocks B and C
	d.AddDep(a.ID, b.ID)
	d.AddDep(a.ID, c.ID)

	blocking, err := d.GetBlocking(a.ID)
	if err != nil {
		t.Fatalf("GetBlocking: %v", err)
	}
	if len(blocking) != 2 {
		t.Fatalf("expected 2 tasks blocked by A, got %d", len(blocking))
	}
}

func TestEditTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, err := d.AddTask("original", "original desc", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	newTitle := "updated"
	updated, err := d.EditTask(task.ID, &newTitle, nil, nil)
	if err != nil {
		t.Fatalf("EditTask failed: %v", err)
	}

	if updated.Title != "updated" {
		t.Errorf("expected title 'updated', got %q", updated.Title)
	}
	if updated.Description != "original desc" {
		t.Errorf("expected description unchanged, got %q", updated.Description)
	}
}

func TestClaimTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, err := d.AddTask("claim me", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	claimed, err := d.ClaimTask(task.ID, "alice")
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed.Status != "active" {
		t.Errorf("expected status 'active', got %q", claimed.Status)
	}
	if claimed.Assignee == nil || *claimed.Assignee != "alice" {
		t.Errorf("expected assignee 'alice', got %v", claimed.Assignee)
	}
}

func TestClaimTask_AlreadyClaimed(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, _ := d.AddTask("claim me", "", "", "", nil)
	d.ClaimTask(task.ID, "alice")

	_, err = d.ClaimTask(task.ID, "bob")
	if err == nil {
		t.Fatal("expected error when claiming already-claimed task")
	}
}

func TestReleaseTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, _ := d.AddTask("release me", "", "", "", nil)
	d.ClaimTask(task.ID, "alice")

	released, err := d.ReleaseTask(task.ID)
	if err != nil {
		t.Fatalf("ReleaseTask: %v", err)
	}
	if released.Status != "open" {
		t.Errorf("expected status 'open', got %q", released.Status)
	}
	if released.Assignee != nil {
		t.Errorf("expected nil assignee, got %v", released.Assignee)
	}
}

func TestDoneTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, _ := d.AddTask("finish me", "", "", "", nil)
	done, err := d.DoneTask(task.ID)
	if err != nil {
		t.Fatalf("DoneTask: %v", err)
	}
	if done.Status != "done" {
		t.Errorf("expected status 'done', got %q", done.Status)
	}
}

func TestBlockTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, _ := d.AddTask("block me", "", "", "", nil)
	blocked, err := d.BlockTask(task.ID, "waiting on API")
	if err != nil {
		t.Fatalf("BlockTask: %v", err)
	}
	if blocked.Status != "blocked" {
		t.Errorf("expected status 'blocked', got %q", blocked.Status)
	}
	if blocked.BlockReason == nil || *blocked.BlockReason != "waiting on API" {
		t.Errorf("expected block_reason 'waiting on API', got %v", blocked.BlockReason)
	}
}

func TestReopenTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, _ := d.AddTask("reopen me", "", "", "", nil)
	d.BlockTask(task.ID, "reason")

	reopened, err := d.ReopenTask(task.ID)
	if err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	if reopened.Status != "open" {
		t.Errorf("expected status 'open', got %q", reopened.Status)
	}
	if reopened.BlockReason != nil {
		t.Errorf("expected nil block_reason, got %v", reopened.BlockReason)
	}
	if reopened.Assignee != nil {
		t.Errorf("expected nil assignee, got %v", reopened.Assignee)
	}
}

func TestStatusTransition_CreatesNote(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, _ := d.AddTask("noted", "", "", "", nil)

	// Claim creates a note
	d.ClaimTask(task.ID, "alice")

	notes, err := d.GetNotes(task.ID)
	if err != nil {
		t.Fatalf("GetNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(notes))
	}
	if notes[0].Author == nil || *notes[0].Author != "system" {
		t.Errorf("expected author 'system', got %v", notes[0].Author)
	}
	if notes[0].Content != "Status changed: open → active" {
		t.Errorf("unexpected note content: %q", notes[0].Content)
	}

	// Release creates a second note
	d.ReleaseTask(task.ID)
	notes, _ = d.GetNotes(task.ID)
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes after release, got %d", len(notes))
	}
}

func TestAddNote(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, err := d.AddTask("noted task", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	author := "human"
	note, err := d.AddNote(task.ID, "this is a note", &author)
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	if note.Content != "this is a note" {
		t.Errorf("expected content 'this is a note', got %q", note.Content)
	}
	if note.Author == nil || *note.Author != "human" {
		t.Errorf("expected author 'human', got %v", note.Author)
	}
	if note.TaskID != task.ID {
		t.Errorf("expected task_id %q, got %q", task.ID, note.TaskID)
	}

	// Verify via GetNotes
	notes, err := d.GetNotes(task.ID)
	if err != nil {
		t.Fatalf("GetNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(notes))
	}
	if notes[0].Content != "this is a note" {
		t.Errorf("expected content 'this is a note', got %q", notes[0].Content)
	}
}

func TestReadyTasks(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	// A blocks B, C is independent
	a, _ := d.AddTask("task A", "", "", "", nil)
	b, _ := d.AddTask("task B", "", "", "", nil)
	c, _ := d.AddTask("task C", "", "", "", nil)
	d.AddDep(a.ID, b.ID)

	ready, err := d.ReadyTasks()
	if err != nil {
		t.Fatalf("ReadyTasks: %v", err)
	}

	ids := make(map[string]bool)
	for _, task := range ready {
		ids[task.ID] = true
	}

	if !ids[a.ID] {
		t.Errorf("expected A (%s) to be ready", a.ID)
	}
	if ids[b.ID] {
		t.Errorf("expected B (%s) to NOT be ready (blocked by A)", b.ID)
	}
	if !ids[c.ID] {
		t.Errorf("expected C (%s) to be ready", c.ID)
	}
}

func TestReadyTasks_OrderByUnblockCount(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	// A blocks B, C, E (3 dependents) — created first
	a, _ := d.AddTask("task A", "", "", "", nil)
	b, _ := d.AddTask("task B", "", "", "", nil)
	c, _ := d.AddTask("task C", "", "", "", nil)
	// D blocks E only (1 dependent) — created after A
	dd, _ := d.AddTask("task D", "", "", "", nil)
	e, _ := d.AddTask("task E", "", "", "", nil)

	d.AddDep(a.ID, b.ID)
	d.AddDep(a.ID, c.ID)
	d.AddDep(a.ID, e.ID)
	d.AddDep(dd.ID, e.ID)

	ready, err := d.ReadyTasks()
	if err != nil {
		t.Fatalf("ReadyTasks: %v", err)
	}

	// A should come before D since A unblocks more tasks
	aIdx, dIdx := -1, -1
	for i, task := range ready {
		if task.ID == a.ID {
			aIdx = i
		}
		if task.ID == dd.ID {
			dIdx = i
		}
	}
	if aIdx == -1 {
		t.Fatal("A not found in ready tasks")
	}
	if dIdx == -1 {
		t.Fatal("D not found in ready tasks")
	}
	if aIdx > dIdx {
		t.Errorf("expected A (unblocks 3) before D (unblocks 1), got A at %d, D at %d", aIdx, dIdx)
	}
}

func TestReadyTasks_ExcludesClaimed(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("task A", "", "", "", nil)
	d.ClaimTask(a.ID, "alice")

	ready, err := d.ReadyTasks()
	if err != nil {
		t.Fatalf("ReadyTasks: %v", err)
	}

	for _, task := range ready {
		if task.ID == a.ID {
			t.Errorf("claimed task %s should not be in ready list", a.ID)
		}
	}
}

func TestReadyTasks_BlockerDoneUnblocks(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("task A", "", "", "", nil)
	b, _ := d.AddTask("task B", "", "", "", nil)
	d.AddDep(a.ID, b.ID)

	// B should not be ready initially
	ready, _ := d.ReadyTasks()
	for _, task := range ready {
		if task.ID == b.ID {
			t.Fatal("B should not be ready before A is done")
		}
	}

	// Mark A as done
	d.DoneTask(a.ID)

	// Now B should be ready
	ready, err = d.ReadyTasks()
	if err != nil {
		t.Fatalf("ReadyTasks: %v", err)
	}
	found := false
	for _, task := range ready {
		if task.ID == b.ID {
			found = true
		}
	}
	if !found {
		t.Error("B should be ready after blocker A is done")
	}
}

func TestListTasks(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("open task", "", "", "", nil)
	b, _ := d.AddTask("done task", "", "", "", nil)
	d.DoneTask(b.ID)

	// Default (no --all) excludes done
	tasks, err := d.ListTasks("", false)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	ids := make(map[string]bool)
	for _, task := range tasks {
		ids[task.ID] = true
	}
	if !ids[a.ID] {
		t.Error("expected open task in default list")
	}
	if ids[b.ID] {
		t.Error("expected done task excluded from default list")
	}

	// --all includes done
	tasks, err = d.ListTasks("", true)
	if err != nil {
		t.Fatalf("ListTasks all: %v", err)
	}
	ids = make(map[string]bool)
	for _, task := range tasks {
		ids[task.ID] = true
	}
	if !ids[a.ID] {
		t.Error("expected open task in all list")
	}
	if !ids[b.ID] {
		t.Error("expected done task in all list")
	}
}

func TestListTasks_StatusFilter(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("open task", "", "", "", nil)
	b, _ := d.AddTask("blocked task", "", "", "", nil)
	d.BlockTask(b.ID, "reason")

	tasks, err := d.ListTasks("blocked", false)
	if err != nil {
		t.Fatalf("ListTasks blocked: %v", err)
	}

	if len(tasks) != 1 {
		t.Fatalf("expected 1 blocked task, got %d", len(tasks))
	}
	if tasks[0].ID != b.ID {
		t.Errorf("expected blocked task %q, got %q", b.ID, tasks[0].ID)
	}

	// Ensure open task not included
	for _, task := range tasks {
		if task.ID == a.ID {
			t.Error("open task should not appear in blocked filter")
		}
	}
}

func TestReadyTasks_ExcludesParentTasks(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	// Create a parent task with 2 children
	parent, err := d.AddTask("parent task", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask parent: %v", err)
	}

	child1, err := d.AddTask("child 1", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child1: %v", err)
	}

	child2, err := d.AddTask("child 2", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child2: %v", err)
	}

	ready, err := d.ReadyTasks()
	if err != nil {
		t.Fatalf("ReadyTasks: %v", err)
	}

	ids := make(map[string]bool)
	for _, task := range ready {
		ids[task.ID] = true
	}

	// Parent should NOT appear — it has children
	if ids[parent.ID] {
		t.Errorf("parent task %s should NOT be in ReadyTasks (it has children)", parent.ID)
	}

	// Both children SHOULD appear — they are open, unassigned, and have no blockers
	if !ids[child1.ID] {
		t.Errorf("child1 %s should be in ReadyTasks", child1.ID)
	}
	if !ids[child2.ID] {
		t.Errorf("child2 %s should be in ReadyTasks", child2.ID)
	}
}

func TestDoneTask_AutoCompletesParent(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	parent, err := d.AddTask("parent task", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask parent: %v", err)
	}

	child1, err := d.AddTask("child 1", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child1: %v", err)
	}

	child2, err := d.AddTask("child 2", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child2: %v", err)
	}

	// Done child1 — parent should NOT be done yet (child2 still open)
	if _, err := d.DoneTask(child1.ID); err != nil {
		t.Fatalf("DoneTask child1: %v", err)
	}

	p, err := d.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("GetTask parent after child1 done: %v", err)
	}
	if p.Status == "done" {
		t.Errorf("parent should NOT be done after only child1 is done")
	}

	// Done child2 — parent SHOULD now be done (all children done)
	if _, err := d.DoneTask(child2.ID); err != nil {
		t.Fatalf("DoneTask child2: %v", err)
	}

	p, err = d.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("GetTask parent after child2 done: %v", err)
	}
	if p.Status != "done" {
		t.Errorf("parent should be done after all children are done, got %q", p.Status)
	}
}

func TestDoneTask_ParentNotCompletedWithOpenChildren(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	parent, err := d.AddTask("parent task", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask parent: %v", err)
	}

	child1, err := d.AddTask("child 1", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child1: %v", err)
	}

	_, err = d.AddTask("child 2", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child2: %v", err)
	}

	_, err = d.AddTask("child 3", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child3: %v", err)
	}

	// Done child1 — parent should NOT be done (child2 and child3 still open)
	if _, err := d.DoneTask(child1.ID); err != nil {
		t.Fatalf("DoneTask child1: %v", err)
	}

	p, err := d.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("GetTask parent: %v", err)
	}
	if p.Status == "done" {
		t.Errorf("parent should NOT be done when 2 children are still open")
	}
}

func TestGetChildren(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	parent, err := d.AddTask("parent task", "", "", "", nil)
	if err != nil {
		t.Fatalf("AddTask parent: %v", err)
	}

	child1, err := d.AddTask("child 1", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child1: %v", err)
	}

	child2, err := d.AddTask("child 2", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("AddTask child2: %v", err)
	}

	children, err := d.GetChildren(parent.ID)
	if err != nil {
		t.Fatalf("GetChildren: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	// Verify order (created_at ASC) and content
	if children[0].ID != child1.ID {
		t.Errorf("expected first child %q, got %q", child1.ID, children[0].ID)
	}
	if children[1].ID != child2.ID {
		t.Errorf("expected second child %q, got %q", child2.ID, children[1].ID)
	}

	// Task with no children should return empty slice
	noChildren, err := d.GetChildren(child1.ID)
	if err != nil {
		t.Fatalf("GetChildren for leaf: %v", err)
	}
	if len(noChildren) != 0 {
		t.Errorf("expected 0 children for leaf task, got %d", len(noChildren))
	}
}
