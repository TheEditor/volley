package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/TheEditor/volley/internal/engine"
)

func finalMessage(data map[string]any) string {
	final, ok := data["final_result"].(map[string]any)
	if !ok || data["status"] != "approved" {
		return ""
	}
	workspace, _ := data["workspace"].(string)
	link := func(path string) string {
		return fmt.Sprintf("[%s](<%s>)", path, filepath.Join(workspace, path))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Approved: %s\nSHA-256: %v\n%v\n", link("SPEC.md"), data["spec_hash"], final["meaning"])
	if advisory, ok := final["advisory"].(map[string]any); ok {
		fmt.Fprintf(&out, "\nAdvisory review: %s\nReviewed SHA-256: %v\n", link(fmt.Sprint(advisory["path"])), advisory["reviewed_spec_hash"])
	}
	if final["closing_result"] == "rejected_at_cap" {
		fmt.Fprintf(&out, "\nRejected closing changes: %s\n", link("rounds/closing-rejected.spec.md"))
		fmt.Fprintln(&out, "These critiques reviewed the rejected changes. They did not review the restored final artifact. Their objections are not recorded as resolved.")
		b, _ := json.Marshal(final["rejected_review_evidence"])
		var reviews []engine.RejectedReview
		if json.Unmarshal(b, &reviews) == nil {
			for _, review := range reviews {
				fmt.Fprintf(&out, "\nRejected-change critique: %s\nReviewed SHA-256: %s\nCritique SHA-256: %s\n", link(review.Path), review.SpecHash, review.Hash)
			}
		}
	}
	if pending, ok := data["pending_inputs"]; ok {
		fmt.Fprintf(&out, "\nPending input is outside this approval: %v\n", pending)
	}
	return strings.TrimSpace(out.String())
}
