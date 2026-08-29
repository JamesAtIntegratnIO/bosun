package llm

import "context"

// Fake is a Provider that answers with whatever it was given.
//
// Not in a _test.go file because the caller under test is package main: driving
// the triage workflow needs a model seam that returns a chosen verdict, or a
// chosen failure, without a backend behind it.
type Fake struct {
	// Verdict is returned as-is, unvalidated. A test asserting what the agent
	// does with a badly calibrated verdict has to be able to supply one.
	Verdict *Verdict
	// Err stands in for a model that is down, slow or misconfigured; a case
	// the agent must not let look like a verdict.
	Err error
	// ID is the name reported in logs and PR comments.
	ID string

	// System and User record the last prompt, so a test can assert on the
	// evidence the model was shown, the same string the applier corroborates
	// version-shaped values against.
	System string
	User   string
	Calls  int

	// Migration is what Restructure returns. A test asserting what the harness
	// does with a badly shaped document has to be able to supply one, and that
	// is most of what the structural path's tests are: crafted proposals aimed
	// at each validator in turn.
	Migration *Migration
	// Migrations, when non-empty, is returned one per call, so a test can
	// exercise a pass over several documents.
	Migrations []*Migration
	// MigrationErr stands in for a model that is down on this path only.
	MigrationErr error

	MigrationCalls  int
	MigrationPrompt string
}

func (f *Fake) Restructure(_ context.Context, system, user string) (*Migration, error) {
	f.MigrationCalls++
	f.System, f.MigrationPrompt = system, user
	if f.MigrationErr != nil {
		return nil, f.MigrationErr
	}
	if len(f.Migrations) > 0 {
		m := f.Migrations[0]
		f.Migrations = f.Migrations[1:]
		return m, nil
	}
	return f.Migration, nil
}

func (f *Fake) Classify(_ context.Context, system, user string) (*Verdict, error) {
	f.Calls++
	f.System, f.User = system, user
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Verdict, nil
}

func (f *Fake) Name() string {
	if f.ID == "" {
		return "fake/model"
	}
	return f.ID
}
