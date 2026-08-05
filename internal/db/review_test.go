package db

import "testing"

func TestReviewDigestRoundTrip(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	want := ReviewDigest{
		ScannedAt: "2026-08-05T00:00:00Z",
		Findings: []ReviewFinding{{
			ID: "orphan_branch:/repo/dispatch/stray1", Kind: "orphan_branch",
			Subject: "/repo/dispatch/stray1", Detail: "branch has no task",
		}},
	}
	if err := d.SaveReviewDigest(want); err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadReviewDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ScannedAt != want.ScannedAt || len(got.Findings) != 1 || got.Findings[0] != want.Findings[0] {
		t.Fatalf("loaded digest = %#v, want %#v", got, want)
	}
}
