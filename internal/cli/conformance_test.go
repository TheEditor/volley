package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/TheEditor/volley/internal/contract"
	"strings"
	"testing"
)

func TestGeneratedContractReport(t *testing.T) {
	r, _ := contract.Load()
	data, err := generatedConformance(context.Background(), r)
	b, _ := json.Marshal(data)
	var d struct {
		Results []struct{ ID, Verdict, Reason string }
		Counts  map[string]int
	}
	json.Unmarshal(b, &d)
	for _, v := range d.Results {
		if v.Verdict == "fail" {
			t.Error(v.ID, v.Reason)
		}
	}
	t.Log("profile counts", d.Counts)
	t.Log("CONFORMANCE_EVIDENCE", string(b))
	if err != nil {
		t.Fatal(err)
	}
	if e := contract.Validate("data-conformance.json", data); e != nil {
		t.Fatal(e)
	}
}

func TestAdjacentDiagnosisFaults(t *testing.T) {
	if !fullCI() {
		return
	}
	r, _ := contract.Load()
	for i := 0; i+1 < len(r.DiagnosisOrder); i++ {
		a, b := r.DiagnosisOrder[i], r.DiagnosisOrder[i+1]
		t.Run(a+"-before-"+b, func(t *testing.T) {
			var out, stderr bytes.Buffer
			reached := ""
			o := fixed()
			o.Stage = func(stage string) {
				if stage == a || stage == b {
					reached = stage
					panic("owned unexpected fault")
				}
			}
			exit := Execute(context.Background(), []string{"--json", "--help"}, &out, &stderr, o)
			var value contract.Result
			if e := json.Unmarshal(out.Bytes(), &value); e != nil {
				t.Fatal(e)
			}
			if reached != a || exit != 6 || value.OK || len(value.Errors) != 1 || value.Errors[0].Code != "INTERNAL" || !strings.Contains(stderr.String(), value.Errors[0].Message) {
				t.Fatal(reached, exit, value)
			}
			if e := contract.Validate("envelope.json", value); e != nil {
				t.Fatal(e)
			}
		})
	}
}
