package mr

import (
	"encoding/json"
	"strings"
	"testing"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
)

func oneOverlap() *code.MergeRequestOverlaps {
	return &code.MergeRequestOverlaps{MergeRequests: []*code.MergeRequestOverlap{
		{MergeRequestId: "mr_2", Title: "Other change", HeadRef: "other", BaseRef: "main", Paths: []string{"a.txt"}},
	}}
}

const oneOverlapText = "Also touching these files: 1 other open merge request\n  mr_2: Other change (other into main)\n    a.txt\n"

func TestOverlapText(t *testing.T) {
	for _, test := range []struct {
		name     string
		overlaps *code.MergeRequestOverlaps
		want     string
	}{
		{name: "absent"},
		{name: "none", overlaps: &code.MergeRequestOverlaps{}},
		{name: "one", overlaps: oneOverlap(), want: oneOverlapText},
		{name: "long lists are cut short and counted", overlaps: &code.MergeRequestOverlaps{
			MergeRequests: []*code.MergeRequestOverlap{
				{MergeRequestId: "mr_2", Title: "Wide", HeadRef: "wide", BaseRef: "main", Paths: []string{"a", "b", "c", "d", "e", "f"}, OmittedPathCount: 30},
				{MergeRequestId: "mr_3", Title: "Narrow", HeadRef: "narrow", BaseRef: "wide", Paths: []string{"a", "b", "c", "d", "e"}},
				{MergeRequestId: "mr_4", Title: "Six", HeadRef: "six", BaseRef: "main", Paths: []string{"a", "b", "c", "d", "e", "f"}},
			},
			OmittedMergeRequestCount: 1, Incomplete: true,
		}, want: "Also touching these files: 4 other open merge requests\n" +
			"  mr_2: Wide (wide into main)\n    a\n    b\n    c\n    d\n    e\n    and 31 more files\n" +
			"  mr_3: Narrow (narrow into wide)\n    a\n    b\n    c\n    d\n    e\n" +
			"  mr_4: Six (six into main)\n    a\n    b\n    c\n    d\n    e\n    and 1 more file\n" +
			"  and 1 more merge request\n" +
			"Some open merge requests could not be compared, so others may also touch these files.\n"},
		{name: "omitted requests are counted when none is listed", overlaps: &code.MergeRequestOverlaps{OmittedMergeRequestCount: 3},
			want: "Also touching these files: 3 other open merge requests\n  not listed here\n"},
		{name: "incomplete with nothing found", overlaps: &code.MergeRequestOverlaps{Incomplete: true},
			want: "Some open merge requests could not be compared, so others may also touch these files.\n"},
		{name: "terminal control characters are stripped", overlaps: &code.MergeRequestOverlaps{MergeRequests: []*code.MergeRequestOverlap{
			{MergeRequestId: "mr_2", Title: "Title\x1b[2K\nReadiness: ready", HeadRef: "other\r", BaseRef: "main ", Paths: []string{"a\n  mr_9: Fake"}},
		}}, want: "Also touching these files: 1 other open merge request\n  mr_2: Title [2K Readiness: ready (other  into main )\n    a   mr_9: Fake\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := overlapText(test.overlaps); got != test.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

// jsonOverlapID reads the first overlapping merge request's id out of --json output.
func jsonOverlapID(t *testing.T, out string) string {
	t.Helper()
	var got struct {
		Overlaps struct {
			MergeRequests []struct{ MergeRequestID string }
		}
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(got.Overlaps.MergeRequests) != 1 {
		return ""
	}
	return got.Overlaps.MergeRequests[0].MergeRequestID
}

func TestCreatePrintsOverlapsWithAndWithoutBase(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	overlaps := oneOverlap()
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		return &code.CreateMergeRequestResponse{MergeRequest: fixture(head), Overlaps: overlaps}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestWithBaseProcedure, func(*code.CreateMergeRequestWithBaseRequest) (*code.CreateMergeRequestWithBaseResponse, error) {
		return &code.CreateMergeRequestWithBaseResponse{MergeRequest: fixture(head), Overlaps: overlaps}, nil
	})
	summary := "mr_1: Test change\nState: open\nHead: feature (" + head + ")\nBase: main\n\nDescription\n"
	for _, args := range [][]string{
		{"create", "--title", "Test change", "--body", "Description"},
		{"create", "--title", "Test change", "--body", "Description", "--base", "main"},
	} {
		overlaps = oneOverlap()
		out, err := execute(t, args...)
		if want := summary + "\n" + oneOverlapText; err != nil || out != want {
			t.Errorf("%v: got %q %v, want %q", args, out, err, want)
		}
		out, err = execute(t, append(args, "--json")...)
		if err != nil || jsonOverlapID(t, out) != "mr_2" {
			t.Errorf("%v --json: %q %v", args, out, err)
		}
		// Without an overlap the output is the merge request alone, as before.
		overlaps = nil
		out, err = execute(t, args...)
		if err != nil || out != summary {
			t.Errorf("%v without overlaps: got %q %v, want %q", args, out, err, summary)
		}
		out, err = execute(t, append(args, "--json")...)
		if err != nil || strings.Contains(out, "overlaps") {
			t.Errorf("%v --json without overlaps: %q %v", args, out, err)
		}
	}
}

func TestViewAsksForAndPrintsOverlaps(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if !req.IncludeOverlaps {
			t.Errorf("view did not ask for overlaps: %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head), Overlaps: oneOverlap()}, nil
	})
	want := "mr_1: Test change\nState: open\nHead: feature (" + head + ")\nBase: main\n" + oneOverlapText + "\nDescription\n"
	for _, args := range [][]string{{"view"}, {"view", "--id", "mr_1"}} {
		out, err := execute(t, args...)
		if err != nil || out != want {
			t.Errorf("%v: got %q %v, want %q", args, out, err, want)
		}
		out, err = execute(t, append(args, "--json")...)
		if err != nil || jsonOverlapID(t, out) != "mr_2" {
			t.Errorf("%v --json: %q %v", args, out, err)
		}
	}
}

func TestOnlyViewAsksForOverlaps(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.IncludeOverlaps {
			t.Errorf("a lookup that prints no overlaps asked for them: %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(*code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		return &code.UpdateMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	if _, err := execute(t, "edit", "--title", "Renamed"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAttachingToExistingMRPrintsOverlaps(t *testing.T) {
	head := checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	existing := fixture(head)
	asked := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if !req.IncludeOverlaps {
			return &code.GetMergeRequestResponse{MergeRequest: existing}, nil
		}
		asked++
		return &code.GetMergeRequestResponse{MergeRequest: existing, Overlaps: oneOverlap()}, nil
	})
	newAssetAPI(t, mux)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		existing.Body = req.GetBody()
		return &code.UpdateMergeRequestResponse{MergeRequest: existing}, nil
	})
	args := []string{"create", "--title", "Ignored", "--attach", "asset.png"}
	// The first run adds the attachment; the second finds it already there. Both print the overlaps.
	for run := 1; run <= 2; run++ {
		out, err := execute(t, args...)
		if err != nil || asked != 2*run-1 || !strings.HasSuffix(out, "\n\n"+oneOverlapText) {
			t.Errorf("run %d: asked=%d out=%q err=%v", run, asked, out, err)
		}
		out, err = execute(t, append(args, "--json")...)
		if err != nil || asked != 2*run || jsonOverlapID(t, out[strings.Index(out, "{"):]) != "mr_2" {
			t.Errorf("run %d --json: asked=%d out=%q err=%v", run, asked, out, err)
		}
	}
}
