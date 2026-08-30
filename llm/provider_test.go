package llm

import (
	"strings"
	"testing"
)

// Validate is the last thing between a model's answer and the applier, and it
// had no offline coverage at all.
func TestValidateRejectsWhatCannotBeActedOn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		v       Verdict
		wantErr string
	}{
		{
			name:    "mechanical with no edits has nothing to apply",
			v:       Verdict{Classification: ClassMechanical, Summary: "s"},
			wantErr: "nothing to apply",
		},
		{
			name:    "escalate with neither reason nor reasoning says nothing",
			v:       Verdict{Classification: ClassEscalate, Summary: "s"},
			wantErr: "neither escalationReason nor reasoning",
		},
		{
			name:    "an unknown classification is not a verdict",
			v:       Verdict{Classification: "maybe", Summary: "s"},
			wantErr: "unknown classification",
		},
		{
			name:    "no summary means nothing to publish",
			v:       Verdict{Classification: ClassNoAction},
			wantErr: "no summary",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.v.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// The one repair Validate performs, and the reason it is a verb: failing a
// correct verdict over a soft field would throw the verdict away.
func TestValidateRecoversAnEmptyEscalationReason(t *testing.T) {
	v := Verdict{Classification: ClassEscalate, Summary: "s", Reasoning: "the CRD schema moved"}
	if err := v.Validate(); err != nil {
		t.Fatalf("a recoverable verdict must not be rejected: %v", err)
	}
	if v.EscalationReason != "the CRD schema moved" {
		t.Errorf("the reasoning must be copied into the empty reason, got %q", v.EscalationReason)
	}
}

func TestValidateAcceptsTheThreeGoodShapes(t *testing.T) {
	for _, v := range []Verdict{
		{Classification: ClassMechanical, Summary: "s", Edits: []Edit{{Path: "a", Key: "k", To: "v"}}},
		{Classification: ClassEscalate, Summary: "s", EscalationReason: "r"},
		{Classification: ClassNoAction, Summary: "s"},
	} {
		if err := v.Validate(); err != nil {
			t.Errorf("%s must be valid: %v", v.Classification, err)
		}
	}
}

// The schemas are what constrain the model, and what they must agree with is
// checked in contract_test.go rather than here.
//
// TestTheVerdictSchemaMatchesTheVerdictStruct and its Migration counterpart
// derive both sides -- the schema's properties and the struct's json tags --
// and also check `required` and additionalProperties. The hand-written list
// that used to be here named four of the Verdict's five properties: it omitted
// escalationReason, which is the one VerdictSchema's own comment calls out as
// the field a model will drop if it is not required. A hand-written list
// failing to cover a hand-written list is the whole argument for deriving both.
