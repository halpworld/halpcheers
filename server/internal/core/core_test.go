package core_test

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
)

func TestDayConversions(t *testing.T) {
	// Epoch 1970-01-01 00:00:00 UTC should be Day 0
	epochTime := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
	day0 := core.TimeToDay(epochTime)
	if day0 != 0 {
		t.Fatalf("expected epoch to be Day 0, got %d", day0)
	}
	if day0.Int() != 0 {
		t.Fatalf("expected Day(0).Int() == 0, got %d", day0.Int())
	}
	if !day0.Time().Equal(epochTime) {
		t.Fatalf("expected day0.Time() to match epochTime, got %v", day0.Time())
	}

	// 2026-09-21
	date := time.Date(2026, time.September, 21, 15, 30, 0, 0, time.UTC)
	d := core.TimeToDay(date)
	expectedDays := int32(date.Unix() / 86400)
	if d != core.Day(expectedDays) {
		t.Fatalf("expected %d, got %d", expectedDays, d)
	}

	// Roundtrip through Int and DayFromInt
	dInt := d.Int()
	if core.DayFromInt(dInt) != d {
		t.Fatalf("roundtrip failed: expected %d, got %d", d, core.DayFromInt(dInt))
	}

	// Math helpers
	nextDay := d.AddDays(1)
	if nextDay.Sub(d) != 1 {
		t.Fatalf("expected nextDay - d == 1, got %d", nextDay.Sub(d))
	}
	if d.Sub(nextDay) != -1 {
		t.Fatalf("expected d - nextDay == -1, got %d", d.Sub(nextDay))
	}

	// Today
	today := core.Today()
	if today <= 0 {
		t.Fatalf("expected Today() > 0, got %d", today)
	}
}

func TestRedactingStringers(t *testing.T) {
	// Invariant 9: Verify fmt.Sprintf does NOT leak identifiers
	h := core.Handle("e7k4p2m9qx3v")
	formattedHandle := fmt.Sprintf("target=%s", h)
	if formattedHandle != "target=[REDACTED_HANDLE]" {
		t.Fatalf("expected redacting stringer, got %s", formattedHandle)
	}
	if h.Raw() != "e7k4p2m9qx3v" {
		t.Fatalf("expected raw handle 'e7k4p2m9qx3v', got %s", h.Raw())
	}

	a := core.Alias("@kenth")
	formattedAlias := fmt.Sprintf("alias=%s", a)
	if formattedAlias != "alias=[REDACTED_ALIAS]" {
		t.Fatalf("expected redacting stringer, got %s", formattedAlias)
	}
	if a.Raw() != "@kenth" {
		t.Fatalf("expected raw alias '@kenth', got %s", a.Raw())
	}

	id := core.AccountID(12345)
	formattedID := fmt.Sprintf("account=%s", id)
	if formattedID != "account=[REDACTED_ACCOUNT]" {
		t.Fatalf("expected redacting stringer, got %s", formattedID)
	}
	if id.Int64() != 12345 {
		t.Fatalf("expected raw id 12345, got %d", id.Int64())
	}

	// Verify JSON marshaling produces the actual wire values
	handleJSON, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal handle: %v", err)
	}
	if string(handleJSON) != `"e7k4p2m9qx3v"` {
		t.Fatalf("expected raw JSON handle, got %s", string(handleJSON))
	}

	var unmarshaledHandle core.Handle
	if err := json.Unmarshal(handleJSON, &unmarshaledHandle); err != nil {
		t.Fatalf("unmarshal handle: %v", err)
	}
	if unmarshaledHandle.Raw() != "e7k4p2m9qx3v" {
		t.Fatalf("expected unmarshaled handle 'e7k4p2m9qx3v', got %s", unmarshaledHandle.Raw())
	}

	idJSON, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("marshal id: %v", err)
	}
	if string(idJSON) != `12345` {
		t.Fatalf("expected raw JSON id 12345, got %s", string(idJSON))
	}
}

func TestHandleKindValidation(t *testing.T) {
	validKinds := []core.HandleKind{
		core.HandleKindPersonal,
		core.HandleKindSocial,
		core.HandleKindStream,
		core.HandleKindGroup,
	}
	for _, k := range validKinds {
		if !k.IsValid() {
			t.Errorf("expected %s to be valid", k)
		}
	}
	if core.HandleKind("invalid").IsValid() {
		t.Errorf("expected invalid kind to be invalid")
	}
}

func TestPackageImportsOnlyStdlib(t *testing.T) {
	// Inspect all .go files in core package (excluding tests) to verify zero external imports
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}

	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read file %s: %v", f, err)
		}
		node, err := parser.ParseFile(fset, f, data, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse file %s: %v", f, err)
		}
		for _, imp := range node.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, ".") {
				t.Errorf("file %s imports non-stdlib package: %s", f, path)
			}
		}
	}
}
