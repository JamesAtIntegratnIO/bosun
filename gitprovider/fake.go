package gitprovider

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Fake is an in-memory Provider.
//
// It lives outside _test.go because the workflow it exists to exercise lives in
// package main: the triage tests need a git host they can set up and assert on,
// and this is the only way to give them one without a token and a network.
//
// One triage run at a time; nothing here is synchronised, because the
// workflow it stands in for is sequential and a mutex would force every
// assertion through an accessor.
type Fake struct {
	// PR is what GetPullRequest returns. Nil makes the read fail, which is the
	// only way this provider can fail that way.
	PR *PullRequest
	// OpenPRs is what ListOpenPullRequests returns. Independent of PR so a
	// gate test can serve a list while a triage test keeps serving one.
	OpenPRs []PullRequest
	ListErr error
	// Comments is the pull request's history, oldest first.
	Comments []Comment
	Check    CheckState
	CheckErr error
	// CheckCalls counts CheckStatus calls, so a test can assert whether the
	// status on the commit was read at all.
	CheckCalls int
	PushErr    error

	// Statuses are the commit statuses the run set, oldest first.
	Statuses  []Status
	StatusErr error

	// Posted, Labelled and Pushes are what the run did. Everything the agent is
	// allowed to change about a pull request lands in one of the three, so a
	// test asserting all three has covered the whole write surface.
	Posted []string
	// Updated are bodies written by UpdateComment, oldest first. A gate that
	// re-ran on a repaired pull request should leave one report, so a test
	// asserting "two runs, one comment" reads len(Posted)==1 && len(Updated)==1.
	Updated []string
	// CommentAuthor is the account the fake records as having written a
	// comment. Defaults to something that is plainly not the provider name.
	CommentAuthor string
	// UpdateErr makes every UpdateComment fail, which is how a test reaches
	// the path where the host refuses an edit and the gate must still publish.
	UpdateErr error
	nextID    int64
	Labelled  []string
	Pushes    []Push
}

// Push is one recorded PushFix, including the working tree as it stood. The
// tree is snapshotted rather than committed: what matters to a test is which
// bytes the agent was about to publish, not that git could store them.
type Push struct {
	Branch  string
	Message string
	Tree    map[string]string
	// SHA is the commit this push created and left as the branch head.
	SHA string
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) GetPullRequest(_ context.Context, number int) (*PullRequest, error) {
	if f.PR == nil {
		return nil, fmt.Errorf("no pull request %d", number)
	}
	// A copy, so a label added during the run does not appear retroactively on
	// the pull request the run is already holding.
	out := *f.PR
	out.Labels = append([]string(nil), f.PR.Labels...)
	return &out, nil
}

func (f *Fake) ListOpenPullRequests(_ context.Context) ([]PullRequest, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return append([]PullRequest(nil), f.OpenPRs...), nil
}

func (f *Fake) ListComments(_ context.Context, _ int) ([]Comment, error) {
	return append([]Comment(nil), f.Comments...), nil
}

func (f *Fake) CheckStatus(_ context.Context, _, _ string) (CheckState, error) {
	f.CheckCalls++
	if f.CheckErr != nil {
		return CheckMissing, f.CheckErr
	}
	if f.Check == "" {
		return CheckMissing, nil
	}
	return f.Check, nil
}

func (f *Fake) Comment(_ context.Context, _ int, body string) error {
	f.Posted = append(f.Posted, body)
	if f.PR != nil {
		f.nextID++
		// Deliberately not Name(): that is the provider ("fake"), and a real
		// host records the account. Conflating them let a bug ship where the
		// agent looked for its own comment by author and never found it,
		// because the fake had agreed with the mistake.
		author := f.CommentAuthor
		if author == "" {
			author = "agent[bot]"
		}
		f.Comments = append(f.Comments, Comment{ID: f.nextID, Author: author, Body: body})
	}
	return nil
}

// UpdateComment rewrites a comment in place and records that it did. Tests
// assert on Updated rather than Posted when the point is that one report
// exists however many times the gate ran.
func (f *Fake) UpdateComment(_ context.Context, id int64, body string) error {
	if f.UpdateErr != nil {
		return f.UpdateErr
	}
	for i := range f.Comments {
		if f.Comments[i].ID == id {
			f.Comments[i].Body = body
			f.Updated = append(f.Updated, body)
			return nil
		}
	}
	return fmt.Errorf("fake: no comment with id %d", id)
}

// Statuses records every commit status set, in order, so a test can assert
// both the final verdict and that a pending one preceded it.
func (f *Fake) SetCommitStatus(_ context.Context, sha, name string, state CommitState, description string) error {
	if f.StatusErr != nil {
		return f.StatusErr
	}
	f.Statuses = append(f.Statuses, Status{SHA: sha, Name: name, State: state, Description: description})
	return nil
}

func (f *Fake) AddLabel(_ context.Context, _ int, label string) error {
	f.Labelled = append(f.Labelled, label)
	if f.PR != nil {
		f.PR.Labels = append(f.PR.Labels, label)
	}
	return nil
}

func (f *Fake) PushFix(_ context.Context, pr *PullRequest, root, message string) error {
	if f.PushErr != nil {
		return f.PushErr
	}
	if pr.Branch == "" {
		return fmt.Errorf("pull request has no head branch")
	}
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		return err
	}
	// The real providers advance this to the commit they just pushed; a fake
	// that does not lets a test pass while production writes its verdict to a
	// superseded commit. Deterministic so assertions can name it.
	sha := fmt.Sprintf("fakepush%d", len(f.Pushes)+1)
	f.Pushes = append(f.Pushes, Push{Branch: pr.Branch, Message: message, Tree: tree, SHA: sha})
	pr.HeadSHA = sha
	return nil
}

// Status is one commit status the fake recorded.
type Status struct {
	// SHA is the commit the status was written to. Recorded because it was
	// not: a verdict landing on a superseded commit is invisible to a test
	// that only asserts on state and description, and that is exactly how the
	// migration path shipped a status the head never carried.
	SHA         string
	Name        string
	State       CommitState
	Description string
}
