package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	sdkModule = "github.com/modelcontextprotocol/go-sdk"
	sdkMCP    = sdkModule + "/mcp"
)

// sdkEntryPoints are the parts of the MCP SDK that would open a listening
// socket, dial out over HTTP, or start a subprocess. The SDK is linked in
// whole, so the code for all of them is in the binary; this list is what
// keeps "stdio only, no exec, no second network path" true of this program.
var sdkEntryPoints = map[string]bool{
	"NewStreamableHTTPHandler":  true,
	"StreamableHTTPHandler":     true,
	"NewSSEHandler":             true,
	"SSEHandler":                true,
	"StreamableClientTransport": true,
	"SSEClientTransport":        true,
	"CommandTransport":          true,
}

func TestNoNetworkOrSubprocessTransportFromTheSDK(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		local := ""
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case p == sdkMCP:
				local = "mcp"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			case strings.HasPrefix(p, sdkModule+"/"):
				// auth, oauthex and friends exist to serve HTTP transports.
				t.Errorf("%s imports %q; only %s may be used from the SDK", rel, p, sdkMCP)
			}
		}
		if local == "" {
			return nil
		}
		if local == "." || local == "_" {
			t.Errorf("%s imports %s as %q, which hides uses of it from this test", rel, sdkMCP, local)
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && sdkEntryPoints[sel.Sel.Name] {
				t.Errorf("%s uses %s.%s; this server speaks stdio only and never starts a process",
					fset.Position(sel.Pos()), local, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
