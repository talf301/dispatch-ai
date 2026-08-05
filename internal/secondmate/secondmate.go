// Package secondmate investigates blocked tasks and performs only the one
// explicitly approved mechanical recovery.
package secondmate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/db"
)

const (
	ClassificationAuto                     = "auto-unblockable"
	ClassificationNotAutoUnblockable       = "not-auto-unblockable"
	ClassificationInvestigatableNotFixable = "investigatable-not-auto-fixable"
	ClassificationOptions                  = "investigate-options"
	ActionReopen                           = "reopen"
	ActionInvestigate                      = "investigate"
	ActionPresentOptions                   = "present-options"
	ActionSkipRetry                        = "skip-retry"
)

type Result struct {
	TaskID         string `json:"task_id"`
	Classification string `json:"classification"`
	Action         string `json:"action"`
	Outcome        string `json:"outcome"`
}

// Notifier is intentionally narrower than manager.Manager: secondmate can be
// tested without herdr and cannot acquire unrelated lifecycle powers.
type Notifier func(taskID, message string) error

type Investigator struct {
	DB     *db.DB
	Notify Notifier
}

var emptyPR = regexp.MustCompile(`(?i)gh pr create: exit status 1[\s\S]*no commits between`)

func (i *Investigator) Run() ([]Result, error) {
	tasks, err := i.DB.ListTasks("blocked", true)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(tasks))
	var firstErr error
	for _, task := range tasks {
		result, err := i.investigate(task)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			results = append(results, Result{TaskID: task.ID, Action: ActionInvestigate, Outcome: err.Error()})
			continue
		}
		results = append(results, result)
	}
	return results, firstErr
}

func (i *Investigator) investigate(task db.Task) (Result, error) {
	reason := ""
	if task.BlockReason != nil {
		reason = *task.BlockReason
	}
	notes, err := i.DB.GetNotes(task.ID)
	if err != nil {
		return Result{}, err
	}
	key := retryKey(task.ID, reason)
	metaKey := "secondmate.retry." + task.ID
	previous, exists, err := i.DB.GetMeta(metaKey)
	if err != nil {
		return Result{}, err
	}
	count := 0
	if exists {
		parts := strings.SplitN(previous, ":", 2)
		if len(parts) == 2 {
			_, _ = fmt.Sscanf(parts[1], "%d", &count)
		}
		oldKey := ""
		if len(parts) > 0 {
			oldKey = parts[0]
		}
		if oldKey != key {
			count = 0
		}
	}
	if count >= 2 {
		return i.record(task, reason, ClassificationOptions, ActionSkipRetry,
			"same block condition already retried once", key, count)
	}
	count++
	if err := i.DB.SetMeta(metaKey, fmt.Sprintf("%s:%d", key, count)); err != nil {
		return Result{}, err
	}

	classification := ClassificationOptions
	action := ActionInvestigate
	outcome := fmt.Sprintf("reviewed block reason, %d task notes, and current task state", len(notes))
	kind, err := i.DB.BlockKind(task.ID)
	if err != nil {
		return Result{}, err
	}
	if kind == db.BlockKindPRCreateFailed && emptyPR.MatchString(reason) {
		classification = ClassificationAuto
		action = ActionReopen
		if _, err := i.DB.ReopenTask(task.ID); err != nil {
			outcome = "reopen failed: " + err.Error()
		} else {
			outcome = "reopened after confirmed empty-diff PR creation failure"
		}
	} else {
		classification = classificationFor(kind, reason)
		action = ActionPresentOptions
		outcome = optionsFor(kind, classification)
		if i.Notify != nil {
			message := fmt.Sprintf("Secondmate investigated blocked task %s (%s). %s Reason: %s", task.ID, task.Title, outcome, bounded(reason, 500))
			if err := i.Notify(task.ID, message); err != nil {
				outcome += "; manager notification failed: " + err.Error()
			}
		}
	}
	return i.record(task, reason, classification, action, outcome, key, count)
}

func classificationFor(kind, reason string) string {
	switch kind {
	case db.BlockKindMergeConflict:
		return ClassificationNotAutoUnblockable
	case db.BlockKindPRCreateFailed:
		return ClassificationOptions
	case "":
		// Legacy/manual blocks predate BlockKind. Keep only the recorded
		// prerequisite wording as a compatibility fixture; novel prose is not
		// trusted as a lifecycle signal.
		if strings.Contains(strings.ToLower(reason), "required prerequisite is unavailable in this environment") {
			return ClassificationInvestigatableNotFixable
		}
	}
	return ClassificationOptions
}

func optionsFor(kind, classification string) string {
	switch kind {
	case db.BlockKindMergeConflict:
		return "Options: create a new task that merges the conflicting sibling tip and resolves both sides, or leave this task blocked; reopening it will likely hit the same conflict."
	case db.BlockKindPRCreateFailed:
		return "Options: check whether the branch's work already landed via another PR and whether the base is stale, then reopen if appropriate, or leave it blocked."
	case "":
		if classification == ClassificationInvestigatableNotFixable {
			return "Options: make the required prerequisite available and verify it, then reopen once; create a follow-up task to add it, or leave this task blocked."
		}
	}
	return "Options: inspect the blocker and choose whether to fix and reopen, create a follow-up task, or leave it blocked."
}

func (i *Investigator) record(task db.Task, reason, classification, action, outcome, key string, count int) (Result, error) {
	if err := i.DB.AddSecondmateInvestigation(db.SecondmateInvestigation{
		TaskID: task.ID, BlockReason: reason, Classification: classification,
		Action: action, Outcome: outcome, RetryKey: key, RetryCount: count,
	}); err != nil {
		return Result{}, err
	}
	return Result{TaskID: task.ID, Classification: classification, Action: action, Outcome: outcome}, nil
}

func retryKey(taskID, reason string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + reason))
	return hex.EncodeToString(sum[:])
}

func bounded(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
