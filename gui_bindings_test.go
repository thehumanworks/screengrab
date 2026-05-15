package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// TestCaptureServiceBindingsMatchFrontend protects against the Wails error
//
//	Binding call failed: unknown bound method name 'X.Y.Z'
//
// which the runtime returns when the FQN passed to `Call.ByName` does not
// match a method registered through reflection by Wails v3's getMethods
// (see pkg/application/bindings.go). The check covers three regression
// paths:
//
//  1. The Go struct is renamed but main.js still references the old name.
//  2. A Go method called from main.js is renamed or removed.
//  3. CaptureService is moved out of `package main` into a sub-package, in
//     which case the prefix changes from "main.CaptureService" to the new
//     import-path + struct-name, and this test must be updated alongside
//     main.js.
//
// We intentionally hardcode the expected prefix as "main.CaptureService".
// You may be tempted to derive it from reflect.Type.PkgPath() — do not. The
// test binary compiles screengrab as a normal package (so PkgPath returns
// the module path "screengrab"), but the production binary has
// CaptureService in `package main` and reflect returns the literal string
// "main" at runtime. The two contexts disagree, and the only one that
// matters is the runtime — verified by Wails' own debug log:
//
//	DBG Registering bound method: fqn=main.CaptureService.ListDisplays
//
// The structural sanity checks below (struct name + PkgPath in the test
// context) still fail loudly if CaptureService is renamed or moved, so the
// hardcoded prefix can never silently drift out of sync.
func TestCaptureServiceBindingsMatchFrontend(t *testing.T) {
	const expectedPrefix = "main.CaptureService"

	svcType := reflect.TypeOf(&CaptureService{}).Elem()
	if name := svcType.Name(); name != "CaptureService" {
		t.Fatalf("struct name changed to %q — update expectedPrefix and frontend/main.js SVC_FQN", name)
	}
	if pkg := svcType.PkgPath(); pkg != "screengrab" {
		t.Fatalf("CaptureService PkgPath in test context = %q (expected \"screengrab\"). "+
			"The struct has moved out of package main; the runtime FQN prefix is no longer "+
			"\"main.CaptureService\" — update expectedPrefix and frontend/main.js SVC_FQN.", pkg)
	}

	js, err := os.ReadFile(filepath.Join("frontend", "main.js"))
	if err != nil {
		t.Fatalf("read frontend/main.js: %v", err)
	}

	// Confirm the JS constant matches the Go-side reality.
	fqnRE := regexp.MustCompile(`const\s+SVC_FQN\s*=\s*"([^"]+)"`)
	m := fqnRE.FindSubmatch(js)
	if m == nil {
		t.Fatalf("SVC_FQN constant not found in frontend/main.js")
	}
	if got := string(m[1]); got != expectedPrefix {
		t.Fatalf("frontend/main.js SVC_FQN = %q, want %q — Wails will reject calls under the wrong prefix.\n"+
			"For a `package main` binary, reflect.Type.PkgPath() is the literal string \"main\" at runtime, "+
			"regardless of the module name in go.mod.", got, expectedPrefix)
	}

	// Collect every method called via svc("Method", ...) on the JS side.
	callRE := regexp.MustCompile(`svc\(\s*"([A-Za-z_][A-Za-z0-9_]*)"`)
	jsMethods := map[string]bool{}
	for _, hit := range callRE.FindAllSubmatch(js, -1) {
		jsMethods[string(hit[1])] = true
	}
	if len(jsMethods) == 0 {
		t.Fatalf("no svc(\"Method\", ...) calls found in frontend/main.js")
	}

	// Mirror Wails v3 internalServiceMethods so we exclude exactly what the
	// runtime excludes.
	internal := map[string]bool{
		"ServiceName":     true,
		"ServiceStartup":  true,
		"ServiceShutdown": true,
		"ServeHTTP":       true,
	}

	ptrType := reflect.TypeOf(&CaptureService{})
	goMethods := map[string]bool{}
	for i := 0; i < ptrType.NumMethod(); i++ {
		name := ptrType.Method(i).Name
		if internal[name] {
			continue
		}
		goMethods[name] = true
	}

	missing := []string{}
	for name := range jsMethods {
		if !goMethods[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("frontend/main.js calls these methods that are NOT exported on *CaptureService: %v\nGo side currently exposes: %v", missing, sortedKeys(goMethods))
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
