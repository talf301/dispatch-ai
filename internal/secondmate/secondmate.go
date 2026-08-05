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
	for _, task := range tasks {
		result, err := i.investigate(task)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
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
	if emptyPR.MatchString(reason) {
		classification = ClassificationAuto
		action = ActionReopen
		if _, err := i.DB.ReopenTask(task.ID); err != nil {
			outcome = "reopen failed: " + err.Error()
		} else {
			outcome = "reopened after confirmed empty-diff PR creation failure"
		}
	} else {
		classification = classificationFor(reason)
		action = ActionPresentOptions
		outcome = "no deterministic fix found; human must choose the next action"
		if i.Notify != nil {
			message := fmt.Sprintf("Secondmate investigated blocked task %s (%s). Options: inspect/fix the blocker and reopen, create a follow-up task that resolves it, or leave it blocked. Reason: %s", task.ID, task.Title, bounded(reason, 500))
			if err := i.Notify(task.ID, message); err != nil {
				outcome += "; manager notification failed: " + err.Error()
			}
		}
	}
	return i.record(task, reason, classification, action, outcome, key, count)
}

func classificationFor(reason string) string {
	lower := strings.ToLower(reason)
	if strings.Contains(lower, "merge conflict") || strings.Contains(lower, "merge conflict merging") {
		return ClassificationNotAutoUnblockable
	}
	if strings.Contains(lower, "missing prerequisite") || strings.Contains(lower, "prerequisite") || strings.Contains(lower, "not installed") {
		return ClassificationInvestigatableNotFixable
	}
	return ClassificationOptions
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
