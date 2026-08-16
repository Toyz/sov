package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadBuildInfo(t *testing.T) {
	bi := readBuildInfo()
	if bi == nil {
		t.Fatal("build info should be available in a test binary")
	}
	if bi.GoVersion == "" {
		t.Fatal("GoVersion should be populated")
	}
}

func TestReport_BuildInfoMarshals(t *testing.T) {
	rpt := Report{Build: &BuildInfo{GoVersion: "go1.x", Revision: "abc123", Modified: true}}
	b, _ := json.Marshal(rpt)
	s := string(b)
	if !strings.Contains(s, `"build"`) || !strings.Contains(s, "abc123") {
		t.Fatalf("build info missing from manifest JSON: %s", s)
	}
	// Absent when nil (omitempty).
	b2, _ := json.Marshal(Report{})
	if strings.Contains(string(b2), `"build"`) {
		t.Fatalf("build should be omitted when nil: %s", b2)
	}
}
