// Package docs embeds the hactl manual and the agent skills for use at runtime.
package docs

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed manual.md

// Manual contains the full hactl usage manual, embedded at build time.
var Manual string

//go:embed skills/*.md
var skills embed.FS

// Skill returns the skill `docs/skills/<name>.md` (D-165), and whether there is
// one by that name.
func Skill(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return "", false
	}
	b, err := fs.ReadFile(skills, "skills/"+name+".md")
	if err != nil {
		return "", false
	}
	return string(b), true
}

// SkillNames lists the embedded skills, sorted.
func SkillNames() []string {
	entries, _ := fs.ReadDir(skills, "skills")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
