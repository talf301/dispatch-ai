package daemon

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/llm"
)

type reviewPR struct {
	URL string `json:"url"`
}

type reviewPRDetails struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	HeadRefOID string `json:"headRefOid"`
	Files      []struct {
		Path string `json:"path"`
	} `json:"files"`
}

func (d *Daemon) scanPRReviews() {
	if d.manager == nil {
		return
	}
	out, err := exec.Command("gh", "search", "prs", "--review-requested=@me", "--state=open", "--limit", "1000", "--json", "url").Output()
	if err != nil {
		d.logger.Printf("secondmate: search review requests: %v", err)
		return
	}
	var prs []reviewPR
	if err := json.Unmarshal(out, &prs); err != nil {
		d.logger.Printf("secondmate: parse review requests: %v", err)
		return
	}
	for _, pr := range prs {
		if err := d.reportPR(pr); err != nil {
			d.logger.Printf("secondmate: %s: %v", pr.URL, err)
		}
	}
}

func (d *Daemon) reportPR(pr reviewPR) error {
	if pr.URL == "" {
		return fmt.Errorf("search result has no URL")
	}
	detailsOut, err := exec.Command("gh", "pr", "view", pr.URL, "--json", "title,body,headRefOid,files").Output()
	if err != nil {
		return fmt.Errorf("view PR: %w", err)
	}
	var details reviewPRDetails
	if err := json.Unmarshal(detailsOut, &details); err != nil {
		return fmt.Errorf("parse PR details: %w", err)
	}
	if details.HeadRefOID == "" {
		return fmt.Errorf("PR has no head commit")
	}
	key := "secondmate.pr." + pr.URL
	seen, _, err := d.db.GetMeta(key)
	if err != nil {
		return err
	}
	if seen == details.HeadRefOID {
		return nil
	}
	diff, err := exec.Command("gh", "pr", "diff", pr.URL).Output()
	if err != nil {
		return fmt.Errorf("read diff: %w", err)
	}
	if len(diff) > 120000 {
		diff = diff[:120000] // keep the one-shot prompt bounded
	}
	report, err := llm.Oneshot(fmt.Sprintf(`Review this GitHub pull request and write a concise, substantive digest for the human reviewer.
Include: what the PR does, the important files or behavior it changes, and where to look first for risk. Do not merely restate the title or say to review the PR.

PR: %s
Title: %s
Description:
%s
Changed files:
%s
Diff:
%s`, pr.URL, details.Title, details.Body, fileList(details.Files), diff))
	if err != nil {
		return err
	}
	message := "PR review requested: " + details.Title + "\n" + pr.URL + "\n\n" + strings.TrimSpace(report)
	if err := d.manager.NotifyMessage(key+"."+details.HeadRefOID, message); err != nil {
		return err
	}
	return d.db.SetMeta(key, details.HeadRefOID)
}

func fileList(files []struct {
	Path string `json:"path"`
}) string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return strings.Join(paths, "\n")
}
