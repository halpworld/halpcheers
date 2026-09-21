package obs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// InvariantViolation represents a detected leak of an identifier into a logger or formatter.
type InvariantViolation struct {
	Filename string
	Line     int
	Message  string
}

func (v InvariantViolation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.Filename, v.Line, v.Message)
}

// CheckInvariant9 inspects Go source files under rootDir for Invariant 9 violations.
//
// Invariant 9 (AGENTS.md):
// "No identifiers in logs or metrics. No handle, account ID, alias, IP, endpoint URL
// or user agent in a log line, a metric label, or an error surfaced to a client."
func CheckInvariant9(rootDir string) ([]InvariantViolation, error) {
	var violations []InvariantViolation
	fset := token.NewFileSet()

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden dirs or test fixtures
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.Contains(path, "testdata") {
			return nil
		}

		v, err := checkFile(fset, path)
		if err != nil {
			return err
		}
		violations = append(violations, v...)
		return nil
	})

	return violations, err
}

// CheckSource checks a raw Go source string for Invariant 9 violations (used for unit tests and fixtures).
func CheckSource(filename string, src string) ([]InvariantViolation, error) {
	fset := token.NewFileSet()
	fileNode, err := parser.ParseFile(fset, filename, src, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parse source: %w", err)
	}
	return inspectAST(fset, filename, fileNode), nil
}

func checkFile(fset *token.FileSet, path string) ([]InvariantViolation, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fileNode, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parse file %s: %w", path, err)
	}
	return inspectAST(fset, path, fileNode), nil
}

func inspectAST(fset *token.FileSet, filename string, fileNode *ast.File) []InvariantViolation {
	var violations []InvariantViolation

	ast.Inspect(fileNode, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		callName := getCallExpressionName(call)
		if isLoggingOrFormattingCall(callName) {
			for _, arg := range call.Args {
				if leakedIdent, found := detectLeakedIdentifier(arg); found {
					pos := fset.Position(arg.Pos())
					violations = append(violations, InvariantViolation{
						Filename: filename,
						Line:     pos.Line,
						Message: fmt.Sprintf(
							"INVARIANT 9 VIOLATION: '%s' passed to logging/formatting call '%s'. "+
								"AGENTS.md Invariant 9 strictly forbids logging handles, aliases, account IDs, "+
								"subscription endpoints, IPs, or user agents.",
							leakedIdent, callName,
						),
					})
				}
			}
		}

		return true
	})

	return violations
}

func isLoggingOrFormattingCall(name string) bool {
	switch name {
	case "log.Print", "log.Printf", "log.Println",
		"fmt.Print", "fmt.Printf", "fmt.Println", "fmt.Sprint", "fmt.Sprintf", "fmt.Sprintln",
		"slog.Info", "slog.Warn", "slog.Error", "slog.Debug":
		return true
	default:
		return false
	}
}

func getCallExpressionName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return xIdent.Name + "." + sel.Sel.Name
}

var forbiddenIdentifierTokens = []string{
	"handle", "alias", "accountid", "endpoint", "remoteaddr", "useragent", "user_agent",
}

func detectLeakedIdentifier(expr ast.Expr) (string, bool) {
	switch v := expr.(type) {
	case *ast.Ident:
		nameLower := strings.ToLower(v.Name)
		for _, token := range forbiddenIdentifierTokens {
			if strings.Contains(nameLower, token) {
				return v.Name, true
			}
		}
	case *ast.SelectorExpr:
		selLower := strings.ToLower(v.Sel.Name)
		for _, token := range forbiddenIdentifierTokens {
			if strings.Contains(selLower, token) {
				return v.Sel.Name, true
			}
		}
	}
	return "", false
}
