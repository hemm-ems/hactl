package surfaceaudit

import (
	"go/parser"
	"go/token"
)

// PolicyRequestsFromSource runs the policy surface's request extractor over
// in-memory files, keyed by their repository-relative path, so its fixture
// tests can feed it a send it has never seen.
func PolicyRequestsFromSource(files map[string]string) ([]Site, error) {
	var parsed []srcFile
	for rel, src := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, srcFile{rel: rel, fset: fset, ast: f})
	}
	reqs, err := policyRequests(parsed)
	if err != nil {
		return nil, err
	}
	return requestSitesOf(reqs), nil
}
